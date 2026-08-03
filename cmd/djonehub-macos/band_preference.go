package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	bandModeAuto      = "auto"
	bandModePreferred = "preferred"
)

var (
	bandRegistrationTimeout = 75 * time.Second
	bandRegistrationPoll    = 3 * time.Second
	bandMaskResponsePattern = regexp.MustCompile(`(?i)\+QCFG:\s*"band"\s*,\s*(0x[0-9a-f]+|[0-9a-f]+)\s*,\s*(0x[0-9a-f]+|[0-9a-f]+)\s*,\s*(0x[0-9a-f]+|[0-9a-f]+)`)
	bandRegistrationPattern = regexp.MustCompile(`(?i)\+CEREG:\s*(?:\d+\s*,\s*)?([0-9]+)`)
	bandIMEIPattern         = regexp.MustCompile(`(?m)^\s*([0-9]{14,17})\s*$`)
)

type bandMaskConfig struct {
	GW  string `json:"gw"`
	LTE string `json:"lte"`
	TDS string `json:"tds"`
}

type bandPreferenceStore struct {
	DeviceKey     string         `json:"device_key"`
	Original      bandMaskConfig `json:"original"`
	Mode          string         `json:"mode"`
	SelectedBands []int          `json:"selected_bands,omitempty"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

type bandOperationState struct {
	InProgress   bool      `json:"in_progress"`
	Phase        string    `json:"phase,omitempty"`
	Message      string    `json:"message,omitempty"`
	TargetBands  []int     `json:"target_bands,omitempty"`
	StartedAt    time.Time `json:"started_at,omitempty"`
	CompletedAt  time.Time `json:"completed_at,omitempty"`
	RolledBack   bool      `json:"rolled_back,omitempty"`
	LastError    string    `json:"last_error,omitempty"`
	PreviousMask string    `json:"-"`
}

type bandPreferenceResponse struct {
	Mode              string             `json:"mode"`
	Current           bandMaskConfig     `json:"current"`
	Original          *bandMaskConfig    `json:"original,omitempty"`
	SupportedBands    []int              `json:"supported_bands"`
	EnabledBands      []int              `json:"enabled_bands"`
	SelectedBands     []int              `json:"selected_bands"`
	ServingBand       int                `json:"serving_band,omitempty"`
	CanRestore        bool               `json:"can_restore"`
	RegistrationLimit int                `json:"registration_timeout_seconds"`
	Operation         bandOperationState `json:"operation"`
}

type configureBandPreferenceRequest struct {
	Mode  string `json:"mode"`
	Bands []int  `json:"bands"`
}

func normalizeBandHex(value string) (string, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.TrimPrefix(value, "0x")
	if value == "" {
		return "", errors.New("empty band mask")
	}
	mask := new(big.Int)
	if _, ok := mask.SetString(value, 16); !ok || mask.Sign() < 0 {
		return "", fmt.Errorf("invalid band mask %q", value)
	}
	return "0x" + mask.Text(16), nil
}

func parseBandMaskResponse(response string) (bandMaskConfig, error) {
	match := bandMaskResponsePattern.FindStringSubmatch(response)
	if len(match) != 4 {
		return bandMaskConfig{}, errors.New("模块未返回可解析的频段配置")
	}
	values := make([]string, 3)
	for index := range values {
		normalized, err := normalizeBandHex(match[index+1])
		if err != nil {
			return bandMaskConfig{}, err
		}
		values[index] = normalized
	}
	return bandMaskConfig{GW: values[0], LTE: values[1], TDS: values[2]}, nil
}

func bandMaskBands(value string) ([]int, error) {
	normalized, err := normalizeBandHex(value)
	if err != nil {
		return nil, err
	}
	mask := new(big.Int)
	mask.SetString(strings.TrimPrefix(normalized, "0x"), 16)
	bands := make([]int, 0, mask.BitLen())
	for bit := 0; bit < mask.BitLen(); bit++ {
		if mask.Bit(bit) == 1 {
			bands = append(bands, bit+1)
		}
	}
	return bands, nil
}

func encodeLTEBandMask(bands []int) (string, error) {
	if len(bands) == 0 {
		return "", errors.New("至少选择一个 LTE 频段")
	}
	mask := new(big.Int)
	seen := make(map[int]struct{}, len(bands))
	for _, band := range bands {
		if band < 1 || band > 128 {
			return "", fmt.Errorf("LTE B%d 超出可配置范围", band)
		}
		if _, exists := seen[band]; exists {
			continue
		}
		seen[band] = struct{}{}
		mask.SetBit(mask, band-1, 1)
	}
	return "0x" + mask.Text(16), nil
}

func validateLTEBands(bands []int, allowedMask string) ([]int, string, error) {
	mask, err := encodeLTEBandMask(bands)
	if err != nil {
		return nil, "", err
	}
	allowed, err := bandMaskBands(allowedMask)
	if err != nil {
		return nil, "", err
	}
	allowedSet := make(map[int]struct{}, len(allowed))
	for _, band := range allowed {
		allowedSet[band] = struct{}{}
	}
	selectedSet := make(map[int]struct{}, len(bands))
	for _, band := range bands {
		if _, ok := allowedSet[band]; !ok {
			return nil, "", fmt.Errorf("当前模块原始配置不包含 LTE B%d", band)
		}
		selectedSet[band] = struct{}{}
	}
	selected := make([]int, 0, len(selectedSet))
	for band := range selectedSet {
		selected = append(selected, band)
	}
	sort.Ints(selected)
	return selected, mask, nil
}

func lteBandMaskEqual(left, right bandMaskConfig) bool {
	return strings.EqualFold(left.LTE, right.LTE)
}

func buildBandConfigCommand(config bandMaskConfig) string {
	// QDC507 exposes three masks in the read response but its current firmware
	// rejects the generic three-mask write form. A zero GW mask means "leave
	// unchanged"; omitting TDS changes only the LTE allow-list.
	return fmt.Sprintf(`AT+QCFG="band",0,%s`, strings.TrimPrefix(config.LTE, "0x"))
}

func parseBandRegistration(response string) bool {
	match := bandRegistrationPattern.FindStringSubmatch(response)
	if len(match) != 2 {
		return false
	}
	return match[1] == "1" || match[1] == "5"
}

func parsePacketAttached(response string) bool {
	return regexp.MustCompile(`(?i)\+CGATT:\s*1`).MatchString(response)
}

func parseServingBand(response string) int {
	match := regexp.MustCompile(`(?i)LTE\s+BAND\s+(\d+)`).FindStringSubmatch(response)
	if len(match) != 2 {
		return 0
	}
	band, _ := strconv.Atoi(match[1])
	return band
}

func bandListContains(bands []int, target int) bool {
	for _, band := range bands {
		if band == target {
			return true
		}
	}
	return false
}

func (a *app) loadBandStore() error {
	a.bandMu.Lock()
	defer a.bandMu.Unlock()
	if a.bandLoaded {
		return nil
	}
	a.bandStore.Mode = bandModeAuto
	if a.demo {
		a.bandLoaded = true
		return nil
	}
	if a.bandPath == "" {
		configDir, err := os.UserConfigDir()
		if err != nil {
			return fmt.Errorf("定位频段偏好目录：%w", err)
		}
		a.bandPath = filepath.Join(configDir, "DJOneHub", "band-preference.json")
	}
	data, err := os.ReadFile(a.bandPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("读取频段偏好：%w", err)
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &a.bandStore); err != nil {
			return fmt.Errorf("解析频段偏好：%w", err)
		}
	}
	if a.bandStore.Mode == "" {
		a.bandStore.Mode = bandModeAuto
	}
	a.bandLoaded = true
	return nil
}

func (a *app) persistBandStore() error {
	if a.demo {
		return nil
	}
	a.bandMu.Lock()
	path := a.bandPath
	store := a.bandStore
	a.bandMu.Unlock()
	if path == "" {
		return errors.New("频段偏好文件尚未初始化")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(store)
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func (a *app) readBandConfig() (bandMaskConfig, error) {
	if a.demo {
		_ = a.loadBandStore()
		a.bandMu.Lock()
		store := a.bandStore
		a.bandMu.Unlock()
		original := bandMaskConfig{GW: "0xbff", LTE: "0x180080000c5", TDS: "0x0"}
		if store.Mode == bandModePreferred && len(store.SelectedBands) > 0 {
			mask, _ := encodeLTEBandMask(store.SelectedBands)
			original.LTE = mask
		}
		return original, nil
	}
	response, err := a.runATCommand(`AT+QCFG="band"`, 5*time.Second)
	if err != nil {
		return bandMaskConfig{}, err
	}
	return parseBandMaskResponse(response)
}

func (a *app) readBandDeviceKey() (string, error) {
	if a.demo {
		return "demo-device", nil
	}
	response, err := a.runATCommand("AT+CGSN", 4*time.Second)
	if err != nil {
		return "", err
	}
	match := bandIMEIPattern.FindStringSubmatch(response)
	if len(match) != 2 {
		return "", errors.New("无法确认当前模块身份")
	}
	digest := sha256.Sum256([]byte(match[1]))
	return fmt.Sprintf("%x", digest[:12]), nil
}

func (a *app) readServingBand() int {
	if a.demo {
		a.bandMu.Lock()
		defer a.bandMu.Unlock()
		if len(a.bandStore.SelectedBands) > 0 {
			return a.bandStore.SelectedBands[0]
		}
		return 3
	}
	response, err := a.runATCommand("AT+QNWINFO", 4*time.Second)
	if err != nil {
		return 0
	}
	return parseServingBand(response)
}

func (a *app) bandPreferenceStatus(w http.ResponseWriter, _ *http.Request) {
	response, err := a.currentBandPreferenceResponse()
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *app) currentBandPreferenceResponse() (bandPreferenceResponse, error) {
	if err := a.loadBandStore(); err != nil {
		return bandPreferenceResponse{}, err
	}
	current, err := a.readBandConfig()
	if err != nil {
		return bandPreferenceResponse{}, fmt.Errorf("读取模块频段配置：%w", err)
	}
	deviceKey, err := a.readBandDeviceKey()
	if err != nil {
		return bandPreferenceResponse{}, err
	}
	a.bandMu.Lock()
	store := a.bandStore
	operation := a.bandOperation
	a.bandMu.Unlock()

	hasBackup := store.DeviceKey == deviceKey && store.Original.LTE != ""
	baseline := current
	if hasBackup {
		baseline = store.Original
	}
	supported, err := bandMaskBands(baseline.LTE)
	if err != nil {
		return bandPreferenceResponse{}, err
	}
	enabled, err := bandMaskBands(current.LTE)
	if err != nil {
		return bandPreferenceResponse{}, err
	}
	mode := "custom"
	selected := append([]int(nil), enabled...)
	if hasBackup && lteBandMaskEqual(current, store.Original) {
		mode = bandModeAuto
		selected = nil
	} else if hasBackup && store.Mode == bandModePreferred {
		expected, maskErr := encodeLTEBandMask(store.SelectedBands)
		if maskErr == nil && strings.EqualFold(expected, current.LTE) {
			mode = bandModePreferred
			selected = append([]int(nil), store.SelectedBands...)
		}
	} else if !hasBackup {
		mode = bandModeAuto
		selected = nil
	}

	response := bandPreferenceResponse{
		Mode:              mode,
		Current:           current,
		SupportedBands:    supported,
		EnabledBands:      enabled,
		SelectedBands:     selected,
		ServingBand:       a.readServingBand(),
		CanRestore:        hasBackup,
		RegistrationLimit: int(bandRegistrationTimeout.Seconds()),
		Operation:         operation,
	}
	if hasBackup {
		original := store.Original
		response.Original = &original
	}
	return response, nil
}

func (a *app) configureBandPreference(w http.ResponseWriter, r *http.Request) {
	var body configureBandPreferenceRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	body.Mode = strings.ToLower(strings.TrimSpace(body.Mode))
	if body.Mode != bandModeAuto && body.Mode != bandModePreferred {
		writeError(w, http.StatusBadRequest, "mode 必须是 auto 或 preferred")
		return
	}

	a.bandApplyMu.Lock()
	defer a.bandApplyMu.Unlock()
	a.bandMu.Lock()
	busy := a.bandOperation.InProgress
	a.bandMu.Unlock()
	if busy {
		writeError(w, http.StatusConflict, "已有频段切换正在执行")
		return
	}
	if err := a.loadBandStore(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	current, err := a.readBandConfig()
	if err != nil {
		writeError(w, http.StatusBadGateway, "读取当前频段配置："+err.Error())
		return
	}
	deviceKey, err := a.readBandDeviceKey()
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	a.bandMu.Lock()
	if a.bandStore.DeviceKey != deviceKey || a.bandStore.Original.LTE == "" {
		a.bandStore = bandPreferenceStore{
			DeviceKey: deviceKey,
			Original:  current,
			Mode:      bandModeAuto,
			UpdatedAt: time.Now(),
		}
	}
	store := a.bandStore
	a.bandMu.Unlock()
	if err := a.persistBandStore(); err != nil {
		writeError(w, http.StatusInternalServerError, "保存原始频段配置："+err.Error())
		return
	}

	target := current
	selected := []int(nil)
	if body.Mode == bandModeAuto {
		target = store.Original
	} else {
		var mask string
		selected, mask, err = validateLTEBands(body.Bands, store.Original.LTE)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		target.LTE = mask
	}
	if lteBandMaskEqual(current, target) {
		a.bandMu.Lock()
		a.bandStore.Mode = body.Mode
		a.bandStore.SelectedBands = append([]int(nil), selected...)
		a.bandStore.UpdatedAt = time.Now()
		a.bandOperation = bandOperationState{Phase: "complete", Message: "频段配置无需变更", TargetBands: selected, CompletedAt: time.Now()}
		a.bandMu.Unlock()
		_ = a.persistBandStore()
		response, responseErr := a.currentBandPreferenceResponse()
		if responseErr != nil {
			writeError(w, http.StatusBadGateway, responseErr.Error())
			return
		}
		writeJSON(w, http.StatusOK, response)
		return
	}

	operation := bandOperationState{
		InProgress:   true,
		Phase:        "applying",
		Message:      "正在写入频段配置",
		TargetBands:  append([]int(nil), selected...),
		StartedAt:    time.Now(),
		PreviousMask: current.LTE,
	}
	a.bandMu.Lock()
	a.bandOperation = operation
	a.bandMu.Unlock()
	go a.runBandPreferenceOperation(body.Mode, selected, current, target)
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "operation": operation})
}

func (a *app) runBandPreferenceOperation(mode string, selected []int, previous, target bandMaskConfig) {
	if a.demo {
		a.completeBandPreference(mode, selected, "演示频段配置已应用")
		return
	}
	command := buildBandConfigCommand(target)
	response, err := a.runATCommand(command, 10*time.Second)
	if err == nil && (!strings.Contains(strings.ToUpper(response), "OK") || strings.Contains(strings.ToUpper(response), "ERROR")) {
		err = fmt.Errorf("模块拒绝频段配置：%s", strings.TrimSpace(response))
	}
	if err == nil {
		a.updateBandOperation("verifying", "频段已写入，正在等待网络恢复")
		err = a.waitForBandRegistration(target)
	}
	if err == nil {
		a.completeBandPreference(mode, selected, "频段配置已生效，蜂窝数据已恢复")
		return
	}

	rollbackErr := a.applyBandMask(previous)
	message := "频段切换失败，已恢复到修改前配置"
	if rollbackErr != nil {
		message = "频段切换失败，自动恢复也未成功，请使用恢复自动选频"
		err = fmt.Errorf("%v；恢复失败：%w", err, rollbackErr)
	}
	a.bandMu.Lock()
	a.bandOperation.InProgress = false
	a.bandOperation.Phase = "failed"
	a.bandOperation.Message = message
	a.bandOperation.CompletedAt = time.Now()
	a.bandOperation.RolledBack = rollbackErr == nil
	a.bandOperation.LastError = err.Error()
	a.bandMu.Unlock()
}

func (a *app) applyBandMask(config bandMaskConfig) error {
	response, err := a.runATCommand(buildBandConfigCommand(config), 10*time.Second)
	if err != nil {
		return err
	}
	upper := strings.ToUpper(response)
	if !strings.Contains(upper, "OK") || strings.Contains(upper, "ERROR") {
		return fmt.Errorf("模块返回：%s", strings.TrimSpace(response))
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		current, readErr := a.readBandConfig()
		if readErr == nil && strings.EqualFold(current.LTE, config.LTE) {
			return nil
		}
		time.Sleep(time.Second)
	}
	return errors.New("模块已接受指令，但回读掩码未确认生效")
}

func (a *app) waitForBandRegistration(target bandMaskConfig) error {
	targetBands, err := bandMaskBands(target.LTE)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(bandRegistrationTimeout)
	var lastState string
	for time.Now().Before(deadline) {
		current, configErr := a.readBandConfig()
		registration, registrationErr := a.runATCommand("AT+CEREG?", 4*time.Second)
		attached, attachedErr := a.runATCommand("AT+CGATT?", 4*time.Second)
		network, networkErr := a.runATCommand("AT+QNWINFO", 4*time.Second)
		servingBand := parseServingBand(network)
		lastState = fmt.Sprintf("配置=%v，注册=%v，数据附着=%v，驻留=B%d", configErr, registrationErr, attachedErr, servingBand)
		if configErr == nil && strings.EqualFold(current.LTE, target.LTE) &&
			registrationErr == nil && parseBandRegistration(registration) &&
			attachedErr == nil && parsePacketAttached(attached) &&
			networkErr == nil && servingBand > 0 && bandListContains(targetBands, servingBand) {
			return nil
		}
		time.Sleep(bandRegistrationPoll)
	}
	return fmt.Errorf("%d 秒内未恢复可用网络（%s）", int(bandRegistrationTimeout.Seconds()), lastState)
}

func (a *app) updateBandOperation(phase, message string) {
	a.bandMu.Lock()
	a.bandOperation.Phase = phase
	a.bandOperation.Message = message
	a.bandMu.Unlock()
}

func (a *app) completeBandPreference(mode string, selected []int, message string) {
	a.bandMu.Lock()
	a.bandStore.Mode = mode
	a.bandStore.SelectedBands = append([]int(nil), selected...)
	a.bandStore.UpdatedAt = time.Now()
	a.bandOperation.InProgress = false
	a.bandOperation.Phase = "complete"
	a.bandOperation.Message = message
	a.bandOperation.CompletedAt = time.Now()
	a.bandOperation.LastError = ""
	a.bandOperation.RolledBack = false
	a.bandMu.Unlock()
	_ = a.persistBandStore()
}
