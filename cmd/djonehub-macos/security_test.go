package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidateLoopbackListen(t *testing.T) {
	for _, address := range []string{"127.0.0.1:7575", "localhost:7575", "[::1]:7575"} {
		if err := validateLoopbackListen(address); err != nil {
			t.Fatalf("validateLoopbackListen(%q): %v", address, err)
		}
	}
	for _, address := range []string{":7575", "0.0.0.0:7575", "192.168.1.5:7575", "invalid"} {
		if err := validateLoopbackListen(address); err == nil {
			t.Fatalf("validateLoopbackListen(%q) unexpectedly succeeded", address)
		}
	}
}

func TestLocalControlSecurityProtectsStateChangingRequests(t *testing.T) {
	server := httptest.NewServer(newDemoApp().routes())
	defer server.Close()

	token := fetchActionToken(t, server.URL)

	t.Run("missing token", func(t *testing.T) {
		request, err := http.NewRequest(
			http.MethodPost,
			server.URL+"/api/sms/refresh",
			bytes.NewBufferString("{}"),
		)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("status=%d, want %d", response.StatusCode, http.StatusForbidden)
		}
	})

	t.Run("cross-site origin", func(t *testing.T) {
		request, err := protectedJSONRequest(
			http.MethodPost,
			server.URL+"/api/sms/refresh",
			token,
			"{}",
		)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Origin", "https://attacker.example")
		request.Header.Set("Sec-Fetch-Site", "cross-site")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("status=%d, want %d", response.StatusCode, http.StatusForbidden)
		}
	})

	t.Run("wrong content type", func(t *testing.T) {
		request, err := http.NewRequest(
			http.MethodPost,
			server.URL+"/api/sms/refresh",
			bytes.NewBufferString("{}"),
		)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set(actionTokenHeader, token)
		request.Header.Set("Content-Type", "text/plain")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusUnsupportedMediaType {
			t.Fatalf("status=%d, want %d", response.StatusCode, http.StatusUnsupportedMediaType)
		}
	})

	t.Run("valid same-origin request", func(t *testing.T) {
		request, err := protectedJSONRequest(
			http.MethodPost,
			server.URL+"/api/sms/refresh",
			token,
			"{}",
		)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Origin", server.URL)
		request.Header.Set("Sec-Fetch-Site", "same-origin")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusAccepted {
			t.Fatalf("status=%d, want %d", response.StatusCode, http.StatusAccepted)
		}
	})

	t.Run("multiple JSON values", func(t *testing.T) {
		request, err := protectedJSONRequest(
			http.MethodPost,
			server.URL+"/api/sms/send",
			token,
			"{\"phone\":\"10086\",\"message\":\"test\"}{\"extra\":true}",
		)
		if err != nil {
			t.Fatal(err)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("status=%d, want %d", response.StatusCode, http.StatusBadRequest)
		}
	})
}

func TestLocalControlSecurityRejectsNonLoopbackHost(t *testing.T) {
	handler := newDemoApp().routes()
	request := httptest.NewRequest(http.MethodGet, "http://example.com/api/health", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want %d", response.Code, http.StatusForbidden)
	}
}

func fetchActionToken(t *testing.T, baseURL string) string {
	t.Helper()
	response, err := http.Get(baseURL + "/api/session")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("session status=%d", response.StatusCode)
	}
	body := make(map[string]string)
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["action_token"] == "" {
		t.Fatal("empty action token")
	}
	return body["action_token"]
}

func protectedJSONRequest(method, target, token, body string) (*http.Request, error) {
	request, err := http.NewRequest(method, target, bytes.NewBufferString(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(actionTokenHeader, token)
	return request, nil
}
