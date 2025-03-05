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
	rt := NewRouteTable()
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
	rt := NewRouteTable()
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
	rt := NewRouteTable()
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
	rt := NewRouteTable()
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
	rt := NewRouteTable()
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
