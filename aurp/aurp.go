// Package aurp implements types for encoding and decoding AppleTalk
// Update-Based Routing Protocol (AURP, RFC 1504) messages.
package aurp

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
)

// DomainHeader represents the header used to encapsulate both AppleTalk data
// packets and AURP packets within UDP.
type DomainHeader struct {
	DestinationDI DomainIdentifier
	SourceDI      DomainIdentifier
	Version       uint16 // Should always be 0x0001
	PacketType    uint16 // 2 = AppleTalk data packet, 3 = AURP packet
}

// Encode returns the encoded form of the header.
func (dh *DomainHeader) Encode() ([]byte, error) {
	var b bytes.Buffer
	ddi, err := dh.DestinationDI.Encode()
	if err != nil {
		return nil, err
	}
	sdi, err := dh.SourceDI.Encode()
	if err != nil {
		return nil, err
	}
	b.Write(ddi)
	b.Write(sdi)
	binary.Write(&b, binary.BigEndian, dh.Version)
	binary.Write(&b, binary.BigEndian, uint16(0x0000)) // Reserved
	binary.Write(&b, binary.BigEndian, dh.PacketType)
	return b.Bytes(), nil
}

// ParseDH parses a domain header, returning the DH and the remainder of the
// input slice.
func ParseDH(b []byte) (*DomainHeader, []byte, error) {
	ddi, b, err := ParseDI(b)
	if err != nil {
		return nil, b, err
	}
	sdi, b, err := ParseDI(b)
	if err != nil {
		return nil, b, err
	}
	if len(b) < 6 { // sizeof(version + reserved + packettype)
		return nil, b, fmt.Errorf("insufficient remaining input length %d < 6", len(b))
	}
	ver := binary.BigEndian.Uint16(b[:2])
	if ver != 1 {
		return nil, b, fmt.Errorf("unknown version %d", ver)
	}
	// Note: b[2:4] (reserved field) is ignored
	pt := binary.BigEndian.Uint16(b[4:6])
	if pt != 2 && pt != 3 {
		return nil, b, fmt.Errorf("unknown packet type %d", pt)
	}
	b = b[6:]
	return &DomainHeader{
		DestinationDI: ddi,
		SourceDI:      sdi,
		Version:       ver,
		PacketType:    pt,
	}, b, nil
}

// DomainIdentifier is the byte representation of a domain identifier.
type DomainIdentifier interface {
	Encode() ([]byte, error)
}

// NullDI represents a null domain identifier.
type NullDI struct{}

// Encode returns the encoded form of the identifier.
func (NullDI) Encode() ([]byte, error) {
	return []byte{0x01, 0x00}, nil
}

// IPDI represents an IP address in a domain identifier.
type IPDI net.IP

// Encode returns the encoded form of the identifier.
func (i IPDI) Encode() ([]byte, error) {
	v4 := net.IP(i).To4()
	if v4 == nil {
		return nil, fmt.Errorf("need v4 IP address, got %v", i)
	}
	return append([]byte{
			0x07,       // byte 1: length of the DI in bytes
			0x01,       // byte 2: authority: 1 = IP address
			0x00, 0x00, // bytes 3, 4: distinguisher: reserved
		}, v4...), // bytes 5-8: the IP address
		nil
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

// ParseDI parses a DI from the front of b, and returns the DI and the remainder
// of the input slice.
func ParseDI(b []byte) (DomainIdentifier, []byte, error) {
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
		return NullDI{}, b[2:], nil

	case AuthorityIP:
		if lf != 7 {
			return nil, b, fmt.Errorf("incorrect length %d for IP domain identifier", lf)
		}
		return IPDI(b[5:8]), b[8:], nil

	default:
		return nil, b, fmt.Errorf("unknown domain identifier authority %d", b[1])
	}
}

// TrHeader represent an AURP-Tr packet header.
type TrHeader struct {
	ConnectionID uint16
	Sequence     uint16 // Note: 65535 is succeeded by 1, not 0
}

// Header represents an AURP packet header.
type Header struct {
	CommandCode CmdCode
	Flags       RoutingFlag
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
