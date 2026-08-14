package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math"
	"net"
	"os"
	"os/signal"
	"strings"
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
	bitrate    = 24000
	encBufSize = 1450
)

const meshConfFile = "/etc/mesh.conf"

type BeepConfig struct {
	TXStart bool
	RXEnd   bool
}

type voicePeer struct {
	addr     *net.UDPAddr
	lastSeen time.Time
}

func loadBeepConfig() BeepConfig {
	bc := BeepConfig{TXStart: true, RXEnd: true}
	data, err := os.ReadFile(meshConfFile)
	if err != nil {
		return bc
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			switch strings.TrimSpace(k) {
			case "voice_beep_tx_start":
				bc.TXStart = strings.TrimSpace(v) != "n"
			case "voice_beep_rx_end":
				bc.RXEnd = strings.TrimSpace(v) != "n"
			}
		}
	}
	return bc
}

func generateBeep(beepType string) []int16 {
	switch beepType {
	case "tx-start":
		dur := sampleRate * 80 / 1000
		samples := make([]int16, dur)
		ramp := sampleRate / 100
		for i := range samples {
			t := float64(i) / float64(sampleRate)
			gain := 0.15
			if i < ramp {
				gain *= float64(i) / float64(ramp)
			} else if i > dur-ramp*2 {
				gain *= float64(dur-i) / float64(ramp*2)
			}
			samples[i] = int16(math.Sin(2*math.Pi*1200*t) * gain * 32767)
		}
		return samples
	case "rx-end":
		n := sampleRate * 60 / 1000
		gap := sampleRate * 10 / 1000
		samples := make([]int16, n*2+gap)
		ramp := sampleRate / 100
		for i := 0; i < n; i++ {
			t := float64(i) / float64(sampleRate)
			gain := 0.12
			if i > n-ramp {
				gain *= float64(n-i) / float64(ramp)
			}
			samples[i] = int16(math.Sin(2*math.Pi*800*t) * gain * 32767)
		}
		for i := 0; i < n; i++ {
			t := float64(i) / float64(sampleRate)
			gain := 0.12
			if i > n-ramp {
				gain *= float64(n-i) / float64(ramp)
			}
			samples[n+gap+i] = int16(math.Sin(2*math.Pi*600*t) * gain * 32767)
		}
		return samples
	}
	return nil
}

type Config struct {
	Iface     string
	McastAddr string
	McastPort int
	PTTSource string // "gpio", "openvlm", "vox", "always"
	GPIOPin   int
	GPIOKey   string // evdev key code or "any"
	DeviceIn  string // ALSA device hint for capture
	DeviceOut string // ALSA device hint for playback
	MicGain   float64
}

func main() {
	cfg := Config{}
	flag.StringVar(&cfg.Iface, "iface", "br0", "network interface for multicast")
	flag.StringVar(&cfg.McastAddr, "addr", "239.69.0.1", "multicast group address")
	flag.IntVar(&cfg.McastPort, "port", 4370, "multicast RTP port")
	flag.StringVar(&cfg.PTTSource, "ptt", "openvlm", "PTT source: openvlm, gpio, always, vox")
	flag.IntVar(&cfg.GPIOPin, "gpio-pin", 17, "GPIO pin number for PTT button")
	flag.StringVar(&cfg.GPIOKey, "gpio-key", "any", "evdev key code or 'any'")
	flag.StringVar(&cfg.DeviceIn, "input", "", "ALSA capture device")
	flag.StringVar(&cfg.DeviceOut, "output", "", "ALSA playback device")
	flag.Float64Var(&cfg.MicGain, "gain", 3.0, "software mic gain multiplier")
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

	// Unicast TX socket — 802.11 retries make this far more reliable than multicast
	ucastSock, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return fmt.Errorf("unicast socket: %w", err)
	}
	defer ucastSock.Close()
	if uc, ok := ucastSock.(*net.UDPConn); ok {
		if rc, err := uc.SyscallConn(); err == nil {
			rc.Control(func(fd uintptr) {
				syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_TOS, 0xB8)
			})
		}
	}

	var peersMu sync.RWMutex
	voicePeers := make(map[uint32]*voicePeer)

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

	// Remote PTT — manet-ctrl writes "1" or "0" to this file to trigger PTT
	go remotePTTLoop(ctx, pttCh)

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
		var prevFrame []byte
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
					cur := make([]byte, n)
					copy(cur, encBuf[:n])
					pkt := buildRedundantRTPPacket(seqNum, ssrc, cur, prevFrame)
					seqNum++
					txConn.Write(pkt)
					peersMu.RLock()
					for _, p := range voicePeers {
						ucastSock.WriteTo(pkt, p.addr)
					}
					peersMu.RUnlock()
					prevFrame = cur
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Jitter buffer (shared across playback device lifecycles)
	const jitterFrames = 6
	var jbMu sync.Mutex
	jitterBuf := make([][]int16, 0, 32)
	var jbPlaying atomic.Bool

	// Beep tones for hardware PTT
	beepCfg := loadBeepConfig()
	log.Printf("beep config: tx_start=%v rx_end=%v", beepCfg.TXStart, beepCfg.RXEnd)

	playBeep := func(beepType string) {
		samples := generateBeep(beepType)
		if len(samples) == 0 {
			return
		}
		ensurePlayback()
		jbMu.Lock()
		for i := 0; i < len(samples); i += frameSize {
			end := i + frameSize
			if end > len(samples) {
				end = len(samples)
			}
			frame := make([]int16, end-i)
			copy(frame, samples[i:end])
			jitterBuf = append(jitterBuf, frame)
		}
		jbMu.Unlock()
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
						pttState.setActive(true)
						pttState.setTX(true)
						if beepCfg.TXStart {
							playBeep("tx-start")
							time.Sleep(100 * time.Millisecond)
						}
						ensureCapture()
						broadcasting.Store(true)
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

	// Callbacks reference captureCh and jitterBuf — safe across device reinit
	captureCallbacks := malgo.DeviceCallbacks{
		Data: func(outputSamples, inputSamples []byte, framecount uint32) {
			if !broadcasting.Load() {
				return
			}
			nSamples := len(inputSamples) / 2
			samples := make([]int16, nSamples)
			for i := 0; i < nSamples; i++ {
				s := int32(int16(inputSamples[i*2]) | int16(inputSamples[i*2+1])<<8)
				s = int32(float64(s) * cfg.MicGain)
				if s > 32767 {
					s = 32767
				} else if s < -32768 {
					s = -32768
				}
				samples[i] = int16(s)
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

	// Periodic audio hot-plug retry for all PTT modes
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				audioMu.Lock()
				hasDevices := captureDevicePtr != nil
				audioMu.Unlock()
				if !hasDevices {
					initAudioDevices()
				}
			}
		}
	}()

	defer teardownAudioDevices()

	go func() {
		<-ctx.Done()
		rxConn.Close()
		txConn.Close()
		ucastSock.Close()
	}()

	// Peer expiry — remove peers that stopped transmitting
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				peersMu.Lock()
				for k, p := range voicePeers {
					if time.Since(p.lastSeen) > 30*time.Second {
						delete(voicePeers, k)
					}
				}
				peersMu.Unlock()
			}
		}
	}()

	// RX loop
	wg.Add(1)
	go func() {
		defer wg.Done()
		rxBuf := make([]byte, 2048)
		pcmOut := make([]int16, frameSize)
		var lastRxSeqNum uint16
		var rxSeqInit bool

		addToJB := func(pcm []int16) {
			frame := make([]int16, len(pcm))
			copy(frame, pcm)
			jbMu.Lock()
			if len(jitterBuf) < 32 {
				jitterBuf = append(jitterBuf, frame)
			}
			jbMu.Unlock()
		}

		for {
			n, src, err := rxConn.ReadFromUDP(rxBuf)
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

			pktSeq := uint16(rxBuf[2])<<8 | uint16(rxBuf[3])

			// Dedup: multicast and unicast copies of the same packet both arrive
			if rxSeqInit {
				diff := pktSeq - lastRxSeqNum
				if diff == 0 || diff > 0x8000 {
					continue
				}
			}

			// Learn peer IP for unicast TX
			if src != nil {
				peersMu.Lock()
				voicePeers[pktSSRC] = &voicePeer{
					addr:     &net.UDPAddr{IP: src.IP, Port: cfg.McastPort},
					lastSeen: time.Now(),
				}
				peersMu.Unlock()
			}

			remoteActive.Store(true)
			pttState.setRX(true)
			lastRxTime.Store(time.Now().UnixMilli())
			ensurePlayback()

			// Parse redundant format: [uint16 curLen][curFrame][prevFrame]
			payload := rxBuf[12:n]
			var curFrame, redFrame []byte
			if len(payload) >= 3 {
				curLen := int(payload[0])<<8 | int(payload[1])
				if curLen >= 1 && curLen <= 500 && curLen <= len(payload)-2 {
					curFrame = payload[2 : 2+curLen]
					if 2+curLen < len(payload) {
						redFrame = payload[2+curLen:]
					}
				}
			}
			if curFrame == nil {
				curFrame = payload
			}

			// Gap recovery: PLC for unrecoverable frames, redundant copy for the last
			if rxSeqInit {
				gap := int(pktSeq-lastRxSeqNum) - 1
				if gap > 0 {
					plcCount := gap
					if len(redFrame) > 0 {
						plcCount--
					}
					if plcCount > 3 {
						plcCount = 3
					}
					for i := 0; i < plcCount; i++ {
						if err := decoder.DecodePLC(pcmOut); err == nil {
							addToJB(pcmOut[:frameSize])
						}
					}
					if len(redFrame) > 0 {
						if s, err := decoder.Decode(redFrame, pcmOut); err == nil && s > 0 {
							addToJB(pcmOut[:s])
						}
					}
				}
			}

			samples, err := decoder.Decode(curFrame, pcmOut)
			if err != nil {
				log.Printf("decode error: %v", err)
				lastRxSeqNum = pktSeq
				rxSeqInit = true
				continue
			}

			addToJB(pcmOut[:samples])
			lastRxSeqNum = pktSeq
			rxSeqInit = true
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
					if remoteActive.Swap(false) && beepCfg.RXEnd {
						playBeep("rx-end")
					}
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

// buildRedundantRTPPacket packs the current frame plus the previous frame into
// one RTP packet. Format after the 12-byte header: [uint16 curLen][cur][prev].
// The receiver uses prev to recover a lost packet without waiting for PLC.
func buildRedundantRTPPacket(seq uint16, ssrc uint32, current, prev []byte) []byte {
	pkt := make([]byte, 14+len(current)+len(prev))
	pkt[0] = 0x80
	pkt[1] = 111
	pkt[2] = byte(seq >> 8)
	pkt[3] = byte(seq)
	ts := uint32(seq) * uint32(frameSize)
	pkt[4] = byte(ts >> 24)
	pkt[5] = byte(ts >> 16)
	pkt[6] = byte(ts >> 8)
	pkt[7] = byte(ts)
	pkt[8] = byte(ssrc >> 24)
	pkt[9] = byte(ssrc >> 16)
	pkt[10] = byte(ssrc >> 8)
	pkt[11] = byte(ssrc)
	curLen := len(current)
	pkt[12] = byte(curLen >> 8)
	pkt[13] = byte(curLen)
	copy(pkt[14:], current)
	if len(prev) > 0 {
		copy(pkt[14+curLen:], prev)
	}
	return pkt
}
