package main

import (
	"encoding/binary"
	"errors"
)

var errVoiceAudioUnsupported = errors.New("当前构建不支持 macOS CoreAudio 语音桥接")

const voiceSampleRate = uint32(8000)

type voiceAudioDevice struct {
	Name      string `json:"name"`
	Direction string `json:"direction"`
	Default   bool   `json:"default"`
}

type voiceAudioInventory struct {
	Available      bool               `json:"available"`
	ModuleCapture  string             `json:"module_capture,omitempty"`
	ModulePlayback string             `json:"module_playback,omitempty"`
	MacCapture     string             `json:"mac_capture,omitempty"`
	MacPlayback    string             `json:"mac_playback,omitempty"`
	Devices        []voiceAudioDevice `json:"devices,omitempty"`
	Error          string             `json:"error,omitempty"`
}

type voiceAudioStatus struct {
	Running        bool   `json:"running"`
	Stopping       bool   `json:"stopping,omitempty"`
	ModuleCapture  string `json:"module_capture,omitempty"`
	ModulePlayback string `json:"module_playback,omitempty"`
	MacCapture     string `json:"mac_capture,omitempty"`
	MacPlayback    string `json:"mac_playback,omitempty"`
	SampleRate     uint32 `json:"sample_rate,omitempty"`
	ModuleLevel    uint32 `json:"module_level,omitempty"`
	MacLevel       uint32 `json:"mac_level,omitempty"`
	Warning        string `json:"warning,omitempty"`
	LastError      string `json:"last_error,omitempty"`
}

type voiceAudioBridge interface {
	Inventory() voiceAudioInventory
	Start() error
	Stop()
	Status() voiceAudioStatus
	StartRecording() (voiceRecordingStatus, error)
	StopRecording() (voiceRecordingStatus, error)
	RecordingStatus() voiceRecordingStatus
}

func pcmPeakPercent(input []byte) uint32 {
	var peak uint32
	for offset := 0; offset+1 < len(input); offset += 2 {
		sample := int32(int16(binary.LittleEndian.Uint16(input[offset : offset+2])))
		if sample < 0 {
			sample = -sample
		}
		if uint32(sample) > peak {
			peak = uint32(sample)
		}
	}
	if peak > 32767 {
		peak = 32767
	}
	return peak * 100 / 32767
}
