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

	"gitea.drjosh.dev/josh/jrouter/atalk/atp"
)

type GetZonesPacket struct {
	// "These requests always ask for a single response packet."
	// TReq   = 0b01000000
	// Bitmap = 0b00000001
	TID uint16
	// --- ATP user bytes
	Function Function // 7, 8, or 9
	// Pad uint8 = 0
	StartIndex uint16 // always 0 for
}

func (p *GetZonesPacket) MarshalTReq() (*atp.TReq, error) {
	return &atp.TReq{
		Bitmap:        0b00000001,
		TransactionID: p.TID,
		UserBytes: [4]byte{
			byte(p.Function), 0, byte(p.StartIndex >> 8), byte(p.StartIndex & 0xFF),
		},
	}, nil
}

type GetZonesReplyPacket struct {
	// TResp    = 0b10000000
	// Sequence = 0
	TID uint16
	// --- ATP user bytes
	LastFlag bool // not used for GetMyZone
	// Pad uint8 = 0
	// ZoneCount uint16
	// --- ATP data bytes
	Zones []string // length-prefixed
}

func (p *GetZonesReplyPacket) MarshalTResp() (*atp.TResp, error) {
	r := &atp.TResp{
		EndOfMessage:  true,
		Sequence:      0,
		TransactionID: p.TID,
	}
	if p.LastFlag {
		r.UserBytes[0] = 1
	}
	zc := uint16(len(p.Zones))
	r.UserBytes[2] = byte(zc >> 8)
	r.UserBytes[3] = byte(zc & 0xFF)
	b := bytes.NewBuffer(nil)
	for _, z := range p.Zones {
		if len(z) > 32 {
			return nil, fmt.Errorf("zone name %q too long [%d > 32]", z, len(z))
		}
		b.WriteByte(byte(len(z)))
		b.WriteString(z)
	}
	r.Data = b.Bytes()
	return r, nil
}

func UnmarshalTReq(treq *atp.TReq) (*GetZonesPacket, error) {
	if treq == nil {
		return nil, fmt.Errorf("nil *TReq")
	}
	fn := treq.UserBytes[0]
	if fn != FunctionGetZoneList && fn != FunctionGetLocalZones && fn != FunctionGetMyZone {
		return nil, fmt.Errorf("invalid ZIP function %d", fn)
	}
	return &GetZonesPacket{
		TID:        treq.TransactionID,
		Function:   Function(treq.UserBytes[0]),
		StartIndex: binary.BigEndian.Uint16(treq.UserBytes[2:4]),
	}, nil
}

func UnmarshalTResp(tresp *atp.TResp) (*GetZonesReplyPacket, error) {
	if tresp == nil {
		return nil, fmt.Errorf("nil *TResp")
	}
	p := &GetZonesReplyPacket{
		TID:      tresp.TransactionID,
		LastFlag: tresp.UserBytes[0] != 0,
	}
	data := tresp.Data
	zc := binary.BigEndian.Uint16(tresp.UserBytes[2:4])
	for range zc {
		if len(data) < 1 {
			return nil, fmt.Errorf("insufficient remaining TResp data length %d for length-prefixed string", len(data))
		}
		slen := data[0]
		data = data[1:]
		if len(data) < int(slen) {
			return nil, fmt.Errorf("insufficient remaining TResp data length %d for length-%d prefixed string", len(data), slen)
		}
		p.Zones = append(p.Zones, string(data[:slen]))
		data = data[slen:]
	}
	if len(data) > 0 {
		return nil, fmt.Errorf("%d bytes left over at end of packet", len(data))
	}
	return p, nil
}
