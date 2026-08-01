package main

import (
	"testing"
	"time"
)

type fakeVoiceAudioBridge struct {
	started bool
}

func (b *fakeVoiceAudioBridge) Inventory() voiceAudioInventory {
	return voiceAudioInventory{Available: true}
}
func (b *fakeVoiceAudioBridge) Start() error {
	b.started = true
	return nil
}
func (b *fakeVoiceAudioBridge) Stop()                    { b.started = false }
func (b *fakeVoiceAudioBridge) Status() voiceAudioStatus { return voiceAudioStatus{Running: b.started} }

func TestParseCLCCFiltersDataContexts(t *testing.T) {
	response := "AT+CLCC\r\n" +
		`+CLCC: 1,1,0,1,0,"",128` + "\r\n" +
		`+CLCC: 3,1,4,0,0,"13800138000",129` + "\r\nOK\r\n"
	calls := parseCLCC(response)
	if len(calls) != 1 {
		t.Fatalf("len(calls)=%d want 1: %#v", len(calls), calls)
	}
	if calls[0].ID != 3 || calls[0].Direction != "incoming" || calls[0].State != "incoming" || calls[0].Number != "13800138000" {
		t.Fatalf("call=%+v", calls[0])
	}
}

func TestParseCLCCSupportsQuotedComma(t *testing.T) {
	response := `+CLCC: 4,0,0,0,0,"10086",129,"客服,热线"`
	calls := parseCLCC(response)
	if len(calls) != 1 || calls[0].Number != "10086" || calls[0].State != "active" {
		t.Fatalf("calls=%+v", calls)
	}
}

func TestVoiceDialNumberPattern(t *testing.T) {
	for _, valid := range []string{"10086", "+8613800138000", "*#06#"} {
		if !voiceDialNumberPattern.MatchString(valid) {
			t.Fatalf("valid number %q rejected", valid)
		}
	}
	for _, invalid := range []string{"12", "10086;AT+CFUN=1", "hello", "+86 138"} {
		if voiceDialNumberPattern.MatchString(invalid) {
			t.Fatalf("invalid number %q accepted", invalid)
		}
	}
}

func TestStartAudioRejectsFirmwareWithoutQPCMV(t *testing.T) {
	audio := &fakeVoiceAudioBridge{}
	service := &voiceService{
		runAT: func(_ string, _ time.Duration) (string, error) { return "AT+QPCMV=1,2\r\nERROR", nil },
		audio: audio,
	}
	if err := service.StartAudio(); err == nil {
		t.Fatal("StartAudio() error=nil, want firmware rejection")
	}
	if audio.started {
		t.Fatal("CoreAudio bridge started after QPCMV rejection")
	}
}

func TestStartAudioEnablesRouteBeforeCoreAudio(t *testing.T) {
	audio := &fakeVoiceAudioBridge{}
	service := &voiceService{
		runAT: func(command string, _ time.Duration) (string, error) {
			if command != "AT+QPCMV=1,2" {
				t.Fatalf("unexpected command %q", command)
			}
			return "OK", nil
		},
		audio: audio,
	}
	if err := service.StartAudio(); err != nil {
		t.Fatalf("StartAudio() error=%v", err)
	}
	if !audio.started || !service.audioRoute {
		t.Fatalf("started=%v audioRoute=%v", audio.started, service.audioRoute)
	}
}

func TestPCMPeakPercent(t *testing.T) {
	if got := pcmPeakPercent([]byte{0x00, 0x00, 0xff, 0x7f}); got != 100 {
		t.Fatalf("pcmPeakPercent()=%d want 100", got)
	}
}
