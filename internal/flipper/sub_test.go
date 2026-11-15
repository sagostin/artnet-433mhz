package flipper

import (
	"testing"
	"time"

	"dmxbridge/internal/ook"
)

func keys(t *testing.T, path string) []uint64 {
	t.Helper()
	s, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fr, tm := DecodePWM(s.Raw)
	if tm.TE < 380 || tm.TE > 430 {
		t.Errorf("%s: TE %.0f out of range", path, tm.TE)
	}
	var out []uint64
	for _, p := range Presses(fr, 200*time.Millisecond) {
		out = append(out, p.Key)
	}
	return out
}

func eq(t *testing.T, name string, got, want []uint64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %d presses %X, want %X", name, len(got), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("%s[%d]: got %08X want %08X", name, i, got[i], want[i])
		}
	}
}

func TestCaptures(t *testing.T) {
	eq(t, "smoke", keys(t, "testdata/smoke.sub"), []uint64{0x7CF001FE, 0x7CF003FC})
	eq(t, "xtras", keys(t, "testdata/xtras.sub"), []uint64{
		0x7CF004FB, 0x7CF005FA, 0x7CF006F9, 0x7CF007F8, 0x7CF008F7,
		0x7CF009F6, 0x7CF010EF, 0x7CF011EE, 0x7CF012ED})
	eq(t, "lights", keys(t, "testdata/lights.sub"), []uint64{
		0x7CF013EC, 0x7CF014EB, 0x7CF015EA, 0x7CF016E9, 0x7CF017E8, 0x7CF018E7,
		0x7CF019E6, 0x7CF020DF, 0x7CF021DE, 0x7CF022DD, 0x7CF023DC, 0x7CF024DB})
}

// What we transmit must decode back to the same key with the same timing.
func TestRoundTrip(t *testing.T) {
	p := ook.Proto{TE: 403 * time.Microsecond, Bits: 32, Sync: [2]int{1, 32}, Zero: [2]int{1, 3}, One: [2]int{3, 1}}
	for _, k := range []uint64{0x7CF001FE, 0x7CF003FC, 0x7CF024DB} {
		fr, tm := DecodePWM(ook.Durations(p.Chips(k, 4), p.TE))
		if len(fr) != 4 {
			t.Fatalf("%08X: %d frames", k, len(fr))
		}
		for _, f := range fr {
			if f.Key != k || f.Bits != 32 {
				t.Errorf("got %08X/%d want %08X", f.Key, f.Bits, k)
			}
		}
		if tm.SyncLow < 31 || tm.SyncLow > 33 {
			t.Errorf("sync low %.1f TE", tm.SyncLow)
		}
	}
}
