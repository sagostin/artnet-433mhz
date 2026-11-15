//go:build linux

// Package spidev is a minimal Linux spidev driver with optional software
// chip-select on a GPIO line (for boards whose spidev overlay CS is broken,
// e.g. Orange Pi Zero 3 on some Armbian kernels).
package spidev

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"github.com/warthog618/go-gpiocdev"
	"golang.org/x/sys/unix"
)

const (
	iocWrMode   = 0x40016b01 // SPI_IOC_WR_MODE
	iocWrBits   = 0x40016b03 // SPI_IOC_WR_BITS_PER_WORD
	iocWrMaxHz  = 0x40046b04 // SPI_IOC_WR_MAX_SPEED_HZ
	iocMessage1 = 0x40206b00 // SPI_IOC_MESSAGE(1)
	modeNoCS    = 0x40       // SPI_NO_CS
)

// struct spi_ioc_transfer
type xfer struct {
	txBuf, rxBuf uint64
	len, speedHz uint32
	delayUsecs   uint16
	bitsPerWord  uint8
	csChange     uint8
	txNbits      uint8
	rxNbits      uint8
	wordDelay    uint8
	pad          uint8
}

type Dev struct {
	f     *os.File
	speed uint32
	cs    *gpiocdev.Line
}

// Open opens path at speedHz, SPI mode 0. csGPIO is "" for hardware CS, or
// "chip:offset" (e.g. "gpiochip0:74" = PC10, header pin 26 on the Zero 3)
// to drive CS from a GPIO line instead.
func Open(path string, speedHz uint32, csGPIO string) (*Dev, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	d := &Dev{f: f, speed: speedHz}
	mode := 0
	if csGPIO != "" {
		chip, off, ok := strings.Cut(csGPIO, ":")
		n, err := strconv.Atoi(off)
		if !ok || err != nil {
			f.Close()
			return nil, fmt.Errorf("cs_gpio %q: want chip:offset", csGPIO)
		}
		if d.cs, err = gpiocdev.RequestLine(chip, n, gpiocdev.AsOutput(1), gpiocdev.WithConsumer("dmxbridge-cs")); err != nil {
			f.Close()
			return nil, fmt.Errorf("cs_gpio %s: %w", csGPIO, err)
		}
		mode |= modeNoCS
	}
	fd := int(f.Fd())
	for _, s := range []struct {
		req uint
		v   int
	}{{iocWrMode, mode}, {iocWrBits, 8}, {iocWrMaxHz, int(speedHz)}} {
		if err := unix.IoctlSetPointerInt(fd, s.req, s.v); err != nil {
			d.Close()
			return nil, fmt.Errorf("spidev setup ioctl %#x: %w", s.req, err)
		}
	}
	return d, nil
}

// Tx does one full-duplex transfer with CS held low for its duration.
func (d *Dev) Tx(w []byte) ([]byte, error) {
	if len(w) == 0 {
		return nil, nil
	}
	r := make([]byte, len(w))
	x := xfer{
		txBuf:       uint64(uintptr(unsafe.Pointer(&w[0]))),
		rxBuf:       uint64(uintptr(unsafe.Pointer(&r[0]))),
		len:         uint32(len(w)),
		speedHz:     d.speed,
		bitsPerWord: 8,
	}
	if d.cs != nil {
		if err := d.cs.SetValue(0); err != nil {
			return nil, err
		}
		time.Sleep(20 * time.Microsecond) // CC1101: let SO drop (chip ready)
	}
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, d.f.Fd(), iocMessage1, uintptr(unsafe.Pointer(&x)))
	runtime.KeepAlive(w)
	runtime.KeepAlive(r)
	if d.cs != nil {
		d.cs.SetValue(1)
	}
	if errno != 0 {
		return nil, fmt.Errorf("spi transfer: %w", errno)
	}
	return r, nil
}

func (d *Dev) Close() error {
	if d.cs != nil {
		d.cs.Close()
	}
	return d.f.Close()
}
