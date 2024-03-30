package main

import (
	"bytes"
	"context"
	"log"
	"net"
	"time"

	"gitea.drjosh.dev/josh/jrouter/aurp"
)

const (
	// TODO: check these parameters
	lastHeardFromTimer      = 10 * time.Second
	lastHeardFromRetryLimit = 10
	sendRetryTimer          = 10 * time.Second
	sendRetryLimit          = 5
)

type receiverState int

const (
	receiverStateUnconnected receiverState = iota
	receiverStateConnected
	receiverStateWaitForOpenRsp
	receiverStateWaitForRIRsp
	receiverStateWaitForTickleAck
)

type senderState int

const (
	senderStateUnconnected senderState = iota
	senderStateConnected
	senderStateWaitForRIAck1
	senderStateWaitForRIAck2
	senderStateWaitForRIAck3
)

type peer struct {
	tr    *aurp.Transport
	conn  *net.UDPConn
	raddr *net.UDPAddr
	recv  chan aurp.Packet
}

// send encodes and sends pkt to the remote host.
func (p *peer) send(pkt aurp.Packet) (int, error) {
	var b bytes.Buffer
	if _, err := pkt.WriteTo(&b); err != nil {
		return 0, err
	}
	log.Printf("Sending %T (len %d) to %v", pkt, b.Len(), p.raddr)
	return p.conn.WriteToUDP(b.Bytes(), p.raddr)
}

func (p *peer) handle(ctx context.Context) error {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	lastHeardFrom := time.Now()
	lastSend := time.Now()
	sendRetries := 0

	rstate := receiverStateUnconnected
	sstate := senderStateUnconnected

	// Write an Open-Req packet
	if _, err := p.send(p.tr.NewOpenReqPacket(nil)); err != nil {
		log.Printf("Couldn't send Open-Req packet: %v", err)
		return err
	}

	rstate = receiverStateWaitForOpenRsp

	for {
		select {
		case <-ctx.Done():
			if rstate == receiverStateUnconnected {
				return ctx.Err()
			}
			// Send a best-effort Router Down before returning
			if _, err := p.send(p.tr.NewRDPacket(aurp.ErrCodeNormalClose)); err != nil {
				log.Printf("Couldn't send RD packet: %v", err)
			}
			return ctx.Err()

		case <-ticker.C:
			switch rstate {
			case receiverStateWaitForOpenRsp:
				if time.Since(lastSend) <= sendRetryTimer {
					break
				}
				if sendRetries >= sendRetryLimit {
					log.Printf("Send retry limit reached while waiting for Open-Rsp, closing connection")
					rstate = receiverStateUnconnected
					break
				}

				// Send another Open-Req
				sendRetries++
				if _, err := p.send(p.tr.NewOpenReqPacket(nil)); err != nil {
					log.Printf("Couldn't send Open-Req packet: %v", err)
					return err
				}

			case receiverStateConnected:
				// Check LHFT, send tickle?
				if time.Since(lastHeardFrom) > lastHeardFromTimer {
					if _, err := p.send(p.tr.NewTicklePacket()); err != nil {
						log.Printf("Couldn't send Tickle: %v", err)
					}
				}
				rstate = receiverStateWaitForTickleAck
			}

		case pkt := <-p.recv:
			lastHeardFrom = time.Now()

			switch pkt := pkt.(type) {
			case *aurp.OpenReqPacket:
				if sstate != senderStateUnconnected {
					log.Printf("Open-Req received but sender state is not Unconnected (was %d); ignoring packet", sstate)
					break
				}

				// The peer tells us their connection ID in Open-Req.
				p.tr.RemoteConnID = pkt.ConnectionID

				// Formulate a response.
				var orsp *aurp.OpenRspPacket
				switch {
				case pkt.Version != 1:
					// Respond with Open-Rsp with unknown version error.
					orsp = p.tr.NewOpenRspPacket(0, int16(aurp.ErrCodeInvalidVersion), nil)

				case len(pkt.Options) > 0:
					// Options? OPTIONS? We don't accept no stinkin' _options_
					orsp = p.tr.NewOpenRspPacket(0, int16(aurp.ErrCodeOptionNegotiation), nil)

				default:
					// Accept it I guess.
					orsp = p.tr.NewOpenRspPacket(0, 1, nil)
				}

				log.Printf("Responding with %T", orsp)

				if _, err := p.send(orsp); err != nil {
					log.Printf("Couldn't send Open-Rsp: %v", err)
				}
				if orsp.RateOrErrCode >= 0 {
					sstate = senderStateConnected
				} else {
					sstate = senderStateUnconnected
				}

			case *aurp.OpenRspPacket:
				if rstate != receiverStateWaitForOpenRsp {
					log.Printf("Received Open-Rsp but was not waiting for one (receiver state was %d)", rstate)
				}
				if pkt.RateOrErrCode < 0 {
					// It's an error code.
					log.Printf("Open-Rsp error code from peer %v: %d", p.raddr.IP, pkt.RateOrErrCode)
					// Close the connection
					rstate = receiverStateUnconnected
				}

				// TODO: Make other requests

			case *aurp.RIReqPacket:
				// TODO: Respond with RI-Rsp

			case *aurp.RIRspPacket:
				// TODO: Repsond with RI-Ack
				// TODO: Integrate info into route table

			case *aurp.RIAckPacket:
				// TODO: Continue sending next RI-Rsp (streamed)
				// TODO: If SZI flag is set, send ZI-Rsp (transaction)

			case *aurp.RIUpdPacket:
				// TODO: Integrate info into route table

			case *aurp.RDPacket:
				// TODO: Remove router from route tables

				log.Printf("Router Down: error code %d %s", pkt.ErrorCode, pkt.ErrorCode)
				// Respond with RI-Ack
				if _, err := p.send(p.tr.NewRIAckPacket(pkt.ConnectionID, pkt.Sequence, 0)); err != nil {
					log.Printf("Couldn't send RI-Ack: %v", err)
				}
				// Connection closed
				rstate = receiverStateUnconnected

			case *aurp.ZIReqPacket:
				// TODO: Respond with ZI-Rsp

			case *aurp.ZIRspPacket:
				// TODO: Integrate info into zone table

			case *aurp.TicklePacket:
				// Immediately respond with Tickle-Ack
				if _, err := p.send(p.tr.NewTickleAckPacket()); err != nil {
					log.Printf("Couldn't send Tickle-Ack: %v", err)
				}

			case *aurp.TickleAckPacket:
				if rstate != receiverStateWaitForTickleAck {
					log.Printf("Received Tickle-Ack but was not waiting for one (receever state was %d)", rstate)
				}
				rstate = receiverStateConnected

			}
		}
	}
}
