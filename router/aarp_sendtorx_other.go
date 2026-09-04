//go:build !windows

package router

import "github.com/sfiera/multitalk/pkg/ethertalk"

func mirrorAARPResponse(
	*EtherTalkPort,
	*ethertalk.Packet,
) error {
	return nil
}
