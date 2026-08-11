package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"time"
)

const (
	i2cBus       = 1
	mcuAddr      = 0x2D
	readInterval = 30 * time.Second
	outputFile   = "/run/battery_status.json"
	cellLowMV    = 3150
)

type batteryStatus struct {
	Percentage *int     `json:"percentage"`
	VoltageV   *float64 `json:"voltage_v"`
	CurrentMA  *int     `json:"current_ma"`
	PowerW     *float64 `json:"power_w"`
	Charging   *bool    `json:"charging"`
	Status     string   `json:"status"`
	CellMV     []int    `json:"cell_mv"`
	Timestamp  int64    `json:"timestamp"`
}

type i2cBusHandle struct {
	path string
}

func openBus() (*i2cBusHandle, error) {
	path := fmt.Sprintf("/dev/i2c-%d", i2cBus)
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("I2C bus %s not found: %w", path, err)
	}
	return &i2cBusHandle{path: path}, nil
}

func (b *i2cBusHandle) readBlock(addr byte, reg byte, length int) ([]byte, error) {
	f, err := os.OpenFile(b.path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// ioctl I2C_SLAVE = 0x0703
	if err := ioctl(f.Fd(), 0x0703, uintptr(addr)); err != nil {
		return nil, fmt.Errorf("ioctl I2C_SLAVE: %w", err)
	}

	if _, err := f.Write([]byte{reg}); err != nil {
		return nil, fmt.Errorf("write register: %w", err)
	}

	buf := make([]byte, length)
	if _, err := f.Read(buf); err != nil {
		return nil, fmt.Errorf("read data: %w", err)
	}
	return buf, nil
}

func u16le(data []byte, offset int) int {
	return int(data[offset]) | int(data[offset+1])<<8
}

func s16le(data []byte, offset int) int {
	v := u16le(data, offset)
	if v > 0x7FFF {
		return v - 0x10000
	}
	return v
}

func readBattery(bus *i2cBusHandle) (*batteryStatus, error) {
	statusData, err := bus.readBlock(mcuAddr, 0x02, 1)
	if err != nil {
		return nil, err
	}
	sb := statusData[0]

	var status string
	var charging bool
	switch {
	case sb&0x40 != 0:
		status, charging = "fast_charging", true
	case sb&0x80 != 0:
		status, charging = "charging", true
	case sb&0x20 != 0:
		status, charging = "discharging", false
	default:
		status, charging = "idle", false
	}

	vbusData, err := bus.readBlock(mcuAddr, 0x10, 6)
	if err != nil {
		return nil, err
	}
	vbusMW := u16le(vbusData, 4)

	battData, err := bus.readBlock(mcuAddr, 0x20, 12)
	if err != nil {
		return nil, err
	}
	battMV := u16le(battData, 0)
	battMA := s16le(battData, 2)
	battPct := u16le(battData, 4)

	cellData, err := bus.readBlock(mcuAddr, 0x30, 8)
	if err != nil {
		return nil, err
	}
	cells := make([]int, 4)
	for i := range 4 {
		cells[i] = u16le(cellData, i*2)
	}

	voltageV := float64(battMV) / 1000
	powerW := float64(vbusMW) / 1000
	return &batteryStatus{
		Percentage: &battPct,
		VoltageV:   &voltageV,
		CurrentMA:  &battMA,
		PowerW:     &powerW,
		Charging:   &charging,
		Status:     status,
		CellMV:     cells,
		Timestamp:  time.Now().Unix(),
	}, nil
}

func writeAtomic(path string, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		log.Printf("marshal error: %v", err)
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		log.Printf("write error: %v", err)
		return
	}
	os.Rename(tmp, path)
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("[battery-reader] ")
	log.Printf("Starting — I2C bus %d MCU addr 0x%02X", i2cBus, mcuAddr)

	bus, err := openBus()
	if err != nil {
		log.Fatalf("Failed to open I2C bus: %v", err)
	}

	shutdownTriggered := false
	consecutiveErrors := 0

	for {
		data, err := readBattery(bus)
		if err != nil {
			consecutiveErrors++
			log.Printf("I2C read error (%d consecutive): %v", consecutiveErrors, err)
			if consecutiveErrors == 1 {
				writeAtomic(outputFile, batteryStatus{
					Status:    "unknown",
					Timestamp: time.Now().Unix(),
				})
			}
			time.Sleep(readInterval)
			continue
		}

		consecutiveErrors = 0
		writeAtomic(outputFile, data)

		log.Printf("%d%% | %.3fV | %dmA | %.3fW | %s | cells=%v",
			*data.Percentage, *data.VoltageV, *data.CurrentMA,
			*data.PowerW, data.Status, data.CellMV)

		if !shutdownTriggered && !*data.Charging {
			for _, v := range data.CellMV {
				if v > 0 && v < cellLowMV {
					log.Printf("CRITICAL: Cell voltage %d mV — initiating shutdown", v)
					shutdownTriggered = true
					exec.Command("systemctl", "poweroff").Run()
					break
				}
			}
		}

		time.Sleep(readInterval)
	}
}
