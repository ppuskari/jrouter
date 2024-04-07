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

package rtmp

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/sfiera/multitalk/pkg/ddp"
)

// ResponsePacket represents an RTMP Response packet.
type ResponsePacket struct {
	SenderAddr ddp.Addr
	Extended   bool
	RangeStart ddp.Network
	RangeEnd   ddp.Network
}

// Marshal marshals an RTMP Response packet.
func (rp *ResponsePacket) Marshal() ([]byte, error) {
	b := bytes.NewBuffer(nil)
	b.Grow(10)
	write16(b, rp.SenderAddr.Network)
	b.WriteByte(8)
	b.WriteByte(byte(rp.SenderAddr.Node))
	if !rp.Extended {
		return b.Bytes(), nil
	}
	write16(b, rp.RangeStart)
	b.WriteByte(0x80)
	write16(b, rp.RangeEnd)
	b.WriteByte(0x82)
	return b.Bytes(), nil
}

// UnmarshalResponsePacket unmarshals an RTMP Response packet.
func UnmarshalResponsePacket(data []byte) (*ResponsePacket, error) {
	if len(data) != 4 && len(data) != 10 {
		return nil, fmt.Errorf("invalid input length %d for RTMP Response packet", len(data))
	}
	if data[2] != 8 {
		return nil, fmt.Errorf("unsupported node ID length %d for RTMP Response packet", data[2])
	}
	rp := &ResponsePacket{
		SenderAddr: ddp.Addr{
			Network: ddp.Network(binary.BigEndian.Uint16(data[:2])),
			Node:    ddp.Node(data[3]),
		},
	}
	if len(data) == 4 {
		return rp, nil
	}

	rp.RangeStart = ddp.Network(binary.BigEndian.Uint16(data[4:6]))
	if data[6] != 0x80 {
		return nil, fmt.Errorf("invalid intermediate byte %x for RTMP Response packet", data[6])
	}
	rp.RangeEnd = ddp.Network(binary.BigEndian.Uint16(data[7:9]))
	if data[9] != 0x82 {
		return nil, fmt.Errorf("unsupported version %x for RTMP Response packet", data[9])
	}
	return rp, nil
}
