//go:build darwin

package main

import (
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

type macNetworkService struct {
	Name         string
	HardwarePort string
	Device       string
	Disabled     bool
}

type macIPv4ServiceInfo struct {
	Address string
	Subnet  string
}

func repairCellularDHCP() (service string, address string, changed bool, err error) {
	services, err := discoverMacNetworkServices()
	if err != nil {
		return "", "", false, err
	}
	for _, candidate := range services {
		if isDJICellularService(candidate) && !candidate.Disabled {
			service = candidate.Name
			break
		}
	}
	if service == "" {
		return "", "", false, fmt.Errorf("未找到已启用的 Baiwang 蜂窝网络服务")
	}
	if info, readErr := readMacIPv4ServiceInfo(service); readErr == nil {
		return service, info.Address, false, nil
	}

	for attempt := 1; attempt <= 2; attempt++ {
		if renewErr := renewMacNetworkServiceDHCP(service); renewErr != nil {
			return service, "", false, renewErr
		}
		info, waitErr := waitForMacIPv4Service(service, 15*time.Second)
		if waitErr == nil {
			return service, info.Address, true, nil
		}
		if attempt == 2 {
			return service, "", false, waitErr
		}
	}
	return service, "", false, fmt.Errorf("DHCP 自愈未完成")
}

func discoverMacNetworkServices() ([]macNetworkService, error) {
	out, err := exec.Command("/usr/sbin/networksetup", "-listnetworkserviceorder").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("读取 macOS 网络服务失败: %s", strings.TrimSpace(string(out)))
	}
	return parseMacNetworkServices(string(out)), nil
}

func parseMacNetworkServices(output string) []macNetworkService {
	header := regexp.MustCompile(`^\((\*|\d+)\)\s+(.+)$`)
	detail := regexp.MustCompile(`^\(Hardware Port:\s*([^,]+),\s*Device:\s*([^)]+)\)$`)
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	services := make([]macNetworkService, 0)
	for index := 0; index+1 < len(lines); index++ {
		match := header.FindStringSubmatch(strings.TrimSpace(lines[index]))
		if len(match) != 3 {
			continue
		}
		info := detail.FindStringSubmatch(strings.TrimSpace(lines[index+1]))
		if len(info) != 3 {
			continue
		}
		services = append(services, macNetworkService{
			Name: strings.TrimSpace(match[2]), HardwarePort: strings.TrimSpace(info[1]),
			Device: strings.TrimSpace(info[2]), Disabled: match[1] == "*",
		})
	}
	return services
}

func isDJICellularService(service macNetworkService) bool {
	return strings.EqualFold(strings.TrimSpace(service.HardwarePort), "Baiwang") &&
		regexp.MustCompile(`^en\d+$`).MatchString(service.Device)
}

func renewMacNetworkServiceDHCP(name string) error {
	out, err := exec.Command("/usr/sbin/networksetup", "-setdhcp", name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("续租 %s DHCP 失败: %s", name, strings.TrimSpace(string(out)))
	}
	return nil
}

func readMacIPv4ServiceInfo(name string) (macIPv4ServiceInfo, error) {
	out, err := exec.Command("/usr/sbin/networksetup", "-getinfo", name).CombinedOutput()
	if err != nil {
		return macIPv4ServiceInfo{}, fmt.Errorf("读取 %s IPv4 失败: %s", name, strings.TrimSpace(string(out)))
	}
	info := parseMacIPv4ServiceInfo(string(out))
	address := net.ParseIP(info.Address)
	if address == nil || address.IsUnspecified() || address.IsLinkLocalUnicast() || net.ParseIP(info.Subnet) == nil {
		return macIPv4ServiceInfo{}, fmt.Errorf("%s 尚未取得有效 IPv4 地址", name)
	}
	return info, nil
}

func parseMacIPv4ServiceInfo(output string) macIPv4ServiceInfo {
	var info macIPv4ServiceInfo
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "IP address":
			info.Address = strings.TrimSpace(value)
		case "Subnet mask":
			info.Subnet = strings.TrimSpace(value)
		}
	}
	return info
}

func waitForMacIPv4Service(name string, timeout time.Duration) (macIPv4ServiceInfo, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		info, err := readMacIPv4ServiceInfo(name)
		if err == nil {
			return info, nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return macIPv4ServiceInfo{}, lastErr
		}
		time.Sleep(500 * time.Millisecond)
	}
}
