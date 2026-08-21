package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"time"
)

const (
	gpsdAddr       = "127.0.0.1:2947"
	gpsdTimeout    = 10 * time.Second
	pollInterval   = 5 * time.Second
	gpsStatusPath  = "/run/gps_status.json"
)

type gpsStatus struct {
	HasFix    bool    `json:"has_fix"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Altitude  float64 `json:"altitude"`
	HDOP      float64 `json:"hdop"`
	Timestamp int64   `json:"timestamp"`
}

type tpvMessage struct {
	Class string  `json:"class"`
	Mode  int     `json:"mode"`
	Lat   float64 `json:"lat"`
	Lon   float64 `json:"lon"`
	Alt   float64 `json:"alt"`
	HDOP  float64 `json:"hdop"`
}

func queryGPSD() *tpvMessage {
	conn, err := net.DialTimeout("tcp", gpsdAddr, 5*time.Second)
	if err != nil {
		return nil
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(gpsdTimeout))
	scanner := bufio.NewScanner(conn)

	// discard VERSION banner
	if !scanner.Scan() {
		return nil
	}

	fmt.Fprintf(conn, "?WATCH={\"enable\":true,\"json\":true}\n")

	deadline := time.Now().Add(gpsdTimeout)
	for time.Now().Before(deadline) {
		if !scanner.Scan() {
			break
		}
		var msg tpvMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		if msg.Class == "TPV" {
			return &msg
		}
	}
	return nil
}

func writeStatus(s gpsStatus) {
	data, err := json.Marshal(s)
	if err != nil {
		log.Printf("marshal error: %v", err)
		return
	}
	tmp := gpsStatusPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		log.Printf("write error: %v", err)
		return
	}
	os.Rename(tmp, gpsStatusPath)
}

var Version = "dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "-version" || os.Args[1] == "--version") {
		fmt.Println(Version)
		return
	}

	log.SetFlags(0)
	log.SetPrefix("[gps-reader] ")
	log.Printf("Starting GPS reader daemon (version %s).", Version)

	for {
		tpv := queryGPSD()
		if tpv == nil {
			writeStatus(gpsStatus{HDOP: 99.9, Timestamp: time.Now().Unix()})
		} else {
			hasFix := tpv.Mode >= 2
			hdop := tpv.HDOP
			if hdop == 0 {
				hdop = 99.9
			}
			s := gpsStatus{
				HasFix:    hasFix,
				HDOP:      hdop,
				Timestamp: time.Now().Unix(),
			}
			if hasFix {
				s.Latitude = tpv.Lat
				s.Longitude = tpv.Lon
				s.Altitude = tpv.Alt
			}
			writeStatus(s)
		}
		time.Sleep(pollInterval)
	}
}
