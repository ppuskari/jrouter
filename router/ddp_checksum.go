package router

import (
	"fmt"
	"math/bits"

	"github.com/sfiera/multitalk/pkg/ddp"
)

func computeDDPChecksum(packet *ddp.ExtPacket) (uint16, error) {
	if packet == nil {
		return 0, fmt.Errorf("nil DDP packet")
	}
	raw, err := ddp.ExtMarshal(*packet)
	if err != nil {
		return 0, fmt.Errorf("marshal DDP for checksum: %w", err)
	}
	if len(raw) < 4 {
		return 0, fmt.Errorf("DDP packet too short for checksum")
	}

	var checksum uint16
	for _, value := range raw[4:] {
		checksum = checksum + uint16(value)
		checksum = bits.RotateLeft16(checksum, 1)
	}
	if checksum == 0 {
		return 0xffff, nil
	}
	return checksum, nil
}

func verifyDDPChecksum(packet *ddp.ExtPacket) error {
	if packet == nil || packet.Cksum == 0 {
		return nil
	}
	want := packet.Cksum
	got, err := computeDDPChecksum(packet)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf(
			"invalid DDP checksum: got 0x%04x want 0x%04x",
			want,
			got,
		)
	}
	return nil
}
