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
	voiceRxOnce    sync.Once
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

func voiceStartRx() {
	voiceRxOnce.Do(func() {
		iface, _ := net.InterfaceByName(voiceMcastIface)
		mcastIP := net.ParseIP(voiceMcastAddr)
		rxConn, err := net.ListenMulticastUDP("udp4", iface, &net.UDPAddr{IP: mcastIP, Port: voiceMcastPort})
		if err != nil {
			log.Printf("voice shared rx: %v", err)
			return
		}
		rxConn.SetReadBuffer(1024 * 1024)

		go func() {
			buf := make([]byte, 2048)
			for {
				n, _, err := rxConn.ReadFromUDP(buf)
				if err != nil {
					continue
				}
				if n < 12 {
					continue
				}

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
		}()

		log.Printf("voice shared rx listener started on %s:%d", voiceMcastAddr, voiceMcastPort)
	})
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

	voiceStartRx()

	txAddr := &net.UDPAddr{IP: net.ParseIP(voiceMcastAddr), Port: voiceMcastPort}
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

	log.Printf("voice ws connected from %s (ssrc=%08x)", r.RemoteAddr, ssrc)

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
