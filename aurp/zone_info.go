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
	"strings"

	"github.com/sfiera/multitalk/pkg/ddp"
)

// Subcode is used to distinguish types of zone request/response.
type Subcode uint16

// Various subcodes.
const (
	SubcodeZoneInfoReq       Subcode = 0x0001
	SubcodeZoneInfoNonExt    Subcode = 0x0001
	SubcodeZoneInfoExt       Subcode = 0x0002
	SubcodeGetZonesNet       Subcode = 0x0003
	SubcodeGetDomainZoneList Subcode = 0x0004
)

func parseSubcode(p []byte) (Subcode, []byte, error) {
	if len(p) < 2 {
		return 0, p, fmt.Errorf("insufficient input length %d for subcode", len(p))
	}
	return Subcode(binary.BigEndian.Uint16(p[:2])), p[2:], nil
}

type ZIReqPacket struct {
	Header
	Subcode
	Networks []ddp.Network
}

func (p *ZIReqPacket) WriteTo(w io.Writer) (int64, error) {
	a := acc(w)
	a.writeTo(&p.Header)
	a.write16(uint16(p.Subcode))
	for _, n := range p.Networks {
		a.write16(uint16(n))
	}
	return a.ret()
}

func parseZIReqPacket(p []byte) (*ZIReqPacket, error) {
	if len(p)%2 != 0 {
		return nil, fmt.Errorf("odd number of bytes %d for networks", len(p))
	}
	c := len(p) / 2
	ns := make([]ddp.Network, c)
	for i := range c {
		ns[i] = ddp.Network(binary.BigEndian.Uint16(p[2*i:][:2]))
	}
	return &ZIReqPacket{
		Subcode:  SubcodeZoneInfoReq,
		Networks: ns,
	}, nil
}

// ZIRspPacket represents a ZI-Rsp: a response packet to a ZI-Req.
//
// "When the data sender receives a ZI-Req and the zone list for the network or
// networks for which that ZI-Req requested zone information fits in one ZI-Rsp
// packet, it sends a nonextended ZI-Rsp."
// "When the data sender receives a ZI-Req and the zone list for a network about
// which that ZI-Req requested zone information does not fit in a single ZI-Rsp
// packet, it sends a sequence of extended ZI-Rsp packets."
// "All tuples in a single extended ZI-Rsp packet must contain the same network
// number"
// "Duplicate zone names never exist in extended ZI-Rsp packets"
type ZIRspPacket struct {
	Header
	Subcode
	Zones ZoneTuples
}

func (p *ZIRspPacket) WriteTo(w io.Writer) (int64, error) {
	a := acc(w)
	a.writeTo(&p.Header)
	a.write16(uint16(p.Subcode))
	a.writeTo(p.Zones)
	return a.ret()
}

func parseZIRspPacket(p []byte) (*ZIRspPacket, error) {
	zs, err := parseZoneTuples(p)
	if err != nil {
		return nil, err
	}
	return &ZIRspPacket{
		// Subcode needs to be provided by layer above
		Zones: zs,
	}, nil
}

type GDZLReqPacket struct {
	Header
	Subcode
	StartIndex uint16
}

func (p *GDZLReqPacket) WriteTo(w io.Writer) (int64, error) {
	a := acc(w)
	a.writeTo(&p.Header)
	a.write16(uint16(p.Subcode))
	a.write16(p.StartIndex)
	return a.ret()
}

func parseGDZLReqPacket(p []byte) (*GDZLReqPacket, error) {
	if len(p) < 2 {
		return nil, fmt.Errorf("insufficient input length %d for GDZL-Req packet", len(p))
	}
	return &GDZLReqPacket{
		Subcode:    SubcodeGetDomainZoneList,
		StartIndex: binary.BigEndian.Uint16(p[:2]),
	}, nil
}

type GDZLRspPacket struct {
	Header
	Subcode
	StartIndex int16
	ZoneNames  []string
}

func (p *GDZLRspPacket) WriteTo(w io.Writer) (int64, error) {
	for _, zn := range p.ZoneNames {
		if len(zn) > 127 {
			return 0, fmt.Errorf("zone name %q too long", zn)
		}
	}

	a := acc(w)
	a.writeTo(&p.Header)
	a.write16(uint16(p.Subcode))
	a.write16(uint16(p.StartIndex))
	if p.StartIndex == -1 {
		return a.ret()
	}
	for _, zn := range p.ZoneNames {
		// The spec is not clear what format these take, and Apple's example
		// implementation always returns -1 (not supported), and I have no
		// packet captures of this subcode.
		// I'm guessing they're Pascal-style (length-prefixed) since that's used
		// in the ZI-Rsp long tuples as well as throughout all the ancient Mac
		// code.
		a.write8(uint8(len(zn)))
		a.write([]byte(zn))
	}
	return a.ret()
}

func parseGDZLRspPacket(p []byte) (*GDZLRspPacket, error) {
	if len(p) < 2 {
		return nil, fmt.Errorf("insufficient input length %d for GDZL-Rsp packet", len(p))
	}
	gdzl := &GDZLRspPacket{
		Subcode:    SubcodeGetDomainZoneList,
		StartIndex: int16(binary.BigEndian.Uint16(p[:2])),
	}
	if gdzl.StartIndex == -1 {
		return gdzl, nil
	}
	// See comment in GDZLRspPacket.WriteTo about the assumption here.
	p = p[2:]
	for len(p) > 0 {
		strLen := p[0]
		p = p[1:]
		if len(p) < int(strLen) {
			return nil, fmt.Errorf("insufficient remaining input length %d for zone name with length prefix %d", len(p), strLen)
		}
		gdzl.ZoneNames = append(gdzl.ZoneNames, string(p[:strLen]))
		p = p[strLen:]
	}

	return gdzl, nil
}

type GZNReqPacket struct {
	Header
	Subcode
	ZoneName string
}

func (p *GZNReqPacket) WriteTo(w io.Writer) (int64, error) {
	if len(p.ZoneName) > 127 {
		return 0, fmt.Errorf("zone name %q too long", p.ZoneName)
	}

	a := acc(w)
	a.writeTo(&p.Header)
	a.write16(uint16(p.Subcode))
	a.write8(uint8(len(p.ZoneName)))
	a.write([]byte(p.ZoneName))
	return a.ret()
}

func parseGZNReqPacket(p []byte) (*GZNReqPacket, error) {
	if len(p) < 1 {
		return nil, fmt.Errorf("insufficient input length %d for GZN-Req packet", len(p))
	}
	strLen := p[0]
	p = p[1:]
	if len(p) < int(strLen) {
		return nil, fmt.Errorf("insufficient remaining input length %d for zone name with length prefix %d", len(p), strLen)
	}
	return &GZNReqPacket{
		Subcode:  SubcodeGetZonesNet,
		ZoneName: string(p[:strLen]),
	}, nil
}

type GZNRspPacket struct {
	Header
	Subcode
	ZoneName     string
	NotSupported bool
	Networks     NetworkTuples
}

func (p *GZNRspPacket) WriteTo(w io.Writer) (int64, error) {
	if len(p.ZoneName) > 127 {
		return 0, fmt.Errorf("zone name %q too long", p.ZoneName)
	}

	a := acc(w)
	a.writeTo(&p.Header)
	a.write16(uint16(p.Subcode))
	a.write8(uint8(len(p.ZoneName)))
	if p.NotSupported {
		a.write16(0xffff) // -1
		return a.ret()
	}
	a.writeTo(p.Networks)
	return a.ret()
}

func parseGZNRspPacket(p []byte) (*GZNRspPacket, error) {
	if len(p) < 1 {
		return nil, fmt.Errorf("insufficient input length %d for GZN-Rsp packet", len(p))
	}
	gzn := &GZNRspPacket{
		Subcode: SubcodeGetZonesNet,
	}

	strLen := p[0]
	p = p[1:]
	if len(p) < int(strLen) {
		return nil, fmt.Errorf("insufficient remaining input length %d for zone name with length prefix %d", len(p), strLen)
	}
	gzn.ZoneName = string(p[:strLen])
	p = p[strLen:]

	if len(p) < 2 {
		return nil, fmt.Errorf("insufficient remaining input length %d for GZN-Rsp packet", len(p))
	}
	gzn.NotSupported = p[0] == 0xff && p[1] == 0xff
	if gzn.NotSupported {
		return gzn, nil
	}

	ns, err := parseNetworkTuples(p)
	if err != nil {
		return nil, err
	}
	gzn.Networks = ns
	return gzn, nil
}

type ZoneTuples []ZoneTuple

func (zs ZoneTuples) String() string {
	var sb strings.Builder
	for i, zt := range zs {
		if i > 0 {
			sb.WriteString(", ")
		}
		fmt.Fprintf(&sb, "%d %q", zt.Network, zt.Name)
	}
	return sb.String()
}

type ZoneTuple struct {
	Network ddp.Network
	Name    string
}

func (zs ZoneTuples) WriteTo(w io.Writer) (int64, error) {
	if len(zs) > 65535 {
		return 0, fmt.Errorf("too many zone tuples [%d > 65535]", len(zs))
	}
	for _, zt := range zs {
		if len(zt.Name) > 127 {
			return 0, fmt.Errorf("zone name %q too long", zt.Name)
		}
	}

	a := acc(w)
	a.write16(uint16(len(zs)))
	offsets := make(map[string]uint16)

	for _, zt := range zs {
		a.write16(uint16(zt.Network))

		if offset, wrote := offsets[zt.Name]; wrote {
			// Optimised tuple
			a.write16(0x8000 | offset)
			continue
		}
		// Long tuple
		offsets[zt.Name] = uint16(a.n - 4) // 4 = sizeof(zone count) + sizeof(first network number)
		a.write8(uint8(len(zt.Name)))
		a.write([]byte(zt.Name))
	}

	return a.ret()
}

func parseZoneTuples(p []byte) (ZoneTuples, error) {
	if len(p) < 2 {
		return nil, fmt.Errorf("insufficient input length %d for zone tuples", len(p))
	}
	count := binary.BigEndian.Uint16(p[:2])
	p = p[2:]

	if len(p) < int(3*count) {
		return nil, fmt.Errorf("insufficient remaining input length %d for %d zone tuples", len(p), count)
	}

	zs := make(ZoneTuples, 0, count)
	var fromFirst []byte
	for range count {
		if len(p) < 3 {
			return nil, fmt.Errorf("insufficient remaining input length %d for another zone tuple", len(p))
		}
		var zt ZoneTuple
		zt.Network = ddp.Network(binary.BigEndian.Uint16(p[:2]))
		p = p[2:]
		if nameLen := p[0]; nameLen&0x80 == 0 {
			// Long tuple
			if fromFirst == nil {
				fromFirst = p
			}
			p = p[1:]
			if len(p) < int(nameLen) {
				return nil, fmt.Errorf("insufficient remaining input length %d for zone name of length %d", len(p), nameLen)
			}
			zt.Name = string(p[:nameLen])
			p = p[nameLen:]
		} else {
			// Optimised tuple
			if len(p) < 2 {
				return nil, fmt.Errorf("insufficient remaining input length %d for offset", len(p))
			}
			offset := binary.BigEndian.Uint16(p[:2])
			offset &^= 0x8000
			p = p[2:]
			if int(offset) >= len(fromFirst) {
				return nil, fmt.Errorf("optimized zone tuple offset %d out of range", offset)
			}
			nameLen := fromFirst[offset]
			if len(fromFirst[offset+1:]) < int(nameLen) {
				return nil, fmt.Errorf("insufficient remaining input length %d for zone name of length %d", len(p), nameLen)
			}
			zt.Name = string(fromFirst[offset+1:][:nameLen])
		}

		zs = append(zs, zt)
	}
	return zs, nil
}
