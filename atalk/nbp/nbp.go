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

package nbp

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/sfiera/multitalk/pkg/ddp"
)

// Function represents the NBP packet Function field.
type Function uint8

// Various functions.
const (
	FunctionBrRq      Function = 1
	FunctionLkUp      Function = 2
	FunctionLkUpReply Function = 3 // can have more than 1 tuple
	FunctionFwdReq    Function = 4
)

func (f Function) String() string {
	return map[Function]string{
		FunctionBrRq:      "BrRq",
		FunctionLkUp:      "LkUp",
		FunctionLkUpReply: "LkUp-Reply",
		FunctionFwdReq:    "FwdReq",
	}[f]
}

// Packet represents an NBP packet.
type Packet struct {
	Function Function // top 4 bits of first byte
	// TupleCount uint4 // bottom 4 bits of first byte
	NBPID  uint8
	Tuples []Tuple
}

func (p *Packet) Marshal() ([]byte, error) {
	b := bytes.NewBuffer(nil)
	if p.Function < 1 || p.Function > 4 {
		return nil, fmt.Errorf("invalid NBP function %d", p.Function)
	}
	if len(p.Tuples) > 15 {
		return nil, fmt.Errorf("too many NBP tuples (%d > 15)", len(p.Tuples))
	}
	b.WriteByte(byte(p.Function<<4) | byte(len(p.Tuples)))
	b.WriteByte(p.NBPID)
	for i, t := range p.Tuples {
		if err := t.writeTo(b); err != nil {
			return nil, fmt.Errorf("marshaling NBP tuple %d: %w", i, err)
		}
	}
	return b.Bytes(), nil
}

func Unmarshal(data []byte) (*Packet, error) {
	if len(data) < 2 {
		return nil, fmt.Errorf("insufficient input length %d for NBP packet", len(data))
	}
	p := &Packet{
		Function: Function(data[0] >> 4),
		NBPID:    data[1],
	}
	tupleCount := data[0] & 0x0F
	if tupleCount == 0 {
		return nil, fmt.Errorf("no tuples")
	}
	// Only LkUp-Reply can have more than 1 tuple
	if tupleCount > 1 && p.Function != FunctionLkUpReply {
		return nil, fmt.Errorf("wrong number of tuples %d for function %s", tupleCount, p.Function)
	}

	data = data[2:]
	for range tupleCount {
		if len(data) < 8 {
			return nil, fmt.Errorf("insufficient remaining input length %d for NBP tuple", len(data))
		}
		t := Tuple{
			Network:    ddp.Network(binary.BigEndian.Uint16(data[:2])),
			Node:       ddp.Node(data[2]),
			Socket:     data[3],
			Enumerator: data[4],
		}
		data = data[5:]

		var err error
		t.Object, data, err = readLV(data)
		if err != nil {
			return nil, fmt.Errorf("reading NBP tuple Object: %w", err)
		}
		t.Type, data, err = readLV(data)
		if err != nil {
			return nil, fmt.Errorf("reading NBP tuple Type: %w", err)
		}
		t.Zone, data, err = readLV(data)
		if err != nil {
			return nil, fmt.Errorf("reading NBP tuple Zone: %w", err)
		}
		p.Tuples = append(p.Tuples, t)
	}
	return p, nil
}

// Tuple represents an NBP tuple.
type Tuple struct {
	Network    ddp.Network
	Node       ddp.Node
	Socket     uint8
	Enumerator uint8
	Object     string // length-prefixed
	Type       string // length-prefixed
	Zone       string // length-prefixed; "" interpreted as "*"
}

func (t *Tuple) writeTo(b *bytes.Buffer) error {
	if len(t.Object) > 32 {
		return fmt.Errorf("object field too long (%d > 32)", len(t.Object))
	}
	if len(t.Type) > 32 {
		return fmt.Errorf("type field too long (%d > 32)", len(t.Type))
	}
	if len(t.Zone) > 32 {
		return fmt.Errorf("zone field too long (%d > 32)", len(t.Zone))
	}
	write16(b, t.Network)
	b.WriteByte(byte(t.Node))
	b.WriteByte(t.Socket)
	b.WriteByte(t.Enumerator)
	b.WriteByte(byte(len(t.Object)))
	b.WriteString(t.Object)
	b.WriteByte(byte(len(t.Type)))
	b.WriteString(t.Type)
	b.WriteByte(byte(len(t.Zone)))
	b.WriteString(t.Zone)
	return nil
}

func (t Tuple) String() string {
	return fmt.Sprintf("%d.%d.%d (enum %d) <-> %s:%s@%s",
		t.Network, t.Node, t.Socket, t.Enumerator, t.Object, t.Type, t.Zone,
	)
}

func write16[I ~uint16](b *bytes.Buffer, n I) {
	b.Write([]byte{byte(n >> 8), byte(n & 0xff)})
}

func readLV(data []byte) (string, []byte, error) {
	if len(data) < 1 {
		return "", data, fmt.Errorf("insufficient input length %d for length-prefixed string", len(data))
	}
	slen := int(data[0])
	data = data[1:]
	if len(data) < slen {
		return "", data, fmt.Errorf("insufficient remaining input length %d for length-prefixed string of length %d", len(data), slen)
	}
	return string(data[:slen]), data[slen:], nil
}
