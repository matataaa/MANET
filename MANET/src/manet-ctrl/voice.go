package main

import (
	"context"
	cryptoRand "crypto/rand"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

const (
	voiceChannelCount = 21
	voiceMcastBase    = "239.69.0."
	voiceMcastPort    = 4370
	voiceMcastIface   = "br0"
)

var (
	voiceClients   = make(map[*websocket.Conn]uint32)
	voiceClientsMu sync.RWMutex
	voiceSSRCSeq   uint32

	voiceTxCh atomic.Int32

	voiceRxMu   sync.Mutex
	voiceRxSet  = make(map[int]context.CancelFunc)
	voiceTxPool sync.Map // int -> *net.UDPConn

	voiceChActivity sync.Map // int -> *atomic.Int64 (unix ms)
)

func voiceChannelAddr(ch int) string {
	if ch < 1 || ch > voiceChannelCount {
		ch = 1
	}
	return fmt.Sprintf("%s%d", voiceMcastBase, ch)
}

// voiceListenMulticast binds to the specific multicast group address instead of
// INADDR_ANY so the kernel only delivers packets for this group to this socket.
func voiceListenMulticast(channel int) (*net.UDPConn, error) {
	mcastIP := net.ParseIP(voiceChannelAddr(channel)).To4()
	if mcastIP == nil {
		return nil, fmt.Errorf("invalid channel %d", channel)
	}

	lc := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			var opErr error
			c.Control(func(fd uintptr) {
				opErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
			})
			return opErr
		},
	}

	pc, err := lc.ListenPacket(context.Background(), "udp4", fmt.Sprintf("%s:%d", mcastIP, voiceMcastPort))
	if err != nil {
		return nil, err
	}
	conn := pc.(*net.UDPConn)

	iface, _ := net.InterfaceByName(voiceMcastIface)
	rc, err := conn.SyscallConn()
	if err != nil {
		conn.Close()
		return nil, err
	}

	var joinErr error
	rc.Control(func(fd uintptr) {
		mreq := &syscall.IPMreq{}
		copy(mreq.Multiaddr[:], mcastIP)
		if iface != nil {
			if addrs, aErr := iface.Addrs(); aErr == nil {
				for _, a := range addrs {
					if ipnet, ok := a.(*net.IPNet); ok {
						if ip4 := ipnet.IP.To4(); ip4 != nil {
							copy(mreq.Interface[:], ip4)
							break
						}
					}
				}
			}
		}
		joinErr = syscall.SetsockoptIPMreq(int(fd), syscall.IPPROTO_IP, syscall.IP_ADD_MEMBERSHIP, mreq)
	})
	if joinErr != nil {
		conn.Close()
		return nil, fmt.Errorf("join group %s: %w", mcastIP, joinErr)
	}

	return conn, nil
}

func init() {
	var seed [2]byte
	cryptoRand.Read(seed[:])
	base := uint32(seed[0])<<8 | uint32(seed[1])
	if iface, err := net.InterfaceByName(voiceMcastIface); err == nil && len(iface.HardwareAddr) >= 4 {
		base ^= crc32.ChecksumIEEE(iface.HardwareAddr)
	}
	voiceSSRCSeq = base << 16
	voiceTxCh.Store(1)
}

func voiceInitChannels() {
	conf := loadKVFile(MeshConfFile)
	txCh := 1
	if v, err := strconv.Atoi(conf["voice_channel"]); err == nil && v >= 1 && v <= voiceChannelCount {
		txCh = v
	}
	voiceTxCh.Store(int32(txCh))

	rxChans := []int{txCh}
	if raw := conf["voice_rx_channels"]; raw != "" {
		rxChans = parseChannelList(raw)
	}
	if !intSliceContains(rxChans, txCh) {
		rxChans = append(rxChans, txCh)
	}

	voiceSetRxChannels(rxChans)
	log.Printf("voice channels: tx=%d rx=%v", txCh, rxChans)
}

func parseChannelList(s string) []int {
	var result []int
	seen := make(map[int]bool)
	for _, part := range strings.Split(s, ",") {
		v, err := strconv.Atoi(strings.TrimSpace(part))
		if err == nil && v >= 1 && v <= voiceChannelCount && !seen[v] {
			result = append(result, v)
			seen[v] = true
		}
	}
	return result
}

func intSliceContains(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func voiceSetRxChannels(channels []int) {
	voiceRxMu.Lock()
	defer voiceRxMu.Unlock()

	want := make(map[int]bool)
	for _, ch := range channels {
		if ch >= 1 && ch <= voiceChannelCount {
			want[ch] = true
		}
	}

	for ch, cancel := range voiceRxSet {
		if !want[ch] {
			cancel()
			delete(voiceRxSet, ch)
			log.Printf("voice rx: stopped ch%d", ch)
		}
	}

	for ch := range want {
		if _, exists := voiceRxSet[ch]; !exists {
			ctx, cancel := context.WithCancel(context.Background())
			voiceRxSet[ch] = cancel
			go voiceRxLoop(ctx, ch)
			log.Printf("voice rx: started ch%d (%s)", ch, voiceChannelAddr(ch))
		}
	}
}

func voiceGetRxChannels() []int {
	voiceRxMu.Lock()
	defer voiceRxMu.Unlock()
	var out []int
	for ch := range voiceRxSet {
		out = append(out, ch)
	}
	return out
}

func voiceRxLoop(ctx context.Context, channel int) {
	rxConn, err := voiceListenMulticast(channel)
	if err != nil {
		log.Printf("voice rx ch%d: %v", channel, err)
		return
	}
	defer rxConn.Close()
	rxConn.SetReadBuffer(1024 * 1024)

	go func() {
		<-ctx.Done()
		rxConn.Close()
	}()

	buf := make([]byte, 2048)
	for {
		n, _, err := rxConn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		if n < 12 {
			continue
		}

		val := &atomic.Int64{}
		actual, _ := voiceChActivity.LoadOrStore(channel, val)
		actual.(*atomic.Int64).Store(time.Now().UnixMilli())

		pktSSRC := binary.BigEndian.Uint32(buf[8:12])

		voiceClientsMu.RLock()
		for c, cs := range voiceClients {
			if cs == pktSSRC {
				continue
			}
			pkt := make([]byte, n)
			copy(pkt, buf[:n])
			c.WriteMessage(websocket.BinaryMessage, pkt)
		}
		voiceClientsMu.RUnlock()
	}
}

func voiceGetTxConn(ch int) *net.UDPConn {
	if v, ok := voiceTxPool.Load(ch); ok {
		return v.(*net.UDPConn)
	}
	addr := &net.UDPAddr{IP: net.ParseIP(voiceChannelAddr(ch)), Port: voiceMcastPort}
	conn, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		log.Printf("voice tx conn ch%d: %v", ch, err)
		return nil
	}
	if rc, err := conn.SyscallConn(); err == nil {
		rc.Control(func(fd uintptr) {
			syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_TOS, 0xB8)
		})
	}
	voiceTxPool.Store(ch, conn)
	return conn
}

func handleVoiceWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("voice ws upgrade: %v", err)
		return
	}
	defer conn.Close()

	ssrc := atomic.AddUint32(&voiceSSRCSeq, 1)
	var seqNum uint16

	voiceClientsMu.Lock()
	voiceClients[conn] = ssrc
	voiceClientsMu.Unlock()

	defer func() {
		voiceClientsMu.Lock()
		delete(voiceClients, conn)
		voiceClientsMu.Unlock()
	}()

	txCh := int(voiceTxCh.Load())
	log.Printf("voice ws connected from %s (ssrc=%08x, tx=ch%d)", r.RemoteAddr, ssrc, txCh)

	for {
		msgType, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if msgType != websocket.BinaryMessage || len(msg) == 0 {
			continue
		}

		rtp := voiceBuildRTP(seqNum, ssrc, msg)
		seqNum++

		ch := int(voiceTxCh.Load())
		if txConn := voiceGetTxConn(ch); txConn != nil {
			txConn.Write(rtp)
		}

		voiceClientsMu.RLock()
		for c := range voiceClients {
			if c != conn {
				c.WriteMessage(websocket.BinaryMessage, rtp)
			}
		}
		voiceClientsMu.RUnlock()
	}

	log.Printf("voice ws disconnected %s", r.RemoteAddr)
}

func voiceBuildRTP(seq uint16, ssrc uint32, payload []byte) []byte {
	pkt := make([]byte, 12+len(payload))
	pkt[0] = 0x80
	pkt[1] = 111
	binary.BigEndian.PutUint16(pkt[2:4], seq)
	binary.BigEndian.PutUint32(pkt[4:8], uint32(seq)*960)
	binary.BigEndian.PutUint32(pkt[8:12], ssrc)
	copy(pkt[12:], payload)
	return pkt
}

// --- Channel API ---

func apiVoiceChannels(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		voiceGetChannels(w)
		return
	}
	voiceSetChannels(w, r)
}

func voiceGetChannels(w http.ResponseWriter) {
	txCh := int(voiceTxCh.Load())
	rxList := voiceGetRxChannels()
	rxMap := make(map[int]bool)
	for _, c := range rxList {
		rxMap[c] = true
	}

	now := time.Now().UnixMilli()
	channels := make([]map[string]interface{}, voiceChannelCount)
	for i := 0; i < voiceChannelCount; i++ {
		ch := i + 1
		active := false
		if v, ok := voiceChActivity.Load(ch); ok {
			active = (now - v.(*atomic.Int64).Load()) < 500
		}
		channels[i] = map[string]interface{}{
			"channel": ch,
			"rx":      rxMap[ch],
			"tx":      ch == txCh,
			"active":  active,
		}
	}
	writeJSON(w, 200, map[string]interface{}{
		"tx_channel":  txCh,
		"rx_channels": rxList,
		"channels":    channels,
	})
}

func voiceSetChannels(w http.ResponseWriter, r *http.Request) {
	body := readBody(r)

	txCh := int(voiceTxCh.Load())
	txChanged := false
	if v, ok := body["tx"].(float64); ok {
		ch := int(v)
		if ch >= 1 && ch <= voiceChannelCount && ch != txCh {
			txCh = ch
			txChanged = true
		}
	}

	var rxChans []int
	if rxRaw, ok := body["rx"].([]interface{}); ok {
		for _, v := range rxRaw {
			if n, ok := v.(float64); ok {
				ch := int(n)
				if ch >= 1 && ch <= voiceChannelCount {
					rxChans = append(rxChans, ch)
				}
			}
		}
	} else {
		rxChans = voiceGetRxChannels()
	}

	if !intSliceContains(rxChans, txCh) {
		rxChans = append(rxChans, txCh)
	}

	oldTx := int(voiceTxCh.Swap(int32(txCh)))
	if oldTx != txCh {
		if v, ok := voiceTxPool.LoadAndDelete(oldTx); ok {
			v.(*net.UDPConn).Close()
		}
	}
	voiceSetRxChannels(rxChans)

	rxStrs := make([]string, len(rxChans))
	for i, ch := range rxChans {
		rxStrs[i] = strconv.Itoa(ch)
	}
	saveKVFile(MeshConfFile, map[string]string{
		"voice_channel":     strconv.Itoa(txCh),
		"voice_rx_channels": strings.Join(rxStrs, ","),
	})

	if txChanged {
		go voiceRestartDaemon(txCh)
	}

	writeJSON(w, 200, map[string]interface{}{
		"ok":          true,
		"tx_channel":  txCh,
		"rx_channels": rxChans,
	})
}

func voiceRestartDaemon(txCh int) {
	if exec.Command("systemctl", "is-active", "--quiet", "mesh-voice").Run() != nil {
		return
	}
	vs := getVoiceStatus()
	iface := vs.Interface
	if iface == "" {
		iface = "br0"
	}
	port := vs.Port
	if port == "" {
		port = "4370"
	}
	ptt := vs.PTTMode
	if ptt == "" {
		ptt = "always"
	}
	addr := voiceChannelAddr(txCh)

	execLine := fmt.Sprintf("/usr/local/bin/mesh-voice -iface %s -addr %s -port %s -ptt %s", iface, addr, port, ptt)
	unit := fmt.Sprintf(`[Unit]
Description=Mesh Voice PTT over multicast
After=network.target
Wants=network.target

[Service]
Type=simple
ExecStart=%s
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`, execLine)

	os.WriteFile("/etc/systemd/system/mesh-voice.service", []byte(unit), 0644)
	exec.Command("systemctl", "daemon-reload").Run()
	exec.Command("systemctl", "restart", "mesh-voice").Run()
	log.Printf("voice daemon restarted on ch%d (%s)", txCh, addr)
}
