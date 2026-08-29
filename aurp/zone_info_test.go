/*
   Copyright 2024 Josh Deprez

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

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

func TestParseNonExtendedZoneTuplesIgnoresDeclaredCount(t *testing.T) {
	payload := []byte{
		0x00, 0x01, // declared tuple count: deliberately too small
		0x00, 0x64, 0x01, 'A',
		0x00, 0x65, 0x01, 'B',
	}
	pkt, err := parseZIRspPacket(payload, SubcodeZoneInfoNonExt)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkt.Zones) != 2 {
		t.Fatalf("parsed %d tuples, want 2", len(pkt.Zones))
	}
	if pkt.Zones[0].Network != 0x64 || pkt.Zones[0].Name != "A" {
		t.Fatalf("first tuple = %+v", pkt.Zones[0])
	}
	if pkt.Zones[1].Network != 0x65 || pkt.Zones[1].Name != "B" {
		t.Fatalf("second tuple = %+v", pkt.Zones[1])
	}
}

func TestParseExtendedZIRspCarriesTotalAcrossPartialPacket(t *testing.T) {
	payload := []byte{
		0x00, 0x03, // total tuples in complete zone list
		0x07, 0xd0, 0x05, 'Z', 'o', 'n', 'e', 'A',
		0x07, 0xd0, 0x05, 'Z', 'o', 'n', 'e', 'B',
	}
	pkt, err := parseZIRspPacket(payload, SubcodeZoneInfoExt)
	if err != nil {
		t.Fatal(err)
	}
	if pkt.TotalTuples != 3 {
		t.Fatalf("total tuples = %d, want 3", pkt.TotalTuples)
	}
	if len(pkt.Zones) != 2 {
		t.Fatalf("packet tuples = %d, want 2", len(pkt.Zones))
	}
	for _, zt := range pkt.Zones {
		if zt.Network != 2000 {
			t.Fatalf("extended packet network = %d, want 2000", zt.Network)
		}
	}
}

func TestParseExtendedZIRspRejectsMixedNetworks(t *testing.T) {
	payload := []byte{
		0x00, 0x02,
		0x07, 0xd0, 0x01, 'A',
		0x07, 0xd1, 0x01, 'B',
	}
	if _, err := parseZIRspPacket(payload, SubcodeZoneInfoExt); err == nil {
		t.Fatal("mixed-network extended ZI-Rsp parsed successfully")
	}
}
