package main

import (
	"bytes"
	"math"
	"net/http"
	"net/http/httptest"
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

func TestTrafficQuotaSnapshotDoesNotInflateConfiguredTotalFromManualReadings(t *testing.T) {
	now := time.Now()
	base := gibibytes(100)
	rollover := gibibytes(100)
	used := gibibytes(0.96)
	remaining := gibibytes(200)
	legacyManualTotal := used + remaining
	app := &app{
		demo: true, quotaLoaded: true,
		quota: trafficQuotaStore{
			Month: now.Format("2006-01"), BaseQuotaBytes: base, RolloverBytes: rollover,
			TrackingStartedAt: now, UpdatedAt: now,
			OperatorTotalBytes: &legacyManualTotal, OperatorUsedBytes: &used,
			OperatorRemainingBytes: &remaining, CalibrationLocalBytes: 0,
			CalibrationSource: "manual", LastCalibrationAt: now,
		},
	}

	got := app.trafficQuotaSnapshot()
	if got.PlanTotalBytes != base+rollover {
		t.Fatalf("plan total = %d, want configured total %d", got.PlanTotalBytes, base+rollover)
	}
	if got.UsedBytes != used || got.RemainingBytes != remaining {
		t.Fatalf("manual readings changed: used=%d remaining=%d", got.UsedBytes, got.RemainingBytes)
	}
}

func TestConfigureTrafficQuotaStartsFromLatestManualReading(t *testing.T) {
	now := time.Now()
	oldUsed := gibibytes(0.56)
	oldRemaining := gibibytes(199.44)
	local := gibibytes(0.4)
	app := &app{
		demo: true, quotaLoaded: true,
		quota: trafficQuotaStore{
			Month: now.Format("2006-01"), BaseQuotaBytes: gibibytes(100), RolloverBytes: gibibytes(100),
			LocalRXBytes: local, TrackingStartedAt: now, UpdatedAt: now,
			OperatorUsedBytes: &oldUsed, OperatorRemainingBytes: &oldRemaining,
			CalibrationSource: "manual", LastCalibrationAt: now,
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/api/network/quota/config", bytes.NewBufferString(`{"manual_remaining_gb":200}`))
	response := httptest.NewRecorder()

	app.configureTrafficQuota(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if app.quota.OperatorUsedBytes == nil || *app.quota.OperatorUsedBytes != 0 {
		t.Fatalf("used baseline = %v, want 0 derived from latest remaining reading", app.quota.OperatorUsedBytes)
	}
	if app.quota.OperatorRemainingBytes == nil || *app.quota.OperatorRemainingBytes != gibibytes(200) {
		t.Fatalf("remaining baseline = %v, want 200 GB", app.quota.OperatorRemainingBytes)
	}
	if app.quota.CalibrationLocalBytes != local {
		t.Fatalf("local baseline = %d, want %d", app.quota.CalibrationLocalBytes, local)
	}
	if app.quota.OperatorTotalBytes != nil {
		t.Fatalf("manual update must not overwrite configured total: %d", *app.quota.OperatorTotalBytes)
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
