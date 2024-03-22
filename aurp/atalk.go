package aurp

import "io"

// AppleTalkPacket is for encapsulated AppleTalk traffic.
type AppleTalkPacket struct {
	DomainHeader // where PacketTypeAppleTalk

	Data []byte
}

func (p *AppleTalkPacket) WriteTo(w io.Writer) (int64, error) {
	a := acc(w)
	a.writeTo(&p.DomainHeader)
	a.write(p.Data)
	return a.ret()
}
