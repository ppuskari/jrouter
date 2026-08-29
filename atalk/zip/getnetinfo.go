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

package zip

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/sfiera/multitalk/pkg/ddp"
	"github.com/sfiera/multitalk/pkg/ethernet"
)

type GetNetInfoPacket struct {
	// Destination socket = 6
	// DDP type = 6
	// ---
	// ZIP command = 5
	// Flags = 0 (reserved)
	// Four more bytes of 0 (reserved)
	// Zone name length (1 byte)
	ZoneName string
}

func (p *GetNetInfoPacket) Marshal() ([]byte, error) {
	if len(p.ZoneName) > 32 {
		return nil, fmt.Errorf("zone name too long [%d > 32]", len(p.ZoneName))
	}
	b := bytes.NewBuffer(nil)
	b.WriteByte(FunctionGetNetInfo)
	b.Write(make([]byte, 5))
	b.WriteByte(byte(len(p.ZoneName)))
	b.WriteString(p.ZoneName)
	return b.Bytes(), nil
}

func UnmarshalGetNetInfoPacket(data []byte) (*GetNetInfoPacket, error) {
	if len(data) < 7 {
		return nil, fmt.Errorf("insufficient input length %d for GetNetInfo packet", len(data))
	}
	if data[0] != FunctionGetNetInfo {
		return nil, fmt.Errorf("not a GetNetInfo packet (ZIP command %d != %d)", data[0], FunctionGetNetInfo)
	}
	slen := data[6]
	data = data[7:]
	if len(data) != int(slen) {
		return nil, fmt.Errorf("wrong remaining input length %d for length=%d-prefixed string", len(data), slen)
	}
	return &GetNetInfoPacket{
		ZoneName: string(data),
	}, nil
}

type GetNetInfoReplyPacket struct {
	// Source socket = 6
	// DDP type = 6
	// ---
	// ZIP command = 6
	ZoneInvalid  bool // 0x80 - "set if the zone name in the request is invalid for the network from which the request was sent"
	UseBroadcast bool // 0x40 - "set for data links that do not support multicast"
	OnlyOneZone  bool // 0x20 - "set if the network's zone list contains only one zone name"
	// Remainder of flags reserved
	NetStart ddp.Network
	NetEnd   ddp.Network
	// Zone name length (1 byte)
	ZoneName string
	// Multicast address length (1 byte)
	MulticastAddr ethernet.Addr
	// Only if ZoneInvalid flag is set:
	// Default zone length (1 byte)
	DefaultZoneName string
}

func UnmarshalGetNetInfoReplyPacket(data []byte) (*GetNetInfoReplyPacket, error) {
	if len(data) < 14 {
		return nil, fmt.Errorf("insufficient input length %d for GetNetInfo Reply", len(data))
	}
	if data[0] != FunctionGetNetInfoReply {
		return nil, fmt.Errorf("not a GetNetInfo Reply (ZIP command %d != %d)", data[0], FunctionGetNetInfoReply)
	}

	flags := data[1]
	p := &GetNetInfoReplyPacket{
		ZoneInvalid:  flags&0x80 != 0,
		UseBroadcast: flags&0x40 != 0,
		OnlyOneZone:  flags&0x20 != 0,
		NetStart:     ddp.Network(binary.BigEndian.Uint16(data[2:4])),
		NetEnd:       ddp.Network(binary.BigEndian.Uint16(data[4:6])),
	}

	data = data[6:]
	if len(data) < 1 {
		return nil, fmt.Errorf("missing zone name length")
	}
	zoneLen := int(data[0])
	data = data[1:]
	if len(data) < zoneLen+1 {
		return nil, fmt.Errorf("insufficient input for zone name")
	}
	p.ZoneName = string(data[:zoneLen])
	data = data[zoneLen:]

	mcastLen := int(data[0])
	data = data[1:]
	if mcastLen != 6 || len(data) < mcastLen {
		return nil, fmt.Errorf("invalid multicast address length %d", mcastLen)
	}
	copy(p.MulticastAddr[:], data[:mcastLen])
	data = data[mcastLen:]

	if p.ZoneInvalid {
		if len(data) < 1 {
			return nil, fmt.Errorf("missing default zone name length")
		}
		defaultLen := int(data[0])
		data = data[1:]
		if len(data) != defaultLen {
			return nil, fmt.Errorf("wrong remaining input length %d for default zone length %d", len(data), defaultLen)
		}
		p.DefaultZoneName = string(data)
	} else if len(data) != 0 {
		return nil, fmt.Errorf("unexpected trailing data length %d", len(data))
	}

	return p, nil
}

func (p *GetNetInfoReplyPacket) Marshal() ([]byte, error) {
	if len(p.ZoneName) > 32 {
		return nil, fmt.Errorf("zone name too long [%d > 32]", len(p.ZoneName))
	}
	if len(p.DefaultZoneName) > 32 {
		return nil, fmt.Errorf("default zone name too long [%d > 32]", len(p.DefaultZoneName))
	}

	b := bytes.NewBuffer(nil)
	b.WriteByte(FunctionGetNetInfoReply)
	var flags byte
	if p.ZoneInvalid {
		flags |= 0x80
	}
	if p.UseBroadcast {
		flags |= 0x40
	}
	if p.OnlyOneZone {
		flags |= 0x20
	}
	b.WriteByte(flags)
	write16(b, p.NetStart)
	write16(b, p.NetEnd)
	b.WriteByte(byte(len(p.ZoneName)))
	b.WriteString(p.ZoneName)
	b.WriteByte(6)
	b.Write(p.MulticastAddr[:])
	if p.ZoneInvalid {
		b.WriteByte(byte(len(p.DefaultZoneName)))
		b.WriteString(p.DefaultZoneName)
	}
	return b.Bytes(), nil
}
