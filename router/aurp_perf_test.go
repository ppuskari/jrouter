package router

import (
	"net"
	"testing"
)

func TestSet27RemoteAddrPublicCopyAndInternalFastPath(t *testing.T) {
	peer := &AURPPeer{}
	peer.setRemoteAddr(net.IPv4(192, 0, 2, 77))

	internal := peer.activeRemoteAddr()
	if got := internal.String(); got != "192.0.2.77" {
		t.Fatalf("internal active address = %q, want 192.0.2.77", got)
	}

	public := peer.RemoteAddr()
	public[0] = 203
	if got := peer.activeRemoteAddr().String(); got != "192.0.2.77" {
		t.Fatalf("public RemoteAddr mutation changed internal endpoint: %q", got)
	}

	replacement := net.IPv4(198, 51, 100, 9)
	peer.setRemoteAddr(replacement)
	replacement[0] = 127
	if got := peer.activeRemoteAddr().String(); got != "198.51.100.9" {
		t.Fatalf("setRemoteAddr retained caller storage: %q", got)
	}
}
