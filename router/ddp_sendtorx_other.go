//go:build !windows

package router

func mirrorDDPTransmit(_ *EtherTalkPort, _ []byte) error {
	return nil
}
