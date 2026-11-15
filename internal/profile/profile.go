// Package profile loads a device profile: radio, protocol, DMX patch, channels.
package profile

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"dmxbridge/internal/ook"
)

type Profile struct {
	Node     Node      `yaml:"node"`
	Radio    Radio     `yaml:"radio"`
	Protocol Protocol  `yaml:"protocol"`
	Failsafe Failsafe  `yaml:"failsafe"`
	Channels []Channel `yaml:"channels"`
	Fixture  Fixture   `yaml:"fixture"` // QLC+ fixture identity for dbctl qxf
}

type Fixture struct {
	Manufacturer string `yaml:"manufacturer"`
	Model        string `yaml:"model"`
}

type Node struct {
	Name     string `yaml:"name"`
	Universe uint16 `yaml:"universe"` // Art-Net port-address (QLC+ "Universe N" = N-1)
	Address  int    `yaml:"address"`  // DMX start address, 1-based
}

type Radio struct {
	SPI     string `yaml:"spi"`
	CSGPIO  string `yaml:"cs_gpio"` // "" = hardware CS, else "gpiochip0:74"
	SpeedHz uint32 `yaml:"speed_hz"`
	Freq    Freq   `yaml:"freq"`
	Power   uint8  `yaml:"power"`
}

type Protocol struct {
	TE      time.Duration `yaml:"te"`
	Bits    int           `yaml:"bits"`
	Sync    [2]int        `yaml:"sync"`
	Zero    [2]int        `yaml:"zero"`
	One     [2]int        `yaml:"one"`
	Repeats int           `yaml:"repeats"`
	// If set, codes in channels are command bytes and the key is
	// address<<16 | cmd<<8 | ^cmd (this remote family). Otherwise codes are full keys.
	Address *uint32 `yaml:"address"`
}

type Failsafe struct {
	Timeout time.Duration `yaml:"timeout"`
}

type Channel struct {
	Name string `yaml:"name"`
	Type string `yaml:"type"` // onoff | select | stepper | trigger

	// onoff
	On          *uint64       `yaml:"on"`
	Off         *uint64       `yaml:"off"`
	MaxOn       time.Duration `yaml:"max_on"`
	Cooldown    time.Duration `yaml:"cooldown"`
	Refresh     time.Duration `yaml:"refresh"`      // resend ON this often while on
	IdleRefresh time.Duration `yaml:"idle_refresh"` // resend OFF this often while off

	// select
	Options []Option `yaml:"options"`

	// stepper
	Up    *uint64 `yaml:"up"`
	Down  *uint64 `yaml:"down"`
	Steps int     `yaml:"steps"`

	// trigger
	Cmd *uint64 `yaml:"cmd"`

	Group string `yaml:"group"` // QLC+ channel group override
}

type Option struct {
	Label string `yaml:"label"`
	Cmd   uint64 `yaml:"cmd"`
	Hex   string `yaml:"hex"` // optional colour hint for QLC+
}

// Freq accepts 433920000, "433.92M", "433920k".
type Freq float64

func (f *Freq) UnmarshalYAML(n *yaml.Node) error {
	v, err := ParseFreq(n.Value)
	*f = Freq(v)
	return err
}

func ParseFreq(s string) (float64, error) {
	s = strings.TrimSpace(strings.TrimSuffix(strings.ToLower(s), "hz"))
	mul := 1.0
	switch {
	case strings.HasSuffix(s, "m"):
		mul, s = 1e6, s[:len(s)-1]
	case strings.HasSuffix(s, "k"):
		mul, s = 1e3, s[:len(s)-1]
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("freq %q: %w", s, err)
	}
	return v * mul, nil
}

func Load(path string) (*Profile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	p := &Profile{}
	if err := yaml.Unmarshal(b, p); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	p.defaults()
	return p, p.Validate()
}

func (p *Profile) defaults() {
	if p.Node.Name == "" {
		p.Node.Name = "dmxbridge"
	}
	if p.Fixture.Manufacturer == "" {
		p.Fixture.Manufacturer = "dmxbridge"
	}
	if p.Fixture.Model == "" {
		p.Fixture.Model = p.Node.Name
	}
	if p.Node.Address == 0 {
		p.Node.Address = 1
	}
	if p.Radio.SPI == "" {
		p.Radio.SPI = "/dev/spidev1.1"
	}
	if p.Radio.SpeedHz == 0 {
		p.Radio.SpeedHz = 500_000
	}
	if p.Radio.Power == 0 {
		p.Radio.Power = 0xC0
	}
	if p.Protocol.Repeats == 0 {
		p.Protocol.Repeats = 4
	}
	if p.Failsafe.Timeout == 0 {
		p.Failsafe.Timeout = 1500 * time.Millisecond
	}
	for i := range p.Channels {
		c := &p.Channels[i]
		if c.Type == "onoff" {
			if c.Refresh == 0 {
				c.Refresh = 2 * time.Second
			}
			if c.IdleRefresh == 0 {
				c.IdleRefresh = 10 * time.Second
			}
		}
	}
}

func (p *Profile) Validate() error {
	if err := p.OOK().Validate(); err != nil {
		return err
	}
	if p.Radio.Freq < 300e6 || p.Radio.Freq > 928e6 {
		return fmt.Errorf("radio.freq %.0f Hz out of range", float64(p.Radio.Freq))
	}
	if n := len(p.Channels); n == 0 || p.Node.Address < 1 || p.Node.Address+n-1 > 512 {
		return fmt.Errorf("node.address %d + %d channels does not fit 1..512", p.Node.Address, n)
	}
	if fc := p.OOK().FrameChips(); (p.Protocol.Repeats*fc+8)/8 > 255 {
		return fmt.Errorf("protocol.repeats %d too long for one packet (max %d)", p.Protocol.Repeats, (255*8-8)/fc)
	}
	for i, c := range p.Channels {
		bad := func(s string) error { return fmt.Errorf("channel %d (%s): %s", i+1, c.Name, s) }
		switch c.Type {
		case "onoff":
			if c.On == nil || c.Off == nil {
				return bad("onoff needs on and off")
			}
		case "select":
			if len(c.Options) == 0 || len(c.Options) > 255 {
				return bad("select needs 1..255 options")
			}
		case "stepper":
			if c.Up == nil || c.Down == nil || c.Steps < 1 {
				return bad("stepper needs up, down, steps>=1")
			}
		case "trigger":
			if c.Cmd == nil {
				return bad("trigger needs cmd")
			}
		default:
			return bad("unknown type " + strconv.Quote(c.Type))
		}
	}
	return nil
}

func (p *Profile) OOK() ook.Proto {
	return ook.Proto{TE: p.Protocol.TE, Bits: p.Protocol.Bits, Sync: p.Protocol.Sync, Zero: p.Protocol.Zero, One: p.Protocol.One}
}

// Key turns a profile code into the on-air key.
func (p *Profile) Key(code uint64) uint64 {
	if p.Protocol.Address == nil {
		return code
	}
	c := code & 0xFF
	return uint64(*p.Protocol.Address)<<16 | c<<8 | (^c & 0xFF)
}

// SelectIndex maps a DMX value to an option index; -1 for 0 (no action).
// Shared by the engine and the QLC+ fixture generator so ranges always match.
func SelectIndex(v byte, n int) int {
	if v == 0 || n == 0 {
		return -1
	}
	return (int(v) - 1) * n / 255
}

// StepLevel maps a DMX value to a stepper level 0..steps.
func StepLevel(v byte, steps int) int { return (int(v)*steps + 127) / 255 }
