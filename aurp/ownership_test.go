package aurp

import (
	"bytes"
	"testing"
)

func TestSet27IPDomainIdentifierOwnsParsedBytes(t *testing.T) {
	raw := []byte{0x07, 0x01, 0x00, 0x00, 192, 0, 2, 44}
	di, rest, err := parseDomainIdentifier(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 0 {
		t.Fatalf("remaining bytes = %d, want 0", len(rest))
	}
	if got := di.String(); got != "192.0.2.44" {
		t.Fatalf("parsed DI = %q, want 192.0.2.44", got)
	}

	raw[4] = 203
	if got := di.String(); got != "192.0.2.44" {
		t.Fatalf("parsed DI changed after input reuse: %q", got)
	}
}

func TestSet27OptionTupleRoundTripAndOwnsData(t *testing.T) {
	want := Options{
		{Type: OptionTypeAuthentication, Data: []byte{0xaa, 0xbb}},
		{Type: OptionType(99), Data: []byte{0x01}},
	}

	var buf bytes.Buffer
	if _, err := want.WriteTo(&buf); err != nil {
		t.Fatal(err)
	}
	raw := append([]byte(nil), buf.Bytes()...)

	got, err := parseOptions(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("parsed options = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Type != want[i].Type ||
			!bytes.Equal(got[i].Data, want[i].Data) {
			t.Fatalf("option %d = %#v, want %#v", i, got[i], want[i])
		}
	}

	// count, length, type, then first data byte.
	raw[3] ^= 0xff
	if got[0].Data[0] != 0xaa {
		t.Fatalf("parsed option data aliases input: got 0x%02x", got[0].Data[0])
	}
}

func TestSet27OptionTupleRejectsZeroRemainderLength(t *testing.T) {
	if _, _, err := parseOptionTuple([]byte{0, 0}); err == nil {
		t.Fatal("zero-length option tuple remainder was accepted")
	}
}
