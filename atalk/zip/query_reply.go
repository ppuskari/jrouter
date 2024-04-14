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
)

type QueryPacket struct {
	// Function = 1
	// NetworkCount uint8
	Networks []ddp.Network
}

func (p *QueryPacket) Marshal() ([]byte, error) {
	if len(p.Networks) > 255 {
		return nil, fmt.Errorf("too many networks [%d > 255]", len(p.Networks))
	}
	b := bytes.NewBuffer(nil)
	b.WriteByte(FunctionQuery)
	b.WriteByte(byte(len(p.Networks)))
	for _, n := range p.Networks {
		write16(b, n)
	}
	return b.Bytes(), nil
}

func UnmarshalQueryPacket(data []byte) (*QueryPacket, error) {
	if len(data) < 2 {
		return nil, fmt.Errorf("insufficient input length %d for ZIP Query packet", len(data))
	}
	if data[0] != FunctionQuery {
		return nil, fmt.Errorf("not a ZIP Query packet (funciton = %d)", data[0])
	}
	p := &QueryPacket{
		Networks: make([]ddp.Network, 0, data[1]),
	}
	for range data[1] {
		data = data[2:]
		if len(data) < 2 {
			return nil, fmt.Errorf("insufficient remaining input length %d for network number", len(data))
		}
		p.Networks = append(p.Networks, ddp.Network(binary.BigEndian.Uint16(data[:2])))
	}
	return p, nil
}

type ReplyPacket struct {
	// Function = 2 or 8
	Extended bool
	// NetworkCount uint8
	// "Replies contain the number of zones lists indicated in the Reply header"
	// and
	// "Extended Replies can contain only one zones list. ...
	// (the network numbers in each pair will all be the same in an Extended
	// Reply). The network count in the header indicates, not the number of zone
	// names in the packet, but the number of zone names in the entire zones
	// list for the requested network, which may span more than one packet."
	// and
	// "Note: Extended ZIP Replies may also be used for responding to ZIP
	// queries with zones lists that all fit in one Reply packet. In this case,
	// the network count will be equal to the number of zone names in the
	// packet"
	Networks map[ddp.Network][]string
}

func (p *ReplyPacket) Marshal() ([]byte, error) {
	if len(p.Networks) > 255 {
		return nil, fmt.Errorf("too many networks [%d > 255]", len(p.Networks))
	}
	if len(p.Networks) > 1 && p.Extended {
		return nil, fmt.Errorf("extended reply can only contain 1 network")
	}
	b := bytes.NewBuffer(nil)
	if p.Extended {
		b.WriteByte(FunctionExtendedReply)
	} else {
		b.WriteByte(FunctionReply)
		b.WriteByte(byte(len(p.Networks)))
	}
	for n, zs := range p.Networks {
		if p.Extended {
			if len(zs) > 255 {
				return nil, fmt.Errorf("too many zone names [%d > 255]", len(zs))
			}
			// TODO: handle spreading extended replies across multiple packets
			b.WriteByte(byte(len(zs)))
		}
		for _, z := range zs {
			if len(z) > 32 {
				return nil, fmt.Errorf("len(%q) > 32", z)
			}
			write16(b, n)
			b.WriteByte(byte(len(z)))
			b.WriteString(z)
		}
	}
	return b.Bytes(), nil
}

func UnmarshalReplyPacket(data []byte) (*ReplyPacket, error) {
	if len(data) < 2 {
		return nil, fmt.Errorf("insufficient input length %d for ZIP Reply packet", len(data))
	}
	if data[0] != FunctionReply && data[0] != FunctionExtendedReply {
		return nil, fmt.Errorf("not a Reply or an Extended Reply (function = %d)", data[0])
	}
	p := &ReplyPacket{
		Extended: data[0] == FunctionExtendedReply,
		Networks: make(map[ddp.Network][]string),
	}
	// "network count" is kinda irrelevant for unmarshalling?
	data = data[2:]
	for len(data) > 0 {
		if len(data) < 3 {
			return nil, fmt.Errorf("insufficinet remaining input length %d for zone tuple", len(data))
		}
		network := ddp.Network(binary.BigEndian.Uint16(data[:2]))
		slen := data[2]
		data = data[3:]
		if len(data) < int(slen) {
			return nil, fmt.Errorf("insufficient remaining input length %d for length-%d prefixed string", len(data), slen)
		}
		p.Networks[network] = append(p.Networks[network], string(data[:slen]))
		data = data[slen:]
	}
	return p, nil
}
