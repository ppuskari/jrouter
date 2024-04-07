package rtmp

import "bytes"

func write16[I ~uint16](b *bytes.Buffer, n I) {
	b.Write([]byte{byte(n >> 8), byte(n & 0xff)})
}
