package main

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var voiceDialNumberPattern = regexp.MustCompile(`^\+?[0-9*#]{3,32}$`)

type voiceCall struct {
	ID         int    `json:"id"`
	Direction  string `json:"direction"`
	State      string `json:"state"`
	Number     string `json:"number,omitempty"`
	Type       int    `json:"type,omitempty"`
	Multiparty bool   `json:"multiparty"`
}

type voiceOverview struct {
	Available  bool                `json:"available"`
	Calls      []voiceCall         `json:"calls"`
	Audio      voiceAudioStatus    `json:"audio"`
	Inventory  voiceAudioInventory `json:"inventory"`
	UACEnabled bool                `json:"uac_enabled"`
	AudioRoute bool                `json:"audio_route"`
	USBCfg     string              `json:"usbcfg,omitempty"`
	IMS        string              `json:"ims,omitempty"`
	Warning    string              `json:"warning,omitempty"`
	LastError  string              `json:"last_error,omitempty"`
}

type voiceService struct {
	mu           sync.Mutex
	runAT        func(string, time.Duration) (string, error)
	audio        voiceAudioBridge
	demo         bool
	lastError    string
	audioRoute   bool
	routeAttempt time.Time
}

func newVoiceService(runAT func(string, time.Duration) (string, error), demo bool) *voiceService {
	return &voiceService{runAT: runAT, audio: newVoiceAudioBridge(), demo: demo}
}

func (a *app) currentVoiceService() *voiceService {
	a.voiceMu.Lock()
	defer a.voiceMu.Unlock()
	if a.voice == nil {
		a.voice = newVoiceService(a.runATCommand, a.demo)
	}
	return a.voice
}

func (a *app) stopVoiceService() {
	a.voiceMu.Lock()
	service := a.voice
	a.voiceMu.Unlock()
	if service != nil {
		service.StopAudio()
	}
}

func (a *app) voiceStatus(w http.ResponseWriter, _ *http.Request) {
	snapshot := a.callSnapshot()
	writeJSON(w, http.StatusOK, a.currentVoiceService().Overview(snapshot.Calls, snapshot.LastPollError))
}

func (a *app) voiceCalls(w http.ResponseWriter, _ *http.Request) {
	service := a.currentVoiceService()
	snapshot := a.callSnapshot()
	service.mu.Lock()
	audioRoute := service.audioRoute
	service.mu.Unlock()
	response := map[string]any{
		"calls": snapshot.Calls, "active": snapshot.Active, "history": snapshot.History,
		"polling": snapshot.Polling, "poll_interval_s": int(snapshot.PollInterval.Seconds()),
		"last_poll": snapshot.LastPoll, "last_poll_error": snapshot.LastPollError,
		"audio": service.audio.Status(), "audio_route": audioRoute,
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *app) voiceDial(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Number    string `json:"number"`
		WithAudio bool   `json:"with_audio"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := a.currentVoiceService().Dial(body.Number, body.WithAudio); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *app) voiceAnswer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		WithAudio bool `json:"with_audio"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := a.currentVoiceService().Answer(body.WithAudio); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *app) voiceHangup(w http.ResponseWriter, _ *http.Request) {
	if err := a.currentVoiceService().Hangup(); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *app) voiceAudioStart(w http.ResponseWriter, _ *http.Request) {
	service := a.currentVoiceService()
	if err := service.StartAudio(); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "audio": service.audio.Status()})
}

func (a *app) voiceAudioStop(w http.ResponseWriter, _ *http.Request) {
	a.currentVoiceService().StopAudio()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *voiceService) Overview(calls []voiceCall, callError string) voiceOverview {
	if s.demo {
		return voiceOverview{
			Available: true,
			Calls:     []voiceCall{},
			Audio:     s.audio.Status(),
			Inventory: voiceAudioInventory{
				Available: true, ModuleCapture: "AC Interface", ModulePlayback: "AS Interface",
				MacCapture: "MacBook Pro 麦克风", MacPlayback: "MacBook Pro 扬声器",
			},
			UACEnabled: true,
			IMS:        `"ims",1,1`,
		}
	}

	overview := voiceOverview{Calls: append([]voiceCall(nil), calls...), Audio: s.audio.Status()}
	overview.Inventory = s.audio.Inventory()
	overview.UACEnabled = overview.Inventory.Available
	if callError == "" {
		overview.Available = true
	} else {
		overview.LastError = callError
	}
	if response, err := s.runAT(`AT+QCFG="usbcfg"`, 4*time.Second); err == nil {
		overview.USBCfg = firstATPayload(response, "+QCFG:")
	}
	if response, err := s.runAT(`AT+QCFG="ims"`, 4*time.Second); err == nil {
		overview.IMS = firstATPayload(response, "+QCFG:")
	}
	s.mu.Lock()
	overview.AudioRoute = s.audioRoute
	if overview.LastError == "" {
		overview.LastError = s.lastError
	}
	s.mu.Unlock()
	if !overview.UACEnabled {
		overview.Warning = "模块呼叫控制可用，但 macOS 尚未枚举 USB Audio；需要启用 usbcfg 的 UAC 位并重启模块。"
	} else {
		overview.Warning = "VoLTE 由模块和运营商完成；Mac 仅控制通话并桥接 8 kHz 单声道音频。建议使用耳机避免回声。"
	}
	return overview
}

func (s *voiceService) QueryCalls() ([]voiceCall, error) {
	if s.demo {
		return []voiceCall{}, nil
	}
	response, err := s.runAT("AT+CLCC", 4*time.Second)
	if err != nil {
		return []voiceCall{}, err
	}
	return parseCLCC(response), nil
}

func (s *voiceService) ObserveCalls(calls []voiceCall) {
	active := false
	for _, call := range calls {
		if call.State == "active" {
			active = true
			break
		}
	}
	if active && s.audio.Status().Running {
		s.ensureModuleAudioRoute()
	} else if !active {
		s.mu.Lock()
		s.audioRoute = false
		s.mu.Unlock()
	}
}

func (s *voiceService) Dial(number string, withAudio bool) error {
	number = strings.TrimSpace(number)
	if !voiceDialNumberPattern.MatchString(number) {
		return errors.New("号码格式无效，只允许数字、+、*、#，长度 3-32 位")
	}
	if withAudio {
		if err := s.StartAudio(); err != nil {
			return fmt.Errorf("音频桥未启动，已取消拨号: %w", err)
		}
	}
	response, err := s.runAT("ATD"+number+";", 8*time.Second)
	if err != nil {
		if withAudio {
			s.StopAudio()
		}
		return fmt.Errorf("拨号失败: %w", err)
	}
	if atCommandFailed(response) {
		if withAudio {
			s.StopAudio()
		}
		return fmt.Errorf("拨号被模块拒绝: %s", compactATResponse(response))
	}
	return nil
}

func (s *voiceService) Answer(withAudio bool) error {
	if withAudio {
		if err := s.StartAudio(); err != nil {
			return fmt.Errorf("音频桥未启动，已取消接听: %w", err)
		}
	}
	response, err := s.runAT("ATA", 8*time.Second)
	if err != nil {
		if withAudio {
			s.StopAudio()
		}
		return fmt.Errorf("接听失败: %w", err)
	}
	if atCommandFailed(response) {
		if withAudio {
			s.StopAudio()
		}
		return fmt.Errorf("接听被模块拒绝: %s", compactATResponse(response))
	}
	return nil
}

func (s *voiceService) Hangup() error {
	response, err := s.runAT("AT+CHUP", 5*time.Second)
	_, _ = s.runAT("AT+QPCMV=0", 2*time.Second)
	s.resetAudioRoute()
	s.audio.Stop()
	if err != nil {
		return fmt.Errorf("挂断失败: %w", err)
	}
	if atCommandFailed(response) {
		return fmt.Errorf("挂断被模块拒绝: %s", compactATResponse(response))
	}
	return nil
}

func (s *voiceService) StartAudio() error {
	if !s.demo {
		response, err := s.runAT("AT+QPCMV=1,2", 3*time.Second)
		if err != nil {
			message := fmt.Sprintf("模块 USB 语音路由不可用: %v", err)
			s.setLastError(message)
			return errors.New(message)
		}
		if atCommandFailed(response) {
			message := "当前 QDC507 固件拒绝 QPCMV USB 语音路由，无法启动有声通话；未发起拨号"
			s.setLastError(message)
			return errors.New(message)
		}
		s.mu.Lock()
		s.audioRoute = true
		s.mu.Unlock()
	}
	if err := s.audio.Start(); err != nil {
		if !s.demo {
			_, _ = s.runAT("AT+QPCMV=0", 2*time.Second)
		}
		s.resetAudioRoute()
		s.setLastError(err.Error())
		return err
	}
	s.setLastError("")
	return nil
}

func (s *voiceService) StopAudio() {
	_, _ = s.runAT("AT+QPCMV=0", 2*time.Second)
	s.resetAudioRoute()
	s.audio.Stop()
}

func (s *voiceService) ensureModuleAudioRoute() {
	s.mu.Lock()
	if s.audioRoute || time.Since(s.routeAttempt) < time.Second {
		s.mu.Unlock()
		return
	}
	s.routeAttempt = time.Now()
	s.mu.Unlock()

	response, err := s.runAT("AT+QPCMV=1,2", 3*time.Second)
	if err != nil {
		s.setLastError(fmt.Sprintf("UAC 已枚举，但模块拒绝 QPCMV 音频路由: %v", err))
		return
	}
	if atCommandFailed(response) {
		s.setLastError("通话已接通，但模块返回 QPCMV ERROR；将在下一次状态轮询时重试")
		return
	}
	s.mu.Lock()
	s.audioRoute = true
	s.lastError = ""
	s.mu.Unlock()
}

func (s *voiceService) resetAudioRoute() {
	s.mu.Lock()
	s.audioRoute = false
	s.routeAttempt = time.Time{}
	s.mu.Unlock()
}

func (s *voiceService) setLastError(value string) {
	s.mu.Lock()
	s.lastError = value
	s.mu.Unlock()
}

func parseCLCC(response string) []voiceCall {
	calls := make([]voiceCall, 0)
	for _, line := range strings.Split(strings.ReplaceAll(response, "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToUpper(line), "+CLCC:") {
			continue
		}
		fields := splitATCSV(strings.TrimSpace(line[len("+CLCC:"):]))
		if len(fields) < 5 {
			continue
		}
		id, idErr := strconv.Atoi(fields[0])
		direction, dirErr := strconv.Atoi(fields[1])
		state, stateErr := strconv.Atoi(fields[2])
		mode, modeErr := strconv.Atoi(fields[3])
		multiparty, mptyErr := strconv.Atoi(fields[4])
		if idErr != nil || dirErr != nil || stateErr != nil || modeErr != nil || mptyErr != nil || mode != 0 {
			continue
		}
		call := voiceCall{
			ID: id, Direction: mapCallDirection(direction), State: mapCallState(state), Multiparty: multiparty != 0,
		}
		if len(fields) > 5 {
			call.Number = strings.Trim(fields[5], `"`)
		}
		if len(fields) > 6 {
			call.Type, _ = strconv.Atoi(fields[6])
		}
		calls = append(calls, call)
	}
	return calls
}

func splitATCSV(value string) []string {
	fields := make([]string, 0, 8)
	var current strings.Builder
	quoted := false
	for _, r := range value {
		switch r {
		case '"':
			quoted = !quoted
			current.WriteRune(r)
		case ',':
			if quoted {
				current.WriteRune(r)
			} else {
				fields = append(fields, strings.TrimSpace(current.String()))
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	fields = append(fields, strings.TrimSpace(current.String()))
	return fields
}

func mapCallDirection(value int) string {
	if value == 1 {
		return "incoming"
	}
	return "outgoing"
}

func mapCallState(value int) string {
	states := map[int]string{0: "active", 1: "held", 2: "dialing", 3: "alerting", 4: "incoming", 5: "waiting", 6: "disconnected"}
	if state, ok := states[value]; ok {
		return state
	}
	return "unknown"
}

func firstATPayload(response, prefix string) string {
	for _, line := range strings.Split(strings.ReplaceAll(response, "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToUpper(line), strings.ToUpper(prefix)) {
			return strings.TrimSpace(line[len(prefix):])
		}
	}
	return ""
}

func atCommandFailed(response string) bool {
	upper := strings.ToUpper(response)
	return strings.Contains(upper, "\nERROR") || strings.Contains(upper, "+CME ERROR") || strings.Contains(upper, "+CMS ERROR")
}

func compactATResponse(response string) string {
	return strings.Join(strings.Fields(response), " ")
}
