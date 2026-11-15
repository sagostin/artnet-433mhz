// Package cc1101 drives a TI CC1101 as an OOK packet-mode transmitter.
//
// The chip clocks a pre-encoded chip stream out of its TX FIFO at 1/TE baud
// from its crystal, so pulse timing has zero Linux scheduler jitter. No
// preamble, sync word, whitening or CRC: the FIFO bytes ARE the waveform.
package cc1101

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// SPI is the transport (spidev.Dev satisfies it).
type SPI interface {
	Tx(w []byte) ([]byte, error)
}

const xosc = 26e6

// config registers
const (
	rIOCFG2   = 0x00
	rIOCFG0   = 0x02
	rFIFOTHR  = 0x03
	rPKTLEN   = 0x06
	rPKTCTRL1 = 0x07
	rPKTCTRL0 = 0x08
	rFSCTRL1  = 0x0B
	rFSCTRL0  = 0x0C
	rFREQ2    = 0x0D
	rMDMCFG4  = 0x10
	rMDMCFG3  = 0x11
	rMDMCFG2  = 0x12
	rMDMCFG1  = 0x13
	rMDMCFG0  = 0x14
	rMCSM1    = 0x17
	rMCSM0    = 0x18
	rFREND1   = 0x21
	rFREND0   = 0x22
	rFSCAL3   = 0x23
	rTEST2    = 0x2C
)

// strobes, status registers, FIFO/PATABLE
const (
	sSRES      = 0x30
	sSTX       = 0x35
	sSIDLE     = 0x36
	sSFTX      = 0x3B
	stPARTNUM  = 0x30
	stVERSION  = 0x31
	stMARCSTAT = 0x35
	stTXBYTES  = 0x3A
	aPATABLE   = 0x3E
	aFIFO      = 0x3F

	fRead  = 0x80
	fBurst = 0x40

	fifoSize      = 64
	marcIdle      = 0x01
	marcTxUnderfl = 0x16
)

type Dev struct {
	spi  SPI
	te   time.Duration
	baud float64
}

func New(spi SPI) *Dev { return &Dev{spi: spi} }

// ID returns PARTNUM, VERSION. A healthy chip reads 0x00, 0x14 (some 0x04).
func (d *Dev) ID() (part, ver byte, err error) {
	if part, err = d.status(stPARTNUM); err != nil {
		return
	}
	ver, err = d.status(stVERSION)
	return
}

// Reset strobes SRES and waits for the crystal to settle.
func (d *Dev) Reset() error {
	if _, err := d.spi.Tx([]byte{sSRES}); err != nil {
		return err
	}
	time.Sleep(2 * time.Millisecond)
	return nil
}

// ConfigureOOK sets up async-free OOK TX at freqHz with one chip per te.
// power is the PATABLE "on" byte (0xC0 ≈ +10 dBm at 433 MHz, 0x60 ≈ 0 dBm).
func (d *Dev) ConfigureOOK(freqHz float64, te time.Duration, power byte) error {
	if err := d.Reset(); err != nil {
		return err
	}
	baud := 1 / te.Seconds()
	e, m, err := drate(baud)
	if err != nil {
		return err
	}
	d.baud = (256 + float64(m)) * math.Pow(2, float64(e)) * xosc / (1 << 28)
	d.te = time.Duration(float64(time.Second) / d.baud)

	fw := uint32(math.Round(freqHz * 65536 / xosc))
	regs := []struct{ a, v byte }{
		{rIOCFG2, 0x29},   // CHIP_RDYn
		{rIOCFG0, 0x06},   // asserts during packet TX (spare, for scope/debug)
		{rFIFOTHR, 0x07},  // TX threshold 33 bytes
		{rPKTCTRL1, 0x00}, // no address check, no status append
		{rPKTCTRL0, 0x00}, // fixed length, FIFO mode, no whitening/CRC
		{rFSCTRL1, 0x06},
		{rFSCTRL0, 0x00},
		{rFREQ2, byte(fw >> 16)},
		{rFREQ2 + 1, byte(fw >> 8)},
		{rFREQ2 + 2, byte(fw)},
		{rMDMCFG4, 0x80 | e}, // RX BW 203 kHz (irrelevant for TX) | DRATE_E
		{rMDMCFG3, m},        // DRATE_M
		{rMDMCFG2, 0x30},     // ASK/OOK, no preamble/sync, no Manchester
		{rMDMCFG1, 0x02},
		{rMDMCFG0, 0xF8},
		{rMCSM1, 0x30}, // after TX -> IDLE
		{rMCSM0, 0x18}, // autocal IDLE->TX
		{rFREND1, 0x56},
		{rFREND0, 0x11}, // PA_POWER=1: bit 0 -> PATABLE[0], bit 1 -> PATABLE[1]
		{rFSCAL3, 0xE9}, // SmartRF 433 MHz recommendations
		{rFSCAL3 + 1, 0x2A},
		{rFSCAL3 + 2, 0x00},
		{rFSCAL3 + 3, 0x1F},
		{rTEST2, 0x81},
		{rTEST2 + 1, 0x35},
		{rTEST2 + 2, 0x09},
	}
	for _, r := range regs {
		if _, err := d.spi.Tx([]byte{r.a, r.v}); err != nil {
			return err
		}
	}
	if _, err := d.spi.Tx([]byte{aPATABLE | fBurst, 0x00, power}); err != nil {
		return err
	}
	// verify a couple of writes landed (catches MISO/CS wiring faults)
	if v, _ := d.read(rMDMCFG2); v != 0x30 {
		return fmt.Errorf("cc1101: MDMCFG2 readback %#02x, want 0x30 (check SPI wiring/CS)", v)
	}
	return nil
}

// ActualTE is the chip period the data-rate registers actually produce.
func (d *Dev) ActualTE() time.Duration { return d.te }

// Transmit sends up to 255 bytes as one fixed-length packet, refilling the
// 64-byte FIFO as it drains, and blocks until the radio returns to IDLE.
func (d *Dev) Transmit(data []byte) error {
	if len(data) == 0 || len(data) > 255 {
		return fmt.Errorf("cc1101: packet %d bytes (1..255)", len(data))
	}
	if d.baud == 0 {
		return errors.New("cc1101: not configured")
	}
	d.spi.Tx([]byte{sSIDLE})
	d.spi.Tx([]byte{sSFTX})
	if _, err := d.spi.Tx([]byte{rPKTLEN, byte(len(data))}); err != nil {
		return err
	}
	n := min(len(data), fifoSize)
	if err := d.fifo(data[:n]); err != nil {
		return err
	}
	if _, err := d.spi.Tx([]byte{sSTX}); err != nil {
		return err
	}
	airtime := time.Duration(float64(len(data)*8)/d.baud*float64(time.Second)) + 50*time.Millisecond
	deadline := time.Now().Add(airtime + 100*time.Millisecond)
	for n < len(data) {
		tb, err := d.status(stTXBYTES)
		if err != nil {
			return err
		}
		if tb&0x80 != 0 {
			d.recover()
			return errors.New("cc1101: TX FIFO underflow")
		}
		if free := fifoSize - int(tb&0x7F); free >= 16 {
			k := min(free, len(data)-n)
			if err := d.fifo(data[n : n+k]); err != nil {
				return err
			}
			n += k
		}
		if time.Now().After(deadline) {
			d.recover()
			return errors.New("cc1101: TX refill timeout")
		}
		time.Sleep(2 * time.Millisecond) // ~3.2 ms per byte at 2.5 kbaud
	}
	for {
		st, err := d.status(stMARCSTAT)
		if err != nil {
			return err
		}
		switch st & 0x1F {
		case marcIdle:
			return nil
		case marcTxUnderfl:
			d.recover()
			return errors.New("cc1101: TX underflow")
		}
		if time.Now().After(deadline) {
			d.recover()
			return fmt.Errorf("cc1101: TX did not finish (MARCSTATE %#02x)", st)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (d *Dev) recover() {
	d.spi.Tx([]byte{sSIDLE})
	d.spi.Tx([]byte{sSFTX})
}

func (d *Dev) fifo(b []byte) error {
	_, err := d.spi.Tx(append([]byte{aFIFO | fBurst}, b...))
	return err
}

func (d *Dev) read(a byte) (byte, error) {
	r, err := d.spi.Tx([]byte{a | fRead, 0})
	if err != nil {
		return 0, err
	}
	return r[1], nil
}

// status reads a status register; re-reads until two agree (CC1101 errata
// for TXBYTES/MARCSTATE while the value is changing).
func (d *Dev) status(a byte) (byte, error) {
	var prev byte
	for i := 0; i < 8; i++ {
		r, err := d.spi.Tx([]byte{a | fRead | fBurst, 0})
		if err != nil {
			return 0, err
		}
		if i > 0 && r[1] == prev {
			return prev, nil
		}
		prev = r[1]
	}
	return prev, nil
}

// drate finds DRATE_E/M for the target baud (datasheet §12).
func drate(baud float64) (e, m byte, err error) {
	if baud < 600 || baud > 500e3 {
		return 0, 0, fmt.Errorf("cc1101: baud %.0f out of range (TE too long/short)", baud)
	}
	ex := math.Floor(math.Log2(baud * (1 << 20) / xosc))
	mm := math.Round(baud*(1<<28)/(xosc*math.Pow(2, ex))) - 256
	if mm >= 256 {
		mm, ex = 0, ex+1
	}
	return byte(ex), byte(mm), nil
}
