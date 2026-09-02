package aurp

import "testing"

func TestSet28FixedControlParsersRejectTruncation(t *testing.T) {
	if _, err := parseRD(nil); err == nil {
		t.Fatal("empty Router Down payload was accepted")
	}
	if _, err := parseRD([]byte{0}); err == nil {
		t.Fatal("one-byte Router Down payload was accepted")
	}
	if _, _, err := parseSubcode([]byte{0}); err == nil {
		t.Fatal("one-byte zone subcode was accepted")
	}
	if _, err := parseZIReqPacket([]byte{0}); err == nil {
		t.Fatal("odd-length ZI-Req network list was accepted")
	}
	if _, err := parseZIRspPacket(
		nil,
		SubcodeZoneInfoExt,
	); err == nil {
		t.Fatal("extended ZI-Rsp without tuple count was accepted")
	}
	if _, err := parseZIRspPacket(
		[]byte{0, 2, 0},
		SubcodeZoneInfoExt,
	); err == nil {
		t.Fatal("truncated extended ZI-Rsp tuple was accepted")
	}
	if _, err := parseGDZLReqPacket([]byte{0}); err == nil {
		t.Fatal("truncated GDZL-Req was accepted")
	}
	if _, err := parseGZNReqPacket([]byte{5, 'A'}); err == nil {
		t.Fatal("truncated GZN-Req zone name was accepted")
	}
}
