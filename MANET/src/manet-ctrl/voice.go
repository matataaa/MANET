package main

import (
	cryptoRand "crypto/rand"
	"encoding/binary"
	"hash/crc32"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/gorilla/websocket"
)

const (
	voiceMcastAddr  = "239.69.0.1"
	voiceMcastPort  = 4370
	voiceMcastIface = "br0"
)

var (
	voiceClients   = make(map[*websocket.Conn]uint32)
	voiceClientsMu sync.RWMutex
	voiceSSRCSeq   uint32
)

func init() {
	var seed [2]byte
	cryptoRand.Read(seed[:])
	base := uint32(seed[0])<<8 | uint32(seed[1])
	if iface, err := net.InterfaceByName(voiceMcastIface); err == nil && len(iface.HardwareAddr) >= 4 {
		base ^= crc32.ChecksumIEEE(iface.HardwareAddr)
	}
	voiceSSRCSeq = base << 16
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

	iface, _ := net.InterfaceByName(voiceMcastIface)
	mcastIP := net.ParseIP(voiceMcastAddr)

	txAddr := &net.UDPAddr{IP: mcastIP, Port: voiceMcastPort}
	txConn, err := net.DialUDP("udp4", nil, txAddr)
	if err != nil {
		log.Printf("voice tx: %v", err)
		return
	}
	defer txConn.Close()
	if rc, err := txConn.SyscallConn(); err == nil {
		rc.Control(func(fd uintptr) {
			syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_TOS, 0xB8)
		})
	}

	rxConn, err := net.ListenMulticastUDP("udp4", iface, &net.UDPAddr{IP: mcastIP, Port: voiceMcastPort})
	if err != nil {
		log.Printf("voice rx: %v", err)
		return
	}
	defer rxConn.Close()
	rxConn.SetReadBuffer(256 * 1024)

	var closed atomic.Bool

	// RX: multicast RTP → websocket
	go func() {
		buf := make([]byte, 2048)
		for !closed.Load() {
			n, _, err := rxConn.ReadFromUDP(buf)
			if err != nil {
				if closed.Load() {
					return
				}
				continue
			}
			if n < 12 {
				continue
			}

			pktSSRC := binary.BigEndian.Uint32(buf[8:12])

			if pktSSRC == ssrc {
				continue
			}

			// Skip packets from other web clients on this server
			voiceClientsMu.RLock()
			skip := false
			for _, cs := range voiceClients {
				if cs == pktSSRC {
					skip = true
					break
				}
			}
			voiceClientsMu.RUnlock()
			if skip {
				continue
			}

			pkt := make([]byte, n)
			copy(pkt, buf[:n])
			if err := conn.WriteMessage(websocket.BinaryMessage, pkt); err != nil {
				return
			}
		}
	}()

	log.Printf("voice ws connected from %s (ssrc=%08x)", r.RemoteAddr, ssrc)

	// TX: websocket opus frames → wrap in RTP → multicast + local relay
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
		txConn.Write(rtp)

		voiceClientsMu.RLock()
		for c := range voiceClients {
			if c != conn {
				c.WriteMessage(websocket.BinaryMessage, rtp)
			}
		}
		voiceClientsMu.RUnlock()
	}

	closed.Store(true)
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
