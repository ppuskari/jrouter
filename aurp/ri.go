package aurp

import (
	"encoding/binary"
	"fmt"
	"io"
)

type RIReqPacket struct {
	*Header
}

type RIRspPacket struct {
	*Header

	RTMPData []byte
}

func (p *RIRspPacket) WriteTo(w io.Writer) (int64, error) {
	a := acc(w)
	a.writeTo(p.Header)
	a.write(p.RTMPData)
	return a.ret()
}

type RIAckPacket struct {
	*Header
}

type RIUpdPacket struct {
	*Header

	Events Events
}

func (p *RIUpdPacket) WriteTo(w io.Writer) (int64, error) {
	a := acc(w)
	a.writeTo(p.Header)
	a.writeTo(p.Events)
	return a.ret()
}

func parseRIUpd(p []byte) (*RIUpdPacket, error) {
	var e Events
	for len(p) > 0 {
		et, nextp, err := parseEventTuple(p)
		if err != nil {
			return nil, fmt.Errorf("parsing event tuple %d: %w", len(e), err)
		}
		e = append(e, et)
		p = nextp
	}
	return &RIUpdPacket{
		Events: e,
	}, nil
}

type Events []EventTuple

func (e Events) WriteTo(w io.Writer) (int64, error) {
	a := acc(w)
	for _, et := range e {
		a.writeTo(&et)
	}
	return a.ret()
}

type EventTuple struct {
	EventCode  EventCode
	RangeStart uint16 // or simply the network number
	Distance   uint8
	RangeEnd   uint16
}

func (et *EventTuple) WriteTo(w io.Writer) (int64, error) {
	a := acc(w)
	a.write8(uint8(et.EventCode))
	a.write16(et.RangeStart)
	a.write8(et.Distance)
	if et.Distance&0x80 != 0 { // extended tuple
		a.write16(et.RangeEnd)
	}
	return a.ret()
}

func parseEventTuple(p []byte) (EventTuple, []byte, error) {
	if len(p) < 4 {
		return EventTuple{}, p, fmt.Errorf("insufficient input length %d for network event tuple", len(p))
	}

	var et EventTuple
	et.EventCode = EventCode(p[0])
	et.RangeStart = binary.BigEndian.Uint16(p[1:3])
	et.Distance = p[3]
	if et.Distance&0x80 == 0 {
		return et, p[4:], nil
	}

	if len(p) < 6 {
		return EventTuple{}, p, fmt.Errorf("insufficient input length %d for extended network event tuple", len(p))
	}
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
