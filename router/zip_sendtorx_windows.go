//go:build windows

package router

import (
	"context"
	"fmt"
	"sync"

	"github.com/sfiera/multitalk/pkg/ddp"
	"github.com/sfiera/multitalk/pkg/ethernet"
	"github.com/sfiera/multitalk/pkg/ethertalk"
)

var zipMirrorSlots sync.Map

type zipMirrorSlot struct {
	once   sync.Once
	mirror *npcapRTMPMirror
}

func (port *EtherTalkPort) sendZIPWithReceiveMirror(
	ctx context.Context,
	pkt *ddp.ExtPacket,
) error {
	if err := port.Send(ctx, pkt); err != nil {
		return err
	}
	if err := mirrorZIPPacket(port, pkt); err != nil {
		port.logger.Warn(
			"ZIP: experimental Windows SendToRx mirror failed",
			"error", err,
		)
	}
	return nil
}

func mirrorZIPPacket(
	port *EtherTalkPort,
	pkt *ddp.ExtPacket,
) error {
	v, _ := zipMirrorSlots.LoadOrStore(
		port,
		&zipMirrorSlot{},
	)
	slot := v.(*zipMirrorSlot)

	slot.once.Do(func() {
		mirror, err := openNpcapRTMPMirror(port.device)
		if err != nil {
			port.logger.Warn(
				"ZIP: experimental Windows SendToRx mirror unavailable",
				"error", err,
			)
			return
		}
		slot.mirror = mirror
		port.logger.Warn(
			"ZIP: EXPERIMENTAL Windows Npcap SendToRx mirror enabled",
			"device", port.device,
			"mode", fmt.Sprintf("0x%04x", npcapModeSendToRx),
		)
	})

	if slot.mirror == nil {
		return nil
	}

	var dst ethernet.Addr
	if etherTalkBroadcastNode(pkt.DstNode) {
		dst = ethertalk.AppleTalkBroadcast
	} else {
		addr := ddp.Addr{
			Network: pkt.DstNet,
			Node:    pkt.DstNode,
		}
		var waitCh <-chan struct{}
		dst, waitCh = port.aarpMachine.lookupOrWait(addr)
		if waitCh != nil {
			// ZIP replies are normally sent to a node whose address was
			// just gleaned from the request. If resolution is unexpectedly
			// pending, leave delivery to the normal outbox rather than
			// injecting a frame to an unknown Ethernet destination.
			return nil
		}
	}

	frame, err := ethertalk.AppleTalk(
		port.ethernetAddr,
		*pkt,
	)
	if err != nil {
		return fmt.Errorf(
			"build mirrored ZIP EtherTalk frame: %w",
			err,
		)
	}
	frame.Dst = dst
	raw, err := ethertalk.Marshal(*frame)
	if err != nil {
		return fmt.Errorf(
			"marshal mirrored ZIP EtherTalk frame: %w",
			err,
		)
	}
	if len(raw) < 64 {
		raw = append(raw, make([]byte, 64-len(raw))...)
	}
	return slot.mirror.send(raw)
}
