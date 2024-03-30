package aurp

import (
	"bytes"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestZoneTuplesEncoding(t *testing.T) {
	zones := ZoneTuples{
		{
			Network: 0x64,
			Name:    "The Twilight Zone",
		},
	}

	b := bytes.NewBuffer(nil)
	zones.WriteTo(b)

	got := b.Bytes()
	want := append([]byte{
		0x00, 0x01, // Number of zone tuples
		0x00, 0x64, // Network number
		0x11, // Length of string
	}, zones[0].Name...)
	if diff := cmp.Diff(got, want); diff != "" {
		t.Errorf("encoded zone tuples diff (-got +want):\n%s", diff)
	}
}
