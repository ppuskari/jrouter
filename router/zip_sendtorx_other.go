//go:build !windows

package router

import (
	"context"

	"github.com/sfiera/multitalk/pkg/ddp"
)

func (port *EtherTalkPort) sendZIPWithReceiveMirror(
	ctx context.Context,
	pkt *ddp.ExtPacket,
) error {
	return port.Send(ctx, pkt)
}
