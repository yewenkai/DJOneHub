package main

import (
	"net"
	"strings"
	"testing"
)

func TestSelectPublicProbeIPRejectsFakeIP(t *testing.T) {
	_, err := selectPublicProbeIP([]net.IP{net.ParseIP("198.18.17.210")})
	if err == nil || !strings.Contains(err.Error(), "Fake-IP") {
		t.Fatalf("selectPublicProbeIP() err=%v, want Fake-IP rejection", err)
	}
}

func TestSelectPublicProbeIPRejectsPrivateIP(t *testing.T) {
	_, err := selectPublicProbeIP([]net.IP{net.ParseIP("192.168.1.10")})
	if err == nil || !strings.Contains(err.Error(), "非公网") {
		t.Fatalf("selectPublicProbeIP() err=%v, want private IP rejection", err)
	}
}

func TestSelectPublicProbeIPAcceptsPublicIPv4(t *testing.T) {
	got, err := selectPublicProbeIP([]net.IP{net.ParseIP("8.8.8.8")})
	if err != nil {
		t.Fatalf("selectPublicProbeIP() err=%v", err)
	}
	if got.String() != "8.8.8.8" {
		t.Fatalf("selectPublicProbeIP()=%v", got)
	}
}

func TestMedianWebMetricUsesSuccessfulTargetsOnly(t *testing.T) {
	results := []cellularWebProbeTargetResult{
		{OK: true, TTFBMS: 300},
		{OK: false, TTFBMS: 1},
		{OK: true, TTFBMS: 100},
		{OK: true, TTFBMS: 200},
	}
	got := medianWebMetric(results, func(item cellularWebProbeTargetResult) float64 { return item.TTFBMS })
	if got != 200 {
		t.Fatalf("medianWebMetric()=%v, want 200", got)
	}
}

func TestIsFakeOrNonPublicProbeIP(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
	}{
		{ip: "198.19.255.1", want: true},
		{ip: "10.0.0.1", want: true},
		{ip: "127.0.0.1", want: true},
		{ip: "1.1.1.1", want: false},
	}
	for _, test := range tests {
		if got := isFakeOrNonPublicProbeIP(net.ParseIP(test.ip)); got != test.want {
			t.Errorf("isFakeOrNonPublicProbeIP(%s)=%v, want %v", test.ip, got, test.want)
		}
	}
}
