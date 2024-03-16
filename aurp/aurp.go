// Package aurp implements types for encoding and decoding AppleTalk
// Update-Based Routing Protocol (AURP, RFC 1504) messages.
package aurp

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
)

// DomainHeader represents the header used to encapsulate both AppleTalk data
// packets and AURP packets within UDP.
type DomainHeader struct {
	DestinationDI DomainIdentifier
	SourceDI      DomainIdentifier
	Version       uint16 // Should always be 0x0001
	Reserved      uint16
	PacketType    PacketType // 2 = AppleTalk data packet, 3 = AURP packet
}

// PacketType is used to distinguish domain-header encapsulated packets.
type PacketType uint16

// Various packet types.
const (
	PacketTypeAppleTalk PacketType = 0x0002
	PacketTypeRouting   PacketType = 0x0003
)

// WriteTo writes the encoded form of the domain header to w.
func (dh *DomainHeader) WriteTo(w io.Writer) (int64, error) {
	a := acc(w)
	a.writeTo(dh.DestinationDI)
	a.writeTo(dh.SourceDI)
	a.write16(dh.Version)
	a.write16(dh.Reserved)
	a.write16(uint16(dh.PacketType))
	return a.ret()
}

// parseDomainHeader parses a domain header, returning the DH and the remainder
// of the input slice. It does not validate the version or packet type fields.
func parseDomainHeader(b []byte) (*DomainHeader, []byte, error) {
	ddi, b, err := parseDomainIdentifier(b)
	if err != nil {
		return nil, b, err
	}
	sdi, b, err := parseDomainIdentifier(b)
	if err != nil {
		return nil, b, err
	}
	if len(b) < 6 { // sizeof(version + reserved + packettype)
		return nil, b, fmt.Errorf("insufficient remaining input length %d < 6", len(b))
	}
	return &DomainHeader{
		DestinationDI: ddi,
		SourceDI:      sdi,
		Version:       binary.BigEndian.Uint16(b[:2]),
		Reserved:      binary.BigEndian.Uint16(b[2:4]),
		PacketType:    PacketType(binary.BigEndian.Uint16(b[4:6])),
	}, b[6:], nil
}

// DomainIdentifier is the byte representation of a domain identifier.
type DomainIdentifier interface {
	io.WriterTo
}

// NullDomainIdentifier represents a null domain identifier.
type NullDomainIdentifier struct{}

// WriteTo writes the encoded form of the domain identifier to w.
func (NullDomainIdentifier) WriteTo(w io.Writer) (int64, error) {
	n, err := w.Write([]byte{0x01, 0x00})
	return int64(n), err
}

// IPDomainIdentifier represents an IP address in a domain identifier.
type IPDomainIdentifier net.IP

// WriteTo writes the encoded form of the domain identifier to w.
func (i IPDomainIdentifier) WriteTo(w io.Writer) (int64, error) {
	v4 := net.IP(i).To4()
	if v4 == nil {
		return 0, fmt.Errorf("need v4 IP address, got %v", i)
	}

	a := acc(w)
	a.write([]byte{
		0x07,       // byte 1: length of the DI, in bytes
		0x01,       // byte 2: authority: 1 = IP address
		0x00, 0x00, // bytes 3, 4: distinguisher: reserved)
	})
	a.write(v4) // bytes 5-8: IP address
	return a.ret()
}

// Authority represents the different possible authorities ("types") for domain
// identifiers.
type Authority byte

// Various authorities.
const (
	// AuthorityNull is for null domain identifiers, suitable only when there is
	// no need to distinguish the domains connected to a tunnel.
	AuthorityNull Authority = iota

	// AuthorityIP is for
	AuthorityIP
)

// parseDomainIdentifier parses a DI from the front of b, and returns the DI and
// the remainder of the input slice or an error.
func parseDomainIdentifier(b []byte) (DomainIdentifier, []byte, error) {
	if len(b) < 2 {
		return nil, b, fmt.Errorf("insufficient input length %d for domain identifier", len(b))
	}
	// Now we know there is a length byte and authority byte, see if there is
	// that much more data
	lf := int(b[0])
	if len(b) < 1+lf {
		return nil, b, fmt.Errorf("input length %d < 1+specified length %d in domain identifier", len(b), lf)
	}
	switch Authority(b[1]) {
	case AuthorityNull:
		// That's it, that's the whole DI.
		return NullDomainIdentifier{}, b[2:], nil

	case AuthorityIP:
		if lf != 7 {
			return nil, b, fmt.Errorf("incorrect length %d for IP domain identifier", lf)
		}
		return IPDomainIdentifier(b[5:8]), b[8:], nil

	default:
		return nil, b, fmt.Errorf("unknown domain identifier authority %d", b[1])
	}
}

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

// OptionTuple is used to pass option information in Open-Req and Open-Rsp
// packets.
type OptionTuple struct {
	// Length uint8 = 1(for Type) + len(Data)
	Type OptionType
	Data []byte
}

func (ot *OptionTuple) WriteTo(w io.Writer) (int64, error) {
	if len(ot.Data) > 254 {
		return 0, fmt.Errorf("option tuple data too long [%d > 254]", len(ot.Data))
	}

	a := acc(w)
	a.write([]byte{
		byte(len(ot.Data) + 1),
		byte(ot.Type),
	})
	a.write(ot.Data)
	return a.ret()
}

func parseOptionTuple(p []byte) (OptionTuple, []byte, error) {
	if len(p) < 2 {
		return OptionTuple{}, p, fmt.Errorf("insufficient input length %d for option tuple", len(p))
	}
	olen := int(p[0]) + 1
	if len(p) < olen {
		return OptionTuple{}, p, fmt.Errorf("insufficient input for option tuple data length %d", olen)
	}
	return OptionTuple{
		Type: OptionType(p[1]),
		Data: p[2:olen],
	}, p[olen:], nil
}

// OptionType is used to distinguish different options.
type OptionType uint8

// Various option types
const (
	OptionTypeAuthentication OptionType = 0x01
	// All other types reserved
)

type Options []OptionTuple

func (o Options) WriteTo(w io.Writer) (int64, error) {
	if len(o) > 255 {
		return 0, fmt.Errorf("too many options [%d > 255]", len(o))
	}

	a := acc(w)
	a.write8(uint8(len(o)))
	for _, ot := range o {
		a.writeTo(&ot)
	}
	return a.ret()
}

func parseOptions(p []byte) (Options, error) {
	if len(p) < 1 {
		return nil, fmt.Errorf("insufficint input length %d for options", len(p))
	}
	optc := p[0]
	opts := make([]OptionTuple, optc)
	for i := range optc {
		ot, np, err := parseOptionTuple(p)
		if err != nil {
			return nil, fmt.Errorf("parsing option %d: %w", i, err)
		}
		opts[i] = ot
		p = np
	}
	// TODO: warn about trailing data?
	return opts, nil
}

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

// OpenReq is used to open a one-way connection between AIRs.
type OpenReqPacket struct {
	*Header

	Version uint16 // currently always 1
	Options Options
}

func (p *OpenReqPacket) WriteTo(w io.Writer) (int64, error) {
	a := acc(w)
	a.writeTo(p.Header)
	a.write16(p.Version)
	a.writeTo(p.Options)
	return a.ret()
}

func parseOpenReq(p []byte) (*OpenReqPacket, error) {
	if len(p) < 3 {
		return nil, fmt.Errorf("insufficient input length %d for Open-Req packet", len(p))
	}
	opts, err := parseOptions(p[2:])
	if err != nil {
		return nil, err
	}
	return &OpenReqPacket{
		Version: binary.BigEndian.Uint16(p[:2]),
		Options: opts,
	}, nil
}

// OpenRsp is used to respond to Open-Req.
type OpenRspPacket struct {
	*Header

	RateOrErrCode int16
	Options       Options
}

func (p *OpenRspPacket) WriteTo(w io.Writer) (int64, error) {
	a := acc(w)
	a.writeTo(p.Header)
	a.write16(uint16(p.RateOrErrCode))
	a.writeTo(p.Options)
	return a.ret()
}

func parseOpenRsp(p []byte) (*OpenRspPacket, error) {
	if len(p) < 3 {
		return nil, fmt.Errorf("insufficient input length %d for Open-Rsp packet", len(p))
	}
	opts, err := parseOptions(p[2:])
	if err != nil {
		return nil, err
	}
	return &OpenRspPacket{
		RateOrErrCode: int16(binary.BigEndian.Uint16(p[:2])),
		Options:       opts,
	}, nil
}

type RIReqPacket struct {
	*Header
}

type RIRspPacket struct {
	*Header

	RTMPData []byte
}

func (p *RIRspPacket) WriteTo(w io.Writer) (int64, error) {
	a := acc(w)
	a.writeTo(p.Header)
	a.write(p.RTMPData)
	return a.ret()
}

type RIAckPacket struct {
	*Header
}

type RIUpdPacket struct {
	*Header

	Events Events
}

func (p *RIUpdPacket) WriteTo(w io.Writer) (int64, error) {
	a := acc(w)
	a.writeTo(p.Header)
	a.writeTo(p.Events)
	return a.ret()
}

func parseRIUpd(p []byte) (*RIUpdPacket, error) {
	var e Events
	for len(p) > 0 {
		et, nextp, err := parseEventTuple(p)
		if err != nil {
			return nil, fmt.Errorf("parsing event tuple %d: %w", len(e), err)
		}
		e = append(e, et)
		p = nextp
	}
	return &RIUpdPacket{
		Events: e,
	}, nil
}

type Events []EventTuple

func (e Events) WriteTo(w io.Writer) (int64, error) {
	a := acc(w)
	for _, et := range e {
		a.writeTo(&et)
	}
	return a.ret()
}

type EventTuple struct {
	EventCode  EventCode
	RangeStart uint16 // or simply the network number
	Distance   uint8
	RangeEnd   uint16
}

func (et *EventTuple) WriteTo(w io.Writer) (int64, error) {
	a := acc(w)
	a.write8(uint8(et.EventCode))
	a.write16(et.RangeStart)
	a.write8(et.Distance)
	if et.Distance&0x80 != 0 { // extended tuple
		a.write16(et.RangeEnd)
	}
	return a.ret()
}

func parseEventTuple(p []byte) (EventTuple, []byte, error) {
	if len(p) < 4 {
		return EventTuple{}, p, fmt.Errorf("insufficient input length %d for network event tuple", len(p))
	}

	var et EventTuple
	et.EventCode = EventCode(p[0])
	et.RangeStart = binary.BigEndian.Uint16(p[1:3])
	et.Distance = p[3]
	if et.Distance&0x80 == 0 {
		return et, p[4:], nil
	}

	if len(p) < 6 {
		return EventTuple{}, p, fmt.Errorf("insufficient input length %d for extended network event tuple", len(p))
	}
	et.RangeEnd = binary.BigEndian.Uint16(p[4:6])
	return et, p[6:], nil
}

type EventCode uint8

const (
	EventCodeNull EventCode = 0
	EventCodeNA   EventCode = 1
	EventCodeND   EventCode = 2
	EventCodeNRC  EventCode = 3
	EventCodeNDC  EventCode = 4
	EventCodeZC   EventCode = 5
)

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
