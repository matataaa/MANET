package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	evdev "github.com/gvalkov/golang-evdev"
	hid "github.com/sstallion/go-hid"
)

// gpioPTTLoop reads Linux evdev key events and sends PTT state on ch.
// pttKey is "any" or a decimal EV_KEY code.
func gpioPTTLoop(ctx context.Context, pttKey string, ch chan<- bool) {
	dev, err := findEvdevGPIO()
	if err != nil {
		log.Printf("GPIO PTT: %v (falling back to always-on)", err)
		ch <- true
		return
	}
	defer dev.File.Close()

	log.Printf("GPIO PTT: reading from %s (%s)", dev.Fn, dev.Name)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		ev, err := dev.ReadOne()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}

		if ev.Type != evdev.EV_KEY {
			continue
		}

		match := false
		if pttKey == "any" {
			match = true
		} else if kc, parseErr := strconv.Atoi(pttKey); parseErr == nil && ev.Code == uint16(kc) {
			match = true
		}

		if !match {
			continue
		}

		switch ev.Value {
		case 1: // press
			log.Printf("GPIO PTT: key %d pressed", ev.Code)
			ch <- true
		case 0: // release
			log.Printf("GPIO PTT: key %d released", ev.Code)
			ch <- false
		}
	}
}

// findEvdevGPIO finds the first evdev device with "gpio" or "button" in its name.
func findEvdevGPIO() (*evdev.InputDevice, error) {
	matches, err := filepath.Glob("/dev/input/event*")
	if err != nil {
		return nil, fmt.Errorf("glob /dev/input/event*: %w", err)
	}

	for _, path := range matches {
		dev, err := evdev.Open(path)
		if err != nil {
			continue
		}
		name := strings.ToLower(dev.Name)
		if strings.Contains(name, "gpio") || strings.Contains(name, "button") || strings.Contains(name, "key") {
			return dev, nil
		}
		dev.File.Close()
	}

	return nil, fmt.Errorf("no GPIO/button evdev device found")
}

// === OpenVLM HID PTT ===

const (
	openvlmVID        uint16 = 0x0D8C
	openvlmPID        uint16 = 0x0012
	openvlmReportSize        = 5
	openvlmGPIO3Mask  byte   = 0x04
)

// openvlmPTTLoop reads HID reports from an OpenVLM USB audio dongle
// and sends PTT state changes based on GPIO3.
func openvlmPTTLoop(ctx context.Context, ch chan<- bool) {
	if err := hid.Init(); err != nil {
		log.Printf("OpenVLM PTT: hid init failed: %v (falling back to always-on)", err)
		ch <- true
		return
	}

	dev, err := hid.Open(openvlmVID, openvlmPID, "")
	if err != nil {
		log.Printf("OpenVLM PTT: device not found: %v (falling back to always-on)", err)
		hid.Exit()
		ch <- true
		return
	}

	var closeOnce sync.Once
	closeDev := func() {
		closeOnce.Do(func() {
			dev.Close()
			hid.Exit()
		})
	}

	go func() {
		<-ctx.Done()
		closeDev()
	}()

	defer closeDev()

	log.Printf("OpenVLM PTT: connected (VID=0x%04X PID=0x%04X)", openvlmVID, openvlmPID)

	// Auto-detect ALSA card for the OpenVLM device
	detectALSACard()

	buf := make([]byte, openvlmReportSize)
	prevGPIO3 := false

	for {
		n, err := dev.Read(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("OpenVLM PTT: read error: %v", err)
			return
		}

		payloadStart := 0
		if n >= openvlmReportSize {
			payloadStart = 1 // skip report ID byte
		}
		if n < payloadStart+2 {
			continue
		}

		ir1 := buf[payloadStart+1]
		gpio3 := (ir1 & openvlmGPIO3Mask) != 0

		if gpio3 == prevGPIO3 {
			continue
		}
		prevGPIO3 = gpio3

		if gpio3 {
			log.Println("OpenVLM PTT: GPIO3 HIGH → TX start")
			ch <- true
		} else {
			log.Println("OpenVLM PTT: GPIO3 LOW → TX stop")
			ch <- false
		}
	}
}

// detectALSACard finds the OpenVLM's ALSA card number and sets ALSA_CARD.
func detectALSACard() {
	if os.Getenv("ALSA_CARD") != "" {
		return
	}

	matches, err := filepath.Glob("/proc/asound/card*/usbid")
	if err != nil || len(matches) == 0 {
		return
	}

	target := fmt.Sprintf("%04x:%04x", openvlmVID, openvlmPID)
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if strings.TrimSpace(strings.ToLower(string(data))) != target {
			continue
		}
		cardDir := filepath.Base(filepath.Dir(path))
		cardNum := strings.TrimPrefix(cardDir, "card")
		if cardNum == cardDir {
			continue
		}
		os.Setenv("ALSA_CARD", cardNum)
		log.Printf("OpenVLM: ALSA card %s auto-detected", cardNum)
		return
	}
}
