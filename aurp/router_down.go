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
	"encoding/binary"
	"fmt"
	"io"
)

type RDPacket struct {
	Header

	ErrorCode ErrorCode
}

func (p *RDPacket) WriteTo(w io.Writer) (int64, error) {
	a := acc(w)
	a.writeTo(&p.Header)
	a.write16(uint16(p.ErrorCode))
	return a.ret()
}

func parseRD(p []byte) (*RDPacket, error) {
	if len(p) < 2 {
		return nil, fmt.Errorf("insufficient input length %d for router down packet", len(p))
	}
	return &RDPacket{
		ErrorCode: ErrorCode(binary.BigEndian.Uint16(p[:2])),
	}, nil
}
