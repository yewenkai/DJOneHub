//go:build darwin && cgo

package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gen2brain/malgo"
)

type coreAudioBridge struct {
	mu              sync.Mutex
	inventoryMu     sync.Mutex
	context         *malgo.AllocatedContext
	downlink        *malgo.Device
	uplink          *malgo.Device
	status          voiceAudioStatus
	inventory       voiceAudioInventory
	selected        selectedVoiceDevices
	inventoryLoaded bool
	moduleLevel     atomic.Uint32
	macLevel        atomic.Uint32
	recordingMu     sync.Mutex
	recorder        atomic.Pointer[stereoCallRecorder]
	recordingStatus voiceRecordingStatus
}

type selectedVoiceDevices struct {
	moduleCapture  malgo.DeviceInfo
	modulePlayback malgo.DeviceInfo
	macCapture     malgo.DeviceInfo
	macPlayback    malgo.DeviceInfo
}

func newVoiceAudioBridge() voiceAudioBridge {
	return &coreAudioBridge{}
}

func initCoreAudioContext() (*malgo.AllocatedContext, error) {
	return malgo.InitContext([]malgo.Backend{malgo.BackendCoreaudio}, malgo.ContextConfig{}, nil)
}

func (b *coreAudioBridge) Inventory() voiceAudioInventory {
	b.inventoryMu.Lock()
	defer b.inventoryMu.Unlock()
	b.mu.Lock()
	if b.inventoryLoaded {
		inventory := b.inventory
		b.mu.Unlock()
		return inventory
	}
	b.mu.Unlock()

	context, err := initCoreAudioContext()
	if err != nil {
		return voiceAudioInventory{Error: fmt.Sprintf("初始化 CoreAudio 失败: %v", err)}
	}
	defer func() {
		_ = context.Uninit()
		context.Free()
	}()

	selected, inventory, err := selectVoiceAudioDevices(context)
	if err != nil {
		inventory.Error = err.Error()
		b.cacheInventory(selectedVoiceDevices{}, inventory, false)
		return inventory
	}
	inventory.Available = true
	inventory.ModuleCapture = selected.moduleCapture.Name()
	inventory.ModulePlayback = selected.modulePlayback.Name()
	inventory.MacCapture = selected.macCapture.Name()
	inventory.MacPlayback = selected.macPlayback.Name()
	b.cacheInventory(selected, inventory, true)
	return inventory
}

func (b *coreAudioBridge) cacheInventory(selected selectedVoiceDevices, inventory voiceAudioInventory, loaded bool) {
	b.mu.Lock()
	b.selected = selected
	b.inventory = inventory
	b.inventoryLoaded = loaded
	b.mu.Unlock()
}

func (b *coreAudioBridge) Start() error {
	b.mu.Lock()
	if b.status.Running {
		b.mu.Unlock()
		return nil
	}
	if b.status.Stopping {
		b.mu.Unlock()
		return fmt.Errorf("上一次音频桥仍在停止，请稍后重试")
	}
	selected := b.selected
	selectedReady := b.inventoryLoaded && b.inventory.Available
	b.mu.Unlock()

	if !selectedReady {
		inventory := b.Inventory()
		if !inventory.Available {
			return b.recordStartError(fmt.Errorf("USB 音频端点不可用: %s", inventory.Error))
		}
		b.mu.Lock()
		selected = b.selected
		b.mu.Unlock()
	}

	context, err := initCoreAudioContext()
	if err != nil {
		return b.recordStartError(fmt.Errorf("初始化 CoreAudio 失败: %w", err))
	}
	downlink, err := initDuplexVoiceDevice(
		context, selected.moduleCapture, selected.macPlayback, &b.moduleLevel,
		func(input []byte) {
			if recorder := b.recorder.Load(); recorder != nil {
				recorder.PushRemote(input)
			}
		},
	)
	if err != nil {
		_ = context.Uninit()
		context.Free()
		return b.recordStartError(fmt.Errorf("打开模块下行音频失败: %w", err))
	}
	uplink, err := initDuplexVoiceDevice(
		context, selected.macCapture, selected.modulePlayback, &b.macLevel,
		func(input []byte) {
			if recorder := b.recorder.Load(); recorder != nil {
				recorder.PushLocal(input)
			}
		},
	)
	if err != nil {
		downlink.Uninit()
		_ = context.Uninit()
		context.Free()
		return b.recordStartError(fmt.Errorf("打开模块上行音频失败: %w", err))
	}

	if err := downlink.Start(); err != nil {
		uplink.Uninit()
		downlink.Uninit()
		_ = context.Uninit()
		context.Free()
		return b.recordStartError(fmt.Errorf("启动模块下行音频失败: %w", err))
	}
	if err := uplink.Start(); err != nil {
		_ = downlink.Stop()
		uplink.Uninit()
		downlink.Uninit()
		_ = context.Uninit()
		context.Free()
		return b.recordStartError(fmt.Errorf("启动模块上行音频失败: %w", err))
	}

	b.mu.Lock()
	b.context = context
	b.downlink = downlink
	b.uplink = uplink
	b.status = voiceAudioStatus{
		Running:        true,
		ModuleCapture:  selected.moduleCapture.Name(),
		ModulePlayback: selected.modulePlayback.Name(),
		MacCapture:     selected.macCapture.Name(),
		MacPlayback:    selected.macPlayback.Name(),
		SampleRate:     voiceSampleRate,
		Warning:        "当前为实验性双向音频桥接；建议佩戴耳机，避免扬声器回声进入麦克风。",
	}
	b.mu.Unlock()
	return nil
}

func (b *coreAudioBridge) recordStartError(err error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.status.Running = false
	b.status.Stopping = false
	b.status.LastError = err.Error()
	return err
}

func (b *coreAudioBridge) Stop() {
	_, _ = b.StopRecording()
	b.mu.Lock()
	if !b.status.Running && !b.status.Stopping {
		b.mu.Unlock()
		return
	}
	uplink, downlink, context := b.uplink, b.downlink, b.context
	b.uplink, b.downlink, b.context = nil, nil, nil
	b.status.Running = false
	b.status.Stopping = true
	b.moduleLevel.Store(0)
	b.macLevel.Store(0)
	b.mu.Unlock()

	go func() {
		if uplink != nil {
			_ = uplink.Stop()
			uplink.Uninit()
		}
		if downlink != nil {
			_ = downlink.Stop()
			downlink.Uninit()
		}
		if context != nil {
			_ = context.Uninit()
			context.Free()
		}
		b.mu.Lock()
		b.status.Stopping = false
		b.mu.Unlock()
	}()
}

func (b *coreAudioBridge) StartRecording() (voiceRecordingStatus, error) {
	b.recordingMu.Lock()
	defer b.recordingMu.Unlock()
	if recorder := b.recorder.Load(); recorder != nil {
		return b.recordingStatus, nil
	}
	b.mu.Lock()
	running := b.status.Running
	b.mu.Unlock()
	if !running {
		err := errors.New("请先启动 Mac 双向音频桥，再开始录音")
		b.recordingStatus.LastError = err.Error()
		return b.recordingStatus, err
	}
	directory, err := voiceRecordingsDirectory()
	if err != nil {
		b.recordingStatus.LastError = err.Error()
		return b.recordingStatus, err
	}
	now := time.Now()
	recorder, err := newStereoCallRecorder(directory, now)
	if err != nil {
		b.recordingStatus.LastError = err.Error()
		return b.recordingStatus, err
	}
	b.recorder.Store(recorder)
	b.recordingStatus = voiceRecordingStatus{
		Running:   true,
		StartedAt: &now,
		FileName:  filepath.Base(recorder.finalPath),
		Path:      recorder.finalPath,
	}
	return b.recordingStatus, nil
}

func (b *coreAudioBridge) StopRecording() (voiceRecordingStatus, error) {
	b.recordingMu.Lock()
	defer b.recordingMu.Unlock()
	recorder := b.recorder.Swap(nil)
	if recorder == nil {
		return b.recordingStatus, nil
	}
	status, err := recorder.Stop()
	b.recordingStatus = status
	return status, err
}

func (b *coreAudioBridge) RecordingStatus() voiceRecordingStatus {
	b.recordingMu.Lock()
	defer b.recordingMu.Unlock()
	status := b.recordingStatus
	if status.Running && status.StartedAt != nil {
		status.DurationMilliseconds = time.Since(*status.StartedAt).Milliseconds()
	}
	return status
}

func (b *coreAudioBridge) Status() voiceAudioStatus {
	b.mu.Lock()
	status := b.status
	b.mu.Unlock()
	status.ModuleLevel = b.moduleLevel.Load()
	status.MacLevel = b.macLevel.Load()
	return status
}

func initDuplexVoiceDevice(
	context *malgo.AllocatedContext,
	capture, playback malgo.DeviceInfo,
	level *atomic.Uint32,
	onPCM func([]byte),
) (*malgo.Device, error) {
	config := malgo.DefaultDeviceConfig(malgo.Duplex)
	config.Capture.DeviceID = capture.ID.Pointer()
	config.Capture.Format = malgo.FormatS16
	config.Capture.Channels = 1
	config.Playback.DeviceID = playback.ID.Pointer()
	config.Playback.Format = malgo.FormatS16
	config.Playback.Channels = 1
	config.SampleRate = voiceSampleRate
	config.PeriodSizeInMilliseconds = 20
	config.PerformanceProfile = malgo.LowLatency

	callbacks := malgo.DeviceCallbacks{Data: func(output, input []byte, _ uint32) {
		clear(output)
		copy(output, input)
		level.Store(pcmPeakPercent(input))
		if onPCM != nil {
			onPCM(input)
		}
	}}
	return malgo.InitDevice(context.Context, config, callbacks)
}

func selectVoiceAudioDevices(context *malgo.AllocatedContext) (selectedVoiceDevices, voiceAudioInventory, error) {
	captures, err := context.Devices(malgo.Capture)
	if err != nil {
		return selectedVoiceDevices{}, voiceAudioInventory{}, fmt.Errorf("枚举录音设备失败: %w", err)
	}
	playbacks, err := context.Devices(malgo.Playback)
	if err != nil {
		return selectedVoiceDevices{}, voiceAudioInventory{}, fmt.Errorf("枚举播放设备失败: %w", err)
	}

	inventory := voiceAudioInventory{Devices: make([]voiceAudioDevice, 0, len(captures)+len(playbacks))}
	for i := range captures {
		inventory.Devices = append(inventory.Devices, voiceAudioDevice{
			Name: captures[i].Name(), Direction: "capture", Default: captures[i].IsDefault != 0,
		})
	}
	for i := range playbacks {
		inventory.Devices = append(inventory.Devices, voiceAudioDevice{
			Name: playbacks[i].Name(), Direction: "playback", Default: playbacks[i].IsDefault != 0,
		})
	}

	moduleCapture, ok := findAudioDevice(captures, isModuleCapture)
	if !ok {
		return selectedVoiceDevices{}, inventory, fmt.Errorf("未找到模块录音端点 AC Interface；请先启用 USB UAC 并重启模块")
	}
	modulePlayback, ok := findAudioDevice(playbacks, isModulePlayback)
	if !ok {
		return selectedVoiceDevices{}, inventory, fmt.Errorf("未找到模块播放端点 AS Interface；请先启用 USB UAC 并重启模块")
	}
	macCapture, ok := findPreferredMacDevice(captures, isModuleCapture)
	if !ok {
		return selectedVoiceDevices{}, inventory, fmt.Errorf("未找到可用的 Mac 麦克风")
	}
	macPlayback, ok := findPreferredMacDevice(playbacks, isModulePlayback)
	if !ok {
		return selectedVoiceDevices{}, inventory, fmt.Errorf("未找到可用的 Mac 扬声器或耳机")
	}

	return selectedVoiceDevices{
		moduleCapture: moduleCapture, modulePlayback: modulePlayback,
		macCapture: macCapture, macPlayback: macPlayback,
	}, inventory, nil
}

func findAudioDevice(devices []malgo.DeviceInfo, match func(string) bool) (malgo.DeviceInfo, bool) {
	for i := range devices {
		if match(devices[i].Name()) {
			return devices[i], true
		}
	}
	return malgo.DeviceInfo{}, false
}

func findPreferredMacDevice(devices []malgo.DeviceInfo, moduleMatch func(string) bool) (malgo.DeviceInfo, bool) {
	for i := range devices {
		if devices[i].IsDefault != 0 && !moduleMatch(devices[i].Name()) {
			return devices[i], true
		}
	}
	for i := range devices {
		if !moduleMatch(devices[i].Name()) {
			return devices[i], true
		}
	}
	return malgo.DeviceInfo{}, false
}

func isModuleCapture(name string) bool {
	value := strings.ToLower(strings.TrimSpace(name))
	return value == "ac interface" || (strings.Contains(value, "baiwang") && strings.Contains(value, "capture"))
}

func isModulePlayback(name string) bool {
	value := strings.ToLower(strings.TrimSpace(name))
	return value == "as interface" || (strings.Contains(value, "baiwang") && strings.Contains(value, "playback"))
}
