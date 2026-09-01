package router

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync/atomic"
	"time"

	"drjosh.dev/jrouter/atalk"
	"drjosh.dev/jrouter/atalk/rtmp"
	"github.com/sfiera/multitalk/pkg/ddp"
)

const (
	loopProbeAttempts = 4
	loopProbeInterval = 2 * time.Second
)

var loopProbeNonce atomic.Uint64

type loopProbeInvestigation struct {
	key    string
	token  []byte
	peer   *AURPPeer
	port   *EtherTalkPort
	remote Route
	local  Route
}

func newLoopProbeToken() []byte {
	token := make([]byte, 16)
	binary.BigEndian.PutUint64(token[:8], uint64(time.Now().UnixNano()))
	binary.BigEndian.PutUint64(token[8:], loopProbeNonce.Add(1))
	return token
}

func loopProbeDestination(
	localAddr ddp.Addr,
	local Route,
	remote Route,
) (ddp.Network, error) {
	if localAddr.Network < local.NetStart ||
		localAddr.Network > local.NetEnd {
		return 0, fmt.Errorf(
			"local probe address %d outside route %d-%d",
			localAddr.Network,
			local.NetStart,
			local.NetEnd,
		)
	}
	offset := uint32(localAddr.Network) - uint32(local.NetStart)
	dst := uint32(remote.NetStart) + offset
	if dst > uint32(remote.NetEnd) {
		return 0, fmt.Errorf(
			"probe mapping %d exceeds remote route %d-%d",
			dst,
			remote.NetStart,
			remote.NetEnd,
		)
	}
	return ddp.Network(dst), nil
}

func buildLoopProbePacket(
	localAddr ddp.Addr,
	local Route,
	remote Route,
	token []byte,
) (*ddp.ExtPacket, error) {
	dstNet, err := loopProbeDestination(localAddr, local, remote)
	if err != nil {
		return nil, err
	}
	body, err := (&rtmp.RequestPacket{
		Function: rtmp.FunctionLoopProbe,
		Data:     append([]byte(nil), token...),
	}).Marshal()
	if err != nil {
		return nil, err
	}
	return &ddp.ExtPacket{
		ExtHeader: ddp.ExtHeader{
			Size:      uint16(len(body)) + atalk.DDPExtHeaderSize,
			Cksum:     0,
			DstNet:    dstNet,
			DstNode:   localAddr.Node,
			DstSocket: 1,
			SrcNet:    localAddr.Network,
			SrcNode:   localAddr.Node,
			SrcSocket: 1,
			Proto:     ddp.ProtoRTMPReq,
		},
		Data: body,
	}, nil
}

func (rtr *Router) startLoopInvestigation(
	peer *AURPPeer,
	remote Route,
	local Route,
) {
	if peer == nil || peer.LoopDisabled() {
		return
	}
	port, ok := local.Target.(*EtherTalkPort)
	if !ok || port.aarpMachine == nil {
		return
	}
	addr, ok := port.aarpMachine.Address()
	if !ok {
		return
	}

	key := fmt.Sprintf("%s|%d", peer.TunnelID(), remote.NetStart)
	token := newLoopProbeToken()
	investigation := &loopProbeInvestigation{
		key:    key,
		token:  token,
		peer:   peer,
		port:   port,
		remote: remote,
		local:  local,
	}

	rtr.loopProbeMu.Lock()
	if rtr.loopProbeByKey == nil {
		rtr.loopProbeByKey = make(map[string]string)
	}
	if rtr.loopProbes == nil {
		rtr.loopProbes = make(map[string]*loopProbeInvestigation)
	}
	if _, exists := rtr.loopProbeByKey[key]; exists {
		rtr.loopProbeMu.Unlock()
		return
	}
	tokenKey := string(token)
	rtr.loopProbeByKey[key] = tokenKey
	rtr.loopProbes[tokenKey] = investigation
	rtr.loopProbeMu.Unlock()

	rtr.Logger.Warn(
		"AURP: starting routing-loop investigation",
		"peer", peer.TunnelID(),
		"remote-range", fmt.Sprintf("%d-%d", remote.NetStart, remote.NetEnd),
		"local-port", port.device,
		"local-range", fmt.Sprintf("%d-%d", local.NetStart, local.NetEnd),
	)
	go rtr.runLoopInvestigation(investigation, addr.Proto)
}

func (rtr *Router) loopInvestigationActive(
	investigation *loopProbeInvestigation,
) bool {
	rtr.loopProbeMu.Lock()
	defer rtr.loopProbeMu.Unlock()
	return rtr.loopProbeByKey[investigation.key] ==
		string(investigation.token)
}

func (rtr *Router) retireLoopInvestigation(
	investigation *loopProbeInvestigation,
) {
	rtr.loopProbeMu.Lock()
	defer rtr.loopProbeMu.Unlock()
	tokenKey := string(investigation.token)
	if rtr.loopProbeByKey[investigation.key] == tokenKey {
		delete(rtr.loopProbeByKey, investigation.key)
	}
	delete(rtr.loopProbes, tokenKey)
}

func (rtr *Router) runLoopInvestigation(
	investigation *loopProbeInvestigation,
	localAddr ddp.Addr,
) {
	for attempt := 0; attempt < loopProbeAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(loopProbeInterval)
		}
		if !rtr.loopInvestigationActive(investigation) {
			return
		}
		pkt, err := buildLoopProbePacket(
			localAddr,
			investigation.local,
			investigation.remote,
			investigation.token,
		)
		if err != nil {
			rtr.Logger.Error(
				"AURP: couldn't build Loop Probe",
				"error", err,
			)
			break
		}
		if err := investigation.peer.Forward(
			context.Background(),
			pkt,
		); err != nil {
			rtr.Logger.Warn(
				"AURP: couldn't send Loop Probe",
				"peer", investigation.peer.TunnelID(),
				"attempt", attempt+1,
				"error", err,
			)
		}
	}
	time.Sleep(loopProbeInterval)
	if !rtr.loopInvestigationActive(investigation) {
		return
	}
	rtr.retireLoopInvestigation(investigation)
	rtr.Logger.Info(
		"AURP: routing-loop investigation completed without confirmation",
		"peer", investigation.peer.TunnelID(),
		"attempts", loopProbeAttempts,
	)
}

func (rtr *Router) handleLoopProbeReturn(
	port *EtherTalkPort,
	pkt *ddp.ExtPacket,
	token []byte,
) bool {
	if len(token) == 0 {
		return false
	}
	tokenKey := string(token)
	rtr.loopProbeMu.Lock()
	investigation := rtr.loopProbes[tokenKey]
	if investigation == nil || investigation.port != port {
		rtr.loopProbeMu.Unlock()
		return false
	}
	delete(rtr.loopProbes, tokenKey)
	delete(rtr.loopProbeByKey, investigation.key)
	rtr.loopProbeMu.Unlock()

	investigation.peer.confirmedRoutingLoops.Add(1)
	rtr.Logger.Error(
		"AURP: routing loop confirmed by returned Loop Probe",
		"peer", investigation.peer.TunnelID(),
		"local-port", port.device,
		"source", fmt.Sprintf("%d.%d", pkt.SrcNet, pkt.SrcNode),
	)
	select {
	case investigation.peer.loopDetectedCh <- struct{}{}:
	default:
	}
	return true
}
