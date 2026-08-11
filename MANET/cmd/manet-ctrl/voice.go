package main

import (
	"encoding/binary"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
)

const (
	voiceWebMcastAddr = "239.69.0.2"
	voiceWebMcastPort = 4371
	voiceWebIface     = "br0"
	voiceFrameBytes   = 640 // 20ms at 16kHz mono int16
)

var (
	voiceClients   = make(map[*websocket.Conn]bool)
	voiceClientsMu sync.RWMutex
)

func handleVoiceWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("voice ws upgrade: %v", err)
		return
	}
	defer conn.Close()

	voiceClientsMu.Lock()
	voiceClients[conn] = true
	voiceClientsMu.Unlock()

	defer func() {
		voiceClientsMu.Lock()
		delete(voiceClients, conn)
		voiceClientsMu.Unlock()
	}()

	mcastIP := net.ParseIP(voiceWebMcastAddr)

	iface, _ := net.InterfaceByName(voiceWebIface)

	txAddr := &net.UDPAddr{IP: mcastIP, Port: voiceWebMcastPort}
	txConn, err := net.DialUDP("udp4", nil, txAddr)
	if err != nil {
		log.Printf("voice tx: %v", err)
		return
	}
	defer txConn.Close()

	rxConn, err := net.ListenMulticastUDP("udp4", iface, &net.UDPAddr{IP: mcastIP, Port: voiceWebMcastPort})
	if err != nil {
		log.Printf("voice rx: %v", err)
		return
	}
	defer rxConn.Close()
	rxConn.SetReadBuffer(256 * 1024)

	var closed atomic.Bool

	// Tag each connection with a 4-byte ID for echo suppression
	connID := make([]byte, 4)
	binary.BigEndian.PutUint32(connID, uint32(uintptr(0xFFFF)&0xFFFFFFFF))
	pidBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(pidBytes, uint32(r.RemoteAddr[0])^uint32(len(voiceClients)))

	// RX: multicast → websocket
	go func() {
		buf := make([]byte, 2048)
		for !closed.Load() {
			n, src, err := rxConn.ReadFromUDP(buf)
			if err != nil {
				if closed.Load() {
					return
				}
				continue
			}
			if n < 4 {
				continue
			}
			// Skip packets from our own tx (same source port check)
			localAddr := txConn.LocalAddr().(*net.UDPAddr)
			if src.IP.Equal(localAddr.IP) && src.Port == localAddr.Port {
				continue
			}
			// Forward PCM payload (skip 4-byte header) to this WS client
			payload := buf[4:n]
			if err := conn.WriteMessage(websocket.BinaryMessage, payload); err != nil {
				return
			}
		}
	}()

	log.Printf("voice ws connected from %s", r.RemoteAddr)

	// TX: websocket → multicast + local broadcast
	for {
		msgType, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if msgType != websocket.BinaryMessage || len(msg) == 0 {
			continue
		}

		// Wrap with 4-byte header (marker) and send to multicast
		pkt := make([]byte, 4+len(msg))
		pkt[0] = 0xAA
		pkt[1] = 0x55
		binary.BigEndian.PutUint16(pkt[2:4], uint16(len(msg)))
		copy(pkt[4:], msg)
		txConn.Write(pkt)

		// Also broadcast to other local WS clients for zero-latency local relay
		voiceClientsMu.RLock()
		for c := range voiceClients {
			if c != conn {
				c.WriteMessage(websocket.BinaryMessage, msg)
			}
		}
		voiceClientsMu.RUnlock()
	}

	closed.Store(true)
	log.Printf("voice ws disconnected %s", r.RemoteAddr)
}
