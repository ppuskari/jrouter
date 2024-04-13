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
	ZoneInvalid  bool // 0x80
	UseBroadcast bool // 0x40
	OnlyOneZone  bool // 0x20
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
