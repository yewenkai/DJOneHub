package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
)

const actionTokenHeader = "X-DJOneHub-Action-Token"

func newActionToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate action token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func validateLoopbackListen(address string) error {
	host, _, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return fmt.Errorf("invalid HTTP listen address %q: %w", address, err)
	}
	if !isLoopbackHost(host) {
		return fmt.Errorf("HTTP listen address must use localhost or a loopback IP, got %q", address)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.Trim(strings.TrimSpace(host), "[]"), ".")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isLoopbackRequestHost(hostport string) bool {
	hostport = strings.TrimSpace(hostport)
	if hostport == "" {
		return false
	}
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	return isLoopbackHost(host)
}

func localControlSecurity(actionToken string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w)
		if !isLoopbackRequestHost(r.Host) {
			writeSecurityError(w, http.StatusForbidden, "invalid_host", "DJOneHub only accepts loopback requests")
			return
		}
		if !sameOriginRequest(r) {
			writeSecurityError(w, http.StatusForbidden, "cross_site_request", "cross-site requests are not allowed")
			return
		}
		if isStateChangingMethod(r.Method) {
			mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || mediaType != "application/json" {
				writeSecurityError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "application/json is required")
				return
			}
			provided := r.Header.Get(actionTokenHeader)
			if !constantTimeTokenEqual(provided, actionToken) {
				writeSecurityError(w, http.StatusForbidden, "invalid_action_token", "missing or invalid action token")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func sameOriginRequest(r *http.Request) bool {
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))) {
	case "", "none", "same-origin":
	default:
		return false
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host) &&
		(parsed.Scheme == "http" || parsed.Scheme == "https")
}

func isStateChangingMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func constantTimeTokenEqual(provided, expected string) bool {
	if provided == "" || expected == "" || len(provided) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func setSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Permissions-Policy", "camera=(), geolocation=(), microphone=()")
	w.Header().Set(
		"Content-Security-Policy",
		"default-src 'self'; base-uri 'none'; frame-ancestors 'none'; "+
			"form-action 'self'; img-src 'self' data:; script-src 'self' 'unsafe-inline'; style-src 'self'",
	)
}

func writeSecurityError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"code":  code,
		"error": message,
	})
}
