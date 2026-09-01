package aurp

import (
	"net"
	"strings"
	"testing"
)

func TestDomainIdentifierDisplayIsStableForIPv4(t *testing.T) {
	got := DomainIdentifierDisplay(IPDomainIdentifier(net.IPv4(198, 51, 100, 9)))
	if want := "198.51.100.9 (0xc6336409)"; got != want {
		t.Fatalf("display = %q, want %q", got, want)
	}
}

func TestDomainHeaderStringUsesDomainIdentifierDisplay(t *testing.T) {
	dh := DomainHeader{
		SourceDI:      IPDomainIdentifier(net.IPv4(198, 51, 100, 9)),
		DestinationDI: IPDomainIdentifier(net.IPv4(192, 0, 2, 1)),
		Version:       1,
		PacketType:    PacketTypeRouting,
	}
	got := dh.String()
	for _, want := range []string{"198.51.100.9 (0xc6336409)", "192.0.2.1 (0xc0000201)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("header string %q does not contain %q", got, want)
		}
	}
}
