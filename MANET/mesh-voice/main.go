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
	encoder.SetComplexity(10)
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

	// PTT source
	pttCh := make(chan bool, 4)
	switch cfg.PTTSource {
	case "always":
		broadcasting.Store(true)
		log.Println("PTT: always-on (open mic)")
	case "gpio":
		go gpioPTTLoop(ctx, cfg.GPIOKey, pttCh)
		log.Printf("PTT: GPIO evdev (key=%s)", cfg.GPIOKey)
	case "openvlm":
		go openvlmPTTLoop(ctx, pttCh)
		log.Println("PTT: OpenVLM HID (GPIO3)")
	case "vox":
		log.Println("PTT: VOX (not yet implemented, using always-on)")
		broadcasting.Store(true)
	default:
		return fmt.Errorf("unknown PTT source: %s", cfg.PTTSource)
	}

	// PTT event handler
	go func() {
		for {
			select {
			case down := <-pttCh:
				if down && !remoteActive.Load() {
					broadcasting.Store(true)
					log.Println("TX: start")
				} else {
					broadcasting.Store(false)
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

	// miniaudio context
	malgoCtx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return fmt.Errorf("malgo context: %w", err)
	}
	defer malgoCtx.Free()
	defer malgoCtx.Uninit()

	var wg sync.WaitGroup

	// === TX: capture → encode → multicast ===
	captureCfg := malgo.DefaultDeviceConfig(malgo.Capture)
	captureCfg.Capture.Format = malgo.FormatS16
	captureCfg.Capture.Channels = channels
	captureCfg.SampleRate = sampleRate
	captureCfg.PeriodSizeInFrames = uint32(frameSize)

	pcmBuf := make([]int16, 0, frameSize*4)
	encBuf := make([]byte, encBufSize)
	var pcmMu sync.Mutex

	captureCallbacks := malgo.DeviceCallbacks{
		Data: func(outputSamples, inputSamples []byte, framecount uint32) {
			if !broadcasting.Load() {
				return
			}
			// Convert bytes to int16 samples
			nSamples := len(inputSamples) / 2
			samples := make([]int16, nSamples)
			for i := 0; i < nSamples; i++ {
				samples[i] = int16(inputSamples[i*2]) | int16(inputSamples[i*2+1])<<8
			}

			pcmMu.Lock()
			pcmBuf = append(pcmBuf, samples...)
			for len(pcmBuf) >= frameSize {
				frame := make([]int16, frameSize)
				copy(frame, pcmBuf[:frameSize])
				pcmBuf = pcmBuf[frameSize:]
				pcmMu.Unlock()

				n, err := encoder.Encode(frame, encBuf)
				if err != nil {
					log.Printf("encode error: %v", err)
					pcmMu.Lock()
					continue
				}

				// Build minimal RTP header
				pkt := buildRTPPacket(seqNum, ssrc, encBuf[:n])
				seqNum++

				txConn.Write(pkt)
				pcmMu.Lock()
			}
			pcmMu.Unlock()
		},
	}

	captureDevice, err := malgo.InitDevice(malgoCtx.Context, captureCfg, captureCallbacks)
	if err != nil {
		return fmt.Errorf("capture device: %w", err)
	}
	defer captureDevice.Uninit()

	if err := captureDevice.Start(); err != nil {
		return fmt.Errorf("capture start: %w", err)
	}
	defer captureDevice.Stop()

	// === RX: multicast → decode → playback ===
	playbackCfg := malgo.DefaultDeviceConfig(malgo.Playback)
	playbackCfg.Playback.Format = malgo.FormatS16
	playbackCfg.Playback.Channels = channels
	playbackCfg.SampleRate = sampleRate
	playbackCfg.PeriodSizeInFrames = uint32(frameSize)

	playBuf := make(chan []int16, 20)

	playbackCallbacks := malgo.DeviceCallbacks{
		Data: func(outputSamples, inputSamples []byte, framecount uint32) {
			need := int(framecount)
			written := 0
			for written < need {
				select {
				case frame := <-playBuf:
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
				default:
					// Silence
					for i := written * 2; i < len(outputSamples); i++ {
						outputSamples[i] = 0
					}
					return
				}
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

	// RX loop
	wg.Add(1)
	go func() {
		defer wg.Done()
		rxBuf := make([]byte, 2048)
		pcmOut := make([]int16, frameSize)

		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			n, _, err := rxConn.ReadFromUDP(rxBuf)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				continue
			}

			if n < 12 {
				continue
			}

			// Parse RTP header
			pktSSRC := uint32(rxBuf[8])<<24 | uint32(rxBuf[9])<<16 | uint32(rxBuf[10])<<8 | uint32(rxBuf[11])
			if pktSSRC == ssrc {
				continue // skip own packets
			}

			payload := rxBuf[12:n]

			// Half-duplex: mark remote active
			remoteActive.Store(true)

			samples, err := decoder.Decode(payload, pcmOut)
			if err != nil {
				log.Printf("decode error: %v", err)
				continue
			}

			frame := make([]int16, samples)
			copy(frame, pcmOut[:samples])

			select {
			case playBuf <- frame:
			default:
				// drop if buffer full
			}
		}
	}()

	// Half-duplex decay: clear remoteActive after silence
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			// Simple approach: clear after each RX check cycle
			// A proper jitter-buffer-based approach would be better
			remoteActive.Store(false)
			select {
			case <-ctx.Done():
				return
			case <-timerChan(200):
			}
		}
	}()

	log.Printf("mesh-voice running on %s → %s:%d", cfg.Iface, cfg.McastAddr, cfg.McastPort)
	<-ctx.Done()
	log.Println("shutting down...")
	wg.Wait()
	return nil
}

func timerChan(ms int) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		syscall.Select(0, nil, nil, nil, &syscall.Timeval{
			Sec:  int64(ms / 1000),
			Usec: int64((ms % 1000) * 1000),
		})
		close(ch)
	}()
	return ch
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
