package main

import (
	"testing"
	"time"
)

func TestCallMonitorMarksMissedIncomingCall(t *testing.T) {
	instance := &app{}
	started := time.Date(2026, 8, 2, 9, 0, 0, 0, time.Local)
	instance.applyCallPoll([]voiceCall{{ID: 1, Direction: "incoming", State: "incoming", Number: "10086"}}, started)
	instance.applyCallPoll(nil, started.Add(8*time.Second))

	snapshot := instance.callSnapshot()
	if snapshot.Active != nil {
		t.Fatal("active call was not cleared")
	}
	if len(snapshot.History) != 1 || !snapshot.History[0].Missed {
		t.Fatalf("history = %+v, want one missed call", snapshot.History)
	}
}

func TestCallMonitorDoesNotMarkAnsweredCallMissed(t *testing.T) {
	instance := &app{}
	started := time.Date(2026, 8, 2, 9, 0, 0, 0, time.Local)
	instance.applyCallPoll([]voiceCall{{ID: 1, Direction: "incoming", State: "incoming", Number: "10086"}}, started)
	instance.applyCallPoll([]voiceCall{{ID: 1, Direction: "incoming", State: "active", Number: "10086"}}, started.Add(3*time.Second))
	instance.applyCallPoll(nil, started.Add(8*time.Second))

	snapshot := instance.callSnapshot()
	if len(snapshot.History) != 1 || snapshot.History[0].Missed {
		t.Fatalf("history = %+v, want one answered call", snapshot.History)
	}
}

func TestCallMonitorKeepsNewestHundredRecords(t *testing.T) {
	instance := &app{}
	started := time.Date(2026, 8, 2, 9, 0, 0, 0, time.Local)
	for index := 0; index < 105; index++ {
		at := started.Add(time.Duration(index) * time.Minute)
		instance.applyCallPoll([]voiceCall{{ID: index + 1, Direction: "outgoing", State: "dialing"}}, at)
		instance.applyCallPoll(nil, at.Add(time.Second))
	}
	if got := len(instance.callSnapshot().History); got != 100 {
		t.Fatalf("history length = %d, want 100", got)
	}
}

func TestCallMonitorReturnsEmptyCollections(t *testing.T) {
	snapshot := (&app{}).callSnapshot()
	if snapshot.Calls == nil || snapshot.History == nil {
		t.Fatalf("empty snapshot must use JSON arrays: %+v", snapshot)
	}
}
