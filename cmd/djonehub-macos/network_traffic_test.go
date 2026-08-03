package main

import "testing"

func TestParseMacInterfaceCountersUsesLinkRow(t *testing.T) {
	input := `Name Mtu Network Address Ipkts Ierrs Ibytes Opkts Oerrs Obytes Coll
en9 1500 <Link#22> aa:bb:cc:dd:ee:ff 120 0 4096 80 0 2048 0
en9 1500 192.168.225 192.168.225.20 120 - 4096 80 - 2048 -`

	got := parseMacInterfaceCounters(input)["en9"]
	if got.RX != 4096 || got.TX != 2048 {
		t.Fatalf("en9 counters = %+v, want RX=4096 TX=2048", got)
	}
}

func TestSelectDJITrafficInterfaceRequiresSupportedUSBDevice(t *testing.T) {
	interfaces := []macNetInterface{
		{Name: "en9", Kind: "ethernet", Status: "active", IPv4: "192.168.31.177"},
		{Name: "en19", Kind: "ethernet", Status: "active", IPv4: "192.168.225.25"},
	}
	services := []macNetworkService{
		{Name: "AX88179A", HardwarePort: "AX88179A", Device: "en9"},
		{Name: "Baiwang", HardwarePort: "Baiwang", Device: "en19"},
	}
	if got := selectDJITrafficInterface(nil, interfaces, services); got != "" {
		t.Fatalf("selected interface without DJI device = %q, want empty", got)
	}
	device := &usbDeviceStatus{VendorID: "2c7c", ProductID: "0125"}
	if got := selectDJITrafficInterface(device, interfaces, services[:1]); got != "" {
		t.Fatalf("selected interface with wired dock only = %q, want empty", got)
	}
	if got := selectDJITrafficInterface(device, interfaces, services); got != "en19" {
		t.Fatalf("selected interface = %q, want en19", got)
	}
}

func TestSessionTrafficIsDownloadPlusUpload(t *testing.T) {
	rx, tx, total := sessionTrafficFromCounters(
		networkByteCounters{RX: 8192, TX: 4096},
		networkByteCounters{RX: 2048, TX: 1024},
	)
	if rx != 6144 || tx != 3072 || total != 9216 {
		t.Fatalf("session traffic = rx:%d tx:%d total:%d", rx, tx, total)
	}
}
