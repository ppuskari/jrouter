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

package atalk

import (
	"math/bits"

	"github.com/sfiera/multitalk/pkg/ethernet"
)

// Inside AppleTalk, appendix D
var toUpperMap = []byte{
	// The alphabet
	0x61: 0x41,
	0x62: 0x42,
	0x63: 0x43,
	0x64: 0x44,
	0x65: 0x45,
	0x66: 0x46,
	0x67: 0x47,
	0x68: 0x48,
	0x69: 0x49,
	0x6A: 0x4A,
	0x6B: 0x4B,
	0x6C: 0x4C,
	0x6D: 0x4D,
	0x6E: 0x4E,
	0x6F: 0x4F,
	0x70: 0x50,
	0x71: 0x51,
	0x72: 0x52,
	0x73: 0x53,
	0x74: 0x54,
	0x75: 0x55,
	0x76: 0x56,
	0x77: 0x57,
	0x78: 0x58,
	0x79: 0x59,
	0x7A: 0x5A,

	// Letters with diacritics, etc
	0x88: 0xCB,
	0x8A: 0x80,
	0x8B: 0xCC,
	0x8C: 0x81,
	0x8D: 0x82,
	0x8E: 0x83,
	0x96: 0x84,
	0x9A: 0x85,
	0x9B: 0xCD,
	0x9F: 0x86,
	0xBE: 0xAE,
	0xBF: 0xAF,
	0xCF: 0xCE,
}

func Checksum(s string) uint16 {
	// Inside AppleTalk, pp 4-17 and pp 8-18
	var cksum uint16
	for _, b := range []byte(s) {
		cksum += uint16(b)
		cksum = bits.RotateLeft16(cksum, -1)
	}
	if cksum == 0 {
		cksum = 0xFFFF
	}
	return cksum
}

func ToUpper(s string) string {
	// Inside Appletalk, appendix D
	sb := []byte(s)
	out := make([]byte, len(sb))
	for i, b := range sb {
		if u := toUpperMap[b]; u != 0 {
			out[i] = u
		} else {
			out[i] = b
		}
	}
	return string(out)
}

func MulticastAddr(zone string) ethernet.Addr {
	// Inside AppleTalk, pp 3-10 and pp 8-18
	h := Checksum(ToUpper(zone))
	return ethernet.Addr{0x09, 0x00, 0x07, 0x00, 0x00, byte(h % 0xFD)}
}
