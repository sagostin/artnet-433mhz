// Package artnet receives ArtDMX for one universe and answers ArtPoll so
// controllers (QLC+, DMX-Workshop, Resolume) discover the node.
package artnet

import (
	"encoding/binary"
	"log"
	"net"
	"sync/atomic"
)

const (
	Port     = 6454
	opPoll   = 0x2000
	opReply  = 0x2100
	opDMX    = 0x5000
	replyLen = 239
)

var id = []byte("Art-Net\x00")

type Server struct {
	Universe  uint16 // 15-bit port-address
	Start     int    // DMX start address, 1-based
	Footprint int
	ShortName string
	LongName  string
	OnDMX     func([]byte)
	Report    func() string
	Log       *log.Logger

	polls atomic.Uint32
}

func (s *Server) Serve(pc net.PacketConn) error {
	buf := make([]byte, 1024)
	for {
		n, src, err := pc.ReadFrom(buf)
		if err != nil {
			return err
		}
		p := buf[:n]
		if n < 12 || string(p[:8]) != string(id) {
			continue
		}
		switch binary.LittleEndian.Uint16(p[8:]) {
		case opDMX:
			s.dmx(p)
		case opPoll:
			s.reply(pc, src)
		}
	}
}

func (s *Server) dmx(p []byte) {
	if len(p) < 18 || binary.LittleEndian.Uint16(p[14:]) != s.Universe {
		return
	}
	dlen := int(binary.BigEndian.Uint16(p[16:]))
	if 18+dlen > len(p) {
		return
	}
	data := p[18 : 18+dlen]
	v := make([]byte, s.Footprint)
	if s.Start-1 < len(data) {
		copy(v, data[s.Start-1:]) // short frames: missing channels read as 0
	}
	s.OnDMX(v)
}

func (s *Server) reply(pc net.PacketConn, src net.Addr) {
	ua, ok := src.(*net.UDPAddr)
	if !ok {
		return
	}
	ip, mac := localFor(ua)
	r := make([]byte, replyLen)
	copy(r, id)
	binary.LittleEndian.PutUint16(r[8:], opReply)
	copy(r[10:14], ip.To4())
	binary.LittleEndian.PutUint16(r[14:], Port)
	r[16], r[17] = 0, 1                // firmware version
	r[18] = byte(s.Universe>>8) & 0x7F // NetSwitch
	r[19] = byte(s.Universe>>4) & 0x0F // SubSwitch
	r[20], r[21] = 0x00, 0xFF          // OEM: unknown
	r[23] = 0xD0                       // Status1: indicators normal, port-addr by network
	copy(r[26:44], trunc(s.ShortName, 17))
	copy(r[44:108], trunc(s.LongName, 63))
	n := s.polls.Add(1)
	copy(r[108:172], trunc(fmtReport(n, s.Report()), 63))
	r[173] = 1                       // NumPorts
	r[174] = 0x80                    // PortTypes[0]: output, DMX512
	r[182] = 0x80                    // GoodOutput[0]: data being output
	r[190] = byte(s.Universe) & 0x0F // SwOut[0]
	r[200] = 0x00                    // Style: StNode
	copy(r[201:207], mac)
	copy(r[207:211], ip.To4()) // BindIp
	r[211] = 1                 // BindIndex
	r[212] = 0x08              // Status2: 15-bit port-address
	dst := &net.UDPAddr{IP: ua.IP, Port: Port}
	if _, err := pc.WriteTo(r, dst); err != nil && s.Log != nil {
		s.Log.Printf("artpollreply to %s: %v", dst, err)
	}
}

func fmtReport(n uint32, msg string) string {
	b := []byte("#0001 [0000] ")
	for i := 10; i >= 7; i-- { // the 4 digits inside [....]
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b) + msg
}

func trunc(s string, n int) []byte {
	if len(s) > n {
		s = s[:n]
	}
	return []byte(s)
}

// localFor finds the local IP/MAC the kernel would use to reach dst.
func localFor(dst *net.UDPAddr) (net.IP, net.HardwareAddr) {
	ip := net.IPv4zero
	if c, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: dst.IP, Port: Port}); err == nil {
		ip = c.LocalAddr().(*net.UDPAddr).IP
		c.Close()
	}
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		as, _ := i.Addrs()
		for _, a := range as {
			if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
				return ip, i.HardwareAddr
			}
		}
	}
	return ip, make(net.HardwareAddr, 6)
}
