package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStatusRefreshCoalescesConcurrentRequests(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	instance := &app{
		statusCollectFn: func() (any, error) {
			if calls.Add(1) == 1 {
				close(started)
			}
			<-release
			return map[string]any{"operator": "cached"}, nil
		},
	}

	const requestCount = 20
	var wait sync.WaitGroup
	wait.Add(requestCount)
	for range requestCount {
		go func() {
			defer wait.Done()
			instance.refreshStatus()
		}()
	}
	<-started
	time.Sleep(20 * time.Millisecond)
	close(release)
	wait.Wait()

	if got := calls.Load(); got != 1 {
		t.Fatalf("collector calls=%d, want 1", got)
	}
}

func TestStatusHandlerServesCachedSnapshot(t *testing.T) {
	var calls atomic.Int32
	instance := &app{
		statusCollectFn: func() (any, error) {
			calls.Add(1)
			return map[string]any{"operator": "cached"}, nil
		},
	}

	for range 2 {
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7575/api/status", nil)
		response := httptest.NewRecorder()
		instance.status(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
		if response.Header().Get("X-DJOneHub-Status-Sampled-At") == "" {
			t.Fatal("missing sampled-at header")
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("collector calls=%d, want 1", got)
	}
}

func TestStatusHandlerReturnsUnavailableBeforeFirstSuccessfulSample(t *testing.T) {
	instance := &app{
		statusCollectFn: func() (any, error) {
			return nil, errors.New("sample failed")
		},
	}
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7575/api/status", nil)
	response := httptest.NewRecorder()

	instance.status(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want %d", response.Code, http.StatusServiceUnavailable)
	}
}
