//go:build windows

package router

import (
	"sync"

	"github.com/sfiera/multitalk/pkg/ethertalk"
)

// AARP response mirroring is deliberately isolated for the rc3-exp3 Windows
// experiment. Normal physical-Ethernet transmission still happens first.
var aarpMirrorSlots sync.Map

type aarpMirrorSlot struct {
	once   sync.Once
	mirror *npcapRTMPMirror
}

func mirrorAARPResponse(
	port *EtherTalkPort,
	pkt *ethertalk.Packet,
) error {
	v, _ := aarpMirrorSlots.LoadOrStore(
		port,
		&aarpMirrorSlot{},
	)
	slot := v.(*aarpMirrorSlot)

	slot.once.Do(func() {
		mirror, err := openNpcapRTMPMirror(port.device)
		if err != nil {
			port.logger.Warn(
				"AARP: experimental Windows SendToRx mirror unavailable",
				"error", err,
			)
			return
		}
		slot.mirror = mirror
		port.logger.Warn(
			"AARP: EXPERIMENTAL Windows Npcap SendToRx mirror enabled",
			"device", port.device,
			"mode", npcapModeSendToRx,
		)
	})

	if slot.mirror == nil {
		return nil
	}

	raw, err := ethertalk.Marshal(*pkt)
	if err != nil {
		return err
	}
	if len(raw) < 64 {
		raw = append(raw, make([]byte, 64-len(raw))...)
	}
	return slot.mirror.send(raw)
}
