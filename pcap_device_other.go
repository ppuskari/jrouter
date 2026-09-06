//go:build !windows

package main

import "net"

func pcapDeviceName(iface *net.Interface) (string, error) {
	return iface.Name, nil
}
