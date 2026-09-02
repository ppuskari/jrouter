/*
   Copyright 2025 Josh Deprez

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
	"cmp"
	"context"
	"slices"
	"testing"
	"time"

	gocmp "github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/sfiera/multitalk/pkg/ddp"
)

// Helpful [cmp.Option]s
var (
	ignoreUnexportedRoute = cmpopts.IgnoreUnexported(Route{})
	ignoreTimes           = gocmp.FilterValues(func(time.Time, time.Time) bool { return true }, gocmp.Ignore())
	comparableTarget      = cmpopts.EquateComparable(fakeTarget{})
	comparableObserver    = cmpopts.EquateComparable(&fakeObserver{})
)

type fakeObserver struct {
	events []observerEvent
}

func (o *fakeObserver) sortDeleteEventSubranges() {
	for i, j := 0, 0; i < len(o.events); i = j {
		for j = i + 1; j <= len(o.events); j++ {
			if j < len(o.events) && o.events[i].Event == o.events[j].Event {
				continue
			}
			break
		}

		if o.events[i].Event != "deleted" {
			continue
		}

		slices.SortFunc(o.events[i:j], func(a, b observerEvent) int {
			return cmp.Or(
				cmp.Compare(a.From.TargetKey, b.From.TargetKey),
				cmp.Compare(a.From.NetStart, b.From.NetStart),
			)
		})
	}
}

func (o *fakeObserver) NetworkAdded(r Route) {
	o.events = append(o.events, observerEvent{Event: "added", To: r})
}

func (o *fakeObserver) NetworkDeleted(r Route) {
	o.events = append(o.events, observerEvent{Event: "deleted", From: r})
}

func (o *fakeObserver) BestNetworkChanged(from, to Route) {
	o.events = append(o.events, observerEvent{Event: "changed", From: from, To: to})
}

type observerEvent struct {
	Event    string
	From, To Route
}

type fakeTarget struct {
	key   string
	class TargetClass
}

func (t fakeTarget) Forward(context.Context, *ddp.ExtPacket) error { return nil }
func (t fakeTarget) RouteTargetKey() string                        { return t.key }
func (t fakeTarget) Class() TargetClass                            { return t.class }

func TestRouteTable_AddObserver_RemoveObserver(t *testing.T) {
	rt := NewRouteTable(t.Context())
	obs := &fakeObserver{}
	rt.AddObserver(obs)

	wantObservers := map[RouteTableObserver]struct{}{
		obs: {},
	}
	if diff := gocmp.Diff(rt.observers, wantObservers, comparableObserver, ignoreUnexportedRoute); diff != "" {
		t.Errorf("rt.observers diff (-got +want):\n%s", diff)
	}

	rt.RemoveObserver(obs)
	wantObservers = map[RouteTableObserver]struct{}{}
	if diff := gocmp.Diff(rt.observers, wantObservers, comparableObserver, ignoreUnexportedRoute); diff != "" {
		t.Errorf("rt.observers diff (-got +want):\n%s", diff)
	}
}

func TestRouteTable_Upsert_Insertion(t *testing.T) {
	rt := NewRouteTable(t.Context())
	obs := &fakeObserver{}
	rt.AddObserver(obs)

	direct := fakeTarget{key: "direct", class: TargetClassDirect}
	aurpPeer := fakeTarget{key: "aurpPeer", class: TargetClassAURPPeer}
	etPeer := fakeTarget{key: "etPeer", class: TargetClassAppleTalkPeer}

	directRoute, err := rt.UpsertRoute(direct, true, 100, 101, 0)
	if err != nil {
		t.Errorf("rt.UpsertRoute(direct, true, 100, 101, 0) error = %v", err)
	}
	aurpRoute, err := rt.UpsertRoute(aurpPeer, true, 200, 201, 1)
	if err != nil {
		t.Errorf("rt.UpsertRoute(aurpPeer, true, 200, 201, 1) error = %v", err)
	}
	etRoute, err := rt.UpsertRoute(etPeer, true, 300, 301, 1)
	if err != nil {
		t.Errorf("rt.UpsertRoute(etPeer, true, 300, 301, 1) error = %v", err)
	}

	// At this point the routes are invalid (no zone names)
	for _, want := range []Route{directRoute, aurpRoute, etRoute} {
		for n := ddp.Network(want.NetStart); n <= want.NetEnd; n++ {
			got := rt.Lookup(n)
			if !got.Zero() {
				t.Errorf("rt.Lookup(%d) = %v, want zero route", n, got)
			}
		}
	}

	// The observer should have not been informed of the new routes, because
	// they are invalid without zones.
	var wantEvents []observerEvent
	if diff := gocmp.Diff(obs.events, wantEvents, ignoreTimes, comparableTarget, ignoreUnexportedRoute); diff != "" {
		t.Errorf("obs.added diff (-got +want):\n%s", diff)
	}

	// Now add some zones.
	if err := rt.AddZonesToNetwork(100, "The Twilight Zone"); err != nil {
		t.Errorf("rt.AddZonesToRoute(direct, 100, \"The Twilight Zone\") = %v", err)
	}
	if err := rt.AddZonesToNetwork(200, "The Fright Zone"); err != nil {
		t.Errorf("rt.AddZonesToRoute(peer, 200, \"The Fright Zone\") = %v", err)
	}

	// Now these routes have zones, Lookup should return them.
	for _, want := range []Route{directRoute, aurpRoute} {
		for n := ddp.Network(want.NetStart); n <= want.NetEnd; n++ {
			got := rt.Lookup(n)
			if diff := gocmp.Diff(got, want, ignoreTimes, comparableTarget, ignoreUnexportedRoute); diff != "" {
				t.Errorf("rt.Lookup(%d) = %v, want %v", n, got, want)
			}
		}
	}

	// Both routes should have been published.
	wantEvents = []observerEvent{
		{Event: "added", To: directRoute},
		{Event: "added", To: aurpRoute},
	}
	if diff := gocmp.Diff(obs.events, wantEvents, ignoreTimes, comparableTarget, ignoreUnexportedRoute); diff != "" {
		t.Errorf("obs.added diff (-got +want):\n%s", diff)
	}
}

func TestRouteTable_Upsert_Updating(t *testing.T) {
	rt := NewRouteTable(t.Context())
	obs := &fakeObserver{}
	rt.AddObserver(obs)

	etPeer := fakeTarget{key: "etPeer", class: TargetClassAppleTalkPeer}
	oldRoute, err := rt.UpsertRoute(etPeer, true, 300, 301, 1)
	if err != nil {
		t.Errorf("rt.UpsertRoute(etPeer, true, 300, 301, 1) error = %v", err)
	}
	if err := rt.AddZonesToNetwork(300, "TimeZone"); err != nil {
		t.Errorf("rt.AddZonesToRoute(etPeer, 300, \"TimeZone\") = %v", err)
	}

	// Check that it's there
	for _, n := range []ddp.Network{300, 301} {
		got := rt.Lookup(n)
		if diff := gocmp.Diff(got, oldRoute, ignoreTimes, comparableTarget, ignoreUnexportedRoute); diff != "" {
			t.Errorf("rt.Lookup(%d) = %v, want %v", n, got, oldRoute)
		}
	}

	// Now update it by re-upserting
	newRoute, err := rt.UpsertRoute(etPeer, true, 300, 301, 3)
	if err != nil {
		t.Errorf("rt.UpsertRoute(etPeer, true, 300, 301, 3) error = %v", err)
	}

	// Check that it changed
	for _, n := range []ddp.Network{300, 301} {
		gotRoute := rt.Lookup(n)
		if diff := gocmp.Diff(gotRoute, newRoute, ignoreTimes, comparableTarget, ignoreUnexportedRoute); diff != "" {
			t.Errorf("rt.Lookup(%d) = %v, want %v", n, gotRoute, newRoute)
		}
	}

	// Check the generated events
	wantEvents := []observerEvent{
		{Event: "added", To: oldRoute},
		{Event: "changed", From: oldRoute, To: newRoute},
	}
	if diff := gocmp.Diff(obs.events, wantEvents, ignoreTimes, comparableTarget, ignoreUnexportedRoute); diff != "" {
		t.Errorf("obs.changed diff (-got +want):\n%s", diff)
	}
}

func TestRouteTable_DeleteRoute(t *testing.T) {
	rt := NewRouteTable(t.Context())
	obs := &fakeObserver{}
	rt.AddObserver(obs)

	etPeer := fakeTarget{key: "etPeer", class: TargetClassAppleTalkPeer}
	oldRoute, err := rt.UpsertRoute(etPeer, true, 300, 301, 1)
	if err != nil {
		t.Errorf("rt.UpsertRoute(etPeer, true, 300, 301, 1) error = %v", err)
	}
	if err := rt.AddZonesToNetwork(300, "TimeZone"); err != nil {
		t.Errorf("rt.AddZonesToRoute(etPeer, 300, \"TimeZone\") = %v", err)
	}

	// Check that it's there
	for _, n := range []ddp.Network{300, 301} {
		got := rt.Lookup(n)
		if diff := gocmp.Diff(got, oldRoute, ignoreTimes, comparableTarget, ignoreUnexportedRoute); diff != "" {
			t.Errorf("rt.Lookup(%d) = %v, want %v", n, got, oldRoute)
		}
	}

	// Delete it
	if err := rt.DeleteRoute(etPeer, 300); err != nil {
		t.Errorf("rt.DeleteRoute(etPeer, 300) = %v", err)
	}

	for n := ddp.Network(300); n <= 301; n++ {
		got := rt.Lookup(n)
		if !got.Zero() {
			t.Errorf("rt.Lookup(%d) = %v, want zero route", n, got)
		}
	}

	wantEvents := []observerEvent{
		{Event: "added", To: oldRoute},
		{Event: "deleted", From: oldRoute},
	}
	if diff := gocmp.Diff(obs.events, wantEvents, ignoreTimes, comparableTarget, ignoreUnexportedRoute); diff != "" {
		t.Errorf("obs.changed diff (-got +want):\n%s", diff)
	}
}

func TestRouteTable_DeleteTarget(t *testing.T) {
	rt := NewRouteTable(t.Context())
	obs := &fakeObserver{}
	rt.AddObserver(obs)

	etPeer := fakeTarget{key: "etPeer", class: TargetClassAppleTalkPeer}
	oldRoute1, err := rt.UpsertRoute(etPeer, true, 300, 301, 1)
	if err != nil {
		t.Errorf("rt.UpsertRoute(etPeer, true, 300, 301, 1) error = %v", err)
	}
	if err := rt.AddZonesToNetwork(300, "TimeZone"); err != nil {
		t.Errorf("rt.AddZonesToRoute(etPeer, 300, \"TimeZone\") = %v", err)
	}

	oldRoute2, err := rt.UpsertRoute(etPeer, true, 500, 501, 1)
	if err != nil {
		t.Errorf("rt.UpsertRoute(etPeer, true, 500, 501, 1) error = %v", err)
	}
	if err := rt.AddZonesToNetwork(500, "TimeZone 2"); err != nil {
		t.Errorf("rt.AddZonesToRoute(etPeer, 500, \"TimeZone 2\") = %v", err)
	}

	// Check that they're both there
	for _, n := range []ddp.Network{300, 301} {
		got := rt.Lookup(n)
		if diff := gocmp.Diff(got, oldRoute1, ignoreTimes, comparableTarget, ignoreUnexportedRoute); diff != "" {
			t.Errorf("rt.Lookup(%d) = %v, want %v", n, got, oldRoute1)
		}
	}
	for _, n := range []ddp.Network{500, 501} {
		got := rt.Lookup(n)
		if diff := gocmp.Diff(got, oldRoute2, ignoreTimes, comparableTarget, ignoreUnexportedRoute); diff != "" {
			t.Errorf("rt.Lookup(%d) = %v, want %v", n, got, oldRoute2)
		}
	}

	// Delete the target -> deletes all routes
	rt.DeleteTarget(etPeer)

	for _, n := range []ddp.Network{300, 301, 500, 501} {
		got := rt.Lookup(n)
		if !got.Zero() {
			t.Errorf("rt.Lookup(%d) = %v, want zero route", n, got)
		}
	}

	wantEvents := []observerEvent{
		{Event: "added", To: oldRoute1},
		{Event: "added", To: oldRoute2},
		{Event: "deleted", From: oldRoute1},
		{Event: "deleted", From: oldRoute2},
	}
	// Because DeleteTarget passes network changes through a map, the order may
	// vary, so sort subranges of delete events into network order.
	obs.sortDeleteEventSubranges()
	if diff := gocmp.Diff(obs.events, wantEvents, ignoreTimes, comparableTarget, ignoreUnexportedRoute); diff != "" {
		t.Errorf("obs.changed diff (-got +want):\n%s", diff)
	}
}

func TestRC2RouteTrafficCountsSourceAndDestinationBytes(t *testing.T) {
	rt := NewRouteTable(t.Context())
	src := fakeTarget{key: "traffic-src", class: TargetClassDirect}
	dst := fakeTarget{key: "traffic-dst", class: TargetClassDirect}

	if _, err := rt.UpsertRoute(src, true, 4100, 4100, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.UpsertRoute(dst, true, 4200, 4200, 0); err != nil {
		t.Fatal(err)
	}
	if err := rt.ReplaceZonesForNetwork(4100, "Source Zone"); err != nil {
		t.Fatal(err)
	}
	if err := rt.ReplaceZonesForNetwork(4200, "Destination Zone"); err != nil {
		t.Fatal(err)
	}

	rtr := &Router{RouteTable: rt}
	packet := &ddp.ExtPacket{
		ExtHeader: ddp.ExtHeader{SrcNet: 4100, DstNet: 4200},
		Data:      make([]byte, 87),
	}
	if err := rtr.Output(t.Context(), packet); err != nil {
		t.Fatal(err)
	}

	if got := rt.Lookup(4100).DDPBytesIn(); got != 100 {
		t.Fatalf("source DDP bytes in = %d, want 100", got)
	}
	if got := rt.Lookup(4100).DDPBytesOut(); got != 0 {
		t.Fatalf("source DDP bytes out = %d, want 0", got)
	}
	if got := rt.Lookup(4200).DDPBytesOut(); got != 100 {
		t.Fatalf("destination DDP bytes out = %d, want 100", got)
	}
	if got := rt.Lookup(4200).DDPBytesIn(); got != 0 {
		t.Fatalf("destination DDP bytes in = %d, want 0", got)
	}
}

func TestRC2RouteTrafficSurvivesRouteRefresh(t *testing.T) {
	rt := NewRouteTable(t.Context())
	target := fakeTarget{key: "traffic-refresh", class: TargetClassAppleTalkPeer}

	if _, err := rt.UpsertRoute(target, true, 4300, 4300, 1); err != nil {
		t.Fatal(err)
	}
	if err := rt.ReplaceZonesForNetwork(4300, "Refresh Zone"); err != nil {
		t.Fatal(err)
	}
	before := rt.Lookup(4300)
	before.noteDDPBytesIn(1234)
	before.noteDDPBytesOut(5678)

	if _, err := rt.UpsertRoute(target, true, 4300, 4300, 2); err != nil {
		t.Fatal(err)
	}
	after := rt.Lookup(4300)
	if got := after.DDPBytesIn(); got != 1234 {
		t.Fatalf("DDP bytes in after refresh = %d, want 1234", got)
	}
	if got := after.DDPBytesOut(); got != 5678 {
		t.Fatalf("DDP bytes out after refresh = %d, want 5678", got)
	}
}

func TestRC2AURPIngressTrafficCreditsActualIngressRoute(t *testing.T) {
	rt := NewRouteTable(t.Context())
	bestSource := fakeTarget{key: "best-source", class: TargetClassDirect}
	ingress := &AURPPeer{tunnelID: "cfg:traffic-ingress.example"}
	destination := fakeTarget{key: "traffic-local-dst", class: TargetClassDirect}

	if _, err := rt.UpsertRoute(bestSource, true, 4400, 4400, 0); err != nil {
		t.Fatal(err)
	}
	ingress.RouteTable = rt
	if _, err := rt.UpsertRoute(ingress, true, 4400, 4400, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.UpsertRoute(destination, true, 4500, 4500, 0); err != nil {
		t.Fatal(err)
	}
	if err := rt.ReplaceZonesForNetwork(4400, "Shared Source"); err != nil {
		t.Fatal(err)
	}
	if err := rt.ReplaceZonesForNetwork(4500, "Local Destination"); err != nil {
		t.Fatal(err)
	}

	rtr := &Router{RouteTable: rt, Config: &Config{}}
	packet := &ddp.ExtPacket{
		ExtHeader: ddp.ExtHeader{SrcNet: 4400, DstNet: 4500},
		Data:      make([]byte, 187),
	}
	if err := rtr.OutputFromAURP(t.Context(), ingress, packet); err != nil {
		t.Fatal(err)
	}

	ingressRoute := rt.lookupForTarget(4400, ingress)
	if got := ingressRoute.DDPBytesIn(); got != 200 {
		t.Fatalf("actual ingress route DDP bytes in = %d, want 200", got)
	}
	if got := rt.Lookup(4400).DDPBytesIn(); got != 0 {
		t.Fatalf("best-but-not-ingress route was credited %d bytes", got)
	}
	if got := rt.Lookup(4500).DDPBytesOut(); got != 200 {
		t.Fatalf("destination DDP bytes out = %d, want 200", got)
	}
}
