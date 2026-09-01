package router

import "testing"

func TestSet27PeerStatusIncludesDataPlaneCounters(t *testing.T) {
	peer := newRestartTestPeer(t)
	peer.tunnelID = "cfg:status.example"
	peer.ddpPacketsIn.Store(11)
	peer.ddpPacketsOut.Store(12)
	peer.ddpBytesIn.Store(11000)
	peer.ddpBytesOut.Store(12000)
	peer.receiveQueueHighWater.Store(7)

	row := newAURPPeerStatusRow(
		peer,
		"status.example",
		nil,
		configuredDNSState{},
	)
	if row.DDPPacketsIn != 11 ||
		row.DDPPacketsOut != 12 ||
		row.DDPBytesIn != 11000 ||
		row.DDPBytesOut != 12000 ||
		row.ReceiveQueueHighWater != 7 {
		t.Fatalf("unexpected data-plane status row: %+v", row)
	}
}
