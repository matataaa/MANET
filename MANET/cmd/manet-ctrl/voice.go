package main

import (
	"encoding/binary"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
)

const (
	voiceMcastAddr = "239.69.0.1"
	voiceMcastPort = 4370
	voiceIface     = "br0"
	rtpHeaderSize  = 12
)

func handleVoiceWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("voice ws upgrade: %v", err)
		return
	}
	defer conn.Close()

	mcastIP := net.ParseIP(voiceMcastAddr)
	ssrc := uint32(os.Getpid()&0xFFFF)<<16 | uint32(os.Getuid()&0xFFFF)

	iface, err := net.InterfaceByName(voiceIface)
	if err != nil {
		iface = nil
	}

	txAddr := &net.UDPAddr{IP: mcastIP, Port: voiceMcastPort}
	txConn, err := net.DialUDP("udp4", nil, txAddr)
	if err != nil {
		log.Printf("voice tx: %v", err)
		return
	}
	defer txConn.Close()

	rxConn, err := net.ListenMulticastUDP("udp4", iface, &net.UDPAddr{IP: mcastIP, Port: voiceMcastPort})
	if err != nil {
		log.Printf("voice rx: %v", err)
		return
	}
	defer rxConn.Close()
	rxConn.SetReadBuffer(512 * 1024)

	var closed atomic.Bool
	var seqNum uint16
	var wmu sync.Mutex

	// RX: multicast → websocket
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
			if n < rtpHeaderSize {
				continue
			}
			pktSSRC := binary.BigEndian.Uint32(buf[8:12])
			if pktSSRC == ssrc {
				continue
			}
			wmu.Lock()
			err = conn.WriteMessage(websocket.BinaryMessage, buf[:n])
			wmu.Unlock()
			if err != nil {
				return
			}
		}
	}()

	// TX: websocket → multicast
	for {
		msgType, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if msgType != websocket.BinaryMessage {
			continue
		}
		if len(msg) == 0 {
			continue
		}

		// Client sends raw Opus frames; we wrap in RTP
		pkt := make([]byte, rtpHeaderSize+len(msg))
		pkt[0] = 0x80
		pkt[1] = 111
		binary.BigEndian.PutUint16(pkt[2:4], seqNum)
		ts := uint32(seqNum) * 960
		binary.BigEndian.PutUint32(pkt[4:8], ts)
		binary.BigEndian.PutUint32(pkt[8:12], ssrc)
		copy(pkt[rtpHeaderSize:], msg)
		seqNum++

		txConn.Write(pkt)
	}

	closed.Store(true)
	log.Printf("voice ws ended")
}
