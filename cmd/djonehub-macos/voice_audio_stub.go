//go:build !darwin || !cgo

package main

type unsupportedVoiceAudioBridge struct{}

func newVoiceAudioBridge() voiceAudioBridge { return unsupportedVoiceAudioBridge{} }

func (unsupportedVoiceAudioBridge) Inventory() voiceAudioInventory {
	return voiceAudioInventory{Error: errVoiceAudioUnsupported.Error()}
}

func (unsupportedVoiceAudioBridge) Start() error { return errVoiceAudioUnsupported }
func (unsupportedVoiceAudioBridge) Stop()        {}
func (unsupportedVoiceAudioBridge) Status() voiceAudioStatus {
	return voiceAudioStatus{LastError: errVoiceAudioUnsupported.Error()}
}
