package main

import (
	"encoding/binary"
	"os"
	"testing"
	"time"
)

func TestStereoCallRecorderWritesTwoChannelWAV(t *testing.T) {
	directory := t.TempDir()
	startedAt := time.Date(2026, 8, 14, 10, 30, 0, 0, time.Local)
	recorder, err := newStereoCallRecorder(directory, startedAt)
	if err != nil {
		t.Fatalf("newStereoCallRecorder() error=%v", err)
	}
	recorder.PushRemote(pcmSamples(1000, -1000))
	recorder.PushLocal(pcmSamples(2000))
	status, err := recorder.Stop()
	if err != nil {
		t.Fatalf("Stop() error=%v", err)
	}
	if status.DurationMilliseconds != 0 {
		t.Fatalf("duration=%dms want 0ms for two 8kHz samples", status.DurationMilliseconds)
	}

	data, err := os.ReadFile(status.Path)
	if err != nil {
		t.Fatalf("read WAV: %v", err)
	}
	if string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		t.Fatalf("invalid WAV signature: %q %q", data[0:4], data[8:12])
	}
	if channels := binary.LittleEndian.Uint16(data[22:24]); channels != 2 {
		t.Fatalf("channels=%d want 2", channels)
	}
	if sampleRate := binary.LittleEndian.Uint32(data[24:28]); sampleRate != voiceSampleRate {
		t.Fatalf("sampleRate=%d want %d", sampleRate, voiceSampleRate)
	}
	if dataSize := binary.LittleEndian.Uint32(data[40:44]); dataSize != 8 {
		t.Fatalf("dataSize=%d want 8", dataSize)
	}
	want := []int16{1000, 2000, -1000, 0}
	for index, sample := range want {
		got := int16(binary.LittleEndian.Uint16(data[44+index*2 : 46+index*2]))
		if got != sample {
			t.Fatalf("sample[%d]=%d want %d", index, got, sample)
		}
	}
	if _, err := os.Stat(recorder.remotePath); !os.IsNotExist(err) {
		t.Fatalf("remote temp PCM still exists: %v", err)
	}
	if _, err := os.Stat(recorder.localPath); !os.IsNotExist(err) {
		t.Fatalf("local temp PCM still exists: %v", err)
	}
}

func TestUniqueVoiceRecordingPathAvoidsOverwrite(t *testing.T) {
	directory := t.TempDir()
	now := time.Date(2026, 8, 14, 10, 30, 0, 0, time.Local)
	first := uniqueVoiceRecordingPath(directory, now)
	if err := os.WriteFile(first, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	second := uniqueVoiceRecordingPath(directory, now)
	if first == second {
		t.Fatal("recording path would overwrite an existing WAV")
	}
}

func pcmSamples(samples ...int16) []byte {
	data := make([]byte, len(samples)*2)
	for index, sample := range samples {
		binary.LittleEndian.PutUint16(data[index*2:index*2+2], uint16(sample))
	}
	return data
}
