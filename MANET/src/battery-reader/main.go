package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	i2cBus       = 1
	readInterval = 30 * time.Second
	outputFile   = "/run/battery_status.json"
)

// Field types match manet-ctrl's BatteryInfo (src/manet-ctrl/config.go)
// exactly — Timestamp as an RFC3339 string in particular, not a Unix int:
// json.Unmarshal into BatteryInfo.Timestamp (*string) errors out on a JSON
// number, which made getBattery() silently discard every reading (Unmarshal
// err != nil, falls through to nothing) even when this file was being
// written correctly. That mismatch predates this rewrite — it affected the
// original HAT (E) reader the same way, which is likely why battery data
// never actually reached the UI even when the E-variant chip was present.
type batteryStatus struct {
	Percentage *int     `json:"percentage"`
	VoltageV   *float64 `json:"voltage_v"`
	CurrentMA  *float64 `json:"current_ma"`
	PowerW     *float64 `json:"power_w"`
	Charging   *bool    `json:"charging"`
	Status     string   `json:"status"`
	CellMV     []int    `json:"cell_mv,omitempty"`
	Timestamp  string   `json:"timestamp"`
}

func nowStamp() string { return time.Now().UTC().Format(time.RFC3339) }

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

// i2cTimeout bounds every transaction. A misbehaving device (bad seating,
// clock-stretching forever) can make the underlying read()/write() syscall
// block indefinitely — confirmed against real hardware: both a plain
// i2cdetect and this reader hung solid on this bus with no device answering
// cleanly. The blocked syscall itself can't be cancelled from Go, so a timed-
// out attempt leaks one goroutine parked in the kernel — cheap, and still far
// better than freezing the whole read loop (and holding the adapter lock)
// forever.
const i2cTimeout = 2 * time.Second

func (b *i2cBusHandle) readBlock(addr byte, reg byte, length int) ([]byte, error) {
	type result struct {
		buf []byte
		err error
	}
	done := make(chan result, 1)

	go func() {
		buf, err := b.readBlockBlocking(addr, reg, length)
		done <- result{buf, err}
	}()

	select {
	case r := <-done:
		return r.buf, r.err
	case <-time.After(i2cTimeout):
		return nil, fmt.Errorf("addr 0x%02X reg 0x%02X: timed out after %s (device not responding)", addr, reg, i2cTimeout)
	}
}

func (b *i2cBusHandle) readBlockBlocking(addr byte, reg byte, length int) ([]byte, error) {
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

func u16be(data []byte, offset int) int {
	return int(data[offset])<<8 | int(data[offset+1])
}

func s16be(data []byte, offset int) int {
	v := u16be(data, offset)
	if v > 0x7FFF {
		return v - 0x10000
	}
	return v
}

// ---------------------------------------------------------------------------
// A battery chip knows its own I2C address and how to turn raw registers
// into a batteryStatus. detectChip() probes each known chip in turn so
// battery_monitor=y works fleet-wide without a config knob for which HAT
// model is physically present.
// ---------------------------------------------------------------------------
type batteryChip interface {
	name() string
	read(bus *i2cBusHandle) (*batteryStatus, error)
}

// ---------------------------------------------------------------------------
// Waveshare UPS HAT (E) — IP2368 power management MCU at I2C 0x2D.
// Register map: see MCU register map in the original upstream implementation
// this was ported from (block reads, little-endian, 4 individually reported
// cells — this is a multi-cell fuel-gauge MCU, not a raw current-sense chip).
// ---------------------------------------------------------------------------
type ip2368Chip struct{}

const ip2368Addr = 0x2D

func (ip2368Chip) name() string { return "IP2368 (UPS HAT E)" }

func (ip2368Chip) read(bus *i2cBusHandle) (*batteryStatus, error) {
	statusData, err := bus.readBlock(ip2368Addr, 0x02, 1)
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

	vbusData, err := bus.readBlock(ip2368Addr, 0x10, 6)
	if err != nil {
		return nil, err
	}
	vbusMW := u16le(vbusData, 4)

	battData, err := bus.readBlock(ip2368Addr, 0x20, 12)
	if err != nil {
		return nil, err
	}
	battMV := u16le(battData, 0)
	battMA := s16le(battData, 2)
	battPct := u16le(battData, 4)

	cellData, err := bus.readBlock(ip2368Addr, 0x30, 8)
	if err != nil {
		return nil, err
	}
	cells := make([]int, 4)
	for i := range 4 {
		cells[i] = u16le(cellData, i*2)
	}

	voltageV := float64(battMV) / 1000
	powerW := float64(vbusMW) / 1000
	battMAf := float64(battMA)
	return &batteryStatus{
		Percentage: &battPct,
		VoltageV:   &voltageV,
		CurrentMA:  &battMAf,
		PowerW:     &powerW,
		Charging:   &charging,
		Status:     status,
		CellMV:     cells,
		Timestamp:  nowStamp(),
	}, nil
}

// ---------------------------------------------------------------------------
// Waveshare UPS HAT (B) — TI INA219 high-side current/power monitor.
//
// Unlike the IP2368, the INA219 is a raw current-sense chip with no fuel
// gauge, no charge-state register, and no per-cell reporting: it only ever
// measures one voltage rail (the battery pack, post-boost-converter on this
// HAT) and the current through one shunt resistor. Percentage and
// charge/discharge status are both derived here, not read from the chip.
//
// Register map (TI INA219 datasheet, address is board-strapped, not
// software-configurable):
//   0x00  Configuration            (write-only setup, 16-bit)
//   0x01  Shunt Voltage            (16-bit signed, LSB = 10 uV)
//   0x02  Bus Voltage              (16-bit; bits[15:3] = voltage in 4 mV
//                                   steps, bit1 = conversion ready, bit0 =
//                                   math overflow)
//   0x03  Power                    (16-bit, needs calibration register set)
//   0x04  Current                  (16-bit signed, needs calibration)
//   0x05  Calibration              (16-bit, programs the current/power LSBs)
//
// Two things below are assumptions, not measurements, because this was
// written without the physical HAT responding on the bus to calibrate
// against (see conversation notes — the unit this was developed against had
// an I2C wiring/seating problem). Verify against a real board and adjust:
//
//   - shuntOhms: 0.1 ohm is the INA219 reference-design default and what
//     most breakout/HAT boards ship, but confirm against the Waveshare
//     schematic — a wrong value scales every current/power reading linearly.
//   - Percentage curve: assumes a 2S Li-ion pack (7.0V empty .. 8.4V full),
//     matching Waveshare UPS HAT (B)'s two 18650 cells in series. If the
//     board reports a single-cell range instead, adjust packEmptyV/packFullV.
// ---------------------------------------------------------------------------
type ina219Chip struct {
	addr byte
}

// Candidate addresses to probe, in order. 0x40 is the INA219 hardware
// default (both address pins to GND); 0x41/0x44/0x45 are the other values
// reachable by strapping only one address pin, which is how most INA219
// HATs (including Waveshare's) select a non-default address to avoid
// clashing with other I2C peripherals on the same header.
var ina219Candidates = []byte{0x40, 0x41, 0x44, 0x45, 0x42, 0x43}

const (
	ina219RegConfig = 0x00
	ina219RegShunt  = 0x01
	ina219RegBus    = 0x02
	ina219RegPower  = 0x03
	ina219RegCurr   = 0x04
	ina219RegCal    = 0x05

	// Calibration: current_LSB chosen for a ~3.2A full-scale range, which
	// covers this HAT's charge/discharge current comfortably with good
	// resolution. cal = trunc(0.04096 / (current_LSB * shuntOhms)).
	shuntOhms   = 0.1
	currentLSB  = 0.0001 // 100 uA/bit -> ~3.2A full scale
	ina219CalRV = 4096   // 0.04096 / (0.0001 * 0.1), see datasheet 8.5.4

	packEmptyV = 7.0 // 2S Li-ion, ~3.5V/cell
	packFullV  = 8.4 // 2S Li-ion, 4.2V/cell
)

func (c ina219Chip) name() string { return fmt.Sprintf("INA219 (UPS HAT B) @0x%02X", c.addr) }

func (c ina219Chip) configure(bus *i2cBusHandle) error {
	done := make(chan error, 1)
	go func() { done <- c.configureBlocking(bus) }()

	select {
	case err := <-done:
		return err
	case <-time.After(i2cTimeout):
		return fmt.Errorf("addr 0x%02X: configure timed out after %s (device not responding)", c.addr, i2cTimeout)
	}
}

func (c ina219Chip) configureBlocking(bus *i2cBusHandle) error {
	f, err := os.OpenFile(bus.path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := ioctl(f.Fd(), 0x0703, uintptr(c.addr)); err != nil {
		return fmt.Errorf("ioctl I2C_SLAVE: %w", err)
	}

	// Config register: 32V bus range, +-320mV shunt range (gain /8),
	// 12-bit ADC, continuous shunt+bus conversion. 0x399F per datasheet
	// table 8.5.1's "reset default" equivalent for this range/gain combo.
	writeReg16 := func(reg byte, val uint16) error {
		_, err := f.Write([]byte{reg, byte(val >> 8), byte(val)})
		return err
	}
	if err := writeReg16(ina219RegConfig, 0x399F); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := writeReg16(ina219RegCal, ina219CalRV); err != nil {
		return fmt.Errorf("write calibration: %w", err)
	}
	return nil
}

func (c ina219Chip) read(bus *i2cBusHandle) (*batteryStatus, error) {
	busData, err := bus.readBlock(c.addr, ina219RegBus, 2)
	if err != nil {
		return nil, err
	}
	busRaw := u16be(busData, 0)
	if busRaw&0x01 != 0 {
		return nil, fmt.Errorf("INA219 math overflow — recalibrate")
	}
	voltageV := float64(busRaw>>3) * 0.004

	currData, err := bus.readBlock(c.addr, ina219RegCurr, 2)
	if err != nil {
		return nil, err
	}
	currentMA := float64(s16be(currData, 0)) * currentLSB * 1000

	powerData, err := bus.readBlock(c.addr, ina219RegPower, 2)
	if err != nil {
		return nil, err
	}
	// Power LSB = 20 * current_LSB per datasheet.
	powerW := float64(u16be(powerData, 0)) * currentLSB * 20

	charging := currentMA > 0
	status := "discharging"
	if charging {
		status = "charging"
	} else if currentMA > -20 {
		status = "idle"
	}

	pct := int((voltageV - packEmptyV) / (packFullV - packEmptyV) * 100)
	if pct < 0 {
		pct = 0
	} else if pct > 100 {
		pct = 100
	}

	return &batteryStatus{
		Percentage: &pct,
		VoltageV:   &voltageV,
		CurrentMA:  &currentMA,
		PowerW:     &powerW,
		Charging:   &charging,
		Status:     status,
		Timestamp:  nowStamp(),
	}, nil
}

// detectChip tries each known battery chip once at startup and keeps
// whichever answers. Runs once — chip identity doesn't change at runtime.
func detectChip(bus *i2cBusHandle) batteryChip {
	ip2368 := ip2368Chip{}
	if _, err := ip2368.read(bus); err == nil {
		log.Printf("Detected %s", ip2368.name())
		return ip2368
	}

	for _, addr := range ina219Candidates {
		chip := ina219Chip{addr: addr}
		if err := chip.configure(bus); err != nil {
			continue
		}
		if _, err := chip.read(bus); err == nil {
			log.Printf("Detected %s", chip.name())
			return chip
		}
	}

	return nil
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

func readMeshConf(key string) string {
	f, err := os.Open("/etc/mesh.conf")
	if err != nil {
		return ""
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, key+"=") {
			return strings.TrimPrefix(line, key+"=")
		}
	}
	return ""
}

var Version = "dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "-version" || os.Args[1] == "--version") {
		fmt.Println(Version)
		return
	}

	log.SetFlags(0)
	log.SetPrefix("[battery-reader] ")
	log.Printf("starting (version %s)", Version)

	if readMeshConf("battery_monitor") != "y" {
		log.Printf("battery_monitor not enabled in /etc/mesh.conf — exiting")
		os.Exit(0)
	}

	const cellLowMV = 3150 // IP2368 per-cell shutdown threshold
	const packLowV = 6.6   // INA219: 2S pack, ~3.3V/cell shutdown threshold

	shutdownTriggered := false
	consecutiveErrors := 0

	var bus *i2cBusHandle
	var chip batteryChip

	for {
		// /dev/i2c-1 may not exist yet even with battery_monitor=y and
		// i2c-dev in /etc/modules-load.d: on a node's very first boot after
		// provisioning, radio-setup.sh appends that entry to the same file
		// systemd-modules-load.service already read earlier in *this* boot,
		// so the device node only appears after the next reboot. Treating
		// that as fatal made this service crash-loop forever (confirmed live
		// on a node with no UPS HAT at all) instead of just waiting like the
		// no-chip-detected case below already does.
		if bus == nil {
			var err error
			bus, err = openBus()
			if err != nil {
				consecutiveErrors++
				log.Printf("I2C bus not available (%d consecutive): %v", consecutiveErrors, err)
				if consecutiveErrors == 1 {
					writeAtomic(outputFile, batteryStatus{
						Status:    "unknown",
						Timestamp: nowStamp(),
					})
				}
				time.Sleep(readInterval)
				continue
			}
			log.Printf("Probing for a battery HAT on I2C bus %d...", i2cBus)
		}

		if chip == nil {
			// Nothing answered at startup; keep retrying detection rather
			// than staying permanently blind if the HAT is hot-plugged or
			// was just a transient bus/wiring issue.
			chip = detectChip(bus)
			if chip == nil {
				log.Printf("No known battery HAT responded (checked IP2368 @0x%02X and INA219 @%v) — reporting unknown and retrying every %s",
					ip2368Addr, ina219Candidates, readInterval)
			}
		}

		var data *batteryStatus
		var err error
		if chip != nil {
			data, err = chip.read(bus)
		} else {
			err = fmt.Errorf("no battery HAT detected")
		}

		if err != nil {
			consecutiveErrors++
			log.Printf("read error (%d consecutive): %v", consecutiveErrors, err)
			if consecutiveErrors == 1 {
				writeAtomic(outputFile, batteryStatus{
					Status:    "unknown",
					Timestamp: nowStamp(),
				})
			}
			time.Sleep(readInterval)
			continue
		}

		consecutiveErrors = 0
		writeAtomic(outputFile, *data)

		log.Printf("%d%% | %.3fV | %.0fmA | %.3fW | %s",
			*data.Percentage, *data.VoltageV, *data.CurrentMA, *data.PowerW, data.Status)

		if !shutdownTriggered && !*data.Charging {
			lowCell := false
			for _, v := range data.CellMV {
				if v > 0 && v < cellLowMV {
					lowCell = true
					break
				}
			}
			lowPack := len(data.CellMV) == 0 && *data.VoltageV > 0 && *data.VoltageV < packLowV
			if lowCell || lowPack {
				log.Printf("CRITICAL: voltage %.3fV — initiating shutdown", *data.VoltageV)
				shutdownTriggered = true
				exec.Command("systemctl", "poweroff").Run()
			}
		}

		time.Sleep(readInterval)
	}
}
