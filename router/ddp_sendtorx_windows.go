//go:build windows

package router

import (
	"fmt"
	"sync"
)

var ddpMirrorSlots sync.Map

type ddpMirrorSlot struct {
	once   sync.Once
	mirror *npcapRTMPMirror
}

// mirrorDDPTransmit injects one already-marshalled EtherTalk DDP frame into
// Npcap's receive path. The normal pcap transmit has already completed before
// this function is called.
func mirrorDDPTransmit(port *EtherTalkPort, raw []byte) error {
	v, _ := ddpMirrorSlots.LoadOrStore(
		port,
		&ddpMirrorSlot{},
	)
	slot := v.(*ddpMirrorSlot)

	slot.once.Do(func() {
		mirror, err := openNpcapRTMPMirror(port.device)
		if err != nil {
			port.logger.Warn(
				"EtherTalk: experimental Windows DDP SendToRx mirror unavailable",
				"error", err,
			)
			return
		}
		slot.mirror = mirror
		port.logger.Warn(
			"EtherTalk: EXPERIMENTAL Windows Npcap DDP SendToRx mirror enabled",
			"device", port.device,
			"mode", fmt.Sprintf("0x%04x", npcapModeSendToRx),
		)
	})

	if slot.mirror == nil {
		return nil
	}
	return slot.mirror.send(raw)
}
