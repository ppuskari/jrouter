package aurp

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
)

func marshalSet27Packet(t *testing.T, pkt Packet) []byte {
	t.Helper()
	var b bytes.Buffer
	if _, err := pkt.WriteTo(&b); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestSet27ParsePacketRejectsMalformedDomainAndVersion(t *testing.T) {
	localDI := IPDomainIdentifier(net.IPv4(192, 0, 2, 1))
	remoteDI := IPDomainIdentifier(net.IPv4(198, 51, 100, 2))
	tr := NewTransport(localDI, remoteDI, 100, 200)
	raw := marshalSet27Packet(t, tr.NewOpenReqPacket(nil))

	for n := 0; n < 22; n++ {
		if _, _, err := ParsePacket(raw[:n]); err == nil {
			t.Fatalf("domain-header prefix length %d unexpectedly parsed", n)
		}
	}

	badVersion := append([]byte(nil), raw...)
	// Two IPv4 DIs are eight bytes each; version follows them.
	binary.BigEndian.PutUint16(badVersion[16:18], 2)
	if _, _, err := ParsePacket(badVersion); err == nil {
		t.Fatal("unsupported AURP domain version unexpectedly parsed")
	}
}

func TestSet27ParsePacketRejectsUnknownRoutingCommand(t *testing.T) {
	localDI := IPDomainIdentifier(net.IPv4(192, 0, 2, 1))
	remoteDI := IPDomainIdentifier(net.IPv4(198, 51, 100, 2))
	tr := NewTransport(localDI, remoteDI, 100, 200)
	raw := marshalSet27Packet(t, tr.NewOpenReqPacket(nil))

	bad := append([]byte(nil), raw...)
	// IP DIs (16) + domain fixed fields (6) + Tr connection/sequence (4).
	const commandOffset = 26
	binary.BigEndian.PutUint16(bad[commandOffset:commandOffset+2], 0x7fff)
	if _, _, err := ParsePacket(bad); err == nil {
		t.Fatal("unknown AURP routing command unexpectedly parsed")
	}
}
