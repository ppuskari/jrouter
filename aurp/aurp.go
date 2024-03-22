// Package aurp implements types for encoding and decoding AppleTalk
// Update-Based Routing Protocol (AURP, RFC 1504) messages.
package aurp

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Header represents an AURP packet header. It includes the AURP-Tr header,
// which includes the domain header.
type Header struct {
	TrHeader

	CommandCode CmdCode
	Flags       RoutingFlag
}

// WriteTo writes the encoded form of the header to w.
func (h *Header) WriteTo(w io.Writer) (int64, error) {
	a := acc(w)
	a.writeTo(&h.TrHeader)
	a.write16(uint16(h.CommandCode))
	a.write16(uint16(h.Flags))
	return a.ret()
}

func parseHeader(p []byte) (Header, []byte, error) {
	if len(p) < 4 {
		return Header{}, p, fmt.Errorf("insufficient input length %d for header", len(p))
	}
	return Header{
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

// RoutingFlag is used in the flags field
type RoutingFlag uint16

const (
	// Open-Req and RI-Req (SUI flags)
	RoutingFlagSUINA      RoutingFlag = 0x4000
	RoutingFlagSUINDOrNRC RoutingFlag = 0x2000
	RoutingFlagSUINDC     RoutingFlag = 0x1000
	RoutingFlagSUIZC      RoutingFlag = 0x0800

	// The combination of the above four flags (the SUI flags).
	RoutingFlagAllSUI RoutingFlag = 0x7800

	// RI-Rsp and GDZL-Rsp
	RoutingFlagLast RoutingFlag = 0x8000

	// Open-Rsp (environment flags)
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
			rir, err := parseRIRsp(p)
			if err != nil {
				return nil, err
			}
			rir.Header = h
			return rir, nil

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

		case CmdCodeRD:
			rd, err := parseRD(p)
			if err != nil {
				return nil, err
			}
			rd.Header = h
			return rd, nil

		case CmdCodeZoneReq:
			sc, p, err := parseSubcode(p)
			if err != nil {
				return nil, err
			}
			switch sc {
			case SubcodeZoneInfoReq:
				zir, err := parseZIReqPacket(p)
				if err != nil {
					return nil, err
				}
				zir.Header = h
				return zir, nil

			case SubcodeGetDomainZoneList:
				gdzl, err := parseGDZLReqPacket(p)
				if err != nil {
					return nil, err
				}
				gdzl.Header = h
				return gdzl, nil

			case SubcodeGetZonesNet:
				gzn, err := parseGZNReqPacket(p)
				if err != nil {
					return nil, err
				}
				gzn.Header = h
				return gzn, nil

			default:
				return nil, fmt.Errorf("unknown subcode %d", sc)
			}

		case CmdCodeZoneRsp:
			sc, p, err := parseSubcode(p)
			if err != nil {
				return nil, err
			}
			switch sc {
			case SubcodeZoneInfoNonExt, SubcodeZoneInfoExt:
				zir, err := parseZIRspPacket(p)
				if err != nil {
					return nil, err
				}
				zir.Header = h
				zir.Subcode = sc // 1 or 2, only known at this layer
				return zir, nil

			case SubcodeGetDomainZoneList:
				gdzl, err := parseGDZLRspPacket(p)
				if err != nil {
					return nil, err
				}
				gdzl.Header = h
				return gdzl, nil

			case SubcodeGetZonesNet:
				gzn, err := parseGZNRspPacket(p)
				if err != nil {
					return nil, err
				}
				gzn.Header = h
				return gzn, nil

			default:
				return nil, fmt.Errorf("unknown subcode %d", sc)
			}

		case CmdCodeTickle:
			return &TicklePacket{
				Header: h,
			}, nil

		case CmdCodeTickleAck:
			return &TickleAckPacket{
				Header: h,
			}, nil

		default:
			return nil, fmt.Errorf("unknown routing packet command code %d", h.CommandCode)
		}

	default:
		return nil, fmt.Errorf("unsupported domain header packet type %d", dh.PacketType)
	}
}
