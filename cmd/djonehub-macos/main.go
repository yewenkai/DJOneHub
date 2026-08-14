package main

import (
	"context"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/iniwex5/vohive/internal/backend"
	"github.com/iniwex5/vohive/internal/config"
	"github.com/iniwex5/vohive/internal/modem"
	"github.com/iniwex5/vohive/pkg/smscodec"
)

//go:embed web/*
var webAssets embed.FS

type receivedSMS struct {
	Sender    string    `json:"sender"`
	Content   string    `json:"content"`
	Code      string    `json:"code,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

type app struct {
	modem             *modem.Manager
	usbAT             *usbAT
	port              string
	demo              bool
	discoveryError    string
	usbDevice         *usbDeviceStatus
	usbATBackoffUntil time.Time
	usbATBackoffErr   string

	smsMu          sync.RWMutex
	sms            []receivedSMS
	smsSendMu      sync.Mutex
	smsReassembler *smscodec.Reassembler

	smsPollInterval  time.Duration
	smsAutoCleanupME bool
	smsLastPoll      time.Time
	smsLastPollError string

	trafficMu        sync.Mutex
	trafficBaselines map[string]networkByteCounters

	quotaMu          sync.Mutex
	quotaQueryMu     sync.Mutex
	quota            trafficQuotaStore
	quotaLoaded      bool
	quotaPath        string
	quotaLastPersist time.Time

	labMu          sync.Mutex
	labSampleMu    sync.Mutex
	labTestMu      sync.Mutex
	labSamples     []cellularLabSample
	labLoaded      bool
	labPath        string
	labLastPersist time.Time

	bandApplyMu   sync.Mutex
	bandMu        sync.Mutex
	bandStore     bandPreferenceStore
	bandLoaded    bool
	bandPath      string
	bandOperation bandOperationState

	egressMu      sync.Mutex
	egressPolicy  egressPolicyStore
	egressLoaded  bool
	egressPath    string
	egressApplied string

	voiceMu sync.Mutex
	voice   *voiceService

	callMu            sync.RWMutex
	callPollInterval  time.Duration
	callConfigured    bool
	callCurrent       []voiceCall
	callActive        *callRecord
	callHistory       []callRecord
	callLastPoll      time.Time
	callLastPollError string

	networkRepairMu sync.Mutex
	dhcpStatusMu    sync.RWMutex
	dhcpStatus      dhcpRepairStatus

	statusMu           sync.RWMutex
	statusSnapshot     any
	statusSampledAt    time.Time
	statusLastError    string
	statusPollInterval time.Duration
	statusCollectFn    func() (any, error)
	statusRefreshMu    sync.Mutex
	statusRefreshing   bool
	statusRefreshDone  chan struct{}
}

type usbInterfaceStatus struct {
	Number    int `json:"number"`
	Class     int `json:"class"`
	Subclass  int `json:"subclass"`
	Protocol  int `json:"protocol"`
	Endpoints int `json:"endpoints"`
}

type usbDeviceStatus struct {
	Product    string               `json:"product"`
	Vendor     string               `json:"vendor"`
	VendorID   string               `json:"vendor_id"`
	ProductID  string               `json:"product_id"`
	LocationID string               `json:"location_id"`
	Speed      string               `json:"speed"`
	Mode       string               `json:"mode"`
	Interfaces []usbInterfaceStatus `json:"interfaces"`
}

type usbDeviceIdentity struct {
	VendorID       int
	ProductID      int
	DefaultVendor  string
	DefaultProduct string
}

var supportedUSBDeviceIdentities = []usbDeviceIdentity{
	{
		VendorID:       0x2ca3,
		ProductID:      0x4006,
		DefaultVendor:  "DJI",
		DefaultProduct: "DJI 4G Module",
	},
	{
		VendorID:       0x2c7c,
		ProductID:      0x0125,
		DefaultVendor:  "Quectel",
		DefaultProduct: "DJI 4G Module (Quectel mode)",
	},
}

func supportedUSBDeviceIdentity(vendorID, productID int) (usbDeviceIdentity, bool) {
	for _, identity := range supportedUSBDeviceIdentities {
		if identity.VendorID == vendorID && identity.ProductID == productID {
			return identity, true
		}
	}
	return usbDeviceIdentity{}, false
}

type networkDiagnostic struct {
	USBNetMode        string            `json:"usbnet_mode"`
	USBCfg            string            `json:"usbcfg"`
	PDPContexts       []pdpContext      `json:"pdp_contexts"`
	ActiveContexts    []int             `json:"active_contexts"`
	PDPAddresses      []string          `json:"pdp_addresses"`
	MacInterfaces     []macNetInterface `json:"mac_interfaces"`
	DefaultRoute      macDefaultRoute   `json:"default_route"`
	USBNetworkPresent bool              `json:"usb_network_present"`
	USBDevice         *usbDeviceStatus  `json:"usb_device,omitempty"`
	DHCPRepair        dhcpRepairStatus  `json:"dhcp_repair"`
	Raw               map[string]string `json:"raw,omitempty"`
	Errors            map[string]string `json:"errors,omitempty"`
}

type pdpContext struct {
	ID  int    `json:"id"`
	PDN string `json:"pdn"`
	APN string `json:"apn"`
}

type macNetInterface struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	IPv4   string `json:"ipv4"`
	Kind   string `json:"kind"`
}

type macDefaultRoute struct {
	Interface string `json:"interface"`
	Gateway   string `json:"gateway"`
}

type networkByteCounters struct {
	RX uint64
	TX uint64
}

type networkTrafficSnapshot struct {
	Available    bool   `json:"available"`
	Interface    string `json:"interface,omitempty"`
	RXBytes      uint64 `json:"rx_bytes"`
	TXBytes      uint64 `json:"tx_bytes"`
	SessionRX    uint64 `json:"session_rx_bytes"`
	SessionTX    uint64 `json:"session_tx_bytes"`
	SessionTotal uint64 `json:"session_total_bytes"`
	SampledAtMS  int64  `json:"sampled_at_ms"`
	Error        string `json:"error,omitempty"`
}

type networkCheckResult struct {
	OK                bool   `json:"ok"`
	Summary           string `json:"summary"`
	Detail            string `json:"detail"`
	TunnelActive      bool   `json:"tunnel_active"`
	LogicalInterface  string `json:"logical_interface,omitempty"`
	PhysicalInterface string `json:"physical_interface,omitempty"`
	PhysicalKind      string `json:"physical_kind,omitempty"`
	PhysicalName      string `json:"physical_name,omitempty"`
}

type macPhysicalRoute struct {
	TunnelActive      bool
	LogicalInterface  string
	LogicalGateway    string
	PhysicalInterface string
	PhysicalGateway   string
	PhysicalKind      string
	PhysicalName      string
	Detection         string
}

type macNWIInfo struct {
	VPNServers map[string][]string
	Interfaces []string
}

func main() {
	var port string
	var listen string
	var demo bool
	flag.StringVar(&port, "port", "", "AT serial port; auto-detected when omitted")
	flag.StringVar(&listen, "listen", "127.0.0.1:7575", "HTTP listen address")
	flag.BoolVar(&demo, "demo", false, "run the web UI with simulated modem data")
	flag.Parse()

	if demo {
		instance := newDemoApp()
		log.Printf("DJOneHub demo mode")
		serve(instance, listen)
		return
	}

	if strings.TrimSpace(port) == "" {
		var err error
		port, err = discoverATPort()
		if err != nil {
			usbDevice := discoverDJIUSBDevice()
			usbATDevice, usbATErr := openDJIUSBAT()
			instance := &app{
				port:             "未发现 AT 串口",
				discoveryError:   err.Error(),
				usbDevice:        usbDevice,
				usbAT:            usbATDevice,
				smsPollInterval:  8 * time.Second,
				smsAutoCleanupME: true,
				smsReassembler:   smscodec.NewReassembler(),
			}
			if usbDevice != nil {
				log.Printf("DJI USB device detected without AT serial port: %s %s (%s:%s)",
					usbDevice.Vendor, usbDevice.Product, usbDevice.VendorID, usbDevice.ProductID)
			}
			if usbATErr != nil {
				log.Printf("USB AT unavailable: %v", usbATErr)
			} else {
				instance.port = usbATDevice.Description()
				instance.discoveryError = ""
				defer usbATDevice.Close()
				log.Printf("USB AT bridge opened on DJI %s", usbATDevice.Description())
				instance.scheduleCellularDHCPRepair("startup")
			}
			log.Printf("modem discovery skipped: %v", err)
			go instance.startSMSPoller(context.Background())
			serve(instance, listen)
			return
		}
	}

	cfg := config.DeviceConfig{
		ID:            "mac-modem",
		Name:          "DJI 4G Module",
		ATPort:        port,
		ManagePort:    port,
		DeviceBackend: backend.BackendAT,
		BaudRate:      115200,
		DataBits:      8,
		StopBits:      1,
		Parity:        "none",
		SMSEnabled:    true,
	}
	manager, err := modem.New(cfg)
	if err != nil {
		log.Fatalf("create modem manager: %v", err)
	}

	instance := &app{modem: manager, port: port, smsPollInterval: 8 * time.Second, smsAutoCleanupME: true}
	manager.SetSMSCallback(instance.recordSMS)
	if err := manager.Start(); err != nil {
		log.Fatalf("open modem on %s: %v", port, err)
	}
	defer manager.Stop()

	if !manager.WaitReady(15 * time.Second) {
		log.Printf("modem initialization is still running; the web UI will remain available")
	}

	go manager.CheckAllSMS()

	serve(instance, listen)
}

func serve(instance *app, listen string) {
	defer instance.stopVoiceService()
	if err := validateLoopbackListen(listen); err != nil {
		log.Fatalf("refuse unsafe HTTP listener: %v", err)
	}
	server := &http.Server{
		Addr:              listen,
		Handler:           instance.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go instance.startCellularLabSampler(ctx)
	go instance.startCallMonitor(ctx)
	go instance.startTrafficQuotaSampler(ctx)
	go instance.startStatusSampler(ctx)

	if !instance.demo {
		log.Printf("DJOneHub is using %s", instance.port)
	}
	log.Printf("Open http://%s", listen)
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Printf("HTTP server stopped unexpectedly: %v", err)
		}
	case <-ctx.Done():
		log.Printf("DJOneHub is stopping")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("HTTP server shutdown: %v", err)
		}
	}
}

func newDemoApp() *app {
	now := time.Now()
	return &app{
		demo:            true,
		port:            "Demo · Quectel EG25-G",
		smsPollInterval: 8 * time.Second,
		sms: []receivedSMS{
			{
				Sender:    "10086",
				Content:   "【DJOneHub 演示】本月套餐剩余流量 18.6GB。",
				Timestamp: now.Add(-18 * time.Minute),
			},
			{
				Sender:    "+44 7400 123456",
				Content:   "Your verification code is 482913. It expires in 10 minutes.",
				Code:      "482913",
				Timestamp: now.Add(-2 * time.Hour),
			},
		},
	}
}

func discoverATPort() (string, error) {
	var ports []string
	for _, pattern := range []string{
		"/dev/cu.usbmodem*",
		"/dev/cu.usbserial*",
		"/dev/cu.wchusbserial*",
	} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return "", err
		}
		ports = append(ports, matches...)
	}

	sort.SliceStable(ports, func(i, j int) bool {
		return portScore(ports[i]) > portScore(ports[j])
	})
	var attempted []string
	for _, port := range ports {
		attempted = append(attempted, port)
		if _, err := modem.ProbeIMEICached(port, 2*time.Second); err == nil {
			return port, nil
		}
	}
	if len(attempted) == 0 {
		return "", errors.New("no Quectel/DJI USB serial ports found; pass -port /dev/cu.* explicitly")
	}
	return "", fmt.Errorf("no AT-capable port found among %s", strings.Join(attempted, ", "))
}

func portScore(port string) int {
	name := strings.ToLower(port)
	if strings.Contains(name, "quectel") || strings.Contains(name, "dji") {
		return 100
	}
	if strings.Contains(name, "usbmodem") {
		return 80
	}
	if strings.Contains(name, "usbserial") {
		return 60
	}
	return 0
}

func discoverDJIUSBDevice() *usbDeviceStatus {
	out, err := exec.Command("ioreg", "-r", "-c", "IOUSBHostInterface", "-l", "-w", "0").Output()
	if err != nil {
		return nil
	}
	return parseDJIUSBDevice(string(out))
}

func parseDJIUSBDevice(out string) *usbDeviceStatus {
	var device *usbDeviceStatus
	var selectedVendorID, selectedProductID, selectedLocationID int
	for _, block := range strings.Split(out, "\n\n") {
		vendorID, okVendor := intProperty(block, "idVendor")
		productID, okProduct := intProperty(block, "idProduct")
		identity, supported := supportedUSBDeviceIdentity(vendorID, productID)
		if !okVendor || !okProduct || !supported {
			continue
		}
		locationID, _ := intProperty(block, "locationID")
		if device == nil {
			selectedVendorID = vendorID
			selectedProductID = productID
			selectedLocationID = locationID
			device = &usbDeviceStatus{
				Product:    stringProperty(block, "USB Product Name"),
				Vendor:     stringProperty(block, "USB Vendor Name"),
				VendorID:   fmt.Sprintf("%04x", vendorID),
				ProductID:  fmt.Sprintf("%04x", productID),
				LocationID: formatHexProperty(block, "locationID"),
				Speed:      usbSpeedName(intPropertyOrZero(block, "USBSpeed")),
				Mode:       "vendor-specific USB mode",
			}
			if strings.TrimSpace(device.Product) == "" {
				device.Product = identity.DefaultProduct
			}
			if strings.TrimSpace(device.Vendor) == "" {
				device.Vendor = identity.DefaultVendor
			}
		} else if vendorID != selectedVendorID || productID != selectedProductID ||
			(selectedLocationID != 0 && locationID != 0 && locationID != selectedLocationID) {
			continue
		}
		ifaceNumber, okIface := intProperty(block, "bInterfaceNumber")
		if !okIface {
			continue
		}
		iface := usbInterfaceStatus{
			Number:    ifaceNumber,
			Class:     intPropertyOrZero(block, "bInterfaceClass"),
			Subclass:  intPropertyOrZero(block, "bInterfaceSubClass"),
			Protocol:  intPropertyOrZero(block, "bInterfaceProtocol"),
			Endpoints: intPropertyOrZero(block, "bNumEndpoints"),
		}
		device.Interfaces = append(device.Interfaces, iface)
	}
	if device == nil {
		return nil
	}
	sort.SliceStable(device.Interfaces, func(i, j int) bool {
		return device.Interfaces[i].Number < device.Interfaces[j].Number
	})
	if allVendorSpecific(device.Interfaces) {
		device.Mode = "vendor-specific QMI/diagnostic mode"
	}
	return device
}

func allVendorSpecific(interfaces []usbInterfaceStatus) bool {
	if len(interfaces) == 0 {
		return false
	}
	for _, iface := range interfaces {
		if iface.Class != 255 {
			return false
		}
	}
	return true
}

func intPropertyOrZero(block, name string) int {
	value, _ := intProperty(block, name)
	return value
}

func intProperty(block, name string) (int, bool) {
	pattern := regexp.MustCompile(`"` + regexp.QuoteMeta(name) + `"\s*=\s*(\d+)`)
	match := pattern.FindStringSubmatch(block)
	if len(match) != 2 {
		return 0, false
	}
	value, err := strconv.Atoi(match[1])
	return value, err == nil
}

func stringProperty(block, name string) string {
	pattern := regexp.MustCompile(`"` + regexp.QuoteMeta(name) + `"\s*=\s*"([^"]*)"`)
	match := pattern.FindStringSubmatch(block)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

func formatHexProperty(block, name string) string {
	value, ok := intProperty(block, name)
	if !ok {
		return ""
	}
	return fmt.Sprintf("0x%x", value)
}

func usbSpeedName(speed int) string {
	switch speed {
	case 1:
		return "low-speed"
	case 2:
		return "full-speed"
	case 3:
		return "high-speed"
	case 4:
		return "super-speed"
	default:
		if speed == 0 {
			return ""
		}
		return fmt.Sprintf("speed-%d", speed)
	}
}

func (a *app) recordSMS(sender, content string, timestamp time.Time) {
	a.mergeSMS([]receivedSMS{{
		Sender: sender, Content: content, Timestamp: timestamp,
	}})
}

func (a *app) mergeSMS(messages []receivedSMS) (newCount int, total int) {
	a.smsMu.Lock()
	seen := make(map[string]bool, len(a.sms)+len(messages))
	newMessages := make([]receivedSMS, 0, len(messages))
	for _, item := range a.sms {
		seen[smsCacheKey(item)] = true
	}
	for _, item := range messages {
		if item.Code == "" {
			item.Code = extractSMSCode(item.Content)
		}
		key := smsCacheKey(item)
		if seen[key] {
			continue
		}
		seen[key] = true
		a.sms = append(a.sms, item)
		newMessages = append(newMessages, item)
		newCount++
	}
	sort.SliceStable(a.sms, func(i, j int) bool {
		return a.sms[i].Timestamp.After(a.sms[j].Timestamp)
	})
	if len(a.sms) > 500 {
		a.sms = a.sms[:500]
	}
	total = len(a.sms)
	a.smsMu.Unlock()
	for _, item := range newMessages {
		a.maybeCalibrateTrafficQuota(item)
	}
	return newCount, total
}

func smsCacheKey(item receivedSMS) string {
	return item.Sender + "\x00" + item.Content + "\x00" + item.Timestamp.Format(time.RFC3339Nano)
}

func (a *app) setSMSPollStatus(err error) {
	a.smsMu.Lock()
	defer a.smsMu.Unlock()
	a.smsLastPoll = time.Now()
	if err != nil {
		a.smsLastPollError = err.Error()
		return
	}
	a.smsLastPollError = ""
}

func (a *app) startSMSPoller(ctx context.Context) {
	interval := a.smsPollInterval
	if interval <= 0 {
		interval = 8 * time.Second
	}
	timer := time.NewTimer(1200 * time.Millisecond)
	defer timer.Stop()
	failures := 0
	previousDelay := interval
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			err := a.pollSMSOnce()
			if err != nil {
				failures++
			} else {
				if failures > 0 {
					log.Printf("SMS polling recovered after %d failed attempt(s)", failures)
				}
				failures = 0
			}
			delay := nextSMSPollDelay(interval, failures)
			if err != nil && (failures == 1 || delay != previousDelay) {
				log.Printf("SMS poll failed; retrying in %s: %v", delay, err)
			}
			previousDelay = delay
			timer.Reset(delay)
		}
	}
}

func nextSMSPollDelay(interval time.Duration, failures int) time.Duration {
	if interval <= 0 {
		interval = 8 * time.Second
	}
	if failures <= 1 {
		return interval
	}
	delay := interval
	for attempt := 1; attempt < failures && delay < 60*time.Second; attempt++ {
		delay *= 2
	}
	if delay > 60*time.Second {
		return 60 * time.Second
	}
	return delay
}

func (a *app) pollSMSOnce() error {
	if a.demo || a.modem != nil {
		return nil
	}
	if err := a.ensureUSBAT(); err != nil {
		a.setSMSPollStatus(err)
		return err
	}
	messages, err := a.readUSBATSMS()
	if err != nil {
		a.resetUSBATIfGone(err)
		a.setSMSPollStatus(err)
		return err
	}
	newCount, total := a.mergeSMS(messages)
	if a.smsAutoCleanupME && len(messages) > 0 {
		before, after, cleanupErr := a.clearUSBATSMSMemory("ME")
		if cleanupErr != nil {
			log.Printf("auto cleanup ME SMS failed: %v", cleanupErr)
		} else if before != after {
			log.Printf("auto cleanup ME SMS: %d -> %d", before, after)
		}
	}
	a.setSMSPollStatus(nil)
	if newCount > 0 {
		log.Printf("SMS poll cached %d new message(s), total %d", newCount, total)
	}
	return nil
}

func (a *app) ensureUSBAT() error {
	if a.demo || a.modem != nil || a.usbAT != nil {
		return nil
	}
	if a.currentUSBDevice() == nil {
		a.port = "未检测到 DJI USB 设备"
		a.discoveryError = "DJI USB device is not connected"
		return errors.New("DJI USB device is not connected")
	}
	if !a.usbATBackoffUntil.IsZero() && time.Now().Before(a.usbATBackoffUntil) {
		if a.usbATBackoffErr != "" {
			return fmt.Errorf("USB AT is cooling down after disconnect: %s", a.usbATBackoffErr)
		}
		return errors.New("USB AT is cooling down after disconnect")
	}
	dev, err := openDJIUSBAT()
	if err != nil {
		return err
	}
	a.usbAT = dev
	a.usbATBackoffUntil = time.Time{}
	a.usbATBackoffErr = ""
	a.port = dev.Description()
	a.discoveryError = ""
	log.Printf("USB AT bridge opened on DJI %s", dev.Description())
	a.scheduleCellularDHCPRepair("usb-reconnected")
	return nil
}

func (a *app) resetUSBATIfGone(err error) {
	if err == nil || a.usbAT == nil {
		return
	}
	text := strings.ToUpper(err.Error())
	if !strings.Contains(text, "NO_DEVICE") &&
		!strings.Contains(text, "NOT_FOUND") &&
		!strings.Contains(text, "USB AT COMMAND TIMED OUT") {
		return
	}
	a.markUSBATDetached(err.Error())
}

// markUSBATDetached clears state belonging to a physically removed module.
// A later status/SMS poll will discover and open a newly connected module.
func (a *app) markUSBATDetached(reason string) {
	if a.usbAT != nil {
		log.Printf("USB AT bridge detached; waiting for a new enumeration: %s", reason)
		a.usbAT.Close()
		a.usbAT = nil
	}
	a.usbDevice = nil
	a.port = "未检测到 DJI USB 设备"
	a.discoveryError = "DJI USB device is not connected"
	a.usbATBackoffUntil = time.Now().Add(2 * time.Second)
	a.usbATBackoffErr = reason
	a.callMu.Lock()
	a.callConfigured = false
	a.callMu.Unlock()
}

func (a *app) routes() http.Handler {
	actionToken, err := newActionToken()
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/session", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"action_token": actionToken})
	})
	mux.HandleFunc("GET /api/health", a.health)
	mux.HandleFunc("GET /api/status", a.status)
	mux.HandleFunc("GET /api/sms", a.listSMS)
	mux.HandleFunc("GET /api/sms/status", a.smsStatus)
	mux.HandleFunc("POST /api/sms/send", a.sendSMS)
	mux.HandleFunc("POST /api/sms/refresh", a.refreshSMS)
	mux.HandleFunc("POST /api/sms/clear-module", a.clearModuleSMS)
	mux.HandleFunc("POST /api/at", a.executeAT)
	mux.HandleFunc("GET /api/network", a.networkDiagnostic)
	mux.HandleFunc("GET /api/network/traffic", a.networkTraffic)
	mux.HandleFunc("GET /api/network/quota", a.trafficQuotaStatus)
	mux.HandleFunc("POST /api/network/quota/config", a.configureTrafficQuota)
	mux.HandleFunc("POST /api/network/quota/query", a.queryTrafficQuota)
	mux.HandleFunc("GET /api/network/lab", a.cellularLabHistory)
	mux.HandleFunc("POST /api/network/lab/sample", a.cellularLabSampleNow)
	mux.HandleFunc("POST /api/network/lab/web-test", a.cellularLabWebTest)
	mux.HandleFunc("POST /api/network/lab/speed-test", a.cellularLabSpeedTest)
	mux.HandleFunc("GET /api/network/bands", a.bandPreferenceStatus)
	mux.HandleFunc("POST /api/network/bands", a.configureBandPreference)
	mux.HandleFunc("POST /api/network/check-4g", a.check4GRoute)
	mux.HandleFunc("POST /api/network/check-proxy", a.checkProxyRoute)
	mux.HandleFunc("POST /api/network/usbnet", a.setUSBNetMode)
	mux.HandleFunc("POST /api/network/reboot-module", a.rebootModule)
	mux.HandleFunc("GET /api/egress", a.egressPolicyStatus)
	mux.HandleFunc("PUT /api/egress", a.saveEgressPolicy)
	mux.HandleFunc("GET /api/egress/apps", a.egressApplications)
	mux.HandleFunc("POST /api/egress/preview", a.previewEgressPolicy)
	mux.HandleFunc("POST /api/egress/apply", a.applyEgressPolicy)
	mux.HandleFunc("POST /api/egress/restore", a.restoreEgressPolicy)
	mux.HandleFunc("GET /api/egress/stash-override", a.downloadEgressOverride)
	mux.HandleFunc("GET /api/voice", a.voiceStatus)
	mux.HandleFunc("GET /api/voice/calls", a.voiceCalls)
	mux.HandleFunc("POST /api/voice/dial", a.voiceDial)
	mux.HandleFunc("POST /api/voice/answer", a.voiceAnswer)
	mux.HandleFunc("POST /api/voice/hangup", a.voiceHangup)
	mux.HandleFunc("POST /api/voice/audio/start", a.voiceAudioStart)
	mux.HandleFunc("POST /api/voice/audio/stop", a.voiceAudioStop)
	mux.HandleFunc("POST /api/voice/recording/start", a.voiceRecordingStart)
	mux.HandleFunc("POST /api/voice/recording/stop", a.voiceRecordingStop)
	content, _ := fs.Sub(webAssets, "web")
	mux.Handle("/", http.FileServer(http.FS(content)))
	return localControlSecurity(actionToken, mux)
}

func (a *app) health(w http.ResponseWriter, _ *http.Request) {
	usbDevice := a.currentUSBDevice()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "port": a.port, "demo": a.demo,
		"usb_device": usbDevice, "discovery_error": a.discoveryError,
	})
}

func (a *app) currentUSBDevice() *usbDeviceStatus {
	if a.modem != nil || a.demo {
		return a.usbDevice
	}
	usbDevice := discoverDJIUSBDevice()
	// Never retain the last successful scan: that is stale after an unplug.
	a.usbDevice = usbDevice
	return usbDevice
}

func (a *app) usbATStatus() (modem.DeviceStatus, error) {
	firmwareResp, _ := a.usbAT.Command("ATI", 3*time.Second)
	cpinResp, cpinErr := a.usbAT.Command("AT+CPIN?", 3*time.Second)
	csqResp, _ := a.usbAT.Command("AT+CSQ", 3*time.Second)
	ceregResp, _ := a.usbAT.Command("AT+CEREG?", 3*time.Second)
	cregResp, _ := a.usbAT.Command("AT+CREG?", 3*time.Second)
	_, _ = a.usbAT.Command("AT+COPS=3,2", 3*time.Second)
	copsResp, _ := a.usbAT.Command("AT+COPS?", 3*time.Second)
	qccidResp, _ := a.usbAT.Command("AT+QCCID", 3*time.Second)
	cimiResp, _ := a.usbAT.Command("AT+CIMI", 3*time.Second)
	qnwinfoResp, _ := a.usbAT.Command("AT+QNWINFO", 3*time.Second)
	qengResp, _ := a.usbAT.Command(`AT+QENG="servingcell"`, 3*time.Second)
	usbnetResp, _ := a.usbAT.Command(`AT+QCFG="usbnet"`, 3*time.Second)

	if cpinErr != nil {
		return modem.DeviceStatus{}, cpinErr
	}

	regStatus := firstNonZeroRegistration(ceregResp, cregResp)
	mode, duplex, band, channel := parseUSBATQNWInfo(qnwinfoResp)
	usbnetMode := -1
	if parsedMode, err := strconv.Atoi(parseUSBNetMode(usbnetResp)); err == nil {
		usbnetMode = parsedMode
	}
	status := modem.DeviceStatus{
		Firmware:      parseUSBATFirmware(firmwareResp),
		ICCID:         parseUSBATPrefixed(qccidResp, "+QCCID:"),
		IMSI:          parseUSBATIMSI(cimiResp),
		Operator:      parseUSBATOperator(copsResp),
		SimInserted:   strings.Contains(strings.ToUpper(cpinResp), "READY"),
		SignalDBM:     parseUSBATCSQDBM(csqResp),
		RegStatus:     regStatus,
		RegStatusText: registrationText(regStatus),
		NetworkMode:   mode,
		NetworkDuplex: duplex,
		RadioBand:     band,
		RadioChannel:  channel,
		USBNetMode:    usbnetMode,
	}
	if cell, ok := modem.ParseServingCellLTEInfo(qengResp); ok {
		status.SignalRSRP = cell.RSRP
		status.SignalRSRQ = cell.RSRQ
		status.SignalSINR = cell.SINR
		status.CellID = cell.CellID
		status.RadioBand = cell.Band
		status.RadioChannel = cell.Channel
		if cell.Duplex != "" {
			status.NetworkDuplex = cell.Duplex
		}
	}
	if status.Operator == "" && strings.Contains(copsResp, "CHN-UNICOM") {
		status.Operator = "CHN-UNICOM"
	}
	return status, nil
}

func parseUSBATFirmware(resp string) string {
	lines := splitATLines(resp)
	var useful []string
	for _, line := range lines {
		up := strings.ToUpper(line)
		if strings.HasPrefix(up, "ATI") || up == "OK" {
			continue
		}
		useful = append(useful, line)
	}
	return strings.Join(useful, " · ")
}

func splitATLines(resp string) []string {
	resp = strings.ReplaceAll(resp, "\r", "\n")
	raw := strings.Split(resp, "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func parseUSBATPrefixed(resp, prefix string) string {
	for _, line := range splitATLines(resp) {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func parseUSBATIMSI(resp string) string {
	for _, line := range splitATLines(resp) {
		up := strings.ToUpper(line)
		if up == "OK" || strings.HasPrefix(up, "AT") {
			continue
		}
		if _, err := strconv.ParseUint(line, 10, 64); err == nil && len(line) >= 5 {
			return line
		}
	}
	return ""
}

func parseUSBATCSQDBM(resp string) int {
	re := regexp.MustCompile(`\+CSQ:\s*(\d+),`)
	match := re.FindStringSubmatch(resp)
	if len(match) != 2 {
		return 0
	}
	rssi, err := strconv.Atoi(match[1])
	if err != nil || rssi == 99 {
		return 0
	}
	return -113 + 2*rssi
}

func parseUSBATOperator(resp string) string {
	re := regexp.MustCompile(`\+COPS:\s*\d+,\d+,"([^"]*)"`)
	match := re.FindStringSubmatch(resp)
	if len(match) != 2 {
		return ""
	}
	return modem.ResolveServingOperatorNameFromPLMN(match[1])
}

func firstNonZeroRegistration(responses ...string) int {
	for _, resp := range responses {
		re := regexp.MustCompile(`\+(?:CE)?REG:\s*\d+,(\d+)`)
		match := re.FindStringSubmatch(resp)
		if len(match) != 2 {
			continue
		}
		status, err := strconv.Atoi(match[1])
		if err == nil && status != 0 {
			return status
		}
	}
	return 0
}

func registrationText(status int) string {
	switch status {
	case 1:
		return "已注册"
	case 5:
		return "漫游注册"
	case 2:
		return "搜索中"
	case 3:
		return "注册被拒绝"
	default:
		return "未注册"
	}
}

func parseUSBATQNWInfo(resp string) (mode, duplex, band string, channel uint32) {
	re := regexp.MustCompile(`\+QNWINFO:\s*"([^"]*)","[^"]*","([^"]*)",(\d+)`)
	match := re.FindStringSubmatch(resp)
	if len(match) != 4 {
		return "", "", "", 0
	}
	mode = match[1]
	radio := match[2]
	if strings.Contains(strings.ToUpper(mode), "FDD") {
		duplex = "FDD"
		mode = strings.TrimSpace(strings.TrimPrefix(mode, "FDD"))
	}
	if strings.Contains(strings.ToUpper(mode), "TDD") {
		duplex = "TDD"
		mode = strings.TrimSpace(strings.TrimPrefix(mode, "TDD"))
	}
	band = strings.TrimPrefix(radio, "LTE ")
	if value, err := strconv.ParseUint(match[3], 10, 32); err == nil {
		channel = uint32(value)
	}
	return mode, duplex, band, channel
}

func (a *app) readUSBATSMS() ([]receivedSMS, error) {
	if _, err := a.usbAT.Command("AT+CMGF=0", 3*time.Second); err != nil {
		return nil, fmt.Errorf("set SMS PDU mode: %w", err)
	}
	memories := []string{"SM", "ME"}
	seen := make(map[string]bool)
	messages := make([]receivedSMS, 0)
	var errs []string
	for _, memory := range memories {
		items, err := a.readUSBATSMSFromMemory(memory)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", memory, err))
			continue
		}
		for _, item := range items {
			key := item.Sender + "\x00" + item.Content + "\x00" + item.Timestamp.Format(time.RFC3339Nano)
			if seen[key] {
				continue
			}
			seen[key] = true
			messages = append(messages, item)
		}
	}
	if len(messages) == 0 && len(errs) == len(memories) {
		return nil, fmt.Errorf("list SMS failed: %s", strings.Join(errs, "; "))
	}
	sort.SliceStable(messages, func(i, j int) bool {
		return messages[i].Timestamp.After(messages[j].Timestamp)
	})
	return messages, nil
}

func (a *app) readUSBATSMSFromMemory(memory string) ([]receivedSMS, error) {
	if _, err := a.usbAT.Command(fmt.Sprintf(`AT+CPMS="%s","%s","%s"`, memory, memory, memory), 5*time.Second); err != nil {
		return nil, fmt.Errorf("select storage: %w", err)
	}
	resp, err := a.usbAT.Command("AT+CMGL=4", 15*time.Second)
	if err != nil {
		return nil, fmt.Errorf("list SMS: %w", err)
	}
	pdus := parseUSBATCMGL(resp)
	messages := make([]receivedSMS, 0, len(pdus))
	for _, item := range pdus {
		msg, concat, err := decodeUSBATPDU(item.header, item.pdu)
		if err != nil {
			messages = append(messages, receivedSMS{
				Sender:    "PDU",
				Content:   fmt.Sprintf("[短信解析失败] %v\n%s", err, item.pdu),
				Timestamp: time.Now(),
			})
			continue
		}
		if concat.IsConcat {
			if a.smsReassembler == nil {
				a.smsReassembler = smscodec.NewReassembler()
			}
			complete, content := a.smsReassembler.Add(msg.Sender, concat, msg.Content)
			if !complete {
				continue
			}
			msg.Content = content
			log.Printf("USB AT long SMS reassembled: segments=%d", concat.Total)
		}
		messages = append(messages, msg)
	}
	if a.smsReassembler != nil {
		a.smsReassembler.Cleanup(10 * time.Minute)
	}
	sort.SliceStable(messages, func(i, j int) bool {
		return messages[i].Timestamp.After(messages[j].Timestamp)
	})
	return messages, nil
}

func (a *app) clearUSBATSMSMemory(memory string) (before, after int, err error) {
	resp, err := a.usbAT.Command(fmt.Sprintf(`AT+CPMS="%s","%s","%s"`, memory, memory, memory), 5*time.Second)
	if err != nil {
		return 0, 0, fmt.Errorf("select storage: %w", err)
	}
	before = parseUSBATCPMSUsed(resp)
	if _, err := a.usbAT.Command("AT+CMGD=1,4", 20*time.Second); err != nil {
		return before, 0, fmt.Errorf("delete messages: %w", err)
	}
	resp, err = a.usbAT.Command(fmt.Sprintf(`AT+CPMS="%s","%s","%s"`, memory, memory, memory), 5*time.Second)
	if err != nil {
		return before, 0, fmt.Errorf("recheck storage: %w", err)
	}
	after = parseUSBATCPMSUsed(resp)
	return before, after, nil
}

func parseUSBATCPMSUsed(resp string) int {
	re := regexp.MustCompile(`\+CPMS:\s*(\d+),`)
	match := re.FindStringSubmatch(resp)
	if len(match) != 2 {
		return 0
	}
	used, err := strconv.Atoi(match[1])
	if err != nil {
		return 0
	}
	return used
}

type usbATSMSPDU struct {
	header string
	pdu    string
}

func parseUSBATCMGL(resp string) []usbATSMSPDU {
	lines := splitATLines(resp)
	var out []usbATSMSPDU
	for i := 0; i < len(lines)-1; i++ {
		if !strings.HasPrefix(lines[i], "+CMGL:") {
			continue
		}
		next := strings.TrimSpace(lines[i+1])
		if !smscodec.IsHexString(next) {
			continue
		}
		pdu, _ := smscodec.TrimFullPDUHexByATHeader(next, lines[i])
		out = append(out, usbATSMSPDU{header: lines[i], pdu: pdu})
		i++
	}
	return out
}

func decodeUSBATPDU(header, pduHex string) (receivedSMS, smscodec.ConcatInfo, error) {
	raw := strings.TrimSpace(pduHex)
	if trimmed, ok := smscodec.TrimFullPDUHexByATHeader(raw, header); ok {
		raw = trimmed
	}
	full, err := hex.DecodeString(raw)
	if err != nil {
		return receivedSMS{}, smscodec.ConcatInfo{}, err
	}
	if len(full) < 2 {
		return receivedSMS{}, smscodec.ConcatInfo{}, errors.New("PDU too short")
	}
	smscLen := int(full[0])
	tpduOffset := 1 + smscLen
	if tpduOffset >= len(full) {
		return receivedSMS{}, smscodec.ConcatInfo{}, errors.New("PDU has invalid SMSC length")
	}
	sender, content, timestamp, concat, err := smscodec.DecodeDeliverTPDU(full[tpduOffset:])
	if err != nil {
		return receivedSMS{}, smscodec.ConcatInfo{}, err
	}
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	return receivedSMS{Sender: sender, Content: content, Timestamp: timestamp}, concat, nil
}

func (a *app) listSMS(w http.ResponseWriter, _ *http.Request) {
	a.smsMu.RLock()
	items := append([]receivedSMS(nil), a.sms...)
	a.smsMu.RUnlock()
	if items == nil {
		items = []receivedSMS{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (a *app) smsStatus(w http.ResponseWriter, _ *http.Request) {
	a.smsMu.RLock()
	lastPoll := a.smsLastPoll
	lastPollError := a.smsLastPollError
	count := len(a.sms)
	a.smsMu.RUnlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"count":           count,
		"polling":         !a.demo && a.modem == nil,
		"poll_interval_s": int(a.smsPollInterval.Seconds()),
		"auto_cleanup_me": a.smsAutoCleanupME,
		"last_poll":       lastPoll,
		"last_poll_error": lastPollError,
	})
}

func (a *app) refreshSMS(w http.ResponseWriter, _ *http.Request) {
	if a.demo {
		writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
		return
	}
	if a.modem == nil {
		if err := a.pollSMSOnce(); err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		a.smsMu.RLock()
		count := len(a.sms)
		a.smsMu.RUnlock()
		writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "count": count})
		return
	}
	go a.modem.CheckAllSMS()
	writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
}

func (a *app) clearModuleSMS(w http.ResponseWriter, _ *http.Request) {
	if a.demo {
		writeJSON(w, http.StatusOK, map[string]any{"cleared": true, "before": 0, "after": 0})
		return
	}
	if a.modem != nil {
		writeError(w, http.StatusServiceUnavailable, "module SMS cleanup is only available through USB AT")
		return
	}
	if err := a.ensureUSBAT(); err != nil {
		writeError(w, http.StatusServiceUnavailable, "AT serial port is unavailable: "+err.Error())
		return
	}
	before, after, err := a.clearUSBATSMSMemory("ME")
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"cleared": true,
		"memory":  "ME",
		"before":  before,
		"after":   after,
	})
}

func (a *app) runATCommand(command string, timeout time.Duration) (string, error) {
	if a.demo {
		responses := map[string]string{
			"AT":                 "OK",
			"AT+CSQ":             "+CSQ: 22,99\r\nOK",
			"AT+COPS?":           "+COPS: 0,0,\"China Mobile\",7\r\nOK",
			"AT+QNWINFO":         "+QNWINFO: \"FDD LTE\",\"46000\",\"LTE BAND 3\",1650\r\nOK",
			"AT+QCFG=\"BAND\"":   "+QCFG: \"band\",0xbff,0x180080000c5,0x0\r\nOK",
			"AT+CGATT?":          "+CGATT: 1\r\nOK",
			"AT+CEREG?":          "+CEREG: 0,1\r\nOK",
			"AT+CGSN":            "860000000000001\r\nOK",
			"AT+QCFG=\"USBNET\"": "+QCFG: \"usbnet\",1\r\nOK",
			"AT+QCFG=\"USBCFG\"": "+QCFG: \"usbcfg\",0x2C7C,0x0125,1,1,1,1,1,0,0\r\nOK",
			"AT+CGDCONT?":        "+CGDCONT: 1,\"IPV4V6\",\"3gnet\",\"0.0.0.0\",0,0,0,0\r\nOK",
			"AT+CGACT?":          "+CGACT: 1,1\r\nOK",
			"AT+CGPADDR=1":       "+CGPADDR: 1,\"10.23.45.67\"\r\nOK",
		}
		response := responses[strings.ToUpper(strings.TrimSpace(command))]
		if response == "" {
			response = "OK"
		}
		return response, nil
	}
	if a.modem == nil {
		if err := a.ensureUSBAT(); err != nil {
			return "", err
		}
		if a.usbAT == nil {
			return "", errors.New("AT serial port is unavailable")
		}
		response, err := a.usbAT.Command(command, timeout)
		if err != nil {
			a.resetUSBATIfGone(err)
		}
		return response, err
	}
	return a.modem.ExecuteAT(command, timeout)
}

func (a *app) sendSMS(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Phone   string `json:"phone"`
		Message string `json:"message"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Phone) == "" || strings.TrimSpace(body.Message) == "" {
		writeError(w, http.StatusBadRequest, "phone and message are required")
		return
	}
	if a.demo {
		a.recordSMS("已发送至 "+body.Phone, body.Message, time.Now())
		writeJSON(w, http.StatusOK, map[string]any{"sent": true, "segments": 1})
		return
	}
	segments, err := a.sendTextSMS(body.Phone, body.Message)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent": true, "segments": segments})
}

func (a *app) sendTextSMS(phone, message string) (int, error) {
	if a.modem == nil {
		return a.sendUSBATSMS(phone, message)
	}
	if err := a.modem.SendSMSWithOptions(phone, message, smsSubmitOptions(message)); err != nil {
		return 0, err
	}
	return 1, nil
}

func (a *app) sendUSBATSMS(phone, message string) (int, error) {
	a.smsSendMu.Lock()
	defer a.smsSendMu.Unlock()

	if err := a.ensureUSBAT(); err != nil {
		return 0, err
	}
	if a.usbAT == nil {
		return 0, errors.New("AT serial port is unavailable")
	}

	modeResponse, err := a.usbAT.Command("AT+CMGF=0", 5*time.Second)
	if err != nil {
		a.resetUSBATIfGone(err)
		return 0, fmt.Errorf("set SMS PDU mode: %w", err)
	}
	if !atProbeSucceeded(modeResponse) {
		return 0, fmt.Errorf("set SMS PDU mode failed: %s", modeResponse)
	}

	tpdus, tpduLengths, err := smscodec.BuildSubmitTPDUsWithOptions(phone, message, smsSubmitOptions(message))
	if err != nil {
		return 0, fmt.Errorf("build SMS PDU: %w", err)
	}
	for i, tpdu := range tpdus {
		pdu := append([]byte{0x00}, tpdu...)
		payload := []byte(strings.ToUpper(hex.EncodeToString(pdu)) + "\x1a")
		response, sendErr := a.usbAT.CommandWithPrompt(
			fmt.Sprintf("AT+CMGS=%d", tpduLengths[i]),
			payload,
			45*time.Second,
		)
		if sendErr != nil {
			a.resetUSBATIfGone(sendErr)
			return i, fmt.Errorf("send SMS segment %d/%d: %w", i+1, len(tpdus), sendErr)
		}
		if atResponseIsError(response) || !strings.Contains(response, "+CMGS:") || !atProbeSucceeded(response) {
			return i, fmt.Errorf("send SMS segment %d/%d failed: %s", i+1, len(tpdus), response)
		}
		if i+1 < len(tpdus) {
			time.Sleep(500 * time.Millisecond)
		}
	}
	return len(tpdus), nil
}

func smsSubmitOptions(message string) smscodec.SubmitOptions {
	for _, r := range message {
		if r > 127 {
			return smscodec.SubmitOptions{Encoding: smscodec.SMSEncodingUCS2}
		}
	}
	return smscodec.SubmitOptions{}
}

func (a *app) executeAT(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Command string `json:"command"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(body.Command)), "AT") {
		writeError(w, http.StatusBadRequest, "command must start with AT")
		return
	}
	if a.demo {
		response, _ := a.runATCommand(body.Command, 20*time.Second)
		writeJSON(w, http.StatusOK, map[string]string{"response": response})
		return
	}
	response, err := a.runATCommand(body.Command, 20*time.Second)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"response": response})
}

func (a *app) networkDiagnostic(w http.ResponseWriter, _ *http.Request) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("network diagnostic panic: %v", recovered)
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("network diagnostic failed: %v", recovered))
		}
	}()
	raw := make(map[string]string)
	errs := make(map[string]string)
	diag := networkDiagnostic{
		USBDevice:     a.currentUSBDevice(),
		MacInterfaces: discoverMacNetworkInterfaces(),
		DefaultRoute:  discoverMacDefaultRoute(),
		DHCPRepair:    a.currentDHCPRepairStatus(),
		Raw:           raw,
		Errors:        errs,
	}
	services, _ := discoverMacNetworkServices()
	diag.USBNetworkPresent = selectDJITrafficInterface(diag.USBDevice, diag.MacInterfaces, services) != ""

	commands := map[string]string{
		"usbnet":  `AT+QCFG="usbnet"`,
		"usbcfg":  `AT+QCFG="usbcfg"`,
		"cgdcont": `AT+CGDCONT?`,
		"cgact":   `AT+CGACT?`,
		"cgpaddr": `AT+CGPADDR=1`,
	}
	for key, command := range commands {
		resp, err := a.runATCommand(command, 8*time.Second)
		if err != nil {
			errs[key] = err.Error()
			continue
		}
		raw[key] = resp
	}

	diag.USBNetMode = parseUSBNetMode(raw["usbnet"])
	diag.USBCfg = parseUSBATPrefixed(raw["usbcfg"], "+QCFG:")
	diag.PDPContexts = parsePDPContexts(raw["cgdcont"])
	diag.ActiveContexts = parseActivePDPContexts(raw["cgact"])
	diag.PDPAddresses = parsePDPAddresses(raw["cgpaddr"])
	if len(errs) == 0 {
		diag.Errors = nil
	}
	writeJSON(w, http.StatusOK, diag)
}

func (a *app) networkTraffic(w http.ResponseWriter, _ *http.Request) {
	snapshot := networkTrafficSnapshot{
		SampledAtMS: time.Now().UnixMilli(),
	}

	interfaces := discoverMacNetworkInterfaces()
	services, _ := discoverMacNetworkServices()
	name := selectDJITrafficInterface(a.currentUSBDevice(), interfaces, services)
	if name == "" {
		snapshot.Error = "未检测到可用的 DJI 4G USB 网卡"
		writeJSON(w, http.StatusOK, snapshot)
		return
	}
	counters, err := discoverMacInterfaceCounters()
	if err != nil {
		snapshot.Interface = name
		snapshot.Error = err.Error()
		writeJSON(w, http.StatusOK, snapshot)
		return
	}
	current, ok := counters[name]
	if !ok {
		snapshot.Interface = name
		snapshot.Error = "未读取到网卡计数"
		writeJSON(w, http.StatusOK, snapshot)
		return
	}

	a.trafficMu.Lock()
	if a.trafficBaselines == nil {
		a.trafficBaselines = make(map[string]networkByteCounters)
	}
	baseline, exists := a.trafficBaselines[name]
	if !exists || current.RX < baseline.RX || current.TX < baseline.TX {
		baseline = current
		a.trafficBaselines[name] = baseline
	}
	a.trafficMu.Unlock()

	snapshot.Available = true
	snapshot.Interface = name
	snapshot.RXBytes = current.RX
	snapshot.TXBytes = current.TX
	snapshot.SessionRX, snapshot.SessionTX, snapshot.SessionTotal = sessionTrafficFromCounters(current, baseline)
	writeJSON(w, http.StatusOK, snapshot)
}

func sessionTrafficFromCounters(current, baseline networkByteCounters) (rx, tx, total uint64) {
	rx = current.RX - baseline.RX
	tx = current.TX - baseline.TX
	return rx, tx, rx + tx
}

func (a *app) check4GRoute(w http.ResponseWriter, _ *http.Request) {
	route := discoverMacDefaultRoute()
	interfaces := discoverMacNetworkInterfaces()
	if route.Interface == "" {
		writeJSON(w, http.StatusOK, networkCheckResult{
			OK:      false,
			Summary: "未读取到默认出口",
			Detail:  "macOS 没有返回 default route",
		})
		return
	}
	services, _ := discoverMacNetworkServices()
	physical := discoverMacPhysicalRoute(route, interfaces, services)
	writeJSON(w, http.StatusOK, networkCheckResult{
		OK:                physical.PhysicalKind == "cellular",
		Summary:           summarizeMacPhysicalRoute(physical),
		Detail:            detailMacPhysicalRoute(physical, interfaces),
		TunnelActive:      physical.TunnelActive,
		LogicalInterface:  physical.LogicalInterface,
		PhysicalInterface: physical.PhysicalInterface,
		PhysicalKind:      physical.PhysicalKind,
		PhysicalName:      physical.PhysicalName,
	})
}

func (a *app) checkProxyRoute(w http.ResponseWriter, _ *http.Request) {
	proxyURL, _ := url.Parse("http://127.0.0.1:7890")
	client := &http.Client{
		Timeout: 8 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
	}
	req, err := http.NewRequest(http.MethodHead, "https://www.google.com/generate_204", nil)
	if err != nil {
		writeJSON(w, http.StatusOK, networkCheckResult{OK: false, Summary: "代理检测请求创建失败", Detail: err.Error()})
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusOK, networkCheckResult{
			OK:      false,
			Summary: "代理未打通",
			Detail:  "127.0.0.1:7890 代理访问失败：" + err.Error(),
		})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent || (resp.StatusCode >= 200 && resp.StatusCode < 400) {
		writeJSON(w, http.StatusOK, networkCheckResult{
			OK:      true,
			Summary: "代理已打通",
			Detail:  fmt.Sprintf("127.0.0.1:7890 -> google generate_204 返回 %s", resp.Status),
		})
		return
	}
	writeJSON(w, http.StatusOK, networkCheckResult{
		OK:      false,
		Summary: "代理响应异常",
		Detail:  fmt.Sprintf("127.0.0.1:7890 返回 %s", resp.Status),
	})
}

func (a *app) setUSBNetMode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode int `json:"mode"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Mode < 0 || body.Mode > 3 {
		writeError(w, http.StatusBadRequest, "only usbnet mode 0, 1, 2 or 3 is allowed")
		return
	}
	command := fmt.Sprintf(`AT+QCFG="usbnet",%d`, body.Mode)
	response, err := a.runATCommand(command, 8*time.Second)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":         body.Mode,
		"response":     response,
		"needs_reboot": true,
	})
}

func (a *app) rebootModule(w http.ResponseWriter, _ *http.Request) {
	response, err := a.runATCommand("AT+CFUN=1,1", 3*time.Second)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"accepted": true,
		"response": response,
	})
}

func parseUSBNetMode(resp string) string {
	re := regexp.MustCompile(`\+QCFG:\s*"usbnet",(\d+)`)
	match := re.FindStringSubmatch(resp)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

func parsePDPContexts(resp string) []pdpContext {
	re := regexp.MustCompile(`\+CGDCONT:\s*(\d+),"([^"]*)","([^"]*)"`)
	var contexts []pdpContext
	for _, match := range re.FindAllStringSubmatch(resp, -1) {
		id, _ := strconv.Atoi(match[1])
		contexts = append(contexts, pdpContext{ID: id, PDN: match[2], APN: match[3]})
	}
	return contexts
}

func parseActivePDPContexts(resp string) []int {
	re := regexp.MustCompile(`\+CGACT:\s*(\d+),1`)
	var contexts []int
	for _, match := range re.FindAllStringSubmatch(resp, -1) {
		id, _ := strconv.Atoi(match[1])
		contexts = append(contexts, id)
	}
	return contexts
}

func parsePDPAddresses(resp string) []string {
	re := regexp.MustCompile(`\+CGPADDR:\s*\d+,"([^"]*)"`)
	match := re.FindStringSubmatch(resp)
	if len(match) != 2 {
		return nil
	}
	var out []string
	for _, part := range strings.Split(match[1], ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func discoverMacNetworkInterfaces() []macNetInterface {
	out, err := exec.Command("ifconfig").Output()
	if err != nil {
		return nil
	}
	var interfaces []macNetInterface
	for _, block := range splitIfconfigBlocks(string(out)) {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		lines := strings.Split(block, "\n")
		if len(lines) == 0 {
			continue
		}
		fields := strings.Fields(lines[0])
		if len(fields) == 0 {
			continue
		}
		name := strings.TrimSuffix(fields[0], ":")
		if name == "" || strings.HasPrefix(name, "lo") || strings.HasPrefix(name, "utun") {
			continue
		}
		item := macNetInterface{Name: name, Status: "unknown", Kind: classifyMacInterfaceName(name)}
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "status:") {
				item.Status = strings.TrimSpace(strings.TrimPrefix(line, "status:"))
			}
			if strings.HasPrefix(line, "inet ") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					item.IPv4 = fields[1]
				}
			}
		}
		interfaces = append(interfaces, item)
	}
	return interfaces
}

func discoverMacDefaultRoute() macDefaultRoute {
	return discoverMacRouteTo("default")
}

func discoverMacRouteTo(destination string) macDefaultRoute {
	out, err := exec.Command("route", "-n", "get", destination).Output()
	if err != nil {
		return macDefaultRoute{}
	}
	return parseMacRoute(string(out))
}

func parseMacRoute(output string) macDefaultRoute {
	var route macDefaultRoute
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "gateway:") {
			route.Gateway = strings.TrimSpace(strings.TrimPrefix(line, "gateway:"))
		}
		if strings.HasPrefix(line, "interface:") {
			route.Interface = strings.TrimSpace(strings.TrimPrefix(line, "interface:"))
		}
	}
	return route
}

func discoverMacNWIInfo() macNWIInfo {
	out, err := exec.Command("scutil", "--nwi").Output()
	if err != nil {
		return macNWIInfo{}
	}
	return parseMacNWIInfo(string(out))
}

func parseMacNWIInfo(output string) macNWIInfo {
	info := macNWIInfo{VPNServers: make(map[string][]string)}
	var current string
	interfaceLine := regexp.MustCompile(`^([A-Za-z0-9_.-]+)\s*:\s*flags`)
	for _, raw := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if match := interfaceLine.FindStringSubmatch(line); len(match) == 2 {
			current = match[1]
			continue
		}
		if strings.HasPrefix(line, "VPN server :") && current != "" {
			server := strings.TrimSpace(strings.TrimPrefix(line, "VPN server :"))
			if server != "" {
				info.VPNServers[current] = append(info.VPNServers[current], server)
			}
			continue
		}
		if strings.HasPrefix(line, "Network interfaces:") {
			info.Interfaces = strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "Network interfaces:")))
		}
	}
	return info
}

func discoverMacPhysicalRoute(logical macDefaultRoute, interfaces []macNetInterface, services []macNetworkService) macPhysicalRoute {
	nwi := discoverMacNWIInfo()
	endpointRoutes := make(map[string]macDefaultRoute)
	for _, server := range nwi.VPNServers[logical.Interface] {
		endpointRoutes[server] = discoverMacRouteTo(server)
	}
	return resolveMacPhysicalRoute(logical, interfaces, services, nwi, endpointRoutes)
}

func resolveMacPhysicalRoute(logical macDefaultRoute, interfaces []macNetInterface, services []macNetworkService, nwi macNWIInfo, endpointRoutes map[string]macDefaultRoute) macPhysicalRoute {
	result := macPhysicalRoute{
		TunnelActive:     isMacTunnelInterface(logical.Interface),
		LogicalInterface: logical.Interface,
		LogicalGateway:   logical.Gateway,
	}
	if !result.TunnelActive {
		return fillMacPhysicalRoute(result, logical, interfaces, services, "default-route")
	}
	for _, server := range nwi.VPNServers[logical.Interface] {
		candidate := endpointRoutes[server]
		if candidate.Interface != "" && !isMacTunnelInterface(candidate.Interface) && isActiveMacInterface(candidate.Interface, interfaces) {
			return fillMacPhysicalRoute(result, candidate, interfaces, services, "vpn-server-route")
		}
	}
	for _, name := range nwi.Interfaces {
		if name == logical.Interface || isMacTunnelInterface(name) || !isActiveMacInterface(name, interfaces) {
			continue
		}
		return fillMacPhysicalRoute(result, macDefaultRoute{Interface: name}, interfaces, services, "nwi-fallback")
	}
	return result
}

func fillMacPhysicalRoute(result macPhysicalRoute, route macDefaultRoute, interfaces []macNetInterface, services []macNetworkService, detection string) macPhysicalRoute {
	result.PhysicalInterface = route.Interface
	result.PhysicalGateway = route.Gateway
	result.Detection = detection
	result.PhysicalKind, result.PhysicalName = classifyMacPhysicalInterface(route.Interface, services)
	if result.PhysicalName == "" {
		result.PhysicalName = route.Interface
	}
	return result
}

func isMacTunnelInterface(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return strings.HasPrefix(name, "utun") || strings.HasPrefix(name, "ipsec") || strings.HasPrefix(name, "ppp") || strings.HasPrefix(name, "tun") || strings.HasPrefix(name, "tap")
}

func isActiveMacInterface(name string, interfaces []macNetInterface) bool {
	for _, item := range interfaces {
		if item.Name == name {
			return item.Status == "active" && item.IPv4 != ""
		}
	}
	return false
}

func classifyMacPhysicalInterface(name string, services []macNetworkService) (kind, displayName string) {
	for _, service := range services {
		if service.Disabled || service.Device != name {
			continue
		}
		port := strings.TrimSpace(service.HardwarePort)
		if isDJICellularService(service) {
			return "cellular", "4G 模块"
		}
		if strings.EqualFold(port, "Wi-Fi") || strings.Contains(strings.ToLower(port), "airport") {
			return "wifi", "Wi-Fi"
		}
		if strings.HasPrefix(name, "en") {
			if service.Name != "" {
				return "ethernet", service.Name
			}
			return "ethernet", "有线网络"
		}
	}
	if name == "en0" {
		return "wifi", "Wi-Fi"
	}
	if strings.HasPrefix(name, "en") {
		return "ethernet", "有线网络"
	}
	return "unknown", name
}

func summarizeMacPhysicalRoute(route macPhysicalRoute) string {
	label := route.PhysicalName
	if label == "" || route.PhysicalKind == "unknown" {
		label = "未知实体网络"
	}
	if route.TunnelActive {
		return "VPN 经 " + label
	}
	return "当前使用 " + label
}

func detailMacPhysicalRoute(route macPhysicalRoute, interfaces []macNetInterface) string {
	if route.PhysicalInterface == "" {
		return fmt.Sprintf("逻辑出口 %s，未能识别承载 VPN 的实体接口", route.LogicalInterface)
	}
	detail := fmt.Sprintf("逻辑出口 %s，实体出口 %s", route.LogicalInterface, route.PhysicalInterface)
	if route.PhysicalGateway != "" {
		detail += " -> " + route.PhysicalGateway
	}
	for _, item := range interfaces {
		if item.Name == route.PhysicalInterface && item.IPv4 != "" {
			detail += "，IP " + item.IPv4
			break
		}
	}
	return detail
}

func splitIfconfigBlocks(out string) []string {
	var blocks []string
	var current []string
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		if line[0] != '\t' && line[0] != ' ' && strings.Contains(line, ":") {
			if len(current) > 0 {
				blocks = append(blocks, strings.Join(current, "\n"))
			}
			current = []string{line}
			continue
		}
		if len(current) > 0 {
			current = append(current, line)
		}
	}
	if len(current) > 0 {
		blocks = append(blocks, strings.Join(current, "\n"))
	}
	return blocks
}

func classifyMacInterfaceName(name string) string {
	switch {
	case strings.HasPrefix(name, "en"):
		return "ethernet"
	case strings.HasPrefix(name, "bridge"):
		return "bridge"
	case strings.HasPrefix(name, "awdl") || strings.HasPrefix(name, "llw") || strings.HasPrefix(name, "ap"):
		return "apple-wireless"
	default:
		return "other"
	}
}

func selectDJITrafficInterface(device *usbDeviceStatus, interfaces []macNetInterface, services []macNetworkService) string {
	if device == nil {
		return ""
	}
	return selectDJICellularInterface(interfaces, services)
}

func discoverMacInterfaceCounters() (map[string]networkByteCounters, error) {
	out, err := exec.Command("netstat", "-ibn").Output()
	if err != nil {
		return nil, err
	}
	return parseMacInterfaceCounters(string(out)), nil
}

func parseMacInterfaceCounters(out string) map[string]networkByteCounters {
	counters := make(map[string]networkByteCounters)
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 || !strings.HasPrefix(fields[2], "<Link#") {
			continue
		}
		name := strings.TrimSuffix(fields[0], "*")
		rx, rxErr := strconv.ParseUint(fields[6], 10, 64)
		tx, txErr := strconv.ParseUint(fields[9], 10, 64)
		if name == "" || rxErr != nil || txErr != nil {
			continue
		}
		counters[name] = networkByteCounters{RX: rx, TX: tx}
	}
	return counters
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "request body must contain exactly one JSON value")
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
