/*
   Copyright 2024 Josh Deprez

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package aurp

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/sfiera/multitalk/pkg/ddp"
)

type RIReqPacket struct {
	Header
}

type RIRspPacket struct {
	Header

	Networks NetworkTuples
}

func (p *RIRspPacket) String() string {
	return fmt.Sprintf("%s\nnetworks=%v", &p.Header, p.Networks)
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

func (p *RIUpdPacket) String() string {
	return fmt.Sprintf("%s\nevents=%v", &p.Header, p.Events)
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
	Extended   bool
	RangeStart ddp.Network
	Distance   uint8
	RangeEnd   ddp.Network
	// 0x00 for extended tuples
}

func (nt NetworkTuple) String() string {
	ext := "ext"
	if !nt.Extended {
		ext = "non-ext"
	}
	return fmt.Sprintf("(%d-%d %s dist %d)", nt.RangeStart, nt.RangeEnd, ext, nt.Distance)
}

func (nt *NetworkTuple) WriteTo(w io.Writer) (int64, error) {
	a := acc(w)
	a.write16(uint16(nt.RangeStart))
	if !nt.Extended {
		// non-extended tuple
		a.write8(nt.Distance)
		return a.ret()
	}
	// extended tuple
	a.write8(nt.Distance | 0x80)
	a.write16(uint16(nt.RangeEnd))
	a.write8(0x00)
	return a.ret()
}

func parseNetworkTuple(p []byte) (NetworkTuple, []byte, error) {
	if len(p) < 3 {
		return NetworkTuple{}, p, fmt.Errorf("insufficient input length %d for network tuple", len(p))
	}

	var nt NetworkTuple
	nt.RangeStart = ddp.Network(binary.BigEndian.Uint16(p[:2]))
	nt.RangeEnd = nt.RangeStart
	nt.Distance = p[2]
	nt.Extended = nt.Distance&0x80 != 0

	if !nt.Extended {
		return nt, p[3:], nil
	}

	if len(p) < 6 {
		return NetworkTuple{}, p, fmt.Errorf("insufficient input length %d for extended network tuple", len(p))
	}

	nt.Distance &^= 0x80
	nt.RangeEnd = ddp.Network(binary.BigEndian.Uint16(p[3:5]))
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
	// Event tuples can be 1, 4, or 6 bytes long. But the only type of length 1
	// is the Null event type sent to probe whether or not the data receiver is
	// still listening. If that's present there probably aren't any other
	// tuples. Hence len(p)/4 (rounded up) is a reasonable estimate of max tuple
	// count.
	e := make(EventTuples, 0, (len(p)+3)/4)
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
	Extended   bool
	RangeStart ddp.Network
	Distance   uint8
	RangeEnd   ddp.Network
}

func (et EventTuple) String() string {
	ext := "ext"
	if !et.Extended {
		ext = "non-ext"
	}
	return fmt.Sprintf("(%d,%s %d-%d %s dist %d)",
		et.EventCode, et.EventCode,
		et.RangeStart, et.RangeEnd, ext, et.Distance,
	)
}

func (et *EventTuple) WriteTo(w io.Writer) (int64, error) {
	a := acc(w)
	a.write8(uint8(et.EventCode))
	if et.EventCode == EventCodeNull {
		// null tuple
		return a.ret()
	}
	a.write16(uint16(et.RangeStart))
	if !et.Extended {
		// non-extended tuple
		a.write8(et.Distance)
		return a.ret()
	}
	// extended tuple
	a.write8(et.Distance | 0x80)
	a.write16(uint16(et.RangeEnd))
	return a.ret()
}

func parseEventTuple(p []byte) (EventTuple, []byte, error) {
	if len(p) < 1 {
		return EventTuple{}, p, fmt.Errorf("insufficient input length %d for any network event tuple", len(p))
	}

	var et EventTuple
	et.EventCode = EventCode(p[0])
	if et.EventCode == EventCodeNull {
		return et, p[1:], nil
	}
	if len(p) < 4 {
		return EventTuple{}, p, fmt.Errorf("insufficient input length %d for non-Null network event tuple", len(p))
	}
	et.RangeStart = ddp.Network(binary.BigEndian.Uint16(p[1:3]))
	et.RangeEnd = et.RangeStart
	et.Distance = p[3]
	et.Extended = et.Distance&0x80 != 0

	if !et.Extended {
		return et, p[4:], nil
	}

	if len(p) < 6 {
		return EventTuple{}, p, fmt.Errorf("insufficient input length %d for extended network event tuple", len(p))
	}

	et.Distance &^= 0x80
	et.RangeEnd = ddp.Network(binary.BigEndian.Uint16(p[4:6]))
	return et, p[6:], nil
}

type EventCode uint8

const (
	// Null event
	EventCodeNull EventCode = 0

	// Network added event
	EventCodeNA EventCode = 1

	// Network deleted event
	EventCodeND EventCode = 2

	// Network route change event
	EventCodeNRC EventCode = 3

	// Network distance change event
	EventCodeNDC EventCode = 4

	// Network zone change event
	// Note: "The ZC event tuple is not yet defined."
	EventCodeZC EventCode = 5
)

func (ec EventCode) String() string {
	switch ec {
	case EventCodeNull:
		return "null"
	case EventCodeNA:
		return "network added"
	case EventCodeND:
		return "network deleted"
	case EventCodeNRC:
		return "network route change"
	case EventCodeNDC:
		return "network distance change"
	case EventCodeZC:
		return "zone name change"
	default:
		return "invalid!"
	}
}
