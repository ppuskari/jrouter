package aurp

import (
	"io"
)

// wtacc is a helper for io.WriterTo implementations.
// It sacrifices early returns for a shorter syntax. However, it won't continue
// writing to the destination writer after detecting an error.
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
	a.write([]byte{x})
}

func (a *wtacc) write16(x uint16) {
	// Could do this with:
	//     binary.Write(a.w, binary.BigEndian, x)
	// - but don't wanna
	a.write([]byte{
		byte(x >> 8),
		byte(x & 0xff),
	})
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
