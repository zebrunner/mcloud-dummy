package ui

import (
	"testing"
	"time"
)

func TestDrainBattery(t *testing.T) {
	drainStep = time.Millisecond
	defer func() { drainStep = 5 * time.Second }()
	a := New(Info{})
	a.SetPower("test", Power{Level: 3, Charging: false})
	done := make(chan struct{})
	go a.DrainBattery(done, 6000) // 0.1 percent per millisecond
	deadline := time.Now().Add(2 * time.Second)
	for a.Power().Level != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("level %d", a.Power().Level)
		}
		time.Sleep(time.Millisecond)
	}
	a.SetPower("test", Power{Level: 98, Charging: true})
	for a.Power().Level != 100 {
		if time.Now().After(deadline) {
			t.Fatalf("level %d", a.Power().Level)
		}
		time.Sleep(time.Millisecond)
	}
	close(done)
	if a.Power().Status() != 5 {
		t.Fatalf("status %d", a.Power().Status())
	}
}

func TestEventsSince(t *testing.T) {
	a := New(Info{})
	if events, seq := a.EventsSince(0); len(events) != 0 || seq != 0 {
		t.Fatalf("no events: %v %d", events, seq)
	}
	for i := range maxEvents + 50 {
		a.Log("test", "event", "%d", i)
	}
	events, seq := a.EventsSince(0)
	if len(events) != maxEvents || seq != maxEvents+50 || events[0].Detail != "50" {
		t.Fatalf("the kept events: %d, seq %d, first %v", len(events), seq, events[0])
	}
	a.Log("test", "event", "new")
	events, seq = a.EventsSince(seq)
	if len(events) != 1 || events[0].Detail != "new" || seq != maxEvents+51 {
		t.Fatalf("the new event: %v, seq %d", events, seq)
	}
	if events, _ := a.EventsSince(seq); len(events) != 0 {
		t.Fatalf("nothing new: %v", events)
	}
}
