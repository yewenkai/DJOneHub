package main

import (
	"net/netip"
	"strings"
	"testing"
)

func TestNormalizeEgressPolicy(t *testing.T) {
	policy, err := normalizeEgressPolicy(egressPolicyStore{
		Enabled:        true,
		GlobalUnderlay: egressUnderlayCellular,
		CorporateCIDRs: []string{"10.1.2.3/8", "10.0.0.0/8", "172.16.0.0/12"},
		Destinations: []egressDestinationRule{
			{Kind: "DOMAIN", Value: "*.Example.COM", Policy: egressDirectCellular},
			{Kind: "ip", Value: "203.0.113.10", Policy: egressDirectCorporate},
		},
		Applications: []egressApplicationRule{
			{Name: "Safari", Path: "/Applications/Safari.app", Executable: "/Applications/Safari.app/Contents/MacOS/Safari", Policy: "vpn"},
		},
	})
	if err != nil {
		t.Fatalf("normalize policy: %v", err)
	}
	if got, want := strings.Join(policy.CorporateCIDRs, ","), "10.0.0.0/8,172.16.0.0/12"; got != want {
		t.Fatalf("corporate CIDRs = %q, want %q", got, want)
	}
	if got := policy.Destinations[0].Value; got != "example.com" {
		t.Fatalf("normalized domain = %q", got)
	}
	if policy.VPNPolicy != "Proxy" {
		t.Fatalf("default VPN policy = %q", policy.VPNPolicy)
	}
}

func TestNormalizeEgressPolicyRejectsUnsafeNetworks(t *testing.T) {
	for _, cidr := range []string{"0.0.0.0/0", "127.0.0.0/8", "169.254.0.0/16", "198.18.0.0/15", "224.0.0.0/4", "2001:db8::/32"} {
		_, err := normalizeEgressPolicy(egressPolicyStore{GlobalUnderlay: egressUnderlayCorporate, CorporateCIDRs: []string{cidr}})
		if err == nil {
			t.Errorf("expected %s to be rejected", cidr)
		}
	}
}

func TestResolveEgressDomainRejectsStashFakeIP(t *testing.T) {
	if validEgressAddress(netip.MustParseAddr("198.18.18.83")) {
		t.Fatal("Stash Fake-IP must never be used as a physical route target")
	}
}

func TestRenderEgressStashOverrideForNativeMac(t *testing.T) {
	policy := egressPolicyStore{
		Enabled:        true,
		GlobalUnderlay: egressUnderlayCorporate,
		VPNPolicy:      "Proxy",
		CorporateCIDRs: []string{"10.0.0.0/8"},
		Destinations: []egressDestinationRule{
			{Kind: "domain", Value: "example.com", Policy: egressDirectCellular},
		},
		Applications: []egressApplicationRule{
			{Name: "Safari", Path: "/Applications/Safari.app", Executable: "/Applications/Safari.app/Contents/MacOS/Safari", Policy: "vpn"},
		},
	}
	runtime := egressRuntimeStatus{
		Corporate: egressEndpoint{Interface: "en9"},
		Cellular:  egressEndpoint{Interface: "en19"},
		Client:    egressClientStatus{ProcessRulesSupported: true},
	}
	override := renderEgressStashOverride(policy, runtime)
	for _, expected := range []string{
		"interface-name: \"en9\"",
		"interface-name: \"en19\"",
		"IP-CIDR,10.0.0.0/8,DIRECT,no-resolve",
		"DOMAIN-SUFFIX,example.com,DJOneHub 4G直连",
		"PROCESS-PATH,/Applications/Safari.app/Contents/MacOS/Safari,Proxy",
	} {
		if !strings.Contains(override, expected) {
			t.Errorf("override missing %q:\n%s", expected, override)
		}
	}
}

func TestRenderEgressStashOverrideOmitsProcessRulesForIOS(t *testing.T) {
	policy := egressPolicyStore{
		Enabled:        true,
		GlobalUnderlay: egressUnderlayCorporate,
		Applications: []egressApplicationRule{
			{Name: "Safari", Path: "/Applications/Safari.app", Executable: "/Applications/Safari.app/Contents/MacOS/Safari", Policy: "vpn"},
		},
	}
	override := renderEgressStashOverride(policy, egressRuntimeStatus{})
	if strings.Contains(override, "PROCESS-PATH") {
		t.Fatalf("iOS Stash override must not contain ineffective process rules:\n%s", override)
	}
	if !strings.Contains(override, "不执行应用进程规则") {
		t.Fatalf("iOS warning missing:\n%s", override)
	}
}

func TestMoveServiceFirstPreservesOrder(t *testing.T) {
	got := moveServiceFirst([]string{"AX88179A", "Wi-Fi", "Baiwang"}, "Baiwang")
	want := []string{"Baiwang", "AX88179A", "Wi-Fi"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("service order = %v, want %v", got, want)
	}
}
