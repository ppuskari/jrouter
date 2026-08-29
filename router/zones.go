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
	"maps"
	"slices"

	"github.com/sfiera/multitalk/pkg/ddp"
)

// AddZonesToRoute adds zone names to the network.
func (rt *RouteTable) AddZonesToNetwork(n ddp.Network, zs ...string) error {
	oldBest := rt.Lookup(n)

	// Keep the forward and reverse zone indexes in sync under one lock order:
	// network first, reverse index second. Only newly learned names are added
	// to networksByZone, preventing duplicate network entries on repeated ZIP
	// or AURP zone responses.
	rt.byNetwork[n].Lock()
	if rt.byNetwork[n].ZoneNames == nil {
		rt.byNetwork[n].ZoneNames = make(Set[string])
	}
	var added []string
	for _, zn := range zs {
		if rt.byNetwork[n].ZoneNames.Contains(zn) {
			continue
		}
		rt.byNetwork[n].ZoneNames.Insert(zn)
		added = append(added, zn)
	}
	if len(added) > 0 {
		rt.networksByZoneMu.Lock()
		for _, zn := range added {
			rt.networksByZone[zn] = append(rt.networksByZone[zn], n)
		}
		rt.networksByZoneMu.Unlock()
	}
	rt.byNetwork[n].Unlock()

	newBest := rt.Lookup(n)
	rt.informObservers(oldBest, newBest)

	return nil
}

// ReplaceZonesForNetwork atomically replaces the complete zone list for one
// network and keeps the reverse zone index synchronized. This is used when a
// complete AURP ZI-Rsp supersedes previously learned zone information.
func (rt *RouteTable) ReplaceZonesForNetwork(n ddp.Network, zs ...string) error {
	oldBest := rt.Lookup(n)

	next := make(Set[string])
	for _, zn := range zs {
		if zn != "" {
			next.Insert(zn)
		}
	}

	rt.byNetwork[n].Lock()
	old := rt.byNetwork[n].ZoneNames
	if old == nil {
		old = make(Set[string])
	}

	rt.networksByZoneMu.Lock()
	for zn := range old {
		if next.Contains(zn) {
			continue
		}
		networks := slices.DeleteFunc(
			rt.networksByZone[zn],
			func(network ddp.Network) bool { return network == n },
		)
		if len(networks) == 0 {
			delete(rt.networksByZone, zn)
		} else {
			rt.networksByZone[zn] = networks
		}
	}
	for zn := range next {
		if old.Contains(zn) {
			continue
		}
		rt.networksByZone[zn] = append(rt.networksByZone[zn], n)
	}
	rt.byNetwork[n].ZoneNames = next
	rt.networksByZoneMu.Unlock()
	rt.byNetwork[n].Unlock()

	newBest := rt.Lookup(n)
	rt.informObservers(oldBest, newBest)
	return nil
}

// clearZonesForNetworkIfNoRoutes removes zone metadata once a network has no
// stored route candidates at all. This prevents a disconnected AURP peer from
// leaving zone names behind that would make a later route immediately appear
// valid before fresh zone information is received.
func (rt *RouteTable) clearZonesForNetworkIfNoRoutes(n ddp.Network) {
	rt.byNetwork[n].Lock()
	defer rt.byNetwork[n].Unlock()

	if len(rt.byNetwork[n].Routes) != 0 ||
		len(rt.byNetwork[n].ZoneNames) == 0 {
		return
	}

	zones := rt.byNetwork[n].ZoneNames.ToSlice()
	rt.byNetwork[n].ZoneNames = nil

	rt.networksByZoneMu.Lock()
	defer rt.networksByZoneMu.Unlock()

	for _, zn := range zones {
		networks := slices.DeleteFunc(
			rt.networksByZone[zn],
			func(network ddp.Network) bool { return network == n },
		)
		if len(networks) == 0 {
			delete(rt.networksByZone, zn)
			continue
		}
		rt.networksByZone[zn] = networks
	}
}

// ZonesForNetworks returns a map of network numbers to zone names in each.
// It only considers valid routes.
func (rt *RouteTable) ZonesForNetworks(networks []ddp.Network) map[ddp.Network][]string {
	zs := make(map[ddp.Network][]string)

	for _, n := range networks {
		r := rt.Lookup(n)
		if r.Zero() {
			continue
		}
		func() {
			rt.byNetwork[n].RLock()
			defer rt.byNetwork[n].RUnlock()
			zs[n] = append(zs[n], rt.byNetwork[n].ZoneNames.ToSlice()...)
		}()
	}

	return zs
}

// RoutesForZone returns best routes for the zone name.
// (Zones can span multiple different networks.) This is used for handling
// NBP BrRq.
func (rt *RouteTable) RoutesForZone(zone string) []Route {
	var routes []Route
	for _, n := range rt.networksForZone(zone) {
		func() {
			rt.byNetwork[n].RLock()
			defer rt.byNetwork[n].RUnlock()
			if !rt.byNetwork[n].ZoneNames.Contains(zone) {
				return
			}
			r := rt.Lookup(n) // reader side of lock is shared, so we're good.
			if r.Zero() {
				return
			}
			routes = append(routes, r)
		}()
	}
	return routes
}

func (rt *RouteTable) networksForZone(zone string) []ddp.Network {
	rt.networksByZoneMu.RLock()
	defer rt.networksByZoneMu.RUnlock()
	return rt.networksByZone[zone]
}

// AllZoneNames returns all zone names known to the router having at least one
// valid route. This is used by the ZIP GetZoneList function.
func (rt *RouteTable) AllZoneNames() []string {
	rt.networksByZoneMu.RLock()
	defer rt.networksByZoneMu.RUnlock()
	return slices.Sorted(maps.Keys(rt.networksByZone))
}
