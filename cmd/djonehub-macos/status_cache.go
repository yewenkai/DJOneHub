package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/iniwex5/vohive/internal/modem"
)

const defaultStatusPollInterval = 10 * time.Second

func (a *app) startStatusSampler(ctx context.Context) {
	interval := a.statusPollInterval
	if interval <= 0 {
		interval = defaultStatusPollInterval
	}
	a.statusMu.Lock()
	a.statusPollInterval = interval
	a.statusMu.Unlock()

	a.refreshStatus()
	timer := time.NewTicker(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			a.refreshStatus()
		}
	}
}

func (a *app) refreshStatus() {
	a.statusRefreshMu.Lock()
	if a.statusRefreshing {
		done := a.statusRefreshDone
		a.statusRefreshMu.Unlock()
		<-done
		return
	}
	a.statusRefreshing = true
	a.statusRefreshDone = make(chan struct{})
	done := a.statusRefreshDone
	a.statusRefreshMu.Unlock()

	snapshot, err := a.safeCollectStatus()
	a.statusMu.Lock()
	if err != nil {
		a.statusLastError = err.Error()
	} else {
		a.statusSnapshot = snapshot
		a.statusSampledAt = time.Now()
		a.statusLastError = ""
	}
	a.statusMu.Unlock()

	a.statusRefreshMu.Lock()
	a.statusRefreshing = false
	close(done)
	a.statusRefreshMu.Unlock()
}

func (a *app) safeCollectStatus() (snapshot any, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("modem status sampler panic: %v", recovered)
		}
	}()
	return a.collectStatus()
}

func (a *app) collectStatus() (any, error) {
	if a.statusCollectFn != nil {
		return a.statusCollectFn()
	}
	return a.collectStatusFromModem()
}

func (a *app) collectStatusFromModem() (any, error) {
	if a.demo {
		return modem.DeviceStatus{
			IMEI:          "867400000000001",
			Firmware:      "EG25GGBR07A08M2G",
			ICCID:         "89860123456789012345",
			IMSI:          "460001234567890",
			Operator:      "China Mobile",
			SimInserted:   true,
			SignalDBM:     -73,
			SignalRSRP:    -96,
			SignalRSRQ:    -9,
			RegStatus:     1,
			RegStatusText: "已注册",
			NetworkMode:   "LTE",
			NetworkDuplex: "FDD",
			RadioBand:     "B3",
			USBNetMode:    0,
		}, nil
	}
	if a.modem != nil {
		return a.modem.GetFullStatus(), nil
	}

	// A libusb handle may survive a physical unplug. Refresh the macOS USB
	// inventory before using it so the cache never retains a detached module.
	if a.usbAT != nil && a.currentUSBDevice() == nil {
		a.markUSBATDetached("DJI USB device disconnected")
	}
	if err := a.ensureUSBAT(); err != nil {
		log.Printf("USB AT retry failed: %v", err)
	}
	if a.usbAT != nil {
		status, err := a.usbATStatus()
		if err == nil {
			return status, nil
		}
		a.resetUSBATIfGone(err)
		log.Printf("USB AT status failed: %v", err)
	}

	usbDevice := a.currentUSBDevice()
	summary := "未发现 AT 串口"
	operator := "未连接"
	network := "不可用"
	if usbDevice != nil {
		summary = fmt.Sprintf(
			"%s %s (%s:%s)",
			usbDevice.Vendor,
			usbDevice.Product,
			usbDevice.VendorID,
			usbDevice.ProductID,
		)
		operator = "已检测到 USB 设备"
		network = usbDevice.Mode
	}
	return map[string]any{
		"operator":        operator,
		"signal_dbm":      nil,
		"network_mode":    network,
		"sim_inserted":    false,
		"hardware_status": summary,
		"discovery_error": a.discoveryError,
		"usb_device":      usbDevice,
	}, nil
}

func (a *app) cachedStatus() (snapshot any, sampledAt time.Time, err error) {
	a.statusMu.RLock()
	defer a.statusMu.RUnlock()
	if a.statusSnapshot == nil {
		if a.statusLastError == "" {
			return nil, time.Time{}, errors.New("modem status has not been sampled")
		}
		return nil, time.Time{}, errors.New(a.statusLastError)
	}
	return a.statusSnapshot, a.statusSampledAt, nil
}

func (a *app) status(w http.ResponseWriter, _ *http.Request) {
	snapshot, sampledAt, err := a.cachedStatus()
	if err != nil {
		a.refreshStatus()
		snapshot, sampledAt, err = a.cachedStatus()
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	w.Header().Set("X-DJOneHub-Status-Sampled-At", sampledAt.UTC().Format(time.RFC3339Nano))
	writeJSON(w, http.StatusOK, snapshot)
}
