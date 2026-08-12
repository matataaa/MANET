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

	// DSCP EF (Expedited Forwarding) → WMM AC_VO on 802.11
	if rc, err := txConn.SyscallConn(); err == nil {
		rc.Control(func(fd uintptr) {
			syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_TOS, 0xB8)
		})
	}

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
	vlmCh := make(chan bool, 4)
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
		go openvlmPTTLoop(ctx, pttCh, vlmCh)
		log.Println("PTT: OpenVLM HID (GPIO3)")
	case "vox":
		log.Println("PTT: VOX (not yet implemented, using always-on)")
		broadcasting.Store(true)
		pttState.setActive(true)
		pttState.setConnected(true)
	default:
		return fmt.Errorf("unknown PTT source: %s", cfg.PTTSource)
	}

	// Audio device management — handles hot-plug of USB audio (VLM)
	var audioMu sync.Mutex
	var captureRunning atomic.Bool
	var playbackRunning atomic.Bool
	var malgoCtxPtr *malgo.AllocatedContext
	var captureDevicePtr *malgo.Device
	var playbackDevicePtr *malgo.Device
	var canCapture bool

	ensureCapture := func() {
		audioMu.Lock()
		defer audioMu.Unlock()
		if captureRunning.Load() || captureDevicePtr == nil {
			return
		}
		if err := captureDevicePtr.Start(); err != nil {
			log.Printf("capture resume: %v", err)
			return
		}
		captureRunning.Store(true)
	}

	pauseCapture := func() {
		audioMu.Lock()
		defer audioMu.Unlock()
		if !captureRunning.Load() || captureDevicePtr == nil {
			return
		}
		captureDevicePtr.Stop()
		captureRunning.Store(false)
	}

	ensurePlayback := func() {
		audioMu.Lock()
		defer audioMu.Unlock()
		if playbackRunning.Load() || playbackDevicePtr == nil {
			return
		}
		if err := playbackDevicePtr.Start(); err != nil {
			log.Printf("playback resume: %v", err)
			return
		}
		playbackRunning.Store(true)
	}

	pausePlayback := func() {
		audioMu.Lock()
		defer audioMu.Unlock()
		if !playbackRunning.Load() || playbackDevicePtr == nil {
			return
		}
		playbackDevicePtr.Stop()
		playbackRunning.Store(false)
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
						ensureCapture()
						broadcasting.Store(true)
						pttState.setActive(true)
						pttState.setTX(true)
						log.Println("TX: start")
					}
				} else {
					broadcasting.Store(false)
					pttState.setActive(false)
					pttState.setTX(false)
					pauseCapture()
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

	var wg sync.WaitGroup

	// Capture channel and encoder goroutine (always running, no-ops without device)
	captureCh := make(chan []int16, 8)
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

	// Jitter buffer (shared across playback device lifecycles)
	const jitterFrames = 3
	var jbMu sync.Mutex
	jitterBuf := make([][]int16, 0, 32)
	var jbPlaying atomic.Bool

	// Callbacks reference captureCh and jitterBuf — safe across device reinit
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

	alwaysOn := cfg.PTTSource == "always" || cfg.PTTSource == "vox"

	// initAudioDevices creates capture and playback devices. Non-fatal — service
	// runs without hardware audio (web clients still work via manet-ctrl relay).
	initAudioDevices := func() {
		audioMu.Lock()
		defer audioMu.Unlock()

		if captureDevicePtr != nil {
			return
		}

		mctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
		if err != nil {
			log.Printf("audio: no context available: %v", err)
			return
		}
		malgoCtxPtr = mctx

		capCfg := malgo.DefaultDeviceConfig(malgo.Capture)
		capCfg.Capture.Format = malgo.FormatS16
		capCfg.Capture.Channels = channels
		capCfg.SampleRate = sampleRate
		capCfg.PeriodSizeInFrames = uint32(frameSize)

		capDev, err := malgo.InitDevice(mctx.Context, capCfg, captureCallbacks)
		if err != nil {
			log.Printf("audio: no capture device: %v", err)
			malgoCtxPtr = nil
			mctx.Uninit()
			mctx.Free()
			return
		}

		pbCfg := malgo.DefaultDeviceConfig(malgo.Playback)
		pbCfg.Playback.Format = malgo.FormatS16
		pbCfg.Playback.Channels = channels
		pbCfg.SampleRate = sampleRate
		pbCfg.PeriodSizeInFrames = uint32(frameSize)

		pbDev, err := malgo.InitDevice(mctx.Context, pbCfg, playbackCallbacks)
		if err != nil {
			log.Printf("audio: no playback device: %v", err)
			capDev.Uninit()
			malgoCtxPtr = nil
			mctx.Uninit()
			mctx.Free()
			return
		}

		captureDevicePtr = capDev
		playbackDevicePtr = pbDev
		canCapture = true

		if alwaysOn {
			if err := capDev.Start(); err != nil {
				log.Printf("capture start: %v", err)
			} else {
				captureRunning.Store(true)
			}
		}

		log.Println("audio: capture and playback devices initialized")
	}

	teardownAudioDevices := func() {
		audioMu.Lock()
		defer audioMu.Unlock()

		if captureDevicePtr == nil && playbackDevicePtr == nil {
			return
		}

		if captureRunning.Load() && captureDevicePtr != nil {
			captureDevicePtr.Stop()
			captureRunning.Store(false)
		}
		if playbackRunning.Load() && playbackDevicePtr != nil {
			playbackDevicePtr.Stop()
			playbackRunning.Store(false)
		}

		if captureDevicePtr != nil {
			captureDevicePtr.Uninit()
			captureDevicePtr = nil
		}
		if playbackDevicePtr != nil {
			playbackDevicePtr.Uninit()
			playbackDevicePtr = nil
		}
		if malgoCtxPtr != nil {
			malgoCtxPtr.Uninit()
			malgoCtxPtr.Free()
			malgoCtxPtr = nil
		}
		canCapture = false
		broadcasting.Store(false)

		jbMu.Lock()
		jitterBuf = jitterBuf[:0]
		jbPlaying.Store(false)
		jbMu.Unlock()

		log.Println("audio: devices torn down")
	}

	// Initial audio attempt (may fail if no USB audio yet)
	initAudioDevices()

	if !canCapture && !alwaysOn {
		log.Println("audio: no devices available — waiting for hot-plug")
	}

	// VLM hot-plug handler: reinit audio when device appears/disappears
	if cfg.PTTSource == "openvlm" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case connected := <-vlmCh:
					if connected {
						time.Sleep(500 * time.Millisecond)
						initAudioDevices()
					} else {
						teardownAudioDevices()
					}
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	defer teardownAudioDevices()

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
			ensurePlayback()

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

	// Half-duplex decay: clear remoteActive after silence, pause playback after idle
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
				silenceMs := time.Now().UnixMilli() - lastRxTime.Load()
				if silenceMs > 500 {
					remoteActive.Store(false)
					pttState.setRX(false)
				}
				if silenceMs > 2000 && playbackRunning.Load() {
					pausePlayback()
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
	pkt[0] = 0x80 // V=2, P=0, X=0, CC=0
	pkt[1] = 111  // PT=111 (dynamic, Opus)
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
