package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

const echoSSRC = 0xEC40EC40

func main() {
	iface := flag.String("iface", "br0", "network interface for multicast")
	addr := flag.String("addr", "239.69.0.1", "multicast group address")
	port := flag.Int("port", 4370, "multicast RTP port")
	delayMs := flag.Int("delay", 300, "echo delay in milliseconds")
	flag.Parse()

	if err := run(*iface, *addr, *port, *delayMs); err != nil {
		log.Fatalf("voice-echo: %v", err)
	}
}

func run(ifaceName, mcastAddr string, mcastPort, delayMs int) error {
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return fmt.Errorf("interface %s: %w", ifaceName, err)
	}

	mcastIP := net.ParseIP(mcastAddr)
	if mcastIP == nil {
		return fmt.Errorf("invalid multicast address: %s", mcastAddr)
	}

	rxConn, err := net.ListenMulticastUDP("udp4", iface, &net.UDPAddr{IP: mcastIP, Port: mcastPort})
	if err != nil {
		return fmt.Errorf("rx listen: %w", err)
	}
	defer rxConn.Close()
	rxConn.SetReadBuffer(1024 * 1024)

	txConn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: mcastIP, Port: mcastPort})
	if err != nil {
		return fmt.Errorf("tx dial: %w", err)
	}
	defer txConn.Close()

	if rc, err := txConn.SyscallConn(); err == nil {
		rc.Control(func(fd uintptr) {
			syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_TOS, 0xB8)
		})
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	delay := time.Duration(delayMs) * time.Millisecond
	var mu sync.Mutex
	var queue []echoPacket

	// Echo sender — drains the queue and sends delayed packets
	go func() {
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			now := time.Now()
			mu.Lock()
			sent := 0
			for sent < len(queue) && now.After(queue[sent].sendAt) {
				pkt := queue[sent]
				sent++
				mu.Unlock()
				txConn.Write(pkt.data)
				mu.Lock()
			}
			if sent > 0 {
				queue = append([]echoPacket(nil), queue[sent:]...)
			}
			mu.Unlock()
		}
	}()

	log.Printf("voice-echo listening on %s:%d via %s (delay=%dms)", mcastAddr, mcastPort, ifaceName, delayMs)

	go func() {
		buf := make([]byte, 2048)
		for {
			n, _, err := rxConn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if n < 12 {
				continue
			}

			pktSSRC := binary.BigEndian.Uint32(buf[8:12])
			if pktSSRC == echoSSRC {
				continue
			}

			echo := make([]byte, n)
			copy(echo, buf[:n])
			binary.BigEndian.PutUint32(echo[8:12], echoSSRC)

			mu.Lock()
			queue = append(queue, echoPacket{
				data:   echo,
				sendAt: time.Now().Add(delay),
			})
			mu.Unlock()
		}
	}()

	<-sigCh
	log.Println("voice-echo shutting down")
	return nil
}

type echoPacket struct {
	data   []byte
	sendAt time.Time
}
