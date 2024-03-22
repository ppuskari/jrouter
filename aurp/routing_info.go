package aurp

import (
	"encoding/binary"
	"fmt"
	"io"
)

type RIReqPacket struct {
	Header
}

type RIRspPacket struct {
	Header

	Networks NetworkTuples
}

func (p *RIRspPacket) WriteTo(w io.Writer) (int64, error) {
	a := acc(w)
	a.writeTo(&p.Header)
	a.writeTo(p.Networks)
	return a.ret()
}

func parseRIRsp(p []byte) (*RIRspPacket, error) {
	n, err := parseNetworkTuples(p)
	if err != nil {
		return nil, err
	}
	return &RIRspPacket{
		Networks: n,
	}, nil
}

type RIAckPacket struct {
	Header
}

type RIUpdPacket struct {
	Header

	Events EventTuples
}

func (p *RIUpdPacket) WriteTo(w io.Writer) (int64, error) {
	a := acc(w)
	a.writeTo(&p.Header)
	a.writeTo(p.Events)
	return a.ret()
}

func parseRIUpd(p []byte) (*RIUpdPacket, error) {
	e, err := parseEventTuples(p)
	if err != nil {
		return nil, err
	}
	return &RIUpdPacket{
		Events: e,
	}, nil
}

type NetworkTuples []NetworkTuple

func (n NetworkTuples) WriteTo(w io.Writer) (int64, error) {
	a := acc(w)
	for _, nt := range n {
		a.writeTo(&nt)
	}
	return a.ret()
}

func parseNetworkTuples(p []byte) (NetworkTuples, error) {
	// Each network tuple is at least 3 bytes, so we need to store at most
	// len(p)/3 of them.
	n := make(NetworkTuples, 0, len(p)/3)
	for len(p) > 0 {
		nt, nextp, err := parseNetworkTuple(p)
		if err != nil {
			return nil, fmt.Errorf("parsing network tuple %d: %w", len(n), err)
		}
		n = append(n, nt)
		p = nextp
	}
	return n, nil
}

type NetworkTuple struct {
	RangeStart uint16
	Distance   uint8
	RangeEnd   uint16
	// 0x00 for extended tuples
}

func (nt *NetworkTuple) WriteTo(w io.Writer) (int64, error) {
	a := acc(w)
	a.write16(nt.RangeStart)
	if nt.RangeStart == nt.RangeEnd {
		// non-extended tuple
		a.write8(nt.Distance)
		return a.ret()
	}
	// extended tuple
	a.write8(nt.Distance | 0x80)
	a.write16(nt.RangeEnd)
	a.write8(0x00)
	return a.ret()
}

func parseNetworkTuple(p []byte) (NetworkTuple, []byte, error) {
	if len(p) < 3 {
		return NetworkTuple{}, p, fmt.Errorf("insufficient input length %d for network tuple", len(p))
	}

	var nt NetworkTuple
	nt.RangeStart = binary.BigEndian.Uint16(p[:2])
	nt.RangeEnd = nt.RangeStart
	nt.Distance = p[2]

	if nt.Distance&0x80 == 0 {
		return nt, p[3:], nil
	}

	if len(p) < 6 {
		return NetworkTuple{}, p, fmt.Errorf("insufficient input length %d for extended network tuple", len(p))
	}

	nt.Distance &^= 0x80
	nt.RangeEnd = binary.BigEndian.Uint16(p[3:5])
	return nt, p[6:], nil
}

type EventTuples []EventTuple

func (e EventTuples) WriteTo(w io.Writer) (int64, error) {
	a := acc(w)
	for _, et := range e {
		a.writeTo(&et)
	}
	return a.ret()
}

func parseEventTuples(p []byte) (EventTuples, error) {
	// Each event tuple is at least 4 bytes, so we need to store at most
	// len(p)/4 of them.
	e := make(EventTuples, 0, len(p)/4)
	for len(p) > 0 {
		et, nextp, err := parseEventTuple(p)
		if err != nil {
			return nil, fmt.Errorf("parsing event tuple %d: %w", len(e), err)
		}
		e = append(e, et)
		p = nextp
	}
	return e, nil
}

type EventTuple struct {
	EventCode  EventCode
	RangeStart uint16
	Distance   uint8
	RangeEnd   uint16
}

func (et *EventTuple) WriteTo(w io.Writer) (int64, error) {
	a := acc(w)
	a.write8(uint8(et.EventCode))
	a.write16(et.RangeStart)
	if et.RangeStart == et.RangeEnd {
		// non-extended tuple
		a.write8(et.Distance)
		return a.ret()
	}
	// extended tuple
	a.write8(et.Distance | 0x80)
	a.write16(et.RangeEnd)
	return a.ret()
}

func parseEventTuple(p []byte) (EventTuple, []byte, error) {
	if len(p) < 4 {
		return EventTuple{}, p, fmt.Errorf("insufficient input length %d for network event tuple", len(p))
	}

	var et EventTuple
	et.EventCode = EventCode(p[0])
	et.RangeStart = binary.BigEndian.Uint16(p[1:3])
	et.RangeEnd = et.RangeStart
	et.Distance = p[3]

	if et.Distance&0x80 == 0 {
		return et, p[4:], nil
	}

	if len(p) < 6 {
		return EventTuple{}, p, fmt.Errorf("insufficient input length %d for extended network event tuple", len(p))
	}

	et.Distance &^= 0x80
	et.RangeEnd = binary.BigEndian.Uint16(p[4:6])
	return et, p[6:], nil
}

type EventCode uint8

const (
	EventCodeNull EventCode = 0
	EventCodeNA   EventCode = 1
	EventCodeND   EventCode = 2
	EventCodeNRC  EventCode = 3
	EventCodeNDC  EventCode = 4
	EventCodeZC   EventCode = 5
)
