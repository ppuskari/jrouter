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
func parseDomainHeader(b []byte) (DomainHeader, []byte, error) {
	ddi, b, err := parseDomainIdentifier(b)
	if err != nil {
		return DomainHeader{}, b, err
	}
	sdi, b, err := parseDomainIdentifier(b)
	if err != nil {
		return DomainHeader{}, b, err
	}
	if len(b) < 6 { // sizeof(version + reserved + packettype)
		return DomainHeader{}, b, fmt.Errorf("insufficient remaining input length %d < 6", len(b))
	}
	return DomainHeader{
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
