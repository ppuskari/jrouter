package aurp

import (
	"encoding/binary"
	"io"
)

// wtacc is a helper for io.WriterTo implementations.
// It sacrifices early returns for a shorter syntax. However, it refuses to
// continue writing to the destination writer after detecting an error.
type wtacc struct {
	w   io.Writer
	n   int64
	err error
}

func acc(w io.Writer) wtacc { return wtacc{w: w} }

func (a *wtacc) ret() (int64, error) {
	return a.n, a.err
}

func (a *wtacc) write8(x uint8) {
	if a.err != nil {
		return
	}
	a.err = binary.Write(a.w, binary.BigEndian, x)
	if a.err != nil {
		return
	}
	a.n++
}

func (a *wtacc) write16(x uint16) {
	if a.err != nil {
		return
	}
	a.err = binary.Write(a.w, binary.BigEndian, x)
	if a.err != nil {
		return
	}
	a.n += 2
}

func (a *wtacc) write(b []byte) {
	if a.err != nil {
		return
	}
	n, err := a.w.Write(b)
	a.n += int64(n)
	a.err = err
}

func (a *wtacc) writeTo(wt io.WriterTo) {
	if a.err != nil {
		return
	}
	n, err := wt.WriteTo(a.w)
	a.n += n
	a.err = err
}
