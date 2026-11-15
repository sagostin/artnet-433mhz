package cc1101

import (
	"math"
	"testing"
)

func TestDrate(t *testing.T) {
	for _, te := range []float64{403e-6, 350e-6, 200e-6, 1e-3} {
		e, m, err := drate(1 / te)
		if err != nil {
			t.Fatal(err)
		}
		got := (256 + float64(m)) * math.Pow(2, float64(e)) * xosc / (1 << 28)
		if math.Abs(got-1/te)/(1/te) > 0.003 {
			t.Errorf("te %v: baud %.1f want %.1f (E=%d M=%d)", te, got, 1/te, e, m)
		}
	}
}
