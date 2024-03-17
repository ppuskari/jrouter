package aurp

import (
	"encoding/binary"
	"fmt"
	"io"
)

// CmdSubcode is used to distinguish types of zone request/response.
type CmdSubcode uint16

// Various subcodes.
const (
	CmdSubcodeZoneInfoReq       CmdSubcode = 0x0001
	CmdSubcodeZoneInfoNonExt    CmdSubcode = 0x0001
	CmdSubcodeZoneInfoExt       CmdSubcode = 0x0002
	CmdSubcodeGetZonesNet       CmdSubcode = 0x0003
	CmdSubcodeGetDomainZoneList CmdSubcode = 0x0004
)

func parseSubcode(p []byte) (CmdSubcode, []byte, error) {
	if len(p) < 2 {
		return 0, p, fmt.Errorf("insufficient input length %d for subcode", len(p))
	}
	return CmdSubcode(binary.BigEndian.Uint16(p[:2])), p[2:], nil
}

type ZIReqPacket struct {
	Header

	Subcode  CmdSubcode
	Networks []uint16
}

func (p *ZIReqPacket) WriteTo(w io.Writer) (int64, error) {
	p.Sequence = 0
	p.CommandCode = CmdCodeZoneReq
	p.Flags = 0
	p.Subcode = CmdSubcodeZoneInfoReq

	a := acc(w)
	a.writeTo(&p.Header)
	a.write16(uint16(p.Subcode))
	for _, n := range p.Networks {
		a.write16(n)
	}
	return a.ret()
}

func parseZIReqPacket(p []byte) (*ZIReqPacket, error) {
	if len(p)%2 != 0 {
		return nil, fmt.Errorf("odd number of bytes %d for networks", len(p))
	}
	c := len(p) / 2
	ns := make([]uint16, 0, c)
	for i := range c {
		ns[i] = binary.BigEndian.Uint16(p[i*2:][:2])
	}
	return &ZIReqPacket{
		Subcode:  CmdSubcodeZoneInfoReq,
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
	Subcode CmdSubcode
	Zones   ZoneTuples
}

func (p *ZIRspPacket) WriteTo(w io.Writer) (int64, error) {
	p.Sequence = 0
	p.CommandCode = CmdCodeZoneRsp
	p.Flags = 0
	// Subcode can vary for this packet type: it's either 1 or 2

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

type ZoneTuples []ZoneTuple

type ZoneTuple struct {
	Network uint16
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
		a.write16(zt.Network)

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
		zt.Network = binary.BigEndian.Uint16(p[:2])
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
			if len(fromFirst) < int(nameLen) {
				return nil, fmt.Errorf("insufficient remaining input length %d for zone name of length %d", len(p), nameLen)
			}
			zt.Name = string(fromFirst[offset+1:][:nameLen])
		}

		zs = append(zs, zt)
	}
	return zs, nil
}
