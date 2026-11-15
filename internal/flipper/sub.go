// Package flipper parses Flipper Zero .sub files and decodes PWM/OOK remotes
// from RAW captures without assuming a specific protocol.
package flipper

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Sub struct {
	Frequency uint64
	Preset    string
	Protocol  string
	Key       uint64 // parsed captures only
	Bit       int
	TE        int
	Raw       []int // RAW captures: +mark / -space, microseconds
}

func ReadFile(path string) (*Sub, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Parse(f)
}

func Parse(r io.Reader) (*Sub, error) {
	s := &Sub{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "Frequency":
			s.Frequency, _ = strconv.ParseUint(v, 10, 64)
		case "Preset":
			s.Preset = v
		case "Protocol":
			s.Protocol = v
		case "Bit":
			s.Bit, _ = strconv.Atoi(v)
		case "TE":
			s.TE, _ = strconv.Atoi(v)
		case "Key":
			h := strings.ReplaceAll(v, " ", "")
			s.Key, _ = strconv.ParseUint(h, 16, 64)
		case "RAW_Data":
			for _, f := range strings.Fields(v) {
				n, err := strconv.Atoi(f)
				if err != nil {
					return nil, fmt.Errorf("RAW_Data: %w", err)
				}
				// merge same-sign runs so the list strictly alternates
				if l := len(s.Raw); l > 0 && (s.Raw[l-1] > 0) == (n > 0) {
					s.Raw[l-1] += n
				} else {
					s.Raw = append(s.Raw, n)
				}
			}
		}
	}
	return s, sc.Err()
}

type Frame struct {
	At   time.Duration
	Key  uint64
	Bits int
}

// Press is a run of identical frames — one button press on the remote.
type Press struct {
	At     time.Duration
	Key    uint64
	Bits   int
	Frames int
}

type Timing struct {
	TE       float64 // µs
	Ratio    float64 // long/short pulse ratio, ~3 for EV1527
	SyncHigh float64 // TE units
	SyncLow  float64 // TE units
}

// DecodePWM finds frames of the form [sync mark, long gap][bits...] where each
// bit is a constant-period mark/space pair, long mark = 1, short mark = 0.
func DecodePWM(raw []int) ([]Frame, Timing) {
	var (
		frames                 []Frame
		periods, shorts, longs []float64
		syncHighs, syncLows    []float64
		ts                     = make([]time.Duration, len(raw)+1)
	)
	for i, d := range raw {
		ts[i+1] = ts[i] + time.Duration(abs(d))*time.Microsecond
	}
	for i := 0; i+1 < len(raw); {
		m, g := raw[i], -raw[i+1]
		if !(m > 0 && g > 0 && g >= 3000 && g > 8*m) {
			i++
			continue
		}
		var (
			bits, n    int
			key        uint64
			p0         float64
			fp, fs, fl []float64
		)
		j := i + 2
	bitLoop:
		for ; j+1 < len(raw); j += 2 {
			bm, bs := float64(raw[j]), float64(-raw[j+1])
			if bm <= 0 || bs <= 0 {
				break
			}
			p := bm + bs
			if n == 0 {
				p0 = p
			} else if math.Abs(p-p0) > 0.3*p0 {
				break // period changed: next sync or noise
			}
			switch {
			case bm >= 1.8*bs:
				key = key<<1 | 1
				fl, fs = append(fl, bm), append(fs, bs)
			case bs >= 1.8*bm:
				key <<= 1
				fs, fl = append(fs, bm), append(fl, bs)
			default:
				break bitLoop
			}
			fp = append(fp, p)
			n++
			bits++
		}
		if bits >= 8 && bits <= 64 {
			frames = append(frames, Frame{At: ts[i], Key: key, Bits: bits})
			periods, shorts, longs = append(periods, fp...), append(shorts, fs...), append(longs, fl...)
			syncHighs, syncLows = append(syncHighs, float64(m)), append(syncLows, float64(g))
			i = j
		} else {
			i++
		}
	}
	// keep only the dominant frame length (drops truncated first/last frames)
	cnt := map[int]int{}
	for _, f := range frames {
		cnt[f.Bits]++
	}
	best := 0
	for b, c := range cnt {
		if c > cnt[best] || (c == cnt[best] && b > best) {
			best = b
		}
	}
	out := frames[:0]
	for _, f := range frames {
		if f.Bits == best {
			out = append(out, f)
		}
	}
	var t Timing
	if len(periods) > 0 {
		t.Ratio = median(longs) / median(shorts)
		t.TE = median(periods) / (1 + math.Round(t.Ratio))
		t.SyncHigh = median(syncHighs) / t.TE
		t.SyncLow = median(syncLows) / t.TE
	}
	return out, t
}

// Presses groups consecutive identical frames separated by < gap.
func Presses(frames []Frame, gap time.Duration) []Press {
	var ps []Press
	var lastAt time.Duration
	for _, f := range frames {
		if l := len(ps); l > 0 && ps[l-1].Key == f.Key && f.At-lastAt < gap {
			ps[l-1].Frames++
		} else {
			ps = append(ps, Press{At: f.At, Key: f.Key, Bits: f.Bits, Frames: 1})
		}
		lastAt = f.At
	}
	return ps
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func median(x []float64) float64 {
	if len(x) == 0 {
		return 0
	}
	s := append([]float64(nil), x...)
	sort.Float64s(s)
	return s[len(s)/2]
}
