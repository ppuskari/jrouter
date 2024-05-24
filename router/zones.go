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

func (rt *RouteTable) AddZonesToNetwork(n ddp.Network, zs ...string) {
	rt.routesMu.Lock()
	defer rt.routesMu.Unlock()
	for r := range rt.routes {
		if n < r.NetStart || n > r.NetEnd {
			continue
		}
		if r.ZoneNames == nil {
			r.ZoneNames = make(StringSet)
		}
		r.ZoneNames.Insert(zs...)
	}
}

func (rt *RouteTable) ZonesForNetworks(ns []ddp.Network) map[ddp.Network][]string {
	zs := make(map[ddp.Network][]string)

	rt.routesMu.Lock()
	defer rt.routesMu.Unlock()
	for r := range rt.routes {
		if !r.Valid() {
			continue
		}
		if _, ok := slices.BinarySearch(ns, r.NetStart); ok {
			for z := range r.ZoneNames {
				zs[r.NetStart] = append(zs[r.NetStart], z)
			}
		}
	}
	return zs
}

func (rt *RouteTable) RoutesForZone(zone string) []*Route {
	rt.routesMu.Lock()
	defer rt.routesMu.Unlock()

	var routes []*Route
	for r := range rt.routes {
		if !r.Valid() {
			continue
		}
		if r.ZoneNames.Contains(zone) {
			routes = append(routes, r)
		}
	}
	return routes
}

func (rt *RouteTable) AllZoneNames() (zones []string) {
	defer slices.Sort(zones)

	rt.routesMu.Lock()
	defer rt.routesMu.Unlock()

	zs := make(StringSet)
	for r := range rt.routes {
		if !r.Valid() {
			continue
		}
		zs.Add(r.ZoneNames)
	}

	return zs.ToSlice()
}
