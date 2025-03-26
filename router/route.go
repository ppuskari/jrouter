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
type Route struct {
	RouteKey // embeds TargetKey and NetStart

	Extended bool
	NetEnd   ddp.Network

	// Target provides a way to forward packets using this route
	Target RouteTarget

	Distance uint8
	LastSeen time.Time

	// reference back to the netStart network
	network *network
}

// Zero reports whether the route is a zero value for Route (trivially invalid).
func (r Route) Zero() bool {
	return r.Target == nil || r.network == nil
}

// Valid reports whether the route is valid.
// A valid route has a target, one or more zone names, and if it is learned from
// an AppleTalk router, the last data update is not too old.
func (r Route) Valid() bool {
	if r.Zero() {
		return false
	}
	if len(r.network.ZoneNames) == 0 {
		return false
	}
	if r.Target.Class() == TargetClassAppleTalkPeer {
		return time.Since(r.LastSeen) <= maxRouteAge
	}
	return true
}

// ZoneNames returns the zone names for the network associated with this route.
func (r Route) ZoneNames() []string {
	if r.Zero() {
		return nil
	}
	return r.network.ZoneNames.ToSlice()
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
