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
	"slices"

	"github.com/sfiera/multitalk/pkg/ddp"
)

func (rt *RouteTable) AddZoneToNetwork(n ddp.Network, z string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for r := range rt.routes {
		if n < r.NetStart || n > r.NetEnd {
			continue
		}
		if !r.Valid() {
			continue
		}
		if slices.Contains(r.ZoneNames, z) {
			continue
		}
		r.ZoneNames = append(r.ZoneNames, z)
	}
}

func (rt *RouteTable) ZonesForNetworks(ns []ddp.Network) map[ddp.Network][]string {
	zs := make(map[ddp.Network][]string)

	rt.mu.Lock()
	defer rt.mu.Unlock()
	for r := range rt.routes {
		if !r.Valid() {
			continue
		}
		if _, ok := slices.BinarySearch(ns, r.NetStart); ok {
			zs[r.NetStart] = append(zs[r.NetStart], r.ZoneNames...)
		}
	}
	return zs
}

func (rt *RouteTable) RoutesForZone(zone string) []*Route {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	var routes []*Route
	for r := range rt.routes {
		if !r.Valid() {
			continue
		}
		if slices.Contains(r.ZoneNames, zone) {
			routes = append(routes, r)
		}
	}
	return routes
}

func (rt *RouteTable) AllZoneNames() (zones []string) {
	defer slices.Sort(zones)

	rt.mu.Lock()
	defer rt.mu.Unlock()

	seen := make(map[string]struct{})
	for r := range rt.routes {
		if !r.Valid() {
			continue
		}
		for _, z := range r.ZoneNames {
			if _, s := seen[z]; s {
				continue
			}
			seen[z] = struct{}{}
			zones = append(zones, z)
		}
	}

	return zones
}
