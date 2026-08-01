package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/iniwex5/vohive/internal/modem"
)

const (
	cellularLabSampleInterval = 30 * time.Second
	cellularLabPingInterval   = 60 * time.Second
	cellularLabHistoryLimit   = 2880 // 24 hours at 30-second intervals
	cellularLabDefaultBytes   = 5_000_000
	cellularLabMaxBytes       = 20_000_000
)

type cellularLabSample struct {
	SampledAtMS       int64    `json:"sampled_at_ms"`
	Operator          string   `json:"operator,omitempty"`
	NetworkMode       string   `json:"network_mode,omitempty"`
	Duplex            string   `json:"duplex,omitempty"`
	Band              string   `json:"band,omitempty"`
	Channel           uint32   `json:"channel,omitempty"`
	MCC               string   `json:"mcc,omitempty"`
	MNC               string   `json:"mnc,omitempty"`
	CellID            string   `json:"cell_id,omitempty"`
	PCI               int      `json:"pci,omitempty"`
	TAC               string   `json:"tac,omitempty"`
	RSRP              *int     `json:"rsrp,omitempty"`
	RSRQ              *int     `json:"rsrq,omitempty"`
	SINR              *int     `json:"sinr,omitempty"`
	LatencyMS         *float64 `json:"latency_ms,omitempty"`
	PacketLossPercent *float64 `json:"packet_loss_percent,omitempty"`
	DownloadMbps      *float64 `json:"download_mbps,omitempty"`
	DownloadBytes     int64    `json:"download_bytes,omitempty"`
	RouteInterface    string   `json:"route_interface,omitempty"`
	CellularRoute     bool     `json:"cellular_route"`
	Error             string   `json:"error,omitempty"`
}

type cellularLabHistoryResponse struct {
	Samples               []cellularLabSample `json:"samples"`
	Latest                *cellularLabSample  `json:"latest,omitempty"`
	SampleIntervalSeconds int                 `json:"sample_interval_seconds"`
	SpeedTestDefaultBytes int                 `json:"speed_test_default_bytes"`
	HistoryHours          int                 `json:"history_hours"`
}

func intPointer(value int) *int { return &value }

func (a *app) startCellularLabSampler(ctx context.Context) {
	_, _ = a.captureCellularLabSample(false)
	ticker := time.NewTicker(cellularLabSampleInterval)
	defer ticker.Stop()
	lastPing := time.Time{}
	for {
		select {
		case <-ctx.Done():
			a.persistCellularLab(true)
			return
		case now := <-ticker.C:
			withPing := now.Sub(lastPing) >= cellularLabPingInterval
			if withPing {
				lastPing = now
			}
			_, _ = a.captureCellularLabSample(withPing)
		}
	}
}

func (a *app) cellularLabHistory(w http.ResponseWriter, _ *http.Request) {
	if err := a.loadCellularLab(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.labMu.Lock()
	samples := append([]cellularLabSample(nil), a.labSamples...)
	a.labMu.Unlock()
	response := cellularLabHistoryResponse{
		Samples:               samples,
		SampleIntervalSeconds: int(cellularLabSampleInterval.Seconds()),
		SpeedTestDefaultBytes: cellularLabDefaultBytes,
		HistoryHours:          24,
	}
	if len(samples) > 0 {
		latest := samples[len(samples)-1]
		response.Latest = &latest
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *app) cellularLabSampleNow(w http.ResponseWriter, _ *http.Request) {
	sample, err := a.captureCellularLabSample(true)
	if err != nil && sample.Error == "" {
		sample.Error = err.Error()
	}
	writeJSON(w, http.StatusOK, sample)
}

func (a *app) captureCellularLabSample(withPing bool) (cellularLabSample, error) {
	a.labSampleMu.Lock()
	defer a.labSampleMu.Unlock()

	sample := cellularLabSample{SampledAtMS: time.Now().UnixMilli()}
	interfaces := discoverMacNetworkInterfaces()
	route := discoverMacDefaultRoute()
	cellularInterface := selectUSBTrafficInterface(interfaces, route)
	sample.RouteInterface = route.Interface
	sample.CellularRoute = cellularInterface != "" && route.Interface == cellularInterface

	var cell modem.ServingCellLTEInfo
	var cellOK bool
	if a.demo {
		cell = modem.ServingCellLTEInfo{RSRP: -91, RSRQ: -9, SINR: 18, Duplex: "FDD", Band: "LTE BAND 3", Channel: 1650, MCC: "460", MNC: "15", CellID: "12AB34", PCI: 132, TAC: "51D7"}
		cellOK = true
		sample.Operator = "中国广电"
		sample.NetworkMode = "LTE"
	} else {
		resp, err := a.runATCommand(`AT+QENG="servingcell"`, 5*time.Second)
		if err != nil {
			sample.Error = "无线指标读取失败：" + err.Error()
		} else {
			cell, cellOK = modem.ParseServingCellLTEInfo(resp)
			if !cellOK {
				sample.Error = "模块未返回可解析的 LTE 服务小区"
			}
		}
		statusResp, statusErr := a.runATCommand("AT+COPS?", 3*time.Second)
		if statusErr == nil {
			sample.Operator = parseUSBATOperator(statusResp)
		}
	}
	if cellOK {
		sample.NetworkMode = "LTE"
		sample.Duplex = cell.Duplex
		sample.Band = cell.Band
		sample.Channel = cell.Channel
		sample.MCC = cell.MCC
		sample.MNC = cell.MNC
		sample.CellID = cell.CellID
		sample.PCI = cell.PCI
		sample.TAC = cell.TAC
		sample.RSRP = intPointer(cell.RSRP)
		sample.RSRQ = intPointer(cell.RSRQ)
		sample.SINR = intPointer(cell.SINR)
	}
	if withPing && sample.CellularRoute {
		latency, loss, err := measureCellularPing()
		if err != nil {
			sample.Error = appendLabError(sample.Error, "连通性测量失败："+err.Error())
		} else {
			sample.LatencyMS = &latency
			sample.PacketLossPercent = &loss
		}
	}

	a.appendCellularLabSample(sample)
	return sample, nil
}

func appendLabError(existing, next string) string {
	if existing == "" {
		return next
	}
	return existing + "；" + next
}

func measureCellularPing() (float64, float64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ping", "-n", "-c", "4", "-W", "1000", "1.1.1.1").CombinedOutput()
	text := string(out)
	lossMatch := regexp.MustCompile(`([0-9.]+)% packet loss`).FindStringSubmatch(text)
	rttMatch := regexp.MustCompile(`(?:round-trip|rtt)[^=]*=\s*[0-9.]+/([0-9.]+)/`).FindStringSubmatch(text)
	if len(lossMatch) != 2 {
		if err != nil {
			return 0, 0, fmt.Errorf("ping: %w", err)
		}
		return 0, 0, errors.New("无法解析丢包率")
	}
	loss, _ := strconv.ParseFloat(lossMatch[1], 64)
	if len(rttMatch) != 2 {
		if loss >= 100 {
			return 0, loss, nil
		}
		return 0, loss, errors.New("无法解析平均延迟")
	}
	latency, _ := strconv.ParseFloat(rttMatch[1], 64)
	return latency, loss, nil
}

func (a *app) cellularLabSpeedTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Bytes int `json:"bytes"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Bytes == 0 {
		body.Bytes = cellularLabDefaultBytes
	}
	if body.Bytes < 1_000_000 || body.Bytes > cellularLabMaxBytes {
		writeError(w, http.StatusBadRequest, "测速流量必须在 1 MB 到 20 MB 之间")
		return
	}
	interfaces := discoverMacNetworkInterfaces()
	route := discoverMacDefaultRoute()
	cellularInterface := selectUSBTrafficInterface(interfaces, route)
	if cellularInterface == "" || route.Interface != cellularInterface {
		writeError(w, http.StatusConflict, fmt.Sprintf("当前默认出口是 %s，不是 4G USB 网卡；已取消测速以避免记录 Wi-Fi/VPN 数据", route.Interface))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	requestURL := fmt.Sprintf("https://speed.cloudflare.com/__down?bytes=%d", body.Bytes)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	req.Header.Set("Cache-Control", "no-cache")
	started := time.Now()
	resp, err := (&http.Client{Timeout: 45 * time.Second}).Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "测速请求失败："+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeError(w, http.StatusBadGateway, "测速服务返回 "+resp.Status)
		return
	}
	readBytes, err := io.Copy(io.Discard, io.LimitReader(resp.Body, int64(body.Bytes)+1))
	if err != nil {
		writeError(w, http.StatusBadGateway, "测速数据读取失败："+err.Error())
		return
	}
	elapsed := time.Since(started).Seconds()
	mbps := float64(readBytes*8) / elapsed / 1_000_000
	sample, _ := a.captureCellularLabSample(true)
	sample.DownloadMbps = &mbps
	sample.DownloadBytes = readBytes
	a.labMu.Lock()
	if len(a.labSamples) > 0 {
		a.labSamples[len(a.labSamples)-1] = sample
	}
	a.labMu.Unlock()
	a.persistCellularLab(true)
	writeJSON(w, http.StatusOK, sample)
}

func (a *app) appendCellularLabSample(sample cellularLabSample) {
	_ = a.loadCellularLab()
	a.labMu.Lock()
	a.labSamples = append(a.labSamples, sample)
	if len(a.labSamples) > cellularLabHistoryLimit {
		a.labSamples = append([]cellularLabSample(nil), a.labSamples[len(a.labSamples)-cellularLabHistoryLimit:]...)
	}
	shouldPersist := time.Since(a.labLastPersist) >= 2*time.Minute
	a.labMu.Unlock()
	if shouldPersist {
		a.persistCellularLab(false)
	}
}

func (a *app) loadCellularLab() error {
	a.labMu.Lock()
	defer a.labMu.Unlock()
	if a.labLoaded {
		return nil
	}
	if a.demo {
		a.labSamples = []cellularLabSample{}
		a.labLoaded = true
		return nil
	}
	if a.labPath == "" {
		configDir, err := os.UserConfigDir()
		if err != nil {
			return fmt.Errorf("定位蜂窝实验数据目录：%w", err)
		}
		a.labPath = filepath.Join(configDir, "DJOneHub", "cellular-lab-history.json")
	}
	data, err := os.ReadFile(a.labPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("读取蜂窝实验历史：%w", err)
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &a.labSamples); err != nil {
			return fmt.Errorf("解析蜂窝实验历史：%w", err)
		}
	}
	if len(a.labSamples) > cellularLabHistoryLimit {
		a.labSamples = a.labSamples[len(a.labSamples)-cellularLabHistoryLimit:]
	}
	a.labLoaded = true
	return nil
}

func (a *app) persistCellularLab(force bool) {
	if a.demo {
		return
	}
	if err := a.loadCellularLab(); err != nil {
		return
	}
	a.labMu.Lock()
	if !force && time.Since(a.labLastPersist) < 2*time.Minute {
		a.labMu.Unlock()
		return
	}
	path := a.labPath
	samples := append([]cellularLabSample(nil), a.labSamples...)
	a.labLastPersist = time.Now()
	a.labMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	data, err := json.Marshal(samples)
	if err != nil {
		return
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err == nil {
		_ = os.Rename(temporary, path)
	}
}
