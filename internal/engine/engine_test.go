package engine

import (
	"io"
	"log"
	"testing"
	"time"

	"dmxbridge/internal/profile"
)

func u(v uint64) *uint64 { return &v }

func testProfile() *profile.Profile {
	a := uint32(0x7CF0)
	return &profile.Profile{
		Protocol: profile.Protocol{Address: &a},
		Failsafe: profile.Failsafe{Timeout: time.Second},
		Channels: []profile.Channel{
			{Name: "fog", Type: "onoff", On: u(0x01), Off: u(0x03), MaxOn: 20 * time.Second, Cooldown: 10 * time.Second, Refresh: 2 * time.Second, IdleRefresh: 10 * time.Second},
			{Name: "color", Type: "select", Options: []profile.Option{{Label: "A", Cmd: 0x04}, {Label: "B", Cmd: 0x05}, {Label: "C", Cmd: 0x06}}},
			{Name: "bright", Type: "stepper", Up: u(0x11), Down: u(0x12), Steps: 4},
		},
	}
}

func drain(q *Scheduler) []uint64 {
	var out []uint64
	for {
		k, ok := q.next()
		if !ok {
			return out
		}
		out = append(out, k)
	}
}

func TestEngine(t *testing.T) {
	p := testProfile()
	q := NewScheduler(len(p.Channels), nil, log.New(io.Discard, "", 0))
	e := New(p, q, log.New(io.Discard, "", 0))
	t0 := time.Now()

	// startup with no signal: fog OFF asserted once
	e.Tick(t0)
	if got := drain(q); len(got) != 1 || got[0] != 0x7CF003FC {
		t.Fatalf("startup: %X", got)
	}

	// fog on + colour B + brightness 50% (home 4 down, 2 up)
	e.SetDMX([]byte{255, 128, 128})
	e.lastRx = t0
	e.Tick(t0)
	got := drain(q)
	want := []uint64{0x7CF001FE, 0x7CF005FA, 0x7CF012ED, 0x7CF012ED, 0x7CF012ED, 0x7CF012ED, 0x7CF011EE, 0x7CF011EE}
	if len(got) != len(want) {
		t.Fatalf("got %X want %X", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("[%d] got %X want %X", i, got, want)
		}
	}

	// max_on cap at 20s -> OFF, cooldown blocks ON
	e.lastRx = t0.Add(20 * time.Second)
	e.Tick(t0.Add(20 * time.Second))
	if got := drain(q); len(got) != 1 || got[0] != 0x7CF003FC {
		t.Fatalf("max_on: %X", got)
	}
	e.lastRx = t0.Add(25 * time.Second)
	e.Tick(t0.Add(25 * time.Second))
	if got := drain(q); len(got) != 0 {
		t.Fatalf("cooldown leaked: %X", got)
	}
	e.lastRx = t0.Add(30 * time.Second)
	e.Tick(t0.Add(30 * time.Second))
	if got := drain(q); len(got) != 1 || got[0] != 0x7CF001FE {
		t.Fatalf("after cooldown: %X", got)
	}

	// signal loss -> fog OFF, colour/brightness untouched
	e.Tick(t0.Add(32 * time.Second))
	if got := drain(q); len(got) != 1 || got[0] != 0x7CF003FC {
		t.Fatalf("failsafe: %X", got)
	}
}

func TestSelectRanges(t *testing.T) {
	seen := map[int]bool{}
	for v := 0; v < 256; v++ {
		seen[profile.SelectIndex(byte(v), 15)] = true
	}
	for i := -1; i < 15; i++ {
		if !seen[i] {
			t.Errorf("index %d unreachable", i)
		}
	}
}
