//go:build windows

package main

import (
	"fmt"
	"net"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func pcapDeviceName(iface *net.Interface) (string, error) {
	var size uint32
	err := windows.GetAdaptersAddresses(0, 0, 0, nil, &size)
	if err != nil && err != syscall.ERROR_BUFFER_OVERFLOW {
		return "", fmt.Errorf("GetAdaptersAddresses size query: %w", err)
	}
	if size == 0 {
		return "", fmt.Errorf("GetAdaptersAddresses returned an empty adapter table")
	}

	buf := make([]byte, size)
	addrs := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
	if err := windows.GetAdaptersAddresses(0, 0, 0, addrs, &size); err != nil {
		return "", fmt.Errorf("GetAdaptersAddresses: %w", err)
	}

	index := uint32(iface.Index)
	for addr := addrs; addr != nil; addr = addr.Next {
		if addr.IfIndex != index && addr.Ipv6IfIndex != index {
			continue
		}

		adapterName := bytePtrString(addr.AdapterName)
		if adapterName == "" {
			return "", fmt.Errorf("Windows adapter %q has no adapter GUID", iface.Name)
		}
		return `\Device\NPF_` + adapterName, nil
	}

	return "", fmt.Errorf("could not map Windows interface %q (index %d) to an Npcap device", iface.Name, iface.Index)
}

func bytePtrString(p *byte) string {
	if p == nil {
		return ""
	}
	var b []byte
	for off := uintptr(0); ; off++ {
		c := *(*byte)(unsafe.Pointer(uintptr(unsafe.Pointer(p)) + off))
		if c == 0 {
			return string(b)
		}
		b = append(b, c)
	}
}
