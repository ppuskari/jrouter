/*
   Copyright 2024 Josh Deprez

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package router

import (
	"context"
	"time"

	"github.com/sfiera/multitalk/pkg/ddp"
)

const (
	maxRouteAge      = 10 * time.Minute // TODO: confirm
	maxRouteDistance = 15
)

// Route represents a route: a destination network range, a way to send packets
// towards the destination, and some other data that affects whether the route
// is used.
type RouteOriginKind uint8

const (
	RouteOriginDirect RouteOriginKind = iota
	RouteOriginAppleTalk
	RouteOriginAURP
)

type RouteOrigin struct {
	Kind RouteOriginKind
	ID   string
}

type aurpTunnelIdentity interface {
	TunnelID() string
}

func routeOriginForTarget(target RouteTarget) RouteOrigin {
	if target == nil {
		return RouteOrigin{}
	}
	switch target.Class() {
	case TargetClassAURPPeer:
		id := target.RouteTargetKey()
		if t, ok := target.(aurpTunnelIdentity); ok {
			id = t.TunnelID()
		}
		return RouteOrigin{Kind: RouteOriginAURP, ID: id}
	case TargetClassAppleTalkPeer:
		return RouteOrigin{
			Kind: RouteOriginAppleTalk,
			ID:   target.RouteTargetKey(),
		}
	default:
		return RouteOrigin{
			Kind: RouteOriginDirect,
			ID:   target.RouteTargetKey(),
		}
	}
}

// Route represents a route: a destination network range, a way to send packets
// towards the destination, and some other data that affects whether the route
// is used.
type Route struct {
	RouteKey // embeds TargetKey and NetStart

	Extended bool
	NetEnd   ddp.Network

	// Target provides a way to forward packets using this route.
	Target RouteTarget

	// Origin records where the route was learned independently from the
	// current forwarding endpoint. In particular, AURP origin identity remains
	// stable across DNS endpoint changes.
	Origin RouteOrigin

	Distance uint8
	LastSeen time.Time

	// reference back to the netStart network
	network *network
}

// Zero reports whether the route is a zero value for Route (trivially invalid).
func (r Route) Zero() bool {
	return r.Target == nil || r.network == nil
}

func (r Route) validIgnoringAge() bool {
	return !r.Zero() && len(r.network.ZoneNames) != 0
}

func (r Route) expiredAt(now time.Time) bool {
	return r.Target != nil &&
		r.Target.Class() == TargetClassAppleTalkPeer &&
		now.Sub(r.LastSeen) > maxRouteAge
}

// Valid reports whether the route is valid.
// A valid route has a target, one or more zone names, and if it is learned from
// an AppleTalk router, the last data update is not too old.
func (r Route) Valid() bool {
	return r.validIgnoringAge() && !r.expiredAt(time.Now())
}

// ZoneNames returns the zone names for the network associated with this route.
func (r Route) ZoneNames() []string {
	if r.Zero() {
		return nil
	}
	return r.network.ZoneNames.ToSlice()
}

// RouteOrigin returns stored route provenance. Older/manually constructed
// routes without explicit provenance derive it from their target.
func (r Route) RouteOrigin() RouteOrigin {
	if r.Origin.ID != "" || r.Target == nil {
		return r.Origin
	}
	return routeOriginForTarget(r.Target)
}

func (r Route) LearnedVia() string {
	switch r.RouteOrigin().Kind {
	case RouteOriginDirect:
		return "local"
	case RouteOriginAppleTalk:
		return "rtmp"
	case RouteOriginAURP:
		return "aurp"
	default:
		return "unknown"
	}
}

// RouteTarget implementations can forward packets somewhere.
type RouteTarget interface {
	// Forward should send the packet to the route target.
	Forward(context.Context, *ddp.ExtPacket) error

	// Class returns the target class for this target.
	Class() TargetClass

	// RouteTargetKey is used for determining if two targets are the same.
	RouteTargetKey() string
}

// TargetClass is an enum type for representing the broad classes of route
// targets.
type TargetClass int

// Target class values.
const (
	TargetClassDirect        TargetClass = iota // directly attached EtherTalk / LocalTalk / etc network
	TargetClassAURPPeer                         // another router over AURP
	TargetClassAppleTalkPeer                    // another router via EtherTalk / LocalTalk / etc
	TargetClassCount                            // how many valid target types there are - insert new classes above.
)
