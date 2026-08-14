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
	voiceMcastPortBase = 4370
	voiceMcastIface   = "br0"
)

type voiceWSClient struct {
	ssrc   uint32
	sendCh chan []byte
}

var (
	voiceClients   = make(map[*websocket.Conn]*voiceWSClient)
	voiceClientsMu sync.RWMutex
	voiceSSRCSeq   uint32

	voiceTxCh atomic.Int32

	voiceRxMu   sync.Mutex
	voiceRxSet  = make(map[int]context.CancelFunc)
	voiceTxPool sync.Map // int -> *net.UDPConn

	voiceChActivity sync.Map // int -> *atomic.Int64 (unix ms)

	voiceLastTxCh atomic.Int32
	voiceLastTxAt atomic.Int64
	voiceLastRxCh atomic.Int32
	voiceLastRxAt atomic.Int64
)

func voiceChannelAddr(ch int) string {
	if ch < 1 || ch > voiceChannelCount {
		ch = 1
	}
	return fmt.Sprintf("%s%d", voiceMcastBase, ch)
}

func voiceChannelPort(ch int) int {
	if ch < 1 || ch > voiceChannelCount {
		ch = 1
	}
	return voiceMcastPortBase + ch - 1
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
	iface, _ := net.InterfaceByName(voiceMcastIface)
	mcastIP := net.ParseIP(voiceChannelAddr(channel))
	port := voiceChannelPort(channel)
	rxConn, err := net.ListenMulticastUDP("udp4", iface, &net.UDPAddr{IP: mcastIP, Port: port})
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

		now := time.Now().UnixMilli()
		val := &atomic.Int64{}
		actual, _ := voiceChActivity.LoadOrStore(channel, val)
		actual.(*atomic.Int64).Store(now)
		voiceLastRxCh.Store(int32(channel))
		voiceLastRxAt.Store(now)

		pktSSRC := binary.BigEndian.Uint32(buf[8:12])

		voiceClientsMu.RLock()
		for _, cl := range voiceClients {
			if cl.ssrc == pktSSRC {
				continue
			}
			pkt := make([]byte, n)
			copy(pkt, buf[:n])
			select {
			case cl.sendCh <- pkt:
			default:
			}
		}
		voiceClientsMu.RUnlock()
	}
}

func voiceGetTxConn(ch int) *net.UDPConn {
	if v, ok := voiceTxPool.Load(ch); ok {
		return v.(*net.UDPConn)
	}
	addr := &net.UDPAddr{IP: net.ParseIP(voiceChannelAddr(ch)), Port: voiceChannelPort(ch)}
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
	sendCh := make(chan []byte, 128)

	client := &voiceWSClient{ssrc: ssrc, sendCh: sendCh}

	voiceClientsMu.Lock()
	voiceClients[conn] = client
	voiceClientsMu.Unlock()

	defer func() {
		voiceClientsMu.Lock()
		delete(voiceClients, conn)
		voiceClientsMu.Unlock()
		close(sendCh)
	}()

	go func() {
		for pkt := range sendCh {
			if err := conn.WriteMessage(websocket.BinaryMessage, pkt); err != nil {
				conn.Close()
				return
			}
		}
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
			voiceLastTxCh.Store(int32(ch))
			voiceLastTxAt.Store(time.Now().UnixMilli())
		}

		voiceClientsMu.RLock()
		for c, cl := range voiceClients {
			if c != conn {
				select {
				case cl.sendCh <- rtp:
				default:
				}
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
	if !checkAuth(w, r) {
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
			"addr":    voiceChannelAddr(ch),
			"port":    voiceChannelPort(ch),
		}
	}
	resp := map[string]interface{}{
		"tx_channel":  txCh,
		"rx_channels": rxList,
		"channels":    channels,
	}
	if ltx := voiceLastTxCh.Load(); ltx > 0 {
		resp["last_tx_channel"] = ltx
		resp["last_tx_at"] = voiceLastTxAt.Load()
	}
	if lrx := voiceLastRxCh.Load(); lrx > 0 {
		resp["last_rx_channel"] = lrx
		resp["last_rx_at"] = voiceLastRxAt.Load()
	}
	writeJSON(w, 200, resp)
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
	conf := loadKVFile(MeshConfFile)
	iface := confGet(conf, "voice_iface", "br0")
	port := strconv.Itoa(voiceChannelPort(txCh))
	ptt := confGet(conf, "voice_ptt_mode", "openvlm")
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
