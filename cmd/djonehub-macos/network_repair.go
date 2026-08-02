package main

import (
	"log"
	"time"
)

type dhcpRepairStatus struct {
	Running     bool      `json:"running"`
	Trigger     string    `json:"trigger,omitempty"`
	Service     string    `json:"service,omitempty"`
	Address     string    `json:"address,omitempty"`
	Changed     bool      `json:"changed"`
	LastAttempt time.Time `json:"last_attempt,omitempty"`
	LastSuccess time.Time `json:"last_success,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
}

func (a *app) scheduleCellularDHCPRepair(trigger string) {
	if a.demo || a.modem != nil || !a.networkRepairMu.TryLock() {
		return
	}
	a.dhcpStatusMu.Lock()
	a.dhcpStatus.Running = true
	a.dhcpStatus.Trigger = trigger
	a.dhcpStatus.LastAttempt = time.Now()
	a.dhcpStatus.LastError = ""
	a.dhcpStatusMu.Unlock()

	go func() {
		defer a.networkRepairMu.Unlock()
		service, address, changed, err := repairCellularDHCP()
		now := time.Now()
		a.dhcpStatusMu.Lock()
		a.dhcpStatus.Running = false
		a.dhcpStatus.Service = service
		a.dhcpStatus.Address = address
		a.dhcpStatus.Changed = changed
		if err != nil {
			a.dhcpStatus.LastError = err.Error()
		} else {
			a.dhcpStatus.LastError = ""
			a.dhcpStatus.LastSuccess = now
		}
		a.dhcpStatusMu.Unlock()
		if err != nil {
			log.Printf("cellular DHCP repair (%s) failed: %v", trigger, err)
			return
		}
		if changed {
			log.Printf("cellular DHCP repair (%s): %s -> %s", trigger, service, address)
		}
	}()
}

func (a *app) currentDHCPRepairStatus() dhcpRepairStatus {
	a.dhcpStatusMu.RLock()
	defer a.dhcpStatusMu.RUnlock()
	return a.dhcpStatus
}
