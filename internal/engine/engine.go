// Package engine turns DMX channel values into radio commands.
package engine

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"dmxbridge/internal/profile"
)

// Sender transmits one key (all repeats) and blocks until done.
type Sender interface {
	Send(key uint64) error
}

// Scheduler serialises a single radio across channels. Each channel owns a
// slot; slots are served in channel order (put fog first). onoff/select slots
// replace their pending command, so a slider sweep sends only where it lands.
type Scheduler struct {
	mu    sync.Mutex
	slots [][]uint64
	wake  chan struct{}
	s     Sender
	log   *log.Logger
}

func NewScheduler(n int, s Sender, l *log.Logger) *Scheduler {
	return &Scheduler{slots: make([][]uint64, n), wake: make(chan struct{}, 1), s: s, log: l}
}

func (q *Scheduler) Replace(slot int, keys ...uint64) {
	q.mu.Lock()
	q.slots[slot] = append([]uint64(nil), keys...)
	q.mu.Unlock()
	q.poke()
}

func (q *Scheduler) Append(slot int, keys ...uint64) {
	q.mu.Lock()
	q.slots[slot] = append(q.slots[slot], keys...)
	q.mu.Unlock()
	q.poke()
}

func (q *Scheduler) poke() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *Scheduler) next() (uint64, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, s := range q.slots {
		if len(s) > 0 {
			q.slots[i] = s[1:]
			return s[0], true
		}
	}
	return 0, false
}

func (q *Scheduler) Run(ctx context.Context) {
	for {
		k, ok := q.next()
		if !ok {
			select {
			case <-q.wake:
				continue
			case <-ctx.Done():
				return
			}
		}
		if err := q.s.Send(k); err != nil {
			q.log.Printf("tx %08X: %v", k, err)
		}
	}
}

type chState struct {
	cfg *profile.Channel

	on                             bool
	onSince, cooldownUntil, lastTx time.Time

	sel int

	high bool

	armed bool
	level int
}

type Engine struct {
	p   *profile.Profile
	q   *Scheduler
	log *log.Logger
	ch  []*chState

	mu     sync.Mutex
	vals   []byte
	lastRx time.Time
	lost   bool
}

func New(p *profile.Profile, q *Scheduler, l *log.Logger) *Engine {
	e := &Engine{p: p, q: q, log: l, vals: make([]byte, len(p.Channels)), lost: true}
	for i := range p.Channels {
		e.ch = append(e.ch, &chState{cfg: &p.Channels[i], sel: -1})
	}
	return e
}

// SetDMX is called by the Art-Net listener with this node's footprint.
func (e *Engine) SetDMX(v []byte) {
	e.mu.Lock()
	copy(e.vals, v)
	e.lastRx = time.Now()
	e.mu.Unlock()
}

func (e *Engine) Run(ctx context.Context) {
	t := time.NewTicker(20 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			e.Tick(now)
		}
	}
}

func (e *Engine) Tick(now time.Time) {
	e.mu.Lock()
	vals := append([]byte(nil), e.vals...)
	lost := now.Sub(e.lastRx) > e.p.Failsafe.Timeout
	if lost != e.lost {
		if lost {
			e.log.Printf("art-net signal lost (> %s): failsafe", e.p.Failsafe.Timeout)
		} else {
			e.log.Print("art-net signal acquired")
		}
		e.lost = lost
	}
	e.mu.Unlock()

	for i, c := range e.ch {
		v := vals[i]
		if lost {
			v = 0
		}
		switch c.cfg.Type {
		case "onoff":
			e.onoff(i, c, v, now)
		case "select":
			if lost {
				continue // hold last look
			}
			if s := profile.SelectIndex(v, len(c.cfg.Options)); s != c.sel {
				c.sel = s
				if s >= 0 {
					o := c.cfg.Options[s]
					e.log.Printf("%s -> %s", c.cfg.Name, o.Label)
					e.q.Replace(i, e.p.Key(o.Cmd))
				}
			}
		case "trigger":
			hi := v > 127
			if hi && !c.high {
				e.log.Printf("%s fired", c.cfg.Name)
				e.q.Append(i, e.p.Key(*c.cfg.Cmd))
			}
			c.high = hi
		case "stepper":
			if lost {
				continue
			}
			e.stepper(i, c, v)
		}
	}
}

func (e *Engine) onoff(i int, c *chState, v byte, now time.Time) {
	cfg := c.cfg
	want := v > 127 && !now.Before(c.cooldownUntil)
	if c.on && cfg.MaxOn > 0 && now.Sub(c.onSince) >= cfg.MaxOn {
		want = false
		c.cooldownUntil = now.Add(cfg.Cooldown)
		e.log.Printf("%s hit max_on %s, cooldown %s", cfg.Name, cfg.MaxOn, cfg.Cooldown)
	}
	send := false
	if want != c.on {
		c.on = want
		if want {
			c.onSince = now
		}
		e.log.Printf("%s %s", cfg.Name, onStr(want))
		send = true
	} else if iv := map[bool]time.Duration{true: cfg.Refresh, false: cfg.IdleRefresh}[c.on]; iv > 0 && now.Sub(c.lastTx) >= iv {
		send = true // re-assert state: a missed OFF can't leave it fogging
	}
	if send {
		k := *cfg.Off
		if c.on {
			k = *cfg.On
		}
		e.q.Replace(i, e.p.Key(k))
		c.lastTx = now
	}
}

// Stepper turns a relative up/down remote into an absolute fader. Unknown
// starting level, so the first non-zero value homes (steps × down) first.
func (e *Engine) stepper(i int, c *chState, v byte) {
	cfg := c.cfg
	if !c.armed {
		if v == 0 {
			return
		}
		c.armed = true
		c.level = profile.StepLevel(v, cfg.Steps)
		seq := make([]uint64, 0, cfg.Steps+c.level)
		for n := 0; n < cfg.Steps; n++ {
			seq = append(seq, e.p.Key(*cfg.Down))
		}
		for n := 0; n < c.level; n++ {
			seq = append(seq, e.p.Key(*cfg.Up))
		}
		e.log.Printf("%s homing, then level %d/%d", cfg.Name, c.level, cfg.Steps)
		e.q.Replace(i, seq...)
		return
	}
	t := profile.StepLevel(v, cfg.Steps)
	if t == c.level {
		return
	}
	k, d := *cfg.Up, t-c.level
	if d < 0 {
		k, d = *cfg.Down, -d
	}
	for n := 0; n < d; n++ {
		e.q.Append(i, e.p.Key(k))
	}
	e.log.Printf("%s level %d -> %d", cfg.Name, c.level, t)
	c.level = t
}

// Report is a one-line state summary for ArtPollReply NodeReport.
func (e *Engine) Report() string {
	var b strings.Builder
	e.mu.Lock()
	if e.lost {
		b.WriteString("nosig ")
	}
	e.mu.Unlock()
	for _, c := range e.ch {
		switch c.cfg.Type {
		case "onoff":
			fmt.Fprintf(&b, "%s=%s ", c.cfg.Name, onStr(c.on))
		case "select":
			if c.sel >= 0 {
				fmt.Fprintf(&b, "%s=%s ", c.cfg.Name, c.cfg.Options[c.sel].Label)
			}
		case "stepper":
			if c.armed {
				fmt.Fprintf(&b, "%s=%d/%d ", c.cfg.Name, c.level, c.cfg.Steps)
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func onStr(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
