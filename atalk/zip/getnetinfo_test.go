package zip

import (
	"testing"

	"github.com/sfiera/multitalk/pkg/ddp"
	"github.com/sfiera/multitalk/pkg/ethernet"
)

func TestGetNetInfoPacketMarshalRoundTrip(t *testing.T) {
	in := &GetNetInfoPacket{ZoneName: "Twilight"}
	raw, err := in.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	out, err := UnmarshalGetNetInfoPacket(raw)
	if err != nil {
		t.Fatal(err)
	}
	if out.ZoneName != in.ZoneName {
		t.Fatalf("zone = %q, want %q", out.ZoneName, in.ZoneName)
	}
}

func TestGetNetInfoReplyMarshalRoundTrip(t *testing.T) {
	in := &GetNetInfoReplyPacket{
		ZoneInvalid:     true,
		OnlyOneZone:     false,
		NetStart:        ddp.Network(1000),
		NetEnd:          ddp.Network(1009),
		ZoneName:        "Requested",
		MulticastAddr:   ethernet.Addr{0x09, 0x00, 0x07, 0xff, 0xff, 0xff},
		DefaultZoneName: "Default",
	}
	raw, err := in.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	gotAny, err := UnmarshalPacket(raw)
	if err != nil {
		t.Fatal(err)
	}
	out, ok := gotAny.(*GetNetInfoReplyPacket)
	if !ok {
		t.Fatalf("decoded type = %T, want *GetNetInfoReplyPacket", gotAny)
	}
	if out.NetStart != in.NetStart ||
		out.NetEnd != in.NetEnd ||
		out.ZoneName != in.ZoneName ||
		out.DefaultZoneName != in.DefaultZoneName ||
		out.MulticastAddr != in.MulticastAddr ||
		!out.ZoneInvalid {
		t.Fatalf("round trip = %+v, want %+v", out, in)
	}
}
