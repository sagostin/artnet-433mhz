package artnet

import (
	"encoding/binary"
	"net"
	"testing"
)

type fakePC struct {
	net.PacketConn
	out []byte
	dst net.Addr
}

func (f *fakePC) WriteTo(b []byte, a net.Addr) (int, error) {
	f.out, f.dst = append([]byte(nil), b...), a
	return len(b), nil
}

func TestPollReply(t *testing.T) {
	s := &Server{Universe: 0x123, ShortName: "fog-01", LongName: "dmxbridge fog-01", Report: func() string { return "fog=off" }}
	pc := &fakePC{}
	s.reply(pc, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 50000})
	r := pc.out
	if len(r) != replyLen || string(r[:8]) != "Art-Net\x00" || binary.LittleEndian.Uint16(r[8:]) != opReply {
		t.Fatalf("bad header % x", r[:10])
	}
	if pc.dst.(*net.UDPAddr).Port != Port {
		t.Errorf("reply to port %d", pc.dst.(*net.UDPAddr).Port)
	}
	// port-address reassembles from Net/Sub/SwOut
	if pa := uint16(r[18])<<8 | uint16(r[19])<<4 | uint16(r[190]); pa != 0x123 {
		t.Errorf("port-address %#x", pa)
	}
	if got := string(r[26:32]); got != "fog-01" {
		t.Errorf("short name %q", got)
	}
	if got := string(r[108:128]); got != "#0001 [0001] fog=off" {
		t.Errorf("report %q", got)
	}
}

func TestDMX(t *testing.T) {
	var got []byte
	s := &Server{Universe: 1, Start: 3, Footprint: 4, OnDMX: func(v []byte) { got = v }}
	p := make([]byte, 18+512)
	copy(p, "Art-Net\x00")
	binary.LittleEndian.PutUint16(p[8:], opDMX)
	binary.LittleEndian.PutUint16(p[14:], 1)
	binary.BigEndian.PutUint16(p[16:], 512)
	copy(p[18:], []byte{9, 9, 1, 2, 3, 4, 9})
	s.dmx(p)
	if string(got) != string([]byte{1, 2, 3, 4}) {
		t.Fatalf("got %v", got)
	}
	got = nil
	binary.LittleEndian.PutUint16(p[14:], 2) // other universe ignored
	s.dmx(p)
	if got != nil {
		t.Fatal("wrong universe accepted")
	}
}
