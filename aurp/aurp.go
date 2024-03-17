// Package aurp implements types for encoding and decoding AppleTalk
// Update-Based Routing Protocol (AURP, RFC 1504) messages.
package aurp

import (
	"encoding/binary"
	"fmt"
	"io"
)

// TrHeader represent an AURP-Tr packet header. It includes the domain header.
type TrHeader struct {
	*DomainHeader

	ConnectionID uint16
	Sequence     uint16 // Note: 65535 is succeeded by 1, not 0
}

// WriteTo writes the encoded form of the header to w, including the domain
// header.
func (h *TrHeader) WriteTo(w io.Writer) (int64, error) {
	a := acc(w)
	a.writeTo(h.DomainHeader)
	a.write16(h.ConnectionID)
	a.write16(h.Sequence)
	return a.ret()
}

func parseTrHeader(p []byte) (*TrHeader, []byte, error) {
	if len(p) < 4 {
		return nil, p, fmt.Errorf("insufficient input length %d for tr header", len(p))
	}
	return &TrHeader{
		ConnectionID: binary.BigEndian.Uint16(p[:2]),
		Sequence:     binary.BigEndian.Uint16(p[2:4]),
	}, p[4:], nil
}

// Header represents an AURP packet header. It includes the AURP-Tr header,
// which includes the domain header.
type Header struct {
	*TrHeader

	CommandCode CmdCode
	Flags       RoutingFlag
}

// WriteTo writes the encoded form of the header to w.
func (h *Header) WriteTo(w io.Writer) (int64, error) {
	a := acc(w)
	a.writeTo(h.TrHeader)
	a.write16(uint16(h.CommandCode))
	a.write16(uint16(h.Flags))
	return a.ret()
}

func parseHeader(p []byte) (*Header, []byte, error) {
	if len(p) < 4 {
		return nil, p, fmt.Errorf("insufficient input length %d for header", len(p))
	}
	return &Header{
		CommandCode: CmdCode(binary.BigEndian.Uint16(p[:2])),
		Flags:       RoutingFlag(binary.BigEndian.Uint16(p[2:4])),
	}, p[4:], nil
}

// CmdCode is the command code used in AURP packets.
type CmdCode uint16

// Various command codes.
const (
	CmdCodeRIReq     CmdCode = 0x0001
	CmdCodeRIRsp     CmdCode = 0x0002
	CmdCodeRIAck     CmdCode = 0x0003
	CmdCodeRIUpd     CmdCode = 0x0004
	CmdCodeRD        CmdCode = 0x0005
	CmdCodeZoneReq   CmdCode = 0x0006 // has subcodes
	CmdCodeZoneRsp   CmdCode = 0x0007 // has subcodes
	CmdCodeOpenReq   CmdCode = 0x0008
	CmdCodeOpenRsp   CmdCode = 0x0009
	CmdCodeTickle    CmdCode = 0x000e
	CmdCodeTickleAck CmdCode = 0x000f
)

// CmdSubcode is used to distinguish types of zone request/response.
type CmdSubcode uint16

// Various subcodes.
const (
	CmdSubcodeZoneInfo1         CmdSubcode = 0x0001
	CmdSubcodeZoneInfo2         CmdSubcode = 0x0002 // only for responses
	CmdSubcodeGetZonesNet       CmdSubcode = 0x0003
	CmdSubcodeGetDomainZoneList CmdSubcode = 0x0004
)

// RoutingFlag is used in the flags field
type RoutingFlag uint16

const (
	// Open-Req and RI-Req
	RoutingFlagSUINA      RoutingFlag = 0x4000
	RoutingFlagSUINDOrNRC RoutingFlag = 0x2000
	RoutingFlagSUINDC     RoutingFlag = 0x1000
	RoutingFlagSUIZC      RoutingFlag = 0x0800

	// RI-Rsp and GDZL-Rsp
	RoutingFlagLast RoutingFlag = 0x8000

	// Open-Rsp
	RoutingFlagRemappingActive   RoutingFlag = 0x4000
	RoutingFlagHopCountReduction RoutingFlag = 0x2000
	RoutingFlagReservedEnv       RoutingFlag = 0x1800

	// RI-Ack
	RoutingFlagSendZoneInfo RoutingFlag = 0x4000
)

// Packet represents a full AURP packet, not including UDP or lower layers, but
// including the domain header and higher layers.
type Packet interface {
	io.WriterTo
}

// AppleTalkPacket is for encapsulated AppleTalk traffic.
type AppleTalkPacket struct {
	*DomainHeader // where PacketTypeAppleTalk

	Data []byte
}

func (p *AppleTalkPacket) WriteTo(w io.Writer) (int64, error) {
	a := acc(w)
	a.writeTo(p.DomainHeader)
	a.write(p.Data)
	return a.ret()
}

// ParsePacket parses the body of a UDP packet for a domain header, and then
// based on the packet type, an AURP-Tr header, an AURP routing header, and
// then a particular packet type.
//
// (This function contains the big switch statement.)
func ParsePacket(p []byte) (Packet, error) {
	dh, p, err := parseDomainHeader(p)
	if err != nil {
		return nil, err
	}
	if dh.Version != 1 {
		return nil, fmt.Errorf("unsupported domain header version %d", dh.Version)
	}
	switch dh.PacketType {
	case PacketTypeAppleTalk:
		return &AppleTalkPacket{
			DomainHeader: dh,
			Data:         p,
		}, nil

	case PacketTypeRouting:
		tr, p, err := parseTrHeader(p)
		if err != nil {
			return nil, err
		}
		tr.DomainHeader = dh
		h, p, err := parseHeader(p)
		if err != nil {
			return nil, err
		}
		h.TrHeader = tr

		switch h.CommandCode {
		case CmdCodeOpenReq:
			oreq, err := parseOpenReq(p)
			if err != nil {
				return nil, err
			}
			oreq.Header = h
			return oreq, nil

		case CmdCodeOpenRsp:
			orsp, err := parseOpenRsp(p)
			if err != nil {
				return nil, err
			}
			orsp.Header = h
			return orsp, nil

		case CmdCodeRIReq:
			return &RIReqPacket{
				Header: h,
			}, nil

		case CmdCodeRIRsp:
			return &RIRspPacket{
				Header:   h,
				RTMPData: p,
			}, nil

		case CmdCodeRIAck:
			return &RIAckPacket{
				Header: h,
			}, nil

		case CmdCodeRIUpd:
			riu, err := parseRIUpd(p)
			if err != nil {
				return nil, err
			}
			riu.Header = h
			return riu, nil

		default:
			return nil, fmt.Errorf("unknown routing packet command code %d", h.CommandCode)
		}

	default:
		return nil, fmt.Errorf("unsupported domain header packet type %d", dh.PacketType)
	}
}
