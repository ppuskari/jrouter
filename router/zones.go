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
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/sfiera/multitalk/pkg/ddp"
)

//const maxZoneAge = 10 * time.Minute // TODO: confirm

type Zone struct {
	Network  ddp.Network
	Name     string
	Local    bool
	LastSeen time.Time
}

func (z Zone) LastSeenAgo() string {
	if z.LastSeen.IsZero() {
		return "never"
	}
	return fmt.Sprintf("%v ago", time.Since(z.LastSeen).Truncate(time.Millisecond))
}

type zoneKey struct {
	network ddp.Network
	name    string
}

type ZoneTable struct {
	mu    sync.Mutex
	zones map[zoneKey]*Zone
}

func NewZoneTable() *ZoneTable {
	return &ZoneTable{
		zones: make(map[zoneKey]*Zone),
	}
}

func (zt *ZoneTable) Dump() []Zone {
	zt.mu.Lock()
	defer zt.mu.Unlock()
	zs := make([]Zone, 0, len(zt.zones))
	for _, z := range zt.zones {
		zs = append(zs, *z)
	}
	return zs
}

func (zt *ZoneTable) Upsert(network ddp.Network, name string, local bool) {
	zt.mu.Lock()
	defer zt.mu.Unlock()
	key := zoneKey{network, name}
	z := zt.zones[key]
	if z != nil {
		z.Local = local
		z.LastSeen = time.Now()
		return
	}
	zt.zones[key] = &Zone{
		Network:  network,
		Name:     name,
		Local:    local,
		LastSeen: time.Now(),
	}
}

func (zt *ZoneTable) Query(ns []ddp.Network) map[ddp.Network][]string {
	slices.Sort(ns)
	zs := make(map[ddp.Network][]string)

	zt.mu.Lock()
	defer zt.mu.Unlock()
	for _, z := range zt.zones {
		// if time.Since(z.LastSeen) > maxZoneAge {
		// 	continue
		// }
		if _, ok := slices.BinarySearch(ns, z.Network); ok {
			zs[z.Network] = append(zs[z.Network], z.Name)
		}
	}
	return zs
}

func (zt *ZoneTable) LookupName(name string) []*Zone {
	zt.mu.Lock()
	defer zt.mu.Unlock()

	var zs []*Zone
	for _, z := range zt.zones {
		if z.Name == name {
			zs = append(zs, z)
		}
	}
	return zs
}

func (zt *ZoneTable) LocalNames() []string {
	zt.mu.Lock()
	seen := make(map[string]struct{})
	zs := make([]string, 0, len(zt.zones))
	for _, z := range zt.zones {
		// if time.Since(z.LastSeen) > maxZoneAge {
		// 	continue
		// }
		if !z.Local {
			continue
		}
		if _, s := seen[z.Name]; s {
			continue
		}
		seen[z.Name] = struct{}{}
		zs = append(zs, z.Name)

	}
	zt.mu.Unlock()

	sort.Strings(zs)
	return zs
}

func (zt *ZoneTable) AllNames() []string {
	zt.mu.Lock()
	seen := make(map[string]struct{})
	zs := make([]string, 0, len(zt.zones))
	for _, z := range zt.zones {
		// if time.Since(z.LastSeen) > maxZoneAge {
		// 	continue
		// }
		if _, s := seen[z.Name]; s {
			continue
		}
		seen[z.Name] = struct{}{}
		zs = append(zs, z.Name)
	}
	zt.mu.Unlock()

	sort.Strings(zs)
	return zs
}
