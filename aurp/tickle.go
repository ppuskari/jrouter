package aurp

import "io"

type TicklePacket struct {
	Header
}

func (p *TicklePacket) WriteTo(w io.Writer) (int64, error) {
	p.Sequence = 0
	p.CommandCode = CmdCodeTickle
	p.Flags = 0
	return p.Header.WriteTo(w)
}

type TickleAckPacket struct {
	Header
}

func (p *TickleAckPacket) WriteTo(w io.Writer) (int64, error) {
	p.Sequence = 0
	p.CommandCode = CmdCodeTickleAck
	p.Flags = 0
	return p.Header.WriteTo(w)
}
