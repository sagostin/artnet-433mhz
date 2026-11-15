// Package radio glues the profile, OOK encoder and CC1101 into a Sender.
package radio

import (
	"fmt"
	"log"
	"time"

	"dmxbridge/internal/cc1101"
	"dmxbridge/internal/ook"
	"dmxbridge/internal/profile"
	"dmxbridge/internal/spidev"
)

type Radio struct {
	spi     *spidev.Dev
	cc      *cc1101.Dev
	proto   ook.Proto
	repeats int
	log     *log.Logger
}

func Open(p *profile.Profile, l *log.Logger) (*Radio, error) {
	spi, err := spidev.Open(p.Radio.SPI, p.Radio.SpeedHz, p.Radio.CSGPIO)
	if err != nil {
		return nil, err
	}
	cc := cc1101.New(spi)
	part, ver, err := cc.ID()
	if err != nil {
		spi.Close()
		return nil, err
	}
	if part != 0x00 || (ver != 0x14 && ver != 0x04) {
		spi.Close()
		return nil, fmt.Errorf("cc1101 not found: PARTNUM %#02x VERSION %#02x (want 0x00 0x14) — check wiring/CS", part, ver)
	}
	if err := cc.ConfigureOOK(float64(p.Radio.Freq), p.Protocol.TE, p.Radio.Power); err != nil {
		spi.Close()
		return nil, err
	}
	l.Printf("cc1101 v%#02x on %s: %.3f MHz, TE %s (actual %s), power %#02x",
		ver, p.Radio.SPI, float64(p.Radio.Freq)/1e6, p.Protocol.TE, cc.ActualTE().Round(100*time.Nanosecond), p.Radio.Power)
	return &Radio{spi: spi, cc: cc, proto: p.OOK(), repeats: p.Protocol.Repeats, log: l}, nil
}

func (r *Radio) Send(key uint64) error {
	return r.SendN(key, r.repeats)
}

func (r *Radio) SendN(key uint64, repeats int) error {
	return r.cc.Transmit(ook.Pack(r.proto.Chips(key, repeats)))
}

func (r *Radio) Close() error { return r.spi.Close() }

// DryRun logs keys instead of transmitting, with realistic airtime.
type DryRun struct {
	Proto   ook.Proto
	Repeats int
	Log     *log.Logger
}

func (d DryRun) Send(key uint64) error {
	chips := d.Proto.Chips(key, d.Repeats)
	air := time.Duration(len(chips)) * d.Proto.TE
	d.Log.Printf("[dry-run] tx %08X (%d frames, %s)", key, d.Repeats, air.Round(time.Millisecond))
	time.Sleep(air)
	return nil
}
