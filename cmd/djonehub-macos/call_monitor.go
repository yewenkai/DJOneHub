package main

import (
	"context"
	"fmt"
	"log"
	"time"
)

type callRecord struct {
	ID        string     `json:"id"`
	Index     int        `json:"index"`
	Direction string     `json:"direction"`
	State     string     `json:"state"`
	Number    string     `json:"number,omitempty"`
	StartedAt time.Time  `json:"started_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	Missed    bool       `json:"missed"`
}

type callMonitorSnapshot struct {
	Calls         []voiceCall
	Active        *callRecord
	History       []callRecord
	Polling       bool
	PollInterval  time.Duration
	LastPoll      time.Time
	LastPollError string
}

func (a *app) startCallMonitor(ctx context.Context) {
	interval := a.callPollInterval
	if interval <= 0 {
		interval = 3 * time.Second
	}
	a.callMu.Lock()
	a.callPollInterval = interval
	a.callMu.Unlock()

	timer := time.NewTimer(800 * time.Millisecond)
	defer timer.Stop()
	lastError := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if err := a.pollCallOnce(); err != nil {
				if err.Error() != lastError {
					log.Printf("call monitor poll failed: %v", err)
					lastError = err.Error()
				}
			} else if lastError != "" {
				log.Printf("call monitor recovered")
				lastError = ""
			}
			timer.Reset(interval)
		}
	}
}

func (a *app) pollCallOnce() error {
	if a.demo {
		a.setCallPollStatus(nil)
		return nil
	}
	if a.modem == nil && a.currentUSBDevice() == nil {
		err := fmt.Errorf("DJI USB device is not connected")
		a.applyCallPoll(nil, time.Now())
		a.setCallPollStatus(err)
		return err
	}

	a.callMu.RLock()
	configured := a.callConfigured
	a.callMu.RUnlock()
	if !configured {
		if _, err := a.runATCommand("AT+CLIP=1", 3*time.Second); err != nil {
			a.setCallPollStatus(err)
			return err
		}
		a.callMu.Lock()
		a.callConfigured = true
		a.callMu.Unlock()
	}

	calls, err := a.currentVoiceService().QueryCalls()
	if err != nil {
		a.setCallPollStatus(err)
		return err
	}
	a.applyCallPoll(calls, time.Now())
	a.currentVoiceService().ObserveCalls(calls)
	a.setCallPollStatus(nil)
	return nil
}

func (a *app) applyCallPoll(calls []voiceCall, now time.Time) {
	var selected *voiceCall
	for index := range calls {
		candidate := &calls[index]
		if selected == nil || callStatePriority(candidate.State) > callStatePriority(selected.State) {
			selected = candidate
		}
	}

	a.callMu.Lock()
	defer a.callMu.Unlock()
	a.callCurrent = append([]voiceCall(nil), calls...)

	if selected == nil {
		a.finishActiveCallLocked(now)
		return
	}
	if a.callActive == nil || a.callActive.Index != selected.ID || a.callActive.Direction != selected.Direction {
		a.finishActiveCallLocked(now)
		a.callActive = &callRecord{
			ID:        fmt.Sprintf("%d-%d", now.UnixMilli(), selected.ID),
			Index:     selected.ID,
			Direction: selected.Direction,
			State:     selected.State,
			Number:    selected.Number,
			StartedAt: now,
			UpdatedAt: now,
		}
		return
	}
	a.callActive.State = selected.State
	a.callActive.UpdatedAt = now
	if selected.Number != "" {
		a.callActive.Number = selected.Number
	}
}

func (a *app) finishActiveCallLocked(now time.Time) {
	if a.callActive == nil {
		return
	}
	ended := now
	a.callActive.EndedAt = &ended
	a.callActive.UpdatedAt = now
	a.callActive.Missed = a.callActive.Direction == "incoming" &&
		(a.callActive.State == "incoming" || a.callActive.State == "waiting")
	a.callHistory = append([]callRecord{*a.callActive}, a.callHistory...)
	if len(a.callHistory) > 100 {
		a.callHistory = a.callHistory[:100]
	}
	a.callActive = nil
}

func callStatePriority(state string) int {
	switch state {
	case "incoming", "waiting":
		return 5
	case "active":
		return 4
	case "alerting":
		return 3
	case "dialing":
		return 2
	case "held":
		return 1
	default:
		return 0
	}
}

func (a *app) setCallPollStatus(err error) {
	a.callMu.Lock()
	defer a.callMu.Unlock()
	a.callLastPoll = time.Now()
	if err != nil {
		a.callLastPollError = err.Error()
		return
	}
	a.callLastPollError = ""
}

func (a *app) callSnapshot() callMonitorSnapshot {
	a.callMu.RLock()
	defer a.callMu.RUnlock()
	var active *callRecord
	if a.callActive != nil {
		copy := *a.callActive
		active = &copy
	}
	interval := a.callPollInterval
	if interval <= 0 {
		interval = 3 * time.Second
	}
	return callMonitorSnapshot{
		Calls:         append([]voiceCall{}, a.callCurrent...),
		Active:        active,
		History:       append([]callRecord{}, a.callHistory...),
		Polling:       !a.demo,
		PollInterval:  interval,
		LastPoll:      a.callLastPoll,
		LastPollError: a.callLastPollError,
	}
}
