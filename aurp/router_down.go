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
