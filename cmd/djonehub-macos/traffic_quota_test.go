package main

import (
	"math"
	"testing"
	"time"
)

func TestParseTrafficQuotaSMS(t *testing.T) {
	calibration, ok := parseTrafficQuotaSMS(
		"10099",
		"尊敬的客户，本月套餐内流量100GB，已使用12.5GB，剩余87.5GB，上月结转20GB。【中国广电】",
	)
	if !ok {
		t.Fatal("traffic quota SMS was not recognized")
	}
	assertTrafficGB(t, "total", calibration.Total, 100)
	assertTrafficGB(t, "used", calibration.Used, 12.5)
	assertTrafficGB(t, "remaining", calibration.Remaining, 87.5)
	assertTrafficGB(t, "rollover", calibration.Rollover, 20)
}

func TestParseTrafficQuotaSMSRejectsUnrelatedMessage(t *testing.T) {
	if _, ok := parseTrafficQuotaSMS("10099", "您的验证码为358896"); ok {
		t.Fatal("OTP message must not calibrate traffic quota")
	}
	if _, ok := parseTrafficQuotaSMS("10086", "剩余流量80GB"); ok {
		t.Fatal("message from another carrier must not calibrate traffic quota")
	}
}

func TestSelectDJICellularInterfaceDoesNotCountWiredEthernet(t *testing.T) {
	interfaces := []macNetInterface{
		{Name: "en18", Status: "active", IPv4: "10.0.0.8"},
		{Name: "en19", Status: "active", IPv4: "192.168.225.22"},
	}
	services := []macNetworkService{
		{Name: "AX88179B", HardwarePort: "AX88179B", Device: "en18"},
		{Name: "Baiwang", HardwarePort: "Baiwang", Device: "en19"},
	}
	if got := selectDJICellularInterface(interfaces, services); got != "en19" {
		t.Fatalf("selected interface = %q, want en19", got)
	}
}

func TestSubtractFloor(t *testing.T) {
	if got := subtractFloor(100, 40); got != 60 {
		t.Fatalf("subtractFloor(100, 40) = %d", got)
	}
	if got := subtractFloor(40, 100); got != 0 {
		t.Fatalf("subtractFloor(40, 100) = %d", got)
	}
}

func TestTrafficQuotaSnapshotKeepsPartialManualCalibrationHonest(t *testing.T) {
	now := time.Now()
	remaining := gibibytes(200)
	app := &app{
		demo: true, quotaLoaded: true,
		quota: trafficQuotaStore{
			Month: now.Format("2006-01"), BaseQuotaBytes: defaultTrafficQuotaBytes,
			LocalRXBytes: 20 * 1024 * 1024, CalibrationLocalBytes: 20 * 1024 * 1024,
			TrackingStartedAt: now, UpdatedAt: now,
			OperatorTotalBytes: &remaining, OperatorRemainingBytes: &remaining,
			CalibrationSource: "manual", LastCalibrationAt: now,
		},
	}
	got := app.trafficQuotaSnapshot()
	if got.PlanTotalBytes != remaining || got.RemainingBytes != remaining {
		t.Fatalf("quota = total:%d remaining:%d, want %d", got.PlanTotalBytes, got.RemainingBytes, remaining)
	}
	if got.UsedKnown || !got.RemainingKnown || !got.PartialEstimate || got.Source != "manual" {
		t.Fatalf("quota flags = %+v", got)
	}
}

func assertTrafficGB(t *testing.T, name string, actual *uint64, expected float64) {
	t.Helper()
	if actual == nil {
		t.Fatalf("%s is nil", name)
	}
	actualGB := float64(*actual) / (1024 * 1024 * 1024)
	if math.Abs(actualGB-expected) > 0.001 {
		t.Fatalf("%s = %.3f GB, want %.3f GB", name, actualGB, expected)
	}
}
