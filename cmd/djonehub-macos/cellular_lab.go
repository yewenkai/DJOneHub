package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
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
	cellularLabWebReadLimit   = 32 * 1024
)

type cellularWebProbeTarget struct {
	Name string
	URL  string
}

var cellularWebProbeTargets = []cellularWebProbeTarget{
	{Name: "百度", URL: "https://www.baidu.com/"},
	{Name: "腾讯云", URL: "https://cloud.tencent.com/"},
	{Name: "京东", URL: "https://www.jd.com/"},
}

type cellularWebProbeTargetResult struct {
	Name       string  `json:"name"`
	URL        string  `json:"url"`
	ResolvedIP string  `json:"resolved_ip,omitempty"`
	StatusCode int     `json:"status_code,omitempty"`
	BytesRead  int64   `json:"bytes_read,omitempty"`
	DNSMS      float64 `json:"dns_ms,omitempty"`
	ConnectMS  float64 `json:"connect_ms,omitempty"`
	TLSMS      float64 `json:"tls_ms,omitempty"`
	TTFBMS     float64 `json:"ttfb_ms,omitempty"`
	TotalMS    float64 `json:"total_ms,omitempty"`
	OK         bool    `json:"ok"`
	Error      string  `json:"error,omitempty"`
}

type cellularWebProbeSummary struct {
	SuccessCount   int                            `json:"success_count"`
	TargetCount    int                            `json:"target_count"`
	SuccessPercent float64                        `json:"success_percent"`
	MedianDNSMS    float64                        `json:"median_dns_ms,omitempty"`
	MedianConnect  float64                        `json:"median_connect_ms,omitempty"`
	MedianTLSMS    float64                        `json:"median_tls_ms,omitempty"`
	MedianTTFBMS   float64                        `json:"median_ttfb_ms,omitempty"`
	MedianTotalMS  float64                        `json:"median_total_ms,omitempty"`
	BytesRead      int64                          `json:"bytes_read"`
	Interface      string                         `json:"interface"`
	SourceIP       string                         `json:"source_ip"`
	Results        []cellularWebProbeTargetResult `json:"results"`
}

type cellularLabSample struct {
	SampledAtMS       int64                    `json:"sampled_at_ms"`
	Operator          string                   `json:"operator,omitempty"`
	NetworkMode       string                   `json:"network_mode,omitempty"`
	Duplex            string                   `json:"duplex,omitempty"`
	Band              string                   `json:"band,omitempty"`
	Channel           uint32                   `json:"channel,omitempty"`
	MCC               string                   `json:"mcc,omitempty"`
	MNC               string                   `json:"mnc,omitempty"`
	CellID            string                   `json:"cell_id,omitempty"`
	PCI               int                      `json:"pci,omitempty"`
	TAC               string                   `json:"tac,omitempty"`
	RSRP              *int                     `json:"rsrp,omitempty"`
	RSRQ              *int                     `json:"rsrq,omitempty"`
	SINR              *int                     `json:"sinr,omitempty"`
	LatencyMS         *float64                 `json:"latency_ms,omitempty"`
	PacketLossPercent *float64                 `json:"packet_loss_percent,omitempty"`
	DownloadMbps      *float64                 `json:"download_mbps,omitempty"`
	DownloadBytes     int64                    `json:"download_bytes,omitempty"`
	WebSuccessPercent *float64                 `json:"web_success_percent,omitempty"`
	WebTTFBMS         *float64                 `json:"web_ttfb_ms,omitempty"`
	WebTotalMS        *float64                 `json:"web_total_ms,omitempty"`
	WebProbe          *cellularWebProbeSummary `json:"web_probe,omitempty"`
	RouteInterface    string                   `json:"route_interface,omitempty"`
	CellularRoute     bool                     `json:"cellular_route"`
	Error             string                   `json:"error,omitempty"`
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
	if a.demo {
		sample.RouteInterface = "en19"
		sample.CellularRoute = true
	} else {
		services, _ := discoverMacNetworkServices()
		cellularInterface := selectDJITrafficInterface(a.currentUSBDevice(), interfaces, services)
		sample.RouteInterface = route.Interface
		sample.CellularRoute = cellularInterface != "" && route.Interface == cellularInterface
	}

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
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ping", "-n", "-c", "10", "-W", "1000", "223.5.5.5").CombinedOutput()
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

func (a *app) cellularLabWebTest(w http.ResponseWriter, r *http.Request) {
	a.labTestMu.Lock()
	defer a.labTestMu.Unlock()
	if a.demo {
		summary := demoCellularWebProbeSummary()
		sample, _ := a.captureCellularLabSample(false)
		sample.WebProbe = &summary
		sample.WebSuccessPercent = floatPointer(summary.SuccessPercent)
		sample.WebTTFBMS = floatPointer(summary.MedianTTFBMS)
		sample.WebTotalMS = floatPointer(summary.MedianTotalMS)
		a.replaceLastCellularLabSample(sample)
		writeJSON(w, http.StatusOK, sample)
		return
	}

	selection, err := currentCellularRouteSelection()
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	summary := runCellularWebProbe(ctx, selection)

	sample, _ := a.captureCellularLabSample(false)
	sample.RouteInterface = selection.Interface
	sample.CellularRoute = true
	sample.WebProbe = &summary
	sample.WebSuccessPercent = floatPointer(summary.SuccessPercent)
	if summary.SuccessCount > 0 {
		sample.WebTTFBMS = floatPointer(summary.MedianTTFBMS)
		sample.WebTotalMS = floatPointer(summary.MedianTotalMS)
	}
	a.replaceLastCellularLabSample(sample)
	a.persistCellularLab(true)
	writeJSON(w, http.StatusOK, sample)
}

func demoCellularWebProbeSummary() cellularWebProbeSummary {
	results := []cellularWebProbeTargetResult{
		{Name: "百度", URL: "https://www.baidu.com/", ResolvedIP: "110.242.68.3", StatusCode: 200, BytesRead: cellularLabWebReadLimit, DNSMS: 18, ConnectMS: 46, TLSMS: 63, TTFBMS: 156, TotalMS: 238, OK: true},
		{Name: "腾讯云", URL: "https://cloud.tencent.com/", ResolvedIP: "43.143.255.47", StatusCode: 200, BytesRead: cellularLabWebReadLimit, DNSMS: 22, ConnectMS: 51, TLSMS: 71, TTFBMS: 184, TotalMS: 291, OK: true},
		{Name: "京东", URL: "https://www.jd.com/", ResolvedIP: "111.13.149.108", StatusCode: 200, BytesRead: cellularLabWebReadLimit, DNSMS: 20, ConnectMS: 49, TLSMS: 68, TTFBMS: 172, TotalMS: 276, OK: true},
	}
	return cellularWebProbeSummary{
		SuccessCount:   3,
		TargetCount:    3,
		SuccessPercent: 100,
		MedianDNSMS:    20,
		MedianConnect:  49,
		MedianTLSMS:    68,
		MedianTTFBMS:   172,
		MedianTotalMS:  276,
		BytesRead:      cellularLabWebReadLimit * 3,
		Interface:      "en19",
		SourceIP:       "192.168.225.21",
		Results:        results,
	}
}

type cellularRouteSelection struct {
	Interface string
	SourceIP  string
}

func currentCellularRouteSelection() (cellularRouteSelection, error) {
	interfaces := discoverMacNetworkInterfaces()
	route := discoverMacDefaultRoute()
	services, _ := discoverMacNetworkServices()
	cellularInterface := selectDJITrafficInterface(discoverDJIUSBDevice(), interfaces, services)
	if cellularInterface == "" || route.Interface != cellularInterface {
		return cellularRouteSelection{}, fmt.Errorf("当前默认出口是 %s，不是 4G USB 网卡；已取消网络测量", route.Interface)
	}
	for _, item := range interfaces {
		if item.Name == cellularInterface && net.ParseIP(item.IPv4) != nil {
			return cellularRouteSelection{Interface: cellularInterface, SourceIP: item.IPv4}, nil
		}
	}
	return cellularRouteSelection{}, fmt.Errorf("4G USB 网卡 %s 没有可用的 IPv4 地址", cellularInterface)
}

func runCellularWebProbe(ctx context.Context, selection cellularRouteSelection) cellularWebProbeSummary {
	summary := cellularWebProbeSummary{
		TargetCount: len(cellularWebProbeTargets),
		Interface:   selection.Interface,
		SourceIP:    selection.SourceIP,
		Results:     make([]cellularWebProbeTargetResult, 0, len(cellularWebProbeTargets)),
	}
	for _, target := range cellularWebProbeTargets {
		targetCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		result := measureCellularWebTarget(targetCtx, target, selection.SourceIP)
		cancel()
		summary.Results = append(summary.Results, result)
		summary.BytesRead += result.BytesRead
		if result.OK {
			summary.SuccessCount++
		}
	}
	if summary.TargetCount > 0 {
		summary.SuccessPercent = float64(summary.SuccessCount) / float64(summary.TargetCount) * 100
	}
	summary.MedianDNSMS = medianWebMetric(summary.Results, func(item cellularWebProbeTargetResult) float64 { return item.DNSMS })
	summary.MedianConnect = medianWebMetric(summary.Results, func(item cellularWebProbeTargetResult) float64 { return item.ConnectMS })
	summary.MedianTLSMS = medianWebMetric(summary.Results, func(item cellularWebProbeTargetResult) float64 { return item.TLSMS })
	summary.MedianTTFBMS = medianWebMetric(summary.Results, func(item cellularWebProbeTargetResult) float64 { return item.TTFBMS })
	summary.MedianTotalMS = medianWebMetric(summary.Results, func(item cellularWebProbeTargetResult) float64 { return item.TotalMS })
	return summary
}

func measureCellularWebTarget(ctx context.Context, target cellularWebProbeTarget, sourceIPv4 string) cellularWebProbeTargetResult {
	result := cellularWebProbeTargetResult{Name: target.Name, URL: target.URL}
	parsed, err := url.Parse(target.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		result.Error = "测试地址无效"
		return result
	}

	dnsStarted := time.Now()
	addresses, err := net.DefaultResolver.LookupIP(ctx, "ip4", parsed.Hostname())
	result.DNSMS = durationMS(time.Since(dnsStarted))
	if err != nil {
		result.Error = "DNS 解析失败：" + err.Error()
		return result
	}
	resolved, resolveErr := selectPublicProbeIP(addresses)
	if resolveErr != nil {
		result.Error = resolveErr.Error()
		return result
	}
	result.ResolvedIP = resolved.String()

	port := parsed.Port()
	if port == "" {
		port = "443"
	}
	sourceIP := net.ParseIP(sourceIPv4)
	if sourceIP == nil {
		result.Error = "4G USB 网卡源地址无效"
		return result
	}
	dialer := &net.Dialer{Timeout: 8 * time.Second, LocalAddr: &net.TCPAddr{IP: sourceIP}}
	var tlsStarted time.Time
	var requestStarted time.Time
	transport := &http.Transport{
		Proxy:             nil,
		DisableKeepAlives: true,
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
		DialContext: func(dialCtx context.Context, network, _ string) (net.Conn, error) {
			started := time.Now()
			conn, dialErr := dialer.DialContext(dialCtx, network, net.JoinHostPort(result.ResolvedIP, port))
			result.ConnectMS = durationMS(time.Since(started))
			return conn, dialErr
		},
	}
	defer transport.CloseIdleConnections()
	trace := &httptrace.ClientTrace{
		TLSHandshakeStart: func() { tlsStarted = time.Now() },
		TLSHandshakeDone: func(_ tls.ConnectionState, _ error) {
			if !tlsStarted.IsZero() {
				result.TLSMS = durationMS(time.Since(tlsStarted))
			}
		},
		GotFirstResponseByte: func() {
			if !requestStarted.IsZero() {
				result.TTFBMS = durationMS(time.Since(requestStarted))
			}
		},
	}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodGet, target.URL, nil)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	req.Header.Set("User-Agent", "DJOneHub-Cellular-Lab/1.0")
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", cellularLabWebReadLimit-1))
	req.Header.Set("Cache-Control", "no-cache")
	client := &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	requestStarted = time.Now()
	resp, err := client.Do(req)
	if err != nil {
		result.TotalMS = durationMS(time.Since(requestStarted))
		result.Error = "请求失败：" + err.Error()
		return result
	}
	defer resp.Body.Close()
	result.StatusCode = resp.StatusCode
	result.BytesRead, err = io.Copy(io.Discard, io.LimitReader(resp.Body, cellularLabWebReadLimit))
	result.TotalMS = durationMS(time.Since(requestStarted))
	if err != nil {
		result.Error = "读取失败：" + err.Error()
		return result
	}
	result.OK = resp.StatusCode >= 200 && resp.StatusCode < 400 && result.BytesRead > 0
	if !result.OK {
		result.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return result
}

func selectPublicProbeIP(addresses []net.IP) (net.IP, error) {
	for _, address := range addresses {
		if isFakeOrNonPublicProbeIP(address) {
			return nil, fmt.Errorf("DNS 返回 %s，疑似代理 Fake-IP 或非公网地址，已取消测试", address.String())
		}
	}
	for _, address := range addresses {
		if address.To4() != nil {
			return address.To4(), nil
		}
	}
	return nil, errors.New("DNS 未返回可用的公网 IPv4 地址")
}

func isFakeOrNonPublicProbeIP(address net.IP) bool {
	if address == nil || address.To4() == nil || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
		return true
	}
	ipv4 := address.To4()
	return ipv4[0] == 198 && (ipv4[1] == 18 || ipv4[1] == 19)
}

func medianWebMetric(results []cellularWebProbeTargetResult, metric func(cellularWebProbeTargetResult) float64) float64 {
	values := make([]float64, 0, len(results))
	for _, result := range results {
		if result.OK {
			values = append(values, metric(result))
		}
	}
	if len(values) == 0 {
		return 0
	}
	sort.Float64s(values)
	middle := len(values) / 2
	if len(values)%2 == 0 {
		return (values[middle-1] + values[middle]) / 2
	}
	return values[middle]
}

func durationMS(value time.Duration) float64 {
	return float64(value.Microseconds()) / 1000
}

func floatPointer(value float64) *float64 { return &value }

func (a *app) replaceLastCellularLabSample(sample cellularLabSample) {
	a.labMu.Lock()
	defer a.labMu.Unlock()
	if len(a.labSamples) > 0 {
		a.labSamples[len(a.labSamples)-1] = sample
	}
}

func (a *app) cellularLabSpeedTest(w http.ResponseWriter, r *http.Request) {
	a.labTestMu.Lock()
	defer a.labTestMu.Unlock()
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
	selection, err := currentCellularRouteSelection()
	if err != nil {
		writeError(w, http.StatusConflict, err.Error()+"，避免记录 Wi-Fi/VPN 数据")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	requestURL := fmt.Sprintf("https://speed.cloudflare.com/__down?bytes=%d", body.Bytes)
	parsedURL, _ := url.Parse(requestURL)
	addresses, err := net.DefaultResolver.LookupIP(ctx, "ip4", parsedURL.Hostname())
	if err != nil {
		writeError(w, http.StatusBadGateway, "测速域名解析失败："+err.Error())
		return
	}
	resolved, err := selectPublicProbeIP(addresses)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, LocalAddr: &net.TCPAddr{IP: net.ParseIP(selection.SourceIP)}}
	transport := &http.Transport{
		Proxy:             nil,
		DisableKeepAlives: true,
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
		DialContext: func(dialCtx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(dialCtx, network, net.JoinHostPort(resolved.String(), "443"))
		},
	}
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	req.Header.Set("Cache-Control", "no-cache")
	started := time.Now()
	resp, err := (&http.Client{Timeout: 45 * time.Second, Transport: transport}).Do(req)
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
	a.replaceLastCellularLabSample(sample)
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
