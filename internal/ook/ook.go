// Package ook encodes fixed-code PWM remotes (EV1527 / PT2262 style) into an
// on-off-keyed chip stream, one chip per TE, for a packet-mode radio to clock out.
package ook

import (
	"fmt"
	"time"
)

// Proto describes a PWM frame. All widths are in TE units as {high, low}.
type Proto struct {
	TE   time.Duration
	Bits int
	Sync [2]int // e.g. {1, 32}: short mark, long gap — starts every frame
	Zero [2]int // e.g. {1, 3}
	One  [2]int // e.g. {3, 1}
}

func (p Proto) Validate() error {
	if p.TE <= 0 || p.Bits <= 0 || p.Bits > 64 {
		return fmt.Errorf("ook: bad proto te=%s bits=%d", p.TE, p.Bits)
	}
	for _, w := range [][2]int{p.Sync, p.Zero, p.One} {
		if w[0] < 0 || w[1] < 0 || w[0]+w[1] == 0 {
			return fmt.Errorf("ook: bad width %v", w)
		}
	}
	return nil
}

// FrameChips is the length of one frame in chips (sync + data).
func (p Proto) FrameChips() int {
	n := p.Sync[0] + p.Sync[1]
	for i := 0; i < p.Bits; i++ {
		n += max(p.Zero[0]+p.Zero[1], p.One[0]+p.One[1])
	}
	return n
}

// Chips returns `repeats` back-to-back frames, MSB first, followed by a
// trailing sync mark so the receiver sees the last frame terminated — the
// same shape the real remote transmits.
func (p Proto) Chips(key uint64, repeats int) []bool {
	c := make([]bool, 0, repeats*p.FrameChips()+p.Sync[0])
	put := func(w [2]int) {
		for i := 0; i < w[0]; i++ {
			c = append(c, true)
		}
		for i := 0; i < w[1]; i++ {
			c = append(c, false)
		}
	}
	for r := 0; r < repeats; r++ {
		put(p.Sync)
		for i := p.Bits - 1; i >= 0; i-- {
			if key>>uint(i)&1 == 1 {
				put(p.One)
			} else {
				put(p.Zero)
			}
		}
	}
	put([2]int{p.Sync[0], 0})
	return c
}

// Pack packs chips MSB-first into bytes, zero-padded (zero = carrier off).
func Pack(chips []bool) []byte {
	b := make([]byte, (len(chips)+7)/8)
	for i, on := range chips {
		if on {
			b[i/8] |= 0x80 >> uint(i%8)
		}
	}
	return b
}

// Durations converts chips to Flipper-style signed microsecond runs
// (+mark, -space). Used for tests and dry-run output.
func Durations(chips []bool, te time.Duration) []int {
	var out []int
	us := int(te / time.Microsecond)
	for i := 0; i < len(chips); {
		j := i
		for j < len(chips) && chips[j] == chips[i] {
			j++
		}
		d := (j - i) * us
		if !chips[i] {
			d = -d
		}
		out = append(out, d)
		i = j
	}
	return out
}
