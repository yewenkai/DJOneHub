package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	egressUnderlayCorporate = "corporate"
	egressUnderlayCellular  = "cellular"

	egressDirectCorporate = "corporate_direct"
	egressDirectCellular  = "cellular_direct"
)

var (
	egressDomainPattern = regexp.MustCompile(`^(?i:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+)$`)
	egressFakeIPPrefix  = netip.MustParsePrefix("198.18.0.0/15")
)

type egressDestinationRule struct {
	Kind   string `json:"kind"`
	Value  string `json:"value"`
	Policy string `json:"policy"`
}

type egressApplicationRule struct {
	Name       string `json:"name"`
	BundleID   string `json:"bundle_id,omitempty"`
	Path       string `json:"path"`
	Executable string `json:"executable,omitempty"`
	Policy     string `json:"policy"`
}

type egressPolicyStore struct {
	Enabled        bool                    `json:"enabled"`
	GlobalUnderlay string                  `json:"global_underlay"`
	VPNPolicy      string                  `json:"vpn_policy"`
	CorporateCIDRs []string                `json:"corporate_cidrs"`
	Destinations   []egressDestinationRule `json:"destinations"`
	Applications   []egressApplicationRule `json:"applications"`
	UpdatedAt      time.Time               `json:"updated_at,omitempty"`
}

type egressEndpoint struct {
	Service   string `json:"service,omitempty"`
	Interface string `json:"interface,omitempty"`
	Address   string `json:"address,omitempty"`
	Gateway   string `json:"gateway,omitempty"`
	Ready     bool   `json:"ready"`
}

type egressClientStatus struct {
	Name                  string `json:"name"`
	Platform              string `json:"platform"`
	Version               string `json:"version,omitempty"`
	Running               bool   `json:"running"`
	ProcessRulesSupported bool   `json:"process_rules_supported"`
	Detail                string `json:"detail"`
}

type egressRuntimeStatus struct {
	LogicalInterface  string             `json:"logical_interface,omitempty"`
	PhysicalInterface string             `json:"physical_interface,omitempty"`
	PhysicalName      string             `json:"physical_name,omitempty"`
	TunnelActive      bool               `json:"tunnel_active"`
	Corporate         egressEndpoint     `json:"corporate"`
	Cellular          egressEndpoint     `json:"cellular"`
	Client            egressClientStatus `json:"client"`
}

type egressRoutePlan struct {
	Target    string   `json:"target"`
	Kind      string   `json:"kind"`
	Policy    string   `json:"policy"`
	Interface string   `json:"interface"`
	Gateway   string   `json:"gateway"`
	Resolved  []string `json:"resolved,omitempty"`
}

type egressPreview struct {
	Runtime          egressRuntimeStatus `json:"runtime"`
	Routes           []egressRoutePlan   `json:"routes"`
	ServiceOrder     []string            `json:"service_order"`
	Override         string              `json:"stash_override"`
	Warnings         []string            `json:"warnings"`
	CanApply         bool                `json:"can_apply"`
	CanRestore       bool                `json:"can_restore"`
	ApplicationCount int                 `json:"application_count"`
}

type egressPolicyResponse struct {
	Policy  egressPolicyStore `json:"policy"`
	Preview egressPreview     `json:"preview"`
}

type egressInstalledApplication struct {
	Name       string `json:"name"`
	BundleID   string `json:"bundle_id,omitempty"`
	Path       string `json:"path"`
	Executable string `json:"executable,omitempty"`
}

type egressAppliedState struct {
	AppliedAt            time.Time         `json:"applied_at"`
	OriginalServiceOrder []string          `json:"original_service_order"`
	AddedRoutes          []egressRoutePlan `json:"added_routes"`
}

func defaultEgressPolicy() egressPolicyStore {
	return egressPolicyStore{
		Enabled:        true,
		GlobalUnderlay: egressUnderlayCorporate,
		VPNPolicy:      "Proxy",
		CorporateCIDRs: []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"},
	}
}

func (a *app) initEgressPaths() error {
	if a.egressPath != "" {
		return nil
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return fmt.Errorf("定位出口策略目录：%w", err)
	}
	directory := filepath.Join(configDir, "DJOneHub")
	a.egressPath = filepath.Join(directory, "egress-policy.json")
	a.egressApplied = filepath.Join(directory, "egress-policy-applied.json")
	return nil
}

func (a *app) loadEgressPolicy() (egressPolicyStore, error) {
	a.egressMu.Lock()
	defer a.egressMu.Unlock()
	if a.egressLoaded {
		return a.egressPolicy, nil
	}
	if err := a.initEgressPaths(); err != nil {
		return egressPolicyStore{}, err
	}
	policy := defaultEgressPolicy()
	data, err := os.ReadFile(a.egressPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return egressPolicyStore{}, fmt.Errorf("读取出口策略：%w", err)
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &policy); err != nil {
			return egressPolicyStore{}, fmt.Errorf("解析出口策略：%w", err)
		}
	}
	normalized, err := normalizeEgressPolicy(policy)
	if err != nil {
		return egressPolicyStore{}, fmt.Errorf("校验已保存的出口策略：%w", err)
	}
	a.egressPolicy = normalized
	a.egressLoaded = true
	return normalized, nil
}

func (a *app) persistEgressPolicy(policy egressPolicyStore) error {
	if err := a.initEgressPaths(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.egressPath), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return err
	}
	temporary := a.egressPath + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, a.egressPath); err != nil {
		return err
	}
	a.egressMu.Lock()
	a.egressPolicy = policy
	a.egressLoaded = true
	a.egressMu.Unlock()
	return nil
}

func normalizeEgressPolicy(policy egressPolicyStore) (egressPolicyStore, error) {
	if policy.GlobalUnderlay == "" {
		policy.GlobalUnderlay = egressUnderlayCorporate
	}
	if policy.GlobalUnderlay != egressUnderlayCorporate && policy.GlobalUnderlay != egressUnderlayCellular {
		return policy, errors.New("全局 VPN 底层出口必须是公司网络或 4G 模块")
	}
	policy.VPNPolicy = strings.TrimSpace(policy.VPNPolicy)
	if policy.VPNPolicy == "" {
		policy.VPNPolicy = "Proxy"
	}
	if strings.ContainsAny(policy.VPNPolicy, "\r\n") || len(policy.VPNPolicy) > 80 {
		return policy, errors.New("Stash VPN 策略组名称不合法")
	}

	return normalizeEgressPolicyLists(policy, map[string]bool{})
}

func normalizeEgressPolicyLists(policy egressPolicyStore, seenCIDRs map[string]bool) (egressPolicyStore, error) {
	// JSON decoding produces independent slices; copy before replacing them.
	rawCIDRs := append([]string(nil), policy.CorporateCIDRs...)
	policy.CorporateCIDRs = nil
	for _, raw := range rawCIDRs {
		prefix, err := validateEgressPrefix(raw)
		if err != nil {
			return policy, fmt.Errorf("公司内网 CIDR %q：%w", raw, err)
		}
		value := prefix.String()
		if !seenCIDRs[value] {
			seenCIDRs[value] = true
			policy.CorporateCIDRs = append(policy.CorporateCIDRs, value)
		}
	}

	seenDestinations := map[string]bool{}
	normalizedDestinations := make([]egressDestinationRule, 0, len(policy.Destinations))
	for _, rule := range policy.Destinations {
		rule.Kind = strings.ToLower(strings.TrimSpace(rule.Kind))
		rule.Value = strings.TrimSpace(strings.ToLower(rule.Value))
		if rule.Policy != egressDirectCorporate && rule.Policy != egressDirectCellular {
			return policy, fmt.Errorf("目标 %q 的出口无效", rule.Value)
		}
		switch rule.Kind {
		case "domain":
			rule.Value = strings.TrimPrefix(rule.Value, "*.")
			if !egressDomainPattern.MatchString(rule.Value) {
				return policy, fmt.Errorf("域名 %q 不合法", rule.Value)
			}
		case "ip":
			address, err := netip.ParseAddr(rule.Value)
			if err != nil || !address.Is4() || !validEgressAddress(address) {
				return policy, fmt.Errorf("IPv4 地址 %q 不合法", rule.Value)
			}
			rule.Value = address.String()
		case "cidr":
			prefix, err := validateEgressPrefix(rule.Value)
			if err != nil {
				return policy, fmt.Errorf("CIDR %q：%w", rule.Value, err)
			}
			rule.Value = prefix.String()
		default:
			return policy, fmt.Errorf("目标 %q 的类型必须是 domain、ip 或 cidr", rule.Value)
		}
		key := rule.Kind + "\x00" + rule.Value
		if !seenDestinations[key] {
			seenDestinations[key] = true
			normalizedDestinations = append(normalizedDestinations, rule)
		}
	}
	policy.Destinations = normalizedDestinations

	seenApps := map[string]bool{}
	normalizedApps := make([]egressApplicationRule, 0, len(policy.Applications))
	for _, rule := range policy.Applications {
		rule.Name = strings.TrimSpace(rule.Name)
		rule.BundleID = strings.TrimSpace(rule.BundleID)
		rule.Path = filepath.Clean(strings.TrimSpace(rule.Path))
		rule.Executable = filepath.Clean(strings.TrimSpace(rule.Executable))
		if !filepath.IsAbs(rule.Path) || !strings.HasSuffix(strings.ToLower(rule.Path), ".app") {
			return policy, fmt.Errorf("应用 %q 的路径不合法", rule.Name)
		}
		if rule.Executable != "." && rule.Executable != "" && !filepath.IsAbs(rule.Executable) {
			return policy, fmt.Errorf("应用 %q 的进程路径不合法", rule.Name)
		}
		switch rule.Policy {
		case "follow_global", egressDirectCorporate, egressDirectCellular, "vpn":
		default:
			return policy, fmt.Errorf("应用 %q 的出口策略无效", rule.Name)
		}
		key := strings.ToLower(rule.Path)
		if !seenApps[key] {
			seenApps[key] = true
			normalizedApps = append(normalizedApps, rule)
		}
	}
	policy.Applications = normalizedApps
	return policy, nil
}

func validateEgressPrefix(raw string) (netip.Prefix, error) {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
	if err != nil || !prefix.Addr().Is4() {
		return netip.Prefix{}, errors.New("必须是 IPv4 CIDR")
	}
	prefix = prefix.Masked()
	if prefix.Bits() == 0 || !validEgressAddress(prefix.Addr()) {
		return netip.Prefix{}, errors.New("不允许默认、回环、链路本地或组播网段")
	}
	return prefix, nil
}

func validEgressAddress(address netip.Addr) bool {
	return address.Is4() && !address.IsUnspecified() && !address.IsLoopback() && !address.IsLinkLocalUnicast() && !address.IsMulticast() && !egressFakeIPPrefix.Contains(address)
}

func (a *app) egressPolicyStatus(w http.ResponseWriter, _ *http.Request) {
	policy, err := a.loadEgressPolicy()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	preview, err := a.buildEgressPreview(policy)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, egressPolicyResponse{Policy: policy, Preview: preview})
}

func (a *app) saveEgressPolicy(w http.ResponseWriter, r *http.Request) {
	var policy egressPolicyStore
	if !decodeJSON(w, r, &policy) {
		return
	}
	normalized, err := normalizeEgressPolicy(policy)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	normalized.UpdatedAt = time.Now()
	if err := a.persistEgressPolicy(normalized); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	preview, err := a.buildEgressPreview(normalized)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, egressPolicyResponse{Policy: normalized, Preview: preview})
}

func (a *app) previewEgressPolicy(w http.ResponseWriter, r *http.Request) {
	var policy egressPolicyStore
	if !decodeJSON(w, r, &policy) {
		return
	}
	normalized, err := normalizeEgressPolicy(policy)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	preview, err := a.buildEgressPreview(normalized)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (a *app) egressApplications(w http.ResponseWriter, _ *http.Request) {
	apps, err := discoverEgressApplications()
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"applications": apps})
}

func (a *app) buildEgressPreview(policy egressPolicyStore) (egressPreview, error) {
	runtime, order, err := discoverEgressRuntime()
	if err != nil {
		return egressPreview{}, err
	}
	preview := egressPreview{Runtime: runtime, ApplicationCount: len(policy.Applications)}
	preview.CanRestore = a.egressAppliedStateExists()
	if policy.GlobalUnderlay == egressUnderlayCellular {
		preview.ServiceOrder = moveServiceFirst(order, runtime.Cellular.Service)
	} else {
		preview.ServiceOrder = moveServiceFirst(order, runtime.Corporate.Service)
	}
	if !policy.Enabled {
		preview.Warnings = append(preview.Warnings, "出口策略当前已关闭；保存配置不会生成或应用路由。")
	}
	if !runtime.Corporate.Ready {
		preview.Warnings = append(preview.Warnings, "未找到可用的公司网络接口，无法应用公司直连规则。")
	}
	if !runtime.Cellular.Ready {
		preview.Warnings = append(preview.Warnings, "4G 模块尚未取得 IPv4 网关，无法应用 4G 直连规则。")
	}
	if len(policy.Applications) > 0 && !runtime.Client.ProcessRulesSupported {
		preview.Warnings = append(preview.Warnings, "当前是 iOS/iPadOS 版 Stash，macOS 上会忽略 PROCESS-NAME/PROCESS-PATH；应用选择已保存，但暂不生效。")
	}
	for _, cidr := range policy.CorporateCIDRs {
		preview.Routes = append(preview.Routes, routePlanFor(cidr, "cidr", egressDirectCorporate, runtime, nil))
	}
	for _, rule := range policy.Destinations {
		var resolved []string
		if rule.Kind == "domain" {
			resolved = resolveEgressDomain(rule.Value)
			if len(resolved) == 0 {
				preview.Warnings = append(preview.Warnings, "域名 "+rule.Value+" 当前未解析到可直接路由的真实 IPv4（常见原因是 Stash Fake-IP）；仍会生成域名规则，但不会伪写主机路由。")
			}
		}
		preview.Routes = append(preview.Routes, routePlanFor(rule.Value, rule.Kind, rule.Policy, runtime, resolved))
	}
	preview.Override = renderEgressStashOverride(policy, runtime)
	preview.CanApply = policy.Enabled && runtime.Corporate.Ready && runtime.Cellular.Ready
	return preview, nil
}

func routePlanFor(target, kind, policy string, runtime egressRuntimeStatus, resolved []string) egressRoutePlan {
	endpoint := runtime.Corporate
	if policy == egressDirectCellular {
		endpoint = runtime.Cellular
	}
	return egressRoutePlan{Target: target, Kind: kind, Policy: policy, Interface: endpoint.Interface, Gateway: endpoint.Gateway, Resolved: resolved}
}

func resolveEgressDomain(domain string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", domain)
	if err != nil {
		return nil
	}
	values := make([]string, 0, len(addresses))
	seen := map[string]bool{}
	for _, address := range addresses {
		if address.Is4() && validEgressAddress(address) && !seen[address.String()] {
			seen[address.String()] = true
			values = append(values, address.String())
		}
	}
	sort.Strings(values)
	return values
}

func renderEgressStashOverride(policy egressPolicyStore, runtime egressRuntimeStatus) string {
	if !policy.Enabled {
		return "# DJOneHub 出口策略已关闭\n"
	}
	lines := []string{
		"# DJOneHub 生成的独立 Override；导入后不会改写 Stash 主配置。",
		"# 域名直连同时依赖 DJOneHub 为当前解析到的 IPv4 写入系统路由。",
	}
	if runtime.Client.ProcessRulesSupported {
		lines = append(lines,
			"proxies:",
			"  - name: DJOneHub 公司直连",
			"    type: direct",
			"    interface-name: "+yamlScalar(runtime.Corporate.Interface),
			"  - name: DJOneHub 4G直连",
			"    type: direct",
			"    interface-name: "+yamlScalar(runtime.Cellular.Interface),
		)
	}
	lines = append(lines, "rules:")
	for _, cidr := range policy.CorporateCIDRs {
		lines = append(lines, "  - IP-CIDR,"+cidr+",DIRECT,no-resolve")
	}
	for _, rule := range policy.Destinations {
		policyName := "DIRECT"
		if runtime.Client.ProcessRulesSupported {
			if rule.Policy == egressDirectCorporate {
				policyName = "DJOneHub 公司直连"
			} else {
				policyName = "DJOneHub 4G直连"
			}
		}
		switch rule.Kind {
		case "domain":
			lines = append(lines, "  - DOMAIN-SUFFIX,"+rule.Value+","+policyName)
		case "ip", "cidr":
			value := rule.Value
			if rule.Kind == "ip" {
				value += "/32"
			}
			lines = append(lines, "  - IP-CIDR,"+value+","+policyName+",no-resolve")
		}
	}
	if runtime.Client.ProcessRulesSupported {
		for _, rule := range policy.Applications {
			if rule.Policy == "follow_global" || rule.Executable == "" || rule.Executable == "." {
				continue
			}
			policyName := policy.VPNPolicy
			if rule.Policy == egressDirectCorporate {
				policyName = "DJOneHub 公司直连"
			} else if rule.Policy == egressDirectCellular {
				policyName = "DJOneHub 4G直连"
			}
			lines = append(lines, "  - PROCESS-PATH,"+yamlRuleField(rule.Executable)+","+yamlRuleField(policyName))
		}
	} else if len(policy.Applications) > 0 {
		lines = append(lines, "# 当前 iOS/iPadOS 版 Stash 不执行应用进程规则；以下配置仅保存在 DJOneHub。")
	}
	return strings.Join(lines, "\n") + "\n"
}

func yamlScalar(value string) string {
	return fmt.Sprintf("%q", value)
}

func yamlRuleField(value string) string {
	return strings.NewReplacer(",", "\\,", "\n", "", "\r", "").Replace(value)
}

func moveServiceFirst(order []string, preferred string) []string {
	if preferred == "" {
		return append([]string(nil), order...)
	}
	result := []string{preferred}
	for _, name := range order {
		if name != preferred {
			result = append(result, name)
		}
	}
	return result
}

func (a *app) egressAppliedStateExists() bool {
	if err := a.initEgressPaths(); err != nil {
		return false
	}
	_, err := os.Stat(a.egressApplied)
	return err == nil
}

func (a *app) downloadEgressOverride(w http.ResponseWriter, _ *http.Request) {
	policy, err := a.loadEgressPolicy()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	preview, err := a.buildEgressPreview(policy)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="DJOneHub.stoverride"`)
	_, _ = w.Write([]byte(preview.Override))
}

func (a *app) applyEgressPolicy(w http.ResponseWriter, _ *http.Request) {
	if a.egressAppliedStateExists() {
		writeError(w, http.StatusConflict, "已有一份已应用策略；请先恢复，再应用新的系统路由")
		return
	}
	policy, err := a.loadEgressPolicy()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	preview, err := a.buildEgressPreview(policy)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if !preview.CanApply {
		writeError(w, http.StatusConflict, "公司网络或 4G 模块未就绪，未执行任何修改")
		return
	}
	state, err := applyEgressPlatform(policy, preview)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if err := a.persistEgressAppliedState(state); err != nil {
		writeError(w, http.StatusInternalServerError, "路由已应用，但保存恢复点失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "applied": state})
}

func (a *app) restoreEgressPolicy(w http.ResponseWriter, _ *http.Request) {
	state, err := a.loadEgressAppliedState()
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err := restoreEgressPlatform(state); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	_ = os.Remove(a.egressApplied)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *app) persistEgressAppliedState(state egressAppliedState) error {
	if err := a.initEgressPaths(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.egressApplied), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(a.egressApplied, data, 0o600)
}

func (a *app) loadEgressAppliedState() (egressAppliedState, error) {
	if err := a.initEgressPaths(); err != nil {
		return egressAppliedState{}, err
	}
	data, err := os.ReadFile(a.egressApplied)
	if errors.Is(err, os.ErrNotExist) {
		return egressAppliedState{}, errors.New("没有可恢复的 DJOneHub 出口策略")
	}
	if err != nil {
		return egressAppliedState{}, err
	}
	var state egressAppliedState
	if err := json.Unmarshal(data, &state); err != nil {
		return egressAppliedState{}, err
	}
	return state, nil
}
