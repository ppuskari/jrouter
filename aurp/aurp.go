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

// ParseDomainHeader parses a domain header, returning the DH and the remainder
// of the input slice. It does not validate the version or packet type fields.
func ParseDomainHeader(b []byte) (*DomainHeader, []byte, error) {
	ddi, b, err := ParseDomainIdentifier(b)
	if err != nil {
		return nil, b, err
	}
	sdi, b, err := ParseDomainIdentifier(b)
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

// ParseDomainIdentifier parses a DI from the front of b, and returns the DI and
// the remainder of the input slice.
func ParseDomainIdentifier(b []byte) (DomainIdentifier, []byte, error) {
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
	DomainHeader

	ConnectionID uint16
	Sequence     uint16 // Note: 65535 is succeeded by 1, not 0
}

// WriteTo writes the encoded form of the header to w.
func (h *TrHeader) WriteTo(w io.Writer) (int64, error) {
	a := acc(w)
	a.writeTo(&h.DomainHeader)
	a.write16(h.ConnectionID)
	a.write16(h.Sequence)
	return a.ret()
}

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
	a := acc(w)
	a.write8(uint8(len(ot.Data) + 1))
	a.write8(uint8(ot.Type))
	a.write(ot.Data)
	return a.ret()
}

// OptionType is used to distinguish different options.
type OptionType uint8

// Various option types
const (
	OptionTypeAuthentication OptionType = 0x01
	// All other types reserved
)

// Packet represents a full AURP packet, not including UDP or lower layers, but
// including the domain header and higher layers.
type Packet interface {
	io.WriterTo
}

// AppleTalkPacket is for encapsulated AppleTalk traffic.
type AppleTalkPacket struct {
	DomainHeader // where PacketTypeAppleTalk
	Data         []byte
}

func (p *AppleTalkPacket) WriteTo(w io.Writer) (int64, error) {
	a := acc(w)
	a.writeTo(&p.DomainHeader)
	a.write(p.Data)
	return a.ret()
}

// OpenReq is used to open a one-way connection between AIRs.
type OpenReqPacket struct {
	Header

	Version uint16 // currently always 1
	//OptionCount uint8 = len(Options)
	Options []OptionTuple
}

func (p *OpenReqPacket) WriteTo(w io.Writer) (int64, error) {
	if len(p.Options) > 255 {
		return 0, fmt.Errorf("too many options [%d > 255]", len(p.Options))
	}

	a := acc(w)
	a.writeTo(&p.Header)
	a.write16(p.Version)
	a.write8(uint8(len(p.Options)))
	for _, o := range p.Options {
		a.writeTo(&o)
	}
	return a.ret()
}

func ParsePacket(p []byte) (Packet, error) {
	// TODO
	return nil, nil
}
