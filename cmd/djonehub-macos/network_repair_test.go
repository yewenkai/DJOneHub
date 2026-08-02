//go:build darwin

package main

import "testing"

func TestParseMacNetworkServicesFindsBaiwang(t *testing.T) {
	services := parseMacNetworkServices(`An asterisk (*) denotes that a network service is disabled.
(1) Wi-Fi
(Hardware Port: Wi-Fi, Device: en0)

(2) Baiwang
(Hardware Port: Baiwang, Device: en19)

(*) Disabled Baiwang
(Hardware Port: Baiwang, Device: en20)
`)
	if len(services) != 3 {
		t.Fatalf("services = %+v, want 3", services)
	}
	if !isDJICellularService(services[1]) || services[1].Disabled {
		t.Fatalf("Baiwang service = %+v, want enabled cellular service", services[1])
	}
	if !services[2].Disabled {
		t.Fatalf("disabled service = %+v, want disabled", services[2])
	}
}

func TestParseMacIPv4ServiceInfo(t *testing.T) {
	info := parseMacIPv4ServiceInfo("IP address: 192.168.225.22\nSubnet mask: 255.255.255.0\nRouter: 192.168.225.1\n")
	if info.Address != "192.168.225.22" || info.Subnet != "255.255.255.0" {
		t.Fatalf("info = %+v", info)
	}
}
