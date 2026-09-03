//go:build !windows

package router

import "github.com/sfiera/multitalk/pkg/ddp"

func mirrorRTMPBroadcast(
	port *EtherTalkPort,
	pkt *ddp.ExtPacket,
) error {
	return nil
}
