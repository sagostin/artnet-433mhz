// dmxbridged: Art-Net -> CC1101 bridge for RF-remote-only effects.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"dmxbridge/internal/artnet"
	"dmxbridge/internal/engine"
	"dmxbridge/internal/profile"
	"dmxbridge/internal/radio"
)

func main() {
	var (
		path   = flag.String("profile", "/etc/dmxbridge/profile.yaml", "device profile")
		listen = flag.String("listen", fmt.Sprintf(":%d", artnet.Port), "Art-Net listen address")
		dry    = flag.Bool("dry-run", false, "log commands instead of transmitting (no radio needed)")
	)
	flag.Parse()
	l := log.New(os.Stderr, "", log.LstdFlags|log.Lmsgprefix)

	p, err := profile.Load(*path)
	if err != nil {
		l.Fatal(err)
	}

	var tx engine.Sender
	if *dry {
		tx = radio.DryRun{Proto: p.OOK(), Repeats: p.Protocol.Repeats, Log: l}
	} else {
		r, err := radio.Open(p, l)
		if err != nil {
			l.Fatal(err)
		}
		defer r.Close()
		tx = r
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	q := engine.NewScheduler(len(p.Channels), tx, l)
	e := engine.New(p, q, l)
	go q.Run(ctx)
	go e.Run(ctx)

	pc, err := net.ListenPacket("udp4", *listen)
	if err != nil {
		l.Fatal(err)
	}
	srv := &artnet.Server{
		Universe:  p.Node.Universe,
		Start:     p.Node.Address,
		Footprint: len(p.Channels),
		ShortName: p.Node.Name,
		LongName:  fmt.Sprintf("dmxbridge %s u%d @%d (%dch)", p.Node.Name, p.Node.Universe, p.Node.Address, len(p.Channels)),
		OnDMX:     e.SetDMX,
		Report:    e.Report,
		Log:       l,
	}
	go func() {
		<-ctx.Done()
		pc.Close()
	}()
	l.Printf("%s: art-net %s universe %d, DMX %d-%d", p.Node.Name, *listen, p.Node.Universe, p.Node.Address, p.Node.Address+len(p.Channels)-1)
	if err := srv.Serve(pc); err != nil && ctx.Err() == nil {
		l.Fatal(err)
	}

	// shutting down: force every onoff channel off before exiting
	for _, c := range p.Channels {
		if c.Type == "onoff" {
			l.Printf("shutdown: %s off", c.Name)
			tx.Send(p.Key(*c.Off))
		}
	}
}
