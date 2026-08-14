//go:build darwin

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func discoverEgressRuntime() (egressRuntimeStatus, []string, error) {
	services, err := discoverMacNetworkServices()
	if err != nil {
		return egressRuntimeStatus{}, nil, err
	}
	interfaces := discoverMacNetworkInterfaces()
	logical := discoverMacDefaultRoute()
	physical := discoverMacPhysicalRoute(logical, interfaces, services)
	runtime := egressRuntimeStatus{
		LogicalInterface:  logical.Interface,
		TunnelActive:      isMacTunnelInterface(logical.Interface),
		PhysicalInterface: physical.PhysicalInterface,
		PhysicalName:      physical.PhysicalName,
		Client:            discoverEgressClientStatus(),
	}
	order := make([]string, 0, len(services))
	for _, service := range services {
		order = append(order, service.Name)
		if service.Disabled {
			continue
		}
		endpoint := readEgressEndpoint(service)
		if isDJICellularService(service) {
			if endpoint.Ready {
				runtime.Cellular = endpoint
			}
			continue
		}
		if !endpoint.Ready || isEgressVirtualService(service) {
			continue
		}
		if runtime.Corporate.Service == "" || service.Device == physical.PhysicalInterface {
			runtime.Corporate = endpoint
		}
	}
	return runtime, order, nil
}

func readEgressEndpoint(service macNetworkService) egressEndpoint {
	endpoint := egressEndpoint{Service: service.Name, Interface: service.Device}
	out, err := exec.Command("/usr/sbin/networksetup", "-getinfo", service.Name).CombinedOutput()
	if err != nil {
		return endpoint
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "IP address":
			endpoint.Address = strings.TrimSpace(value)
		case "Router":
			endpoint.Gateway = strings.TrimSpace(value)
		}
	}
	endpoint.Ready = endpoint.Interface != "" && endpoint.Address != "" && endpoint.Gateway != "" && endpoint.Address != "none" && endpoint.Gateway != "none"
	return endpoint
}

func isEgressVirtualService(service macNetworkService) bool {
	text := strings.ToLower(service.Name + " " + service.HardwarePort + " " + service.Device)
	return strings.Contains(text, "vpn") || strings.Contains(text, "stash") || strings.Contains(text, "thunderbolt bridge") || strings.HasPrefix(service.Device, "utun")
}

func discoverEgressClientStatus() egressClientStatus {
	status := egressClientStatus{Name: "Stash", Platform: "unknown", Detail: "未检测到正在运行的 Stash"}
	processes, _ := exec.Command("/usr/bin/pgrep", "-x", "Stash").Output()
	for _, pid := range strings.Fields(string(processes)) {
		command, _ := exec.Command("/bin/ps", "-p", pid, "-o", "command=").Output()
		path := strings.TrimSpace(string(command))
		marker := strings.Index(path, "/Stash.app/")
		if marker < 0 {
			continue
		}
		bundle := path[:marker+len("/Stash.app")]
		status.Running = true
		info := filepath.Join(bundle, "Info.plist")
		if _, err := os.Stat(info); err != nil {
			info = filepath.Join(bundle, "Contents", "Info.plist")
		}
		platform := plistRaw(info, "DTPlatformName")
		status.Version = plistRaw(info, "CFBundleShortVersionString")
		if strings.EqualFold(platform, "macosx") {
			status.Platform = "macOS"
			status.ProcessRulesSupported = true
			status.Detail = "原生 Stash Mac，可执行应用进程规则"
		} else if strings.EqualFold(platform, "iphoneos") {
			status.Platform = "iOS on Apple Silicon"
			status.Detail = "当前是 iOS/iPadOS 版 Stash，应用进程规则不会生效"
		} else {
			status.Platform = platform
			status.Detail = "无法确认该 Stash 客户端是否支持应用进程规则"
		}
		return status
	}
	return status
}

func plistRaw(path, key string) string {
	if path == "" {
		return ""
	}
	out, err := exec.Command("/usr/bin/plutil", "-extract", key, "raw", "-o", "-", path).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func discoverEgressApplications() ([]egressInstalledApplication, error) {
	roots := []string{"/Applications", "/System/Applications"}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, filepath.Join(home, "Applications"))
	}
	seen := map[string]bool{}
	apps := make([]egressInstalledApplication, 0, 80)
	for _, root := range roots {
		rootDepth := strings.Count(filepath.Clean(root), string(os.PathSeparator))
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil || entry == nil {
				return nil
			}
			if path == root {
				return nil
			}
			depth := strings.Count(filepath.Clean(path), string(os.PathSeparator)) - rootDepth
			if entry.IsDir() && strings.HasSuffix(strings.ToLower(entry.Name()), ".app") {
				if !seen[path] {
					seen[path] = true
					apps = append(apps, readEgressApplication(path))
				}
				return filepath.SkipDir
			}
			if entry.IsDir() && depth < 3 {
				return nil
			}
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		})
	}
	sort.Slice(apps, func(i, j int) bool {
		return strings.ToLower(apps[i].Name) < strings.ToLower(apps[j].Name)
	})
	return apps, nil
}

func readEgressApplication(path string) egressInstalledApplication {
	info := filepath.Join(path, "Contents", "Info.plist")
	if _, err := os.Stat(info); err != nil {
		info = filepath.Join(path, "Info.plist")
	}
	name := plistRaw(info, "CFBundleDisplayName")
	if name == "" {
		name = plistRaw(info, "CFBundleName")
	}
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	executableName := plistRaw(info, "CFBundleExecutable")
	executable := ""
	if executableName != "" {
		candidate := filepath.Join(path, "Contents", "MacOS", executableName)
		if _, err := os.Stat(candidate); err != nil {
			candidate = filepath.Join(path, executableName)
		}
		executable = candidate
	}
	return egressInstalledApplication{Name: name, BundleID: plistRaw(info, "CFBundleIdentifier"), Path: path, Executable: executable}
}

func applyEgressPlatform(_ egressPolicyStore, preview egressPreview) (egressAppliedState, error) {
	state := egressAppliedState{AppliedAt: time.Now()}
	_, order, err := discoverEgressRuntime()
	if err != nil {
		return state, err
	}
	state.OriginalServiceOrder = order
	directory, err := os.MkdirTemp("", "djonehub-egress-")
	if err != nil {
		return state, err
	}
	defer os.RemoveAll(directory)
	resultPath := filepath.Join(directory, "routes.tsv")
	scriptPath := filepath.Join(directory, "apply.sh")
	var script strings.Builder
	script.WriteString("#!/bin/zsh\nset -u\n")
	if len(preview.ServiceOrder) > 0 {
		script.WriteString("/usr/sbin/networksetup -ordernetworkservices")
		for _, service := range preview.ServiceOrder {
			script.WriteByte(' ')
			script.WriteString(shellQuote(service))
		}
		script.WriteString("\n")
	}
	for _, plan := range expandEgressPlans(preview.Routes) {
		flag := "-net"
		if plan.Kind == "ip" {
			flag = "-host"
		}
		script.WriteString("if /sbin/route -n add " + flag + " " + shellQuote(plan.Target) + " " + shellQuote(plan.Gateway) + " >/dev/null 2>&1; then\n")
		record := strings.Join([]string{plan.Kind, plan.Target, plan.Policy, plan.Interface, plan.Gateway}, "\t") + "\n"
		script.WriteString("  /usr/bin/printf %s " + shellQuote(record) + " >> " + shellQuote(resultPath) + "\nfi\n")
	}
	script.WriteString("/usr/bin/touch " + shellQuote(resultPath) + "\n/bin/chmod 0644 " + shellQuote(resultPath) + "\n")
	if err := os.WriteFile(scriptPath, []byte(script.String()), 0o700); err != nil {
		return state, err
	}
	if err := runAdministratorScript(scriptPath); err != nil {
		return state, err
	}
	file, err := os.Open(resultPath)
	if err != nil {
		return state, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		parts := strings.Split(scanner.Text(), "\t")
		if len(parts) != 5 {
			continue
		}
		state.AddedRoutes = append(state.AddedRoutes, egressRoutePlan{Kind: parts[0], Target: parts[1], Policy: parts[2], Interface: parts[3], Gateway: parts[4]})
	}
	return state, scanner.Err()
}

func restoreEgressPlatform(state egressAppliedState) error {
	directory, err := os.MkdirTemp("", "djonehub-egress-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	scriptPath := filepath.Join(directory, "restore.sh")
	var script strings.Builder
	script.WriteString("#!/bin/zsh\nset -u\n")
	for _, plan := range state.AddedRoutes {
		flag := "-net"
		if plan.Kind == "ip" {
			flag = "-host"
		}
		script.WriteString("/sbin/route -n delete " + flag + " " + shellQuote(plan.Target) + " >/dev/null 2>&1 || true\n")
	}
	if len(state.OriginalServiceOrder) > 0 {
		script.WriteString("/usr/sbin/networksetup -ordernetworkservices")
		for _, service := range state.OriginalServiceOrder {
			script.WriteByte(' ')
			script.WriteString(shellQuote(service))
		}
		script.WriteString("\n")
	}
	if err := os.WriteFile(scriptPath, []byte(script.String()), 0o700); err != nil {
		return err
	}
	return runAdministratorScript(scriptPath)
}

func expandEgressPlans(plans []egressRoutePlan) []egressRoutePlan {
	result := make([]egressRoutePlan, 0, len(plans))
	seen := map[string]bool{}
	for _, plan := range plans {
		targets := []string{plan.Target}
		kind := plan.Kind
		if plan.Kind == "domain" {
			targets = plan.Resolved
			kind = "ip"
		}
		if plan.Kind == "ip" {
			kind = "ip"
		}
		for _, target := range targets {
			key := kind + "\x00" + target
			if target == "" || plan.Gateway == "" || seen[key] {
				continue
			}
			seen[key] = true
			copy := plan
			copy.Kind = kind
			copy.Target = target
			copy.Resolved = nil
			result = append(result, copy)
		}
	}
	return result
}

func runAdministratorScript(path string) error {
	command := "/bin/zsh " + shellQuote(path)
	appleScript := "do shell script " + appleScriptString(command) + " with administrator privileges"
	out, err := exec.Command("/usr/bin/osascript", "-e", appleScript).CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("macOS 管理员授权未完成：%s", message)
	}
	return nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func appleScriptString(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
}
