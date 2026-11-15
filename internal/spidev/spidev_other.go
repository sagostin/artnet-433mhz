//go:build !linux

package spidev

import "errors"

type Dev struct{}

func Open(string, uint32, string) (*Dev, error) { return nil, errors.New("spidev: linux only") }
func (*Dev) Tx([]byte) ([]byte, error)          { return nil, errors.New("spidev: linux only") }
func (*Dev) Close() error                       { return nil }
