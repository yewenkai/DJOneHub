package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultTrafficQuotaBytes = uint64(100 * 1024 * 1024 * 1024)
	trafficQuotaSamplePeriod = 30 * time.Second
	trafficQuotaQueryPeriod  = 24 * time.Hour
)

type trafficQuotaStore struct {
	Month                  string    `json:"month"`
	BaseQuotaBytes         uint64    `json:"base_quota_bytes"`
	RolloverBytes          uint64    `json:"rollover_bytes"`
	LocalRXBytes           uint64    `json:"local_rx_bytes"`
	LocalTXBytes           uint64    `json:"local_tx_bytes"`
	LastInterface          string    `json:"last_interface,omitempty"`
	LastRawRX              uint64    `json:"last_raw_rx,omitempty"`
	LastRawTX              uint64    `json:"last_raw_tx,omitempty"`
	TrackingStartedAt      time.Time `json:"tracking_started_at"`
	UpdatedAt              time.Time `json:"updated_at"`
	OperatorTotalBytes     *uint64   `json:"operator_total_bytes,omitempty"`
	OperatorUsedBytes      *uint64   `json:"operator_used_bytes,omitempty"`
	OperatorRemainingBytes *uint64   `json:"operator_remaining_bytes,omitempty"`
	CalibrationLocalBytes  uint64    `json:"calibration_local_bytes,omitempty"`
	LastCalibrationAt      time.Time `json:"last_calibration_at,omitempty"`
	LastCalibrationText    string    `json:"last_calibration_text,omitempty"`
	CalibrationSource      string    `json:"calibration_source,omitempty"`
	QueryCommand           string    `json:"query_command,omitempty"`
	AutoQuery              bool      `json:"auto_query"`
	LastQueryAt            time.Time `json:"last_query_at,omitempty"`
	LastQueryError         string    `json:"last_query_error,omitempty"`
}

type trafficQuotaResponse struct {
	Month               string    `json:"month"`
	BaseQuotaBytes      uint64    `json:"base_quota_bytes"`
	RolloverBytes       uint64    `json:"rollover_bytes"`
	PlanTotalBytes      uint64    `json:"plan_total_bytes"`
	UsedBytes           uint64    `json:"used_bytes"`
	RemainingBytes      uint64    `json:"remaining_bytes"`
	LocalRXBytes        uint64    `json:"local_rx_bytes"`
	LocalTXBytes        uint64    `json:"local_tx_bytes"`
	LocalTrackedBytes   uint64    `json:"local_tracked_bytes"`
	TrackingStartedAt   time.Time `json:"tracking_started_at"`
	UpdatedAt           time.Time `json:"updated_at"`
	Source              string    `json:"source"`
	PartialEstimate     bool      `json:"partial_estimate"`
	UsedKnown           bool      `json:"used_known"`
	RemainingKnown      bool      `json:"remaining_known"`
	LastCalibrationAt   time.Time `json:"last_calibration_at,omitempty"`
	LastCalibrationText string    `json:"last_calibration_text,omitempty"`
	QueryRecipient      string    `json:"query_recipient"`
	QueryCommand        string    `json:"query_command,omitempty"`
	AutoQuery           bool      `json:"auto_query"`
	QueryConfigured     bool      `json:"query_configured"`
	LastQueryAt         time.Time `json:"last_query_at,omitempty"`
	NextQueryAt         time.Time `json:"next_query_at,omitempty"`
	LastQueryError      string    `json:"last_query_error,omitempty"`
}

type trafficQuotaCalibration struct {
	Total     *uint64
	Used      *uint64
	Remaining *uint64
	Rollover  *uint64
}

func (a *app) startTrafficQuotaSampler(ctx context.Context) {
	_ = a.loadTrafficQuota()
	a.sampleTrafficQuota()
	ticker := time.NewTicker(trafficQuotaSamplePeriod)
	defer ticker.Stop()
	defer a.persistTrafficQuota(true)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.sampleTrafficQuota()
			a.maybeAutoQueryTrafficQuota()
		}
	}
}

func (a *app) sampleTrafficQuota() {
	if a.demo || a.loadTrafficQuota() != nil {
		return
	}
	interfaces := discoverMacNetworkInterfaces()
	services, _ := discoverMacNetworkServices()
	name := selectDJITrafficInterface(a.currentUSBDevice(), interfaces, services)
	if name == "" {
		return
	}
	counters, err := discoverMacInterfaceCounters()
	if err != nil {
		return
	}
	current, ok := counters[name]
	if !ok {
		return
	}
	now := time.Now()
	a.quotaMu.Lock()
	a.rollTrafficQuotaMonthLocked(now)
	if a.quota.LastInterface == name && current.RX >= a.quota.LastRawRX && current.TX >= a.quota.LastRawTX {
		a.quota.LocalRXBytes += current.RX - a.quota.LastRawRX
		a.quota.LocalTXBytes += current.TX - a.quota.LastRawTX
	}
	a.quota.LastInterface = name
	a.quota.LastRawRX = current.RX
	a.quota.LastRawTX = current.TX
	a.quota.UpdatedAt = now
	a.quotaMu.Unlock()
	a.persistTrafficQuota(false)
}

func selectDJICellularInterface(interfaces []macNetInterface, services []macNetworkService) string {
	for _, service := range services {
		if !service.Disabled && isDJICellularService(service) && isActiveMacInterface(service.Device, interfaces) {
			return service.Device
		}
	}
	return ""
}

func (a *app) loadTrafficQuota() error {
	a.quotaMu.Lock()
	defer a.quotaMu.Unlock()
	if a.quotaLoaded {
		return nil
	}
	now := time.Now()
	a.quota = newTrafficQuotaStore(now)
	if a.demo {
		a.quotaLoaded = true
		return nil
	}
	if a.quotaPath == "" {
		configDir, err := os.UserConfigDir()
		if err != nil {
			return fmt.Errorf("定位流量统计目录：%w", err)
		}
		a.quotaPath = filepath.Join(configDir, "DJOneHub", "traffic-quota.json")
	}
	data, err := os.ReadFile(a.quotaPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("读取流量统计：%w", err)
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &a.quota); err != nil {
			return fmt.Errorf("解析流量统计：%w", err)
		}
	}
	if a.quota.BaseQuotaBytes == 0 {
		a.quota.BaseQuotaBytes = defaultTrafficQuotaBytes
	}
	if a.quota.TrackingStartedAt.IsZero() {
		a.quota.TrackingStartedAt = now
	}
	a.rollTrafficQuotaMonthLocked(now)
	a.quotaLoaded = true
	return nil
}

func newTrafficQuotaStore(now time.Time) trafficQuotaStore {
	return trafficQuotaStore{
		Month: now.Format("2006-01"), BaseQuotaBytes: defaultTrafficQuotaBytes,
		TrackingStartedAt: now, UpdatedAt: now,
	}
}

func (a *app) rollTrafficQuotaMonthLocked(now time.Time) {
	month := now.Format("2006-01")
	if a.quota.Month == month {
		return
	}
	base := a.quota.BaseQuotaBytes
	command := a.quota.QueryCommand
	auto := a.quota.AutoQuery
	a.quota = newTrafficQuotaStore(now)
	a.quota.BaseQuotaBytes = base
	a.quota.QueryCommand = command
	a.quota.AutoQuery = auto
}

func (a *app) persistTrafficQuota(force bool) {
	if a.demo || a.loadTrafficQuota() != nil {
		return
	}
	a.quotaMu.Lock()
	if !force && time.Since(a.quotaLastPersist) < 2*time.Minute {
		a.quotaMu.Unlock()
		return
	}
	path := a.quotaPath
	data, err := json.Marshal(a.quota)
	if err == nil {
		a.quotaLastPersist = time.Now()
	}
	a.quotaMu.Unlock()
	if err != nil || os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err == nil {
		_ = os.Rename(temporary, path)
	}
}

func (a *app) trafficQuotaSnapshot() trafficQuotaResponse {
	_ = a.loadTrafficQuota()
	a.quotaMu.Lock()
	a.rollTrafficQuotaMonthLocked(time.Now())
	store := a.quota
	a.quotaMu.Unlock()
	local := store.LocalRXBytes + store.LocalTXBytes
	total := store.BaseQuotaBytes + store.RolloverBytes
	used := local
	remaining := subtractFloor(total, used)
	source := "local"
	partial := true
	if store.OperatorRemainingBytes != nil || store.OperatorUsedBytes != nil {
		delta := subtractFloor(local, store.CalibrationLocalBytes)
		// A manually entered used/remaining pair can be rounded independently by
		// the operator. It must not redefine the configured package total.
		if store.OperatorTotalBytes != nil && store.CalibrationSource != "manual" {
			total = *store.OperatorTotalBytes
		}
		if store.OperatorUsedBytes != nil {
			used = *store.OperatorUsedBytes + delta
		}
		if store.OperatorRemainingBytes != nil {
			remaining = subtractFloor(*store.OperatorRemainingBytes, delta)
		} else {
			remaining = subtractFloor(total, used)
		}
		source = store.CalibrationSource
		if source == "" {
			source = "operator_sms"
		}
		partial = store.OperatorUsedBytes == nil || store.OperatorRemainingBytes == nil
	}
	if remaining > total {
		total = remaining
	}
	response := trafficQuotaResponse{
		Month: store.Month, BaseQuotaBytes: store.BaseQuotaBytes, RolloverBytes: store.RolloverBytes,
		PlanTotalBytes: total, UsedBytes: used, RemainingBytes: remaining,
		LocalRXBytes: store.LocalRXBytes, LocalTXBytes: store.LocalTXBytes, LocalTrackedBytes: local,
		TrackingStartedAt: store.TrackingStartedAt, UpdatedAt: store.UpdatedAt,
		Source: source, PartialEstimate: partial,
		UsedKnown: store.OperatorUsedBytes != nil, RemainingKnown: store.OperatorRemainingBytes != nil,
		LastCalibrationAt:   store.LastCalibrationAt,
		LastCalibrationText: store.LastCalibrationText, QueryRecipient: "10099",
		QueryCommand: store.QueryCommand, AutoQuery: store.AutoQuery,
		QueryConfigured: strings.TrimSpace(store.QueryCommand) != "", LastQueryAt: store.LastQueryAt,
		LastQueryError: store.LastQueryError,
	}
	if !store.LastQueryAt.IsZero() {
		response.NextQueryAt = store.LastQueryAt.Add(trafficQuotaQueryPeriod)
	}
	return response
}

func (a *app) trafficQuotaStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.trafficQuotaSnapshot())
}

func (a *app) configureTrafficQuota(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BaseQuotaGB       *float64 `json:"base_quota_gb"`
		RolloverGB        *float64 `json:"rollover_gb"`
		QueryCommand      *string  `json:"query_command"`
		AutoQuery         *bool    `json:"auto_query"`
		ManualUsedGB      *float64 `json:"manual_used_gb"`
		ManualRemainingGB *float64 `json:"manual_remaining_gb"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := a.loadTrafficQuota(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.quotaMu.Lock()
	if body.BaseQuotaGB != nil {
		if *body.BaseQuotaGB < 1 || *body.BaseQuotaGB > 10000 {
			a.quotaMu.Unlock()
			writeError(w, http.StatusBadRequest, "基础套餐必须在 1 GB 到 10000 GB 之间")
			return
		}
		a.quota.BaseQuotaBytes = gibibytes(*body.BaseQuotaGB)
	}
	if body.RolloverGB != nil {
		if *body.RolloverGB < 0 || *body.RolloverGB > 10000 {
			a.quotaMu.Unlock()
			writeError(w, http.StatusBadRequest, "结转流量必须在 0 GB 到 10000 GB 之间")
			return
		}
		a.quota.RolloverBytes = gibibytes(*body.RolloverGB)
	}
	if body.QueryCommand != nil {
		command := strings.TrimSpace(*body.QueryCommand)
		if len([]rune(command)) > 20 {
			a.quotaMu.Unlock()
			writeError(w, http.StatusBadRequest, "查询指令不能超过 20 个字符")
			return
		}
		a.quota.QueryCommand = command
	}
	if body.AutoQuery != nil {
		if *body.AutoQuery && strings.TrimSpace(a.quota.QueryCommand) == "" {
			a.quotaMu.Unlock()
			writeError(w, http.StatusBadRequest, "启用每日查询前必须填写已验证的 10099 指令")
			return
		}
		a.quota.AutoQuery = *body.AutoQuery
	}
	manualChanged := body.ManualUsedGB != nil || body.ManualRemainingGB != nil
	if body.ManualUsedGB != nil {
		if *body.ManualUsedGB < 0 || *body.ManualUsedGB > 10000 {
			a.quotaMu.Unlock()
			writeError(w, http.StatusBadRequest, "当前已用流量必须在 0 GB 到 10000 GB 之间")
			return
		}
	}
	if body.ManualRemainingGB != nil {
		if *body.ManualRemainingGB < 0 || *body.ManualRemainingGB > 10000 {
			a.quotaMu.Unlock()
			writeError(w, http.StatusBadRequest, "当前剩余流量必须在 0 GB 到 10000 GB 之间")
			return
		}
	}
	if manualChanged {
		planTotal := a.quota.BaseQuotaBytes + a.quota.RolloverBytes
		a.quota.OperatorUsedBytes = nil
		a.quota.OperatorRemainingBytes = nil
		if body.ManualUsedGB != nil {
			a.quota.OperatorUsedBytes = uint64Pointer(gibibytes(*body.ManualUsedGB))
		}
		if body.ManualRemainingGB != nil {
			a.quota.OperatorRemainingBytes = uint64Pointer(gibibytes(*body.ManualRemainingGB))
		}
		if a.quota.OperatorUsedBytes == nil {
			used := subtractFloor(planTotal, *a.quota.OperatorRemainingBytes)
			a.quota.OperatorUsedBytes = uint64Pointer(used)
		}
		if a.quota.OperatorRemainingBytes == nil {
			remaining := subtractFloor(planTotal, *a.quota.OperatorUsedBytes)
			a.quota.OperatorRemainingBytes = uint64Pointer(remaining)
		}
		local := a.quota.LocalRXBytes + a.quota.LocalTXBytes
		a.quota.CalibrationLocalBytes = local
		a.quota.LastCalibrationAt = time.Now()
		a.quota.CalibrationSource = "manual"
		a.quota.LastCalibrationText = "手动校准"
		// Manual readings calibrate usage, while base quota plus rollover remains
		// the authoritative package total. Used and remaining values may be
		// rounded differently, so adding them can invent an extra gigabyte.
		a.quota.OperatorTotalBytes = nil
	}
	a.quota.UpdatedAt = time.Now()
	a.quotaMu.Unlock()
	a.persistTrafficQuota(true)
	writeJSON(w, http.StatusOK, a.trafficQuotaSnapshot())
}

func (a *app) queryTrafficQuota(w http.ResponseWriter, _ *http.Request) {
	if err := a.sendTrafficQuotaQuery(false); err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, errTrafficQuotaNotConfigured) {
			status = http.StatusConflict
		}
		if errors.Is(err, errTrafficQuotaRateLimited) {
			status = http.StatusTooManyRequests
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, a.trafficQuotaSnapshot())
}

var (
	errTrafficQuotaNotConfigured = errors.New("尚未配置已验证的 10099 流量查询指令")
	errTrafficQuotaRateLimited   = errors.New("今天已经查询过，24 小时内不会重复发送")
)

func (a *app) sendTrafficQuotaQuery(automatic bool) error {
	a.quotaQueryMu.Lock()
	defer a.quotaQueryMu.Unlock()
	if err := a.loadTrafficQuota(); err != nil {
		return err
	}
	a.quotaMu.Lock()
	command := strings.TrimSpace(a.quota.QueryCommand)
	lastQuery := a.quota.LastQueryAt
	autoEnabled := a.quota.AutoQuery
	a.quotaMu.Unlock()
	if command == "" || (automatic && !autoEnabled) {
		return errTrafficQuotaNotConfigured
	}
	if !lastQuery.IsZero() && time.Since(lastQuery) < trafficQuotaQueryPeriod {
		return errTrafficQuotaRateLimited
	}
	_, err := a.sendTextSMS("10099", command)
	a.quotaMu.Lock()
	if err != nil {
		a.quota.LastQueryError = err.Error()
	} else {
		a.quota.LastQueryAt = time.Now()
		a.quota.LastQueryError = ""
	}
	a.quotaMu.Unlock()
	a.persistTrafficQuota(true)
	return err
}

func (a *app) maybeAutoQueryTrafficQuota() {
	if a.demo {
		return
	}
	if err := a.loadTrafficQuota(); err != nil {
		return
	}
	a.quotaMu.Lock()
	due := a.quota.AutoQuery && strings.TrimSpace(a.quota.QueryCommand) != "" &&
		(a.quota.LastQueryAt.IsZero() || time.Since(a.quota.LastQueryAt) >= trafficQuotaQueryPeriod)
	a.quotaMu.Unlock()
	if due {
		go func() { _ = a.sendTrafficQuotaQuery(true) }()
	}
}

func (a *app) maybeCalibrateTrafficQuota(message receivedSMS) {
	calibration, ok := parseTrafficQuotaSMS(message.Sender, message.Content)
	if !ok || a.loadTrafficQuota() != nil {
		return
	}
	now := time.Now()
	if message.Timestamp.Before(time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())) {
		return
	}
	a.quotaMu.Lock()
	a.rollTrafficQuotaMonthLocked(now)
	local := a.quota.LocalRXBytes + a.quota.LocalTXBytes
	if calibration.Rollover != nil {
		a.quota.RolloverBytes = *calibration.Rollover
	}
	used := calibration.Used
	remaining := calibration.Remaining
	total := a.quota.BaseQuotaBytes + a.quota.RolloverBytes
	if calibration.Total != nil {
		total = *calibration.Total
		a.quota.OperatorTotalBytes = uint64Pointer(total)
	} else if remaining != nil && *remaining > total {
		total = *remaining
		a.quota.OperatorTotalBytes = uint64Pointer(total)
	}
	if used == nil && remaining != nil && total >= *remaining {
		value := total - *remaining
		used = &value
	}
	if remaining == nil && used != nil && total >= *used {
		value := total - *used
		remaining = &value
	}
	a.quota.OperatorUsedBytes = used
	a.quota.OperatorRemainingBytes = remaining
	a.quota.CalibrationLocalBytes = local
	a.quota.LastCalibrationAt = message.Timestamp
	a.quota.LastCalibrationText = strings.TrimSpace(message.Content)
	a.quota.CalibrationSource = "operator_sms"
	a.quota.UpdatedAt = time.Now()
	a.quotaMu.Unlock()
	a.persistTrafficQuota(true)
}

func parseTrafficQuotaSMS(sender, content string) (trafficQuotaCalibration, bool) {
	if !strings.Contains(strings.ReplaceAll(sender, " ", ""), "10099") || !strings.Contains(content, "流量") {
		return trafficQuotaCalibration{}, false
	}
	calibration := trafficQuotaCalibration{
		Total:     findTrafficAmount(content, `(?:总流量|流量总量|套餐(?:内)?(?:通用)?流量|套餐(?:内)?(?:包含|含有|共有|共计))`),
		Used:      findTrafficAmount(content, `(?:已使用|已用|使用量)`),
		Remaining: findTrafficAmount(content, `(?:剩余流量|流量剩余|剩余|余量|可用流量)`),
		Rollover:  findTrafficAmount(content, `(?:上月结余|上月结转|结转流量|结转)`),
	}
	return calibration, calibration.Used != nil || calibration.Remaining != nil
}

func findTrafficAmount(content, label string) *uint64 {
	re := regexp.MustCompile(`(?i)` + label + `[^0-9]{0,16}([0-9]+(?:\.[0-9]+)?)\s*(TB|T|GB|G|MB|M|KB|K)`)
	match := re.FindStringSubmatch(content)
	if len(match) != 3 {
		return nil
	}
	amount, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return nil
	}
	multiplier := float64(1)
	switch strings.ToUpper(match[2]) {
	case "TB", "T":
		multiplier = 1024 * 1024 * 1024 * 1024
	case "GB", "G":
		multiplier = 1024 * 1024 * 1024
	case "MB", "M":
		multiplier = 1024 * 1024
	case "KB", "K":
		multiplier = 1024
	}
	value := uint64(math.Round(amount * multiplier))
	return &value
}

func gibibytes(value float64) uint64 {
	return uint64(math.Round(value * 1024 * 1024 * 1024))
}

func subtractFloor(left, right uint64) uint64 {
	if right >= left {
		return 0
	}
	return left - right
}

func uint64Pointer(value uint64) *uint64 { return &value }
