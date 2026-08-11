package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gen2brain/malgo"
	"github.com/hraban/opus"
)

const (
	sampleRate = 48000
	channels   = 1
	frameSize  = 960 // 20ms at 48kHz
	bitrate    = 32000
	encBufSize = 1450
)

type Config struct {
	Iface     string
	McastAddr string
	McastPort int
	PTTSource string // "gpio", "openvlm", "vox", "always"
	GPIOPin   int
	GPIOKey   string // evdev key code or "any"
	DeviceIn  string // ALSA device hint for capture
	DeviceOut string // ALSA device hint for playback
}

func main() {
	cfg := Config{}
	flag.StringVar(&cfg.Iface, "iface", "br0", "network interface for multicast")
	flag.StringVar(&cfg.McastAddr, "addr", "239.69.0.1", "multicast group address")
	flag.IntVar(&cfg.McastPort, "port", 4370, "multicast RTP port")
	flag.StringVar(&cfg.PTTSource, "ptt", "always", "PTT source: always, gpio, openvlm, vox")
	flag.IntVar(&cfg.GPIOPin, "gpio-pin", 17, "GPIO pin number for PTT button")
	flag.StringVar(&cfg.GPIOKey, "gpio-key", "any", "evdev key code or 'any'")
	flag.StringVar(&cfg.DeviceIn, "input", "", "ALSA capture device")
	flag.StringVar(&cfg.DeviceOut, "output", "", "ALSA playback device")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := run(ctx, cfg); err != nil {
		log.Fatalf("mesh-voice: %v", err)
	}
}

func run(ctx context.Context, cfg Config) error {
	// Resolve multicast interface
	iface, err := net.InterfaceByName(cfg.Iface)
	if err != nil {
		return fmt.Errorf("interface %s: %w", cfg.Iface, err)
	}

	mcastIP := net.ParseIP(cfg.McastAddr)
	if mcastIP == nil {
		return fmt.Errorf("invalid multicast address: %s", cfg.McastAddr)
	}

	// Opus codec
	encoder, err := opus.NewEncoder(sampleRate, channels, opus.AppVoIP)
	if err != nil {
		return fmt.Errorf("opus encoder: %w", err)
	}
	encoder.SetBitrate(bitrate)
	encoder.SetComplexity(5)
	encoder.SetInBandFEC(true)
	encoder.SetPacketLossPerc(5)

	decoder, err := opus.NewDecoder(sampleRate, channels)
	if err != nil {
		return fmt.Errorf("opus decoder: %w", err)
	}

	// Multicast TX socket
	txAddr := &net.UDPAddr{IP: mcastIP, Port: cfg.McastPort}
	txConn, err := net.DialUDP("udp4", nil, txAddr)
	if err != nil {
		return fmt.Errorf("tx dial: %w", err)
	}
	defer txConn.Close()

	// Multicast RX socket
	rxConn, err := net.ListenMulticastUDP("udp4", iface, &net.UDPAddr{IP: mcastIP, Port: cfg.McastPort})
	if err != nil {
		return fmt.Errorf("rx listen: %w", err)
	}
	defer rxConn.Close()
	rxConn.SetReadBuffer(1024 * 1024)

	// PTT state
	var broadcasting atomic.Bool
	var remoteActive atomic.Bool

	// PTT state file
	pttState.init(cfg.PTTSource)
	defer pttState.cleanup()

	// PTT source
	pttCh := make(chan bool, 4)
	switch cfg.PTTSource {
	case "always":
		broadcasting.Store(true)
		pttState.setActive(true)
		pttState.setConnected(true)
		log.Println("PTT: always-on (open mic)")
	case "gpio":
		go gpioPTTLoop(ctx, cfg.GPIOKey, pttCh)
		pttState.setConnected(true)
		log.Printf("PTT: GPIO evdev (key=%s)", cfg.GPIOKey)
	case "openvlm":
		go openvlmPTTLoop(ctx, pttCh)
		log.Println("PTT: OpenVLM HID (GPIO3)")
	case "vox":
		log.Println("PTT: VOX (not yet implemented, using always-on)")
		broadcasting.Store(true)
		pttState.setActive(true)
		pttState.setConnected(true)
	default:
		return fmt.Errorf("unknown PTT source: %s", cfg.PTTSource)
	}

	// PTT event handler
	go func() {
		for {
			select {
			case down := <-pttCh:
				if down {
					if remoteActive.Load() {
						log.Println("TX: blocked (half-duplex, remote active)")
					} else {
						broadcasting.Store(true)
						pttState.setActive(true)
						pttState.setTX(true)
						log.Println("TX: start")
					}
				} else {
					broadcasting.Store(false)
					pttState.setActive(false)
					pttState.setTX(false)
					log.Println("TX: stop")
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// RTP sequence number
	var seqNum uint16
	ssrc := uint32(os.Getpid() & 0xFFFFFFFF)
	var lastRxTime atomic.Int64

	// miniaudio context
	malgoCtx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return fmt.Errorf("malgo context: %w", err)
	}
	defer malgoCtx.Free()
	defer malgoCtx.Uninit()

	var wg sync.WaitGroup

	// === TX: capture → channel → encoder goroutine → multicast ===
	canCapture := false
	captureCfg := malgo.DefaultDeviceConfig(malgo.Capture)
	captureCfg.Capture.Format = malgo.FormatS16
	captureCfg.Capture.Channels = channels
	captureCfg.SampleRate = sampleRate
	captureCfg.PeriodSizeInFrames = uint32(frameSize)

	captureCh := make(chan []int16, 8)

	captureCallbacks := malgo.DeviceCallbacks{
		Data: func(outputSamples, inputSamples []byte, framecount uint32) {
			if !broadcasting.Load() {
				return
			}
			nSamples := len(inputSamples) / 2
			samples := make([]int16, nSamples)
			for i := 0; i < nSamples; i++ {
				samples[i] = int16(inputSamples[i*2]) | int16(inputSamples[i*2+1])<<8
			}
			select {
			case captureCh <- samples:
			default:
			}
		},
	}

	// Encoder goroutine — keeps encoding off the audio callback thread
	wg.Add(1)
	go func() {
		defer wg.Done()
		pcmBuf := make([]int16, 0, frameSize*4)
		encBuf := make([]byte, encBufSize)
		for {
			select {
			case samples, ok := <-captureCh:
				if !ok {
					return
				}
				pcmBuf = append(pcmBuf, samples...)
				for len(pcmBuf) >= frameSize {
					frame := pcmBuf[:frameSize]
					pcmBuf = append([]int16(nil), pcmBuf[frameSize:]...)

					n, err := encoder.Encode(frame, encBuf)
					if err != nil {
						log.Printf("encode error: %v", err)
						continue
					}
					pkt := buildRTPPacket(seqNum, ssrc, encBuf[:n])
					seqNum++
					txConn.Write(pkt)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	captureDevice, err := malgo.InitDevice(malgoCtx.Context, captureCfg, captureCallbacks)
	if err != nil {
		log.Printf("no capture device available — running receive-only: %v", err)
	} else {
		if err := captureDevice.Start(); err != nil {
			log.Printf("capture start failed — running receive-only: %v", err)
			captureDevice.Uninit()
		} else {
			canCapture = true
			defer captureDevice.Stop()
			defer captureDevice.Uninit()
		}
	}

	if !canCapture {
		log.Println("TX disabled (no microphone), RX still active")
		broadcasting.Store(false)
	}

	// === RX: multicast → decode → jitter buffer → playback ===
	playbackCfg := malgo.DefaultDeviceConfig(malgo.Playback)
	playbackCfg.Playback.Format = malgo.FormatS16
	playbackCfg.Playback.Channels = channels
	playbackCfg.SampleRate = sampleRate
	playbackCfg.PeriodSizeInFrames = uint32(frameSize)

	const jitterFrames = 3 // ~60ms pre-buffer before playback starts
	var jbMu sync.Mutex
	jitterBuf := make([][]int16, 0, 32)
	var jbPlaying atomic.Bool

	playbackCallbacks := malgo.DeviceCallbacks{
		Data: func(outputSamples, inputSamples []byte, framecount uint32) {
			jbMu.Lock()
			if !jbPlaying.Load() {
				if len(jitterBuf) < jitterFrames {
					jbMu.Unlock()
					for i := range outputSamples {
						outputSamples[i] = 0
					}
					return
				}
				jbPlaying.Store(true)
			}

			need := int(framecount)
			written := 0
			for written < need && len(jitterBuf) > 0 {
				frame := jitterBuf[0]
				jitterBuf = jitterBuf[1:]
				for i, s := range frame {
					if written+i >= need {
						break
					}
					idx := (written + i) * 2
					if idx+1 < len(outputSamples) {
						outputSamples[idx] = byte(s)
						outputSamples[idx+1] = byte(s >> 8)
					}
				}
				written += len(frame)
			}
			if len(jitterBuf) == 0 {
				jbPlaying.Store(false)
			}
			jbMu.Unlock()

			for i := written * 2; i < len(outputSamples); i++ {
				outputSamples[i] = 0
			}
		},
	}

	playbackDevice, err := malgo.InitDevice(malgoCtx.Context, playbackCfg, playbackCallbacks)
	if err != nil {
		return fmt.Errorf("playback device: %w", err)
	}
	defer playbackDevice.Uninit()

	if err := playbackDevice.Start(); err != nil {
		return fmt.Errorf("playback start: %w", err)
	}
	defer playbackDevice.Stop()

	go func() {
		<-ctx.Done()
		rxConn.Close()
		txConn.Close()
	}()

	// RX loop
	wg.Add(1)
	go func() {
		defer wg.Done()
		rxBuf := make([]byte, 2048)
		pcmOut := make([]int16, frameSize)

		for {
			n, _, err := rxConn.ReadFromUDP(rxBuf)
			if err != nil {
				return
			}

			if n < 12 {
				continue
			}

			pktSSRC := uint32(rxBuf[8])<<24 | uint32(rxBuf[9])<<16 | uint32(rxBuf[10])<<8 | uint32(rxBuf[11])
			if pktSSRC == ssrc {
				continue
			}

			payload := rxBuf[12:n]

			remoteActive.Store(true)
			pttState.setRX(true)
			lastRxTime.Store(time.Now().UnixMilli())

			samples, err := decoder.Decode(payload, pcmOut)
			if err != nil {
				log.Printf("decode error: %v", err)
				continue
			}

			frame := make([]int16, samples)
			copy(frame, pcmOut[:samples])

			jbMu.Lock()
			if len(jitterBuf) < 32 {
				jitterBuf = append(jitterBuf, frame)
			}
			jbMu.Unlock()
		}
	}()

	// Half-duplex decay: clear remoteActive after silence
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if time.Now().UnixMilli()-lastRxTime.Load() > 500 {
					remoteActive.Store(false)
					pttState.setRX(false)
				}
			}
		}
	}()

	log.Printf("mesh-voice running on %s → %s:%d", cfg.Iface, cfg.McastAddr, cfg.McastPort)
	<-ctx.Done()
	log.Println("shutting down...")
	wg.Wait()
	return nil
}


// buildRTPPacket creates a minimal RTP packet (version 2, no extensions).
func buildRTPPacket(seq uint16, ssrc uint32, payload []byte) []byte {
	// 12-byte RTP header
	pkt := make([]byte, 12+len(payload))
	pkt[0] = 0x80       // V=2, P=0, X=0, CC=0
	pkt[1] = 111        // PT=111 (dynamic, Opus)
	pkt[2] = byte(seq >> 8)
	pkt[3] = byte(seq)
	// timestamp increments by frameSize each packet
	ts := uint32(seq) * uint32(frameSize)
	pkt[4] = byte(ts >> 24)
	pkt[5] = byte(ts >> 16)
	pkt[6] = byte(ts >> 8)
	pkt[7] = byte(ts)
	pkt[8] = byte(ssrc >> 24)
	pkt[9] = byte(ssrc >> 16)
	pkt[10] = byte(ssrc >> 8)
	pkt[11] = byte(ssrc)
	copy(pkt[12:], payload)
	return pkt
}
