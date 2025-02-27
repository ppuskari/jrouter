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

// AddZonesToRoute adds zone names to this route.
func (rt *RouteTable) AddZonesToRoute(target RouteTarget, netStart ddp.Network, zs ...string) error {
	route, err := rt.find(target, netStart)
	if err != nil {
		return err
	}

	if route.ZoneNames == nil {
		route.ZoneNames = make(StringSet)
	}
	route.ZoneNames.Insert(zs...)
	return nil
}

// ZonesForNetworks returns a map of network numbers to zone names in each.
// It only considers valid routes.
func (rt *RouteTable) ZonesForNetworks(networks []ddp.Network) map[ddp.Network][]string {
	zs := make(map[ddp.Network][]string)

	for _, n := range networks {
		func() {
			rt.routesByNetworkMu[n].RLock()
			defer rt.routesByNetworkMu[n].RUnlock()

			for _, r := range rt.routesByNetwork[n] {
				if !r.Valid() {
					continue
				}
				for z := range r.ZoneNames {
					zs[n] = append(zs[n], z)
				}
			}
		}()
	}

	return zs
}

// RoutesForZone returns all valid routes containing the zone name.
// (Zones can span multiple different networks.) This is used for handling
// NBP BrRq.
func (rt *RouteTable) RoutesForZone(zone string) []*Route {
	rt.allRoutesMu.RLock()
	defer rt.allRoutesMu.RUnlock()

	var routes []*Route
	for _, r := range rt.allRoutes {
		if !r.Valid() {
			continue
		}
		if r.ZoneNames.Contains(zone) {
			routes = append(routes, r)
		}
	}
	return routes

}

// AllZoneNames returns all zone names known to the router having at least one
// valid route. This is used by the ZIP GetZoneList function.
func (rt *RouteTable) AllZoneNames() []string {
	rt.allRoutesMu.RLock()
	defer rt.allRoutesMu.RUnlock()

	zs := make(StringSet)
	for _, r := range rt.allRoutes {
		if !r.Valid() {
			continue
		}
		zs.Add(r.ZoneNames)
	}

	return slices.Sorted(maps.Keys(zs))
}
