//go:build windows

package router

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"github.com/sfiera/multitalk/pkg/ddp"
	"github.com/sfiera/multitalk/pkg/ethertalk"
	"golang.org/x/sys/windows"
)

// MODE_SENDTORX is an Npcap 1.83+ extension. It causes packets injected on
// this capture handle to enter the adapter receive path instead of the normal
// transmit path. Keep this experimental mirror separate from jrouter's normal
// pcap handle so the proven physical-Ethernet transmit path remains unchanged.
const npcapModeSendToRx = 0x0100

const pcapErrorBufferSize = 256

// One lazy mirror handle per EtherTalk port. The map is intentionally scoped
// to the Windows experiment so no production routing state is changed.
var rtmpMirrorSlots sync.Map

type rtmpMirrorSlot struct {
	once   sync.Once
	mirror *npcapRTMPMirror
}

type npcapRTMPMirror struct {
	mu         sync.Mutex
	dll        *windows.DLL
	handle     uintptr
	sendpacket *windows.Proc
	close      *windows.Proc
	geterr     *windows.Proc
}

func mirrorRTMPBroadcast(
	port *EtherTalkPort,
	pkt *ddp.ExtPacket,
) error {
	v, _ := rtmpMirrorSlots.LoadOrStore(
		port,
		&rtmpMirrorSlot{},
	)
	slot := v.(*rtmpMirrorSlot)

	slot.once.Do(func() {
		mirror, err := openNpcapRTMPMirror(port.device)
		if err != nil {
			port.logger.Warn(
				"RTMP: experimental Windows SendToRx mirror unavailable",
				"error", err,
			)
			return
		}
		slot.mirror = mirror
		port.logger.Warn(
			"RTMP: EXPERIMENTAL Windows Npcap SendToRx mirror enabled",
			"device", port.device,
			"mode", fmt.Sprintf("0x%04x", npcapModeSendToRx),
		)
	})

	if slot.mirror == nil {
		return nil
	}

	frame, err := ethertalk.AppleTalk(
		port.ethernetAddr,
		*pkt,
	)
	if err != nil {
		return fmt.Errorf(
			"build mirrored EtherTalk frame: %w",
			err,
		)
	}
	frame.Dst = ethertalk.AppleTalkBroadcast
	raw, err := ethertalk.Marshal(*frame)
	if err != nil {
		return fmt.Errorf(
			"marshal mirrored EtherTalk frame: %w",
			err,
		)
	}
	if len(raw) < 64 {
		raw = append(raw, make([]byte, 64-len(raw))...)
	}

	return slot.mirror.send(raw)
}

func openNpcapRTMPMirror(
	friendlyDevice string,
) (*npcapRTMPMirror, error) {
	iface, err := net.InterfaceByName(friendlyDevice)
	if err != nil {
		return nil, fmt.Errorf(
			"find Windows interface %q: %w",
			friendlyDevice,
			err,
		)
	}
	captureDevice, err := rtmpMirrorPcapDeviceName(iface)
	if err != nil {
		return nil, err
	}

	dll, err := loadNpcapWpcap()
	if err != nil {
		return nil, err
	}

	openLive, err := dll.FindProc("pcap_open_live")
	if err != nil {
		dll.Release()
		return nil, fmt.Errorf(
			"Npcap wpcap.dll has no pcap_open_live: %w",
			err,
		)
	}
	setmode, err := dll.FindProc("pcap_setmode")
	if err != nil {
		dll.Release()
		return nil, fmt.Errorf(
			"Npcap pcap_setmode unavailable; Npcap 1.83+ is required: %w",
			err,
		)
	}
	sendpacket, err := dll.FindProc("pcap_sendpacket")
	if err != nil {
		dll.Release()
		return nil, fmt.Errorf(
			"Npcap wpcap.dll has no pcap_sendpacket: %w",
			err,
		)
	}
	closeProc, err := dll.FindProc("pcap_close")
	if err != nil {
		dll.Release()
		return nil, fmt.Errorf(
			"Npcap wpcap.dll has no pcap_close: %w",
			err,
		)
	}
	geterr, err := dll.FindProc("pcap_geterr")
	if err != nil {
		dll.Release()
		return nil, fmt.Errorf(
			"Npcap wpcap.dll has no pcap_geterr: %w",
			err,
		)
	}

	dev, err := syscall.BytePtrFromString(captureDevice)
	if err != nil {
		dll.Release()
		return nil, fmt.Errorf(
			"encode Npcap capture device: %w",
			err,
		)
	}
	errbuf := make([]byte, pcapErrorBufferSize)

	// This handle is injection-only. A small snapshot length and no
	// promiscuous capture keep its unused receive buffer inexpensive.
	handle, _, _ := openLive.Call(
		uintptr(unsafe.Pointer(dev)),
		64,
		0,
		1,
		uintptr(unsafe.Pointer(&errbuf[0])),
	)
	if handle == 0 {
		dll.Release()
		return nil, fmt.Errorf(
			"pcap_open_live(%q) for SendToRx mirror: %s",
			captureDevice,
			nulTerminatedString(errbuf),
		)
	}

	modeResult, _, _ := setmode.Call(
		handle,
		npcapModeSendToRx,
	)
	if int32(modeResult) != 0 {
		errText := pcapErrorText(geterr, handle)
		closeProc.Call(handle)
		dll.Release()
		return nil, fmt.Errorf(
			"pcap_setmode(MODE_SENDTORX) failed: %s",
			errText,
		)
	}

	return &npcapRTMPMirror{
		dll:        dll,
		handle:     handle,
		sendpacket: sendpacket,
		close:      closeProc,
		geterr:     geterr,
	}, nil
}

func (m *npcapRTMPMirror) send(raw []byte) error {
	if len(raw) == 0 {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	result, _, _ := m.sendpacket.Call(
		m.handle,
		uintptr(unsafe.Pointer(&raw[0])),
		uintptr(len(raw)),
	)
	runtime.KeepAlive(raw)
	if int32(result) != 0 {
		return fmt.Errorf(
			"pcap_sendpacket SendToRx failed: %s",
			pcapErrorText(m.geterr, m.handle),
		)
	}
	return nil
}

func loadNpcapWpcap() (*windows.DLL, error) {
	if systemRoot := os.Getenv("SystemRoot"); systemRoot != "" {
		path := filepath.Join(
			systemRoot,
			"System32",
			"Npcap",
			"wpcap.dll",
		)
		if dll, err := windows.LoadDLL(path); err == nil {
			return dll, nil
		}
	}

	dll, err := windows.LoadDLL("wpcap.dll")
	if err != nil {
		return nil, fmt.Errorf(
			"load Npcap wpcap.dll: %w",
			err,
		)
	}
	return dll, nil
}

func rtmpMirrorPcapDeviceName(
	iface *net.Interface,
) (string, error) {
	var size uint32
	err := windows.GetAdaptersAddresses(
		0,
		0,
		0,
		nil,
		&size,
	)
	if err != nil &&
		err != syscall.ERROR_BUFFER_OVERFLOW {
		return "", fmt.Errorf(
			"GetAdaptersAddresses size query: %w",
			err,
		)
	}
	if size == 0 {
		return "", fmt.Errorf(
			"GetAdaptersAddresses returned an empty adapter table",
		)
	}

	buf := make([]byte, size)
	addrs := (*windows.IpAdapterAddresses)(
		unsafe.Pointer(&buf[0]),
	)
	if err := windows.GetAdaptersAddresses(
		0,
		0,
		0,
		addrs,
		&size,
	); err != nil {
		return "", fmt.Errorf(
			"GetAdaptersAddresses: %w",
			err,
		)
	}

	index := uint32(iface.Index)
	for addr := addrs; addr != nil; addr = addr.Next {
		if addr.IfIndex != index &&
			addr.Ipv6IfIndex != index {
			continue
		}

		adapterName := rtmpMirrorBytePtrString(
			addr.AdapterName,
		)
		if adapterName == "" {
			return "", fmt.Errorf(
				"Windows adapter %q has no adapter GUID",
				iface.Name,
			)
		}
		return `\Device\NPF_` + adapterName, nil
	}

	return "", fmt.Errorf(
		"could not map Windows interface %q (index %d) to an Npcap device",
		iface.Name,
		iface.Index,
	)
}

func rtmpMirrorBytePtrString(p *byte) string {
	if p == nil {
		return ""
	}
	var b []byte
	for off := uintptr(0); ; off++ {
		c := *(*byte)(unsafe.Pointer(
			uintptr(unsafe.Pointer(p)) + off,
		))
		if c == 0 {
			return string(b)
		}
		b = append(b, c)
	}
}

func pcapErrorText(
	geterr *windows.Proc,
	handle uintptr,
) string {
	p, _, _ := geterr.Call(handle)
	if p == 0 {
		return "unknown Npcap error"
	}
	return rtmpMirrorBytePtrString(
		(*byte)(unsafe.Pointer(p)),
	)
}

func nulTerminatedString(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
