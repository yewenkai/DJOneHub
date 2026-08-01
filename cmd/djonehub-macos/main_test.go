package main

import (
	"testing"
	"time"
)

func TestSupportedUSBDeviceIdentity(t *testing.T) {
	for _, tt := range []struct {
		name      string
		vendorID  int
		productID int
		want      bool
	}{
		{name: "original DJI identity", vendorID: 0x2ca3, productID: 0x4006, want: true},
		{name: "converted Quectel identity", vendorID: 0x2c7c, productID: 0x0125, want: true},
		{name: "unrelated USB device", vendorID: 0x05ac, productID: 0x0001, want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, got := supportedUSBDeviceIdentity(tt.vendorID, tt.productID)
			if got != tt.want {
				t.Fatalf("supportedUSBDeviceIdentity(%04x:%04x) supported = %v, want %v", tt.vendorID, tt.productID, got, tt.want)
			}
		})
	}
}

func TestParseDJIUSBDeviceConvertedQuectelIdentity(t *testing.T) {
	out := `+-o IOUSBHostInterface@2 <class IOUSBHostInterface>
    {
      "USB Vendor Name" = "BAIWANG"
      "USB Product Name" = "Baiwang"
      "idVendor" = 11388
      "idProduct" = 293
      "locationID" = 17825792
      "USBSpeed" = 3
      "bInterfaceNumber" = 2
      "bInterfaceClass" = 255
      "bInterfaceSubClass" = 0
      "bInterfaceProtocol" = 0
      "bNumEndpoints" = 3
    }

+-o IOUSBHostInterface@3 <class IOUSBHostInterface>
    {
      "USB Vendor Name" = "BAIWANG"
      "USB Product Name" = "Baiwang"
      "idVendor" = 11388
      "idProduct" = 293
      "locationID" = 17825792
      "USBSpeed" = 3
      "bInterfaceNumber" = 3
      "bInterfaceClass" = 255
      "bInterfaceSubClass" = 0
      "bInterfaceProtocol" = 0
      "bNumEndpoints" = 3
    }`

	device := parseDJIUSBDevice(out)
	if device == nil {
		t.Fatal("converted Quectel identity was not detected")
	}
	if device.VendorID != "2c7c" || device.ProductID != "0125" {
		t.Fatalf("identity = %s:%s, want 2c7c:0125", device.VendorID, device.ProductID)
	}
	if device.Product != "Baiwang" || device.Vendor != "BAIWANG" {
		t.Fatalf("device labels = %q/%q, want Baiwang/BAIWANG", device.Product, device.Vendor)
	}
	if len(device.Interfaces) != 2 || device.Interfaces[0].Number != 2 || device.Interfaces[1].Number != 3 {
		t.Fatalf("interfaces = %#v, want interfaces 2 and 3", device.Interfaces)
	}
	if device.Mode != "vendor-specific QMI/diagnostic mode" {
		t.Fatalf("mode = %q, want vendor-specific QMI/diagnostic mode", device.Mode)
	}
}

func TestPortScore(t *testing.T) {
	tests := []struct {
		name string
		port string
		want int
	}{
		{name: "named Quectel port", port: "/dev/cu.Quectel-AT", want: 100},
		{name: "usb modem", port: "/dev/cu.usbmodem2101", want: 80},
		{name: "usb serial", port: "/dev/cu.usbserial-1420", want: 60},
		{name: "bluetooth", port: "/dev/cu.Bluetooth-Incoming-Port", want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := portScore(tt.port); got != tt.want {
				t.Fatalf("portScore(%q) = %d, want %d", tt.port, got, tt.want)
			}
		})
	}
}

func TestParseUSBNetMode(t *testing.T) {
	for _, tt := range []struct {
		response string
		want     string
	}{
		{response: "AT+QCFG=\"usbnet\"\r\n+QCFG: \"usbnet\",0\r\nOK", want: "0"},
		{response: "+QCFG: \"usbnet\",1\r\nOK", want: "1"},
		{response: "ERROR", want: ""},
	} {
		if got := parseUSBNetMode(tt.response); got != tt.want {
			t.Fatalf("parseUSBNetMode(%q) = %q, want %q", tt.response, got, tt.want)
		}
	}
}

func TestParseUSBATOperator(t *testing.T) {
	for _, tt := range []struct {
		name     string
		response string
		want     string
	}{
		{
			name:     "known numeric PLMN",
			response: "AT+COPS?\r\n+COPS: 0,2,\"46015\",7\r\nOK",
			want:     "中国广电",
		},
		{
			name:     "long operator name",
			response: "+COPS: 0,0,\"CHN-UNICOM\",7\r\nOK",
			want:     "CHN-UNICOM",
		},
		{
			name:     "missing operator",
			response: "+COPS: 0\r\nOK",
			want:     "",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseUSBATOperator(tt.response); got != tt.want {
				t.Fatalf("parseUSBATOperator(%q) = %q, want %q", tt.response, got, tt.want)
			}
		})
	}
}

func TestNextSMSPollDelay(t *testing.T) {
	base := 8 * time.Second
	for _, tt := range []struct {
		failures int
		want     time.Duration
	}{
		{failures: 0, want: 8 * time.Second},
		{failures: 1, want: 8 * time.Second},
		{failures: 2, want: 16 * time.Second},
		{failures: 3, want: 32 * time.Second},
		{failures: 4, want: 60 * time.Second},
		{failures: 8, want: 60 * time.Second},
	} {
		if got := nextSMSPollDelay(base, tt.failures); got != tt.want {
			t.Fatalf("nextSMSPollDelay(%s, %d) = %s, want %s", base, tt.failures, got, tt.want)
		}
	}
}
