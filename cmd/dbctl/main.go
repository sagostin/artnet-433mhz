// dbctl: bring-up and tooling for dmxbridge.
//
//	dbctl probe                     read CC1101 chip ID
//	dbctl loopback                  SPI self-test (jumper MOSI to MISO)
//	dbctl decode FILE.sub...        decode Flipper RAW captures into keys
//	dbctl send -cmd 0x01            transmit a command (or -key 0x7CF001FE)
//	dbctl qxf > fog.qxf             QLC+ fixture definition from the profile
package main

import (
	"encoding/xml"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"dmxbridge/internal/cc1101"
	"dmxbridge/internal/flipper"
	"dmxbridge/internal/profile"
	"dmxbridge/internal/radio"
	"dmxbridge/internal/spidev"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  dbctl probe  [-spi /dev/spidev1.1] [-cs-gpio gpiochip0:74]
  dbctl loopback [-spi /dev/spidev1.1]   (module off, jumper pin 19 to 21)
  dbctl decode FILE.sub...
  dbctl send   [-profile P] (-cmd 0x01 | -key 0x7CF001FE) [-repeats N]
  dbctl qxf    [-profile P]`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	l := log.New(os.Stderr, "", 0)
	args := os.Args[2:]
	switch os.Args[1] {
	case "probe":
		probe(l, args)
	case "loopback":
		loopback(l, args)
	case "decode":
		decode(l, args)
	case "send":
		send(l, args)
	case "qxf":
		qxf(l, args)
	default:
		usage()
	}
}

func probe(l *log.Logger, args []string) {
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	dev := fs.String("spi", "/dev/spidev1.1", "spidev device")
	cs := fs.String("cs-gpio", "", "software CS line, e.g. gpiochip0:74")
	fs.Parse(args)
	s, err := spidev.Open(*dev, 500_000, *cs)
	if err != nil {
		l.Fatal(err)
	}
	defer s.Close()
	cc := cc1101.New(s)
	cc.Reset()
	part, ver, err := cc.ID()
	if err != nil {
		l.Fatal(err)
	}
	fmt.Printf("PARTNUM %02x  VERSION %02x  ", part, ver)
	switch {
	case part == 0x00 && (ver == 0x14 || ver == 0x04):
		fmt.Println("-> CC1101 OK")
	case part == 0xFF || ver == 0xFF:
		fmt.Println("-> MISO stuck high: check MISO wire / CS / power")
		os.Exit(1)
	case part == 0x00 && ver == 0x00:
		fmt.Println("-> all zero: no chip responding (power, CS, SCK)")
		os.Exit(1)
	default:
		fmt.Println("-> unexpected ID")
		os.Exit(1)
	}
}

// loopback: module unplugged, MOSI (pin 19) jumpered to MISO (pin 21).
// Proves the Pi's SPI controller, pinmux and MISO pin work.
func loopback(l *log.Logger, args []string) {
	fs := flag.NewFlagSet("loopback", flag.ExitOnError)
	dev := fs.String("spi", "/dev/spidev1.1", "spidev device")
	fs.Parse(args)
	s, err := spidev.Open(*dev, 500_000, "")
	if err != nil {
		l.Fatal(err)
	}
	defer s.Close()
	pat := []byte{0xA5, 0x5A, 0x00, 0xFF, 0x3C, 0xC3}
	r, err := s.Tx(pat)
	if err != nil {
		l.Fatal(err)
	}
	fmt.Printf("sent % X\nread % X\n", pat, r)
	if string(r) == string(pat) {
		fmt.Println("-> loopback OK: Pi SPI side is fine, problem is wiring/CS/module")
		return
	}
	fmt.Println("-> loopback FAILED: jumper 19<->21 missing, or SPI1 pins/overlay not working")
	os.Exit(1)
}

func decode(l *log.Logger, files []string) {
	if len(files) == 0 {
		usage()
	}
	for _, f := range files {
		s, err := flipper.ReadFile(f)
		if err != nil {
			l.Fatal(err)
		}
		fmt.Printf("== %s  %.3f MHz  %s  %s\n", f, float64(s.Frequency)/1e6, s.Protocol, s.Preset)
		if s.Protocol != "RAW" {
			fmt.Printf("   parsed: key %X  bits %d  TE %dus\n", s.Key, s.Bit, s.TE)
			continue
		}
		fr, t := flipper.DecodePWM(s.Raw)
		if len(fr) == 0 {
			fmt.Println("   no PWM frames found")
			continue
		}
		fmt.Printf("   TE %.0fus  ratio 1:%.1f  sync %.0f/%.0f TE  %d-bit frames\n", t.TE, t.Ratio, t.SyncHigh, t.SyncLow, fr[0].Bits)
		for _, p := range flipper.Presses(fr, 200*time.Millisecond) {
			note := ""
			if p.Bits == 32 {
				c, n := byte(p.Key>>8), byte(p.Key)
				if c^n == 0xFF {
					note = fmt.Sprintf("  addr %04X cmd %02X", p.Key>>16, c)
				}
			}
			fmt.Printf("   %7.2fs  key %0*X  x%d%s\n", p.At.Seconds(), (p.Bits+3)/4, p.Key, p.Frames, note)
		}
	}
}

func hexArg(s string) (uint64, error) { return strconv.ParseUint(strings.TrimSpace(s), 0, 64) }

func send(l *log.Logger, args []string) {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	path := fs.String("profile", "/etc/dmxbridge/profile.yaml", "device profile (radio + protocol)")
	cmd := fs.String("cmd", "", "command byte (profile has protocol.address)")
	key := fs.String("key", "", "full on-air key")
	rep := fs.Int("repeats", 0, "frames to send (default: profile)")
	fs.Parse(args)
	p, err := profile.Load(*path)
	if err != nil {
		l.Fatal(err)
	}
	var k uint64
	switch {
	case *key != "":
		k, err = hexArg(*key)
	case *cmd != "":
		var c uint64
		c, err = hexArg(*cmd)
		k = p.Key(c)
	default:
		usage()
	}
	if err != nil {
		l.Fatal(err)
	}
	n := p.Protocol.Repeats
	if *rep > 0 {
		n = *rep
	}
	r, err := radio.Open(p, l)
	if err != nil {
		l.Fatal(err)
	}
	defer r.Close()
	t := time.Now()
	if err := r.SendN(k, n); err != nil {
		l.Fatal(err)
	}
	fmt.Printf("sent %08X x%d in %s\n", k, n, time.Since(t).Round(time.Millisecond))
}

// QLC+ fixture definition (.qxf), generated from the same mapping the engine uses.
type xFixture struct {
	XMLName  xml.Name   `xml:"FixtureDefinition"`
	NS       string     `xml:"xmlns,attr"`
	Creator  xCreator   `xml:"Creator"`
	Manuf    string     `xml:"Manufacturer"`
	Model    string     `xml:"Model"`
	Type     string     `xml:"Type"`
	Channels []xChannel `xml:"Channel"`
	Mode     xMode      `xml:"Mode"`
}
type xCreator struct {
	Name    string `xml:"Name"`
	Version string `xml:"Version"`
	Author  string `xml:"Author"`
}
type xChannel struct {
	Name  string `xml:"Name,attr"`
	Group xGroup `xml:"Group"`
	Caps  []xCap `xml:"Capability"`
}
type xGroup struct {
	Byte  int    `xml:"Byte,attr"`
	Value string `xml:",chardata"`
}
type xCap struct {
	Min    int    `xml:"Min,attr"`
	Max    int    `xml:"Max,attr"`
	Preset string `xml:"Preset,attr,omitempty"` // QLC+ 4.12+: ColorMacro + Res1 = swatch
	Res1   string `xml:"Res1,attr,omitempty"`
	Text   string `xml:",chardata"`
}

func capOf(lo, hi int, text string) xCap { return xCap{Min: lo, Max: hi, Text: text} }

type xMode struct {
	Name string    `xml:"Name,attr"`
	Chs  []xModeCh `xml:"Channel"`
}
type xModeCh struct {
	Number int    `xml:"Number,attr"`
	Name   string `xml:",chardata"`
}

func qxf(l *log.Logger, args []string) {
	fs := flag.NewFlagSet("qxf", flag.ExitOnError)
	path := fs.String("profile", "/etc/dmxbridge/profile.yaml", "device profile")
	fs.Parse(args)
	p, err := profile.Load(*path)
	if err != nil {
		l.Fatal(err)
	}
	f := xFixture{
		NS:      "http://www.qlcplus.org/FixtureDefinition",
		Creator: xCreator{Name: "dmxbridge", Version: "1", Author: "dbctl qxf"},
		Manuf:   p.Fixture.Manufacturer,
		Model:   p.Fixture.Model,
		Type:    "Smoke",
		Mode:    xMode{Name: fmt.Sprintf("%d Channel", len(p.Channels))},
	}
	for i, c := range p.Channels {
		name := strings.ToUpper(c.Name[:1]) + c.Name[1:]
		ch := xChannel{Name: name}
		switch c.Type {
		case "onoff":
			ch.Group = xGroup{Value: "Effect"}
			ch.Caps = []xCap{capOf(0, 127, "Off"), capOf(128, 255, "On")}
		case "trigger":
			ch.Group = xGroup{Value: "Effect"}
			ch.Caps = []xCap{capOf(0, 127, "Idle"), capOf(128, 255, "Fire (on rising edge)")}
		case "select":
			ch.Group = xGroup{Value: "Effect"}
			ch.Caps = []xCap{capOf(0, 0, "No change")}
			for oi, o := range c.Options {
				lo, hi := -1, -1
				for v := 1; v < 256; v++ {
					if profile.SelectIndex(byte(v), len(c.Options)) == oi {
						if lo < 0 {
							lo = v
						}
						hi = v
					}
				}
				cp := capOf(lo, hi, o.Label)
				if o.Hex != "" {
					ch.Group.Value = "Colour"
					cp.Preset, cp.Res1 = "ColorMacro", o.Hex
				}
				ch.Caps = append(ch.Caps, cp)
			}
		case "stepper":
			ch.Group = xGroup{Value: "Intensity"}
			for lv := 0; lv <= c.Steps; lv++ {
				lo, hi := -1, -1
				for v := 0; v < 256; v++ {
					if profile.StepLevel(byte(v), c.Steps) == lv {
						if lo < 0 {
							lo = v
						}
						hi = v
					}
				}
				ch.Caps = append(ch.Caps, capOf(lo, hi, fmt.Sprintf("Level %d/%d", lv, c.Steps)))
			}
		}
		if c.Group != "" {
			ch.Group.Value = c.Group
		}
		f.Channels = append(f.Channels, ch)
		f.Mode.Chs = append(f.Mode.Chs, xModeCh{Number: i, Name: name})
	}
	out, _ := xml.MarshalIndent(f, "", " ")
	fmt.Println(`<?xml version="1.0" encoding="UTF-8"?>` + "\n<!DOCTYPE FixtureDefinition>")
	fmt.Println(string(out))
}
