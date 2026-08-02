package main

import "testing"

func TestParseMacNWIInfoFindsVPNServerAndOrder(t *testing.T) {
	info := parseMacNWIInfo(`Network information
   utun8 : flags      : 0x5 (IPv4,DNS)
           address    : 198.18.0.1
           VPN server : 254.1.1.1
     en0 : flags      : 0x5 (IPv4,DNS)
           address    : 192.168.9.66
    en19 : flags      : 0x7 (IPv4,IPv6,DNS)
           address    : 192.168.225.22
Network interfaces: utun8 en0 en19
`)
	if got := info.VPNServers["utun8"]; len(got) != 1 || got[0] != "254.1.1.1" {
		t.Fatalf("VPN servers = %#v", got)
	}
	if len(info.Interfaces) != 3 || info.Interfaces[0] != "utun8" || info.Interfaces[2] != "en19" {
		t.Fatalf("interfaces = %#v", info.Interfaces)
	}
}

func TestResolveMacPhysicalRouteUsesVPNServerRoute(t *testing.T) {
	interfaces := []macNetInterface{
		{Name: "en0", Status: "active", IPv4: "192.168.9.66"},
		{Name: "en19", Status: "active", IPv4: "192.168.225.22"},
	}
	services := []macNetworkService{
		{Name: "Wi-Fi", HardwarePort: "Wi-Fi", Device: "en0"},
		{Name: "Baiwang", HardwarePort: "Baiwang", Device: "en19"},
	}
	nwi := macNWIInfo{
		VPNServers: map[string][]string{"utun8": {"254.1.1.1"}},
		Interfaces: []string{"utun8", "en0", "en19"},
	}
	got := resolveMacPhysicalRoute(
		macDefaultRoute{Interface: "utun8"}, interfaces, services, nwi,
		map[string]macDefaultRoute{"254.1.1.1": {Interface: "en19", Gateway: "192.168.225.1"}},
	)
	if !got.TunnelActive || got.PhysicalKind != "cellular" || got.PhysicalInterface != "en19" {
		t.Fatalf("physical route = %+v", got)
	}
	if summary := summarizeMacPhysicalRoute(got); summary != "VPN 经 4G 模块" {
		t.Fatalf("summary = %q", summary)
	}
}

func TestResolveMacPhysicalRouteFallsBackToActiveWiFi(t *testing.T) {
	interfaces := []macNetInterface{
		{Name: "en0", Status: "active", IPv4: "192.168.9.66"},
		{Name: "en19", Status: "active", IPv4: "192.168.225.22"},
	}
	services := []macNetworkService{
		{Name: "Wi-Fi", HardwarePort: "Wi-Fi", Device: "en0"},
		{Name: "Baiwang", HardwarePort: "Baiwang", Device: "en19"},
	}
	nwi := macNWIInfo{Interfaces: []string{"utun8", "en0", "en19"}}
	got := resolveMacPhysicalRoute(macDefaultRoute{Interface: "utun8"}, interfaces, services, nwi, nil)
	if got.PhysicalKind != "wifi" || got.PhysicalInterface != "en0" || got.Detection != "nwi-fallback" {
		t.Fatalf("physical route = %+v", got)
	}
}

func TestResolveMacPhysicalRouteWithoutVPNUsesDefaultRoute(t *testing.T) {
	interfaces := []macNetInterface{{Name: "en18", Status: "active", IPv4: "10.0.0.20"}}
	services := []macNetworkService{{Name: "AX88179B", HardwarePort: "AX88179B", Device: "en18"}}
	got := resolveMacPhysicalRoute(
		macDefaultRoute{Interface: "en18", Gateway: "10.0.0.1"}, interfaces, services, macNWIInfo{}, nil,
	)
	if got.TunnelActive || got.PhysicalKind != "ethernet" || got.PhysicalName != "AX88179B" {
		t.Fatalf("physical route = %+v", got)
	}
	if summary := summarizeMacPhysicalRoute(got); summary != "当前使用 AX88179B" {
		t.Fatalf("summary = %q", summary)
	}
}

func TestResolveMacPhysicalRouteWithoutVPNRecognizesWiFi(t *testing.T) {
	interfaces := []macNetInterface{{Name: "en0", Status: "active", IPv4: "192.168.1.8"}}
	services := []macNetworkService{{Name: "Wi-Fi", HardwarePort: "Wi-Fi", Device: "en0"}}
	got := resolveMacPhysicalRoute(macDefaultRoute{Interface: "en0"}, interfaces, services, macNWIInfo{}, nil)
	if got.TunnelActive || got.PhysicalKind != "wifi" {
		t.Fatalf("physical route = %+v", got)
	}
}
