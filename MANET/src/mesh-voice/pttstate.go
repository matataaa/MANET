package main

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

const pttStateFile = "/run/mesh-voice-ptt.json"

type PTTState struct {
	mu        sync.Mutex
	Active    bool   `json:"ptt_active"`
	Connected bool   `json:"ptt_connected"`
	Device    string `json:"ptt_device"`
	TX        bool   `json:"tx"`
	RX        bool   `json:"rx"`
}

var pttState = &PTTState{}

func (s *PTTState) setActive(active bool) {
	s.mu.Lock()
	s.Active = active
	s.mu.Unlock()
	s.write()
}

func (s *PTTState) setConnected(connected bool) {
	s.mu.Lock()
	s.Connected = connected
	s.mu.Unlock()
	s.write()
}

func (s *PTTState) setTX(tx bool) {
	s.mu.Lock()
	s.TX = tx
	s.mu.Unlock()
	s.write()
}

func (s *PTTState) setRX(rx bool) {
	s.mu.Lock()
	s.RX = rx
	s.mu.Unlock()
	s.write()
}

func (s *PTTState) init(device string) {
	s.mu.Lock()
	s.Device = device
	s.mu.Unlock()
	s.write()
}

func (s *PTTState) write() {
	s.mu.Lock()
	data, _ := json.Marshal(s)
	s.mu.Unlock()
	os.WriteFile(pttStateFile, data, 0644)
}

func (s *PTTState) cleanup() {
	os.Remove(pttStateFile)
}

func timerChan(ms int) <-chan time.Time {
	return time.After(time.Duration(ms) * time.Millisecond)
}
