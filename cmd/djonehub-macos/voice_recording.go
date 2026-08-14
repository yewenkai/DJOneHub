package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

const (
	voiceRecordingChannels      = uint16(2)
	voiceRecordingBitsPerSample = uint16(16)
	voiceRecordingQueueDepth    = 128
)

type voiceRecordingStatus struct {
	Running              bool       `json:"running"`
	StartedAt            *time.Time `json:"started_at,omitempty"`
	StoppedAt            *time.Time `json:"stopped_at,omitempty"`
	FileName             string     `json:"file_name,omitempty"`
	Path                 string     `json:"path,omitempty"`
	DurationMilliseconds int64      `json:"duration_ms,omitempty"`
	DroppedRemoteFrames  uint64     `json:"dropped_remote_frames,omitempty"`
	DroppedLocalFrames   uint64     `json:"dropped_local_frames,omitempty"`
	LastError            string     `json:"last_error,omitempty"`
}

type stereoCallRecorder struct {
	startedAt time.Time
	finalPath string

	remotePath string
	localPath  string
	remoteFile *os.File
	localFile  *os.File
	remotePCM  chan []byte
	localPCM   chan []byte
	stop       chan struct{}
	stopOnce   sync.Once
	writers    sync.WaitGroup

	errorMu   sync.Mutex
	writerErr error

	droppedRemote atomic.Uint64
	droppedLocal  atomic.Uint64
}

func newStereoCallRecorder(directory string, now time.Time) (*stereoCallRecorder, error) {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("创建录音目录失败: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, fmt.Errorf("设置录音目录权限失败: %w", err)
	}

	remoteFile, err := os.CreateTemp(directory, ".djonehub-remote-*.pcm")
	if err != nil {
		return nil, fmt.Errorf("创建对方语音缓存失败: %w", err)
	}
	localFile, err := os.CreateTemp(directory, ".djonehub-local-*.pcm")
	if err != nil {
		_ = remoteFile.Close()
		_ = os.Remove(remoteFile.Name())
		return nil, fmt.Errorf("创建本机语音缓存失败: %w", err)
	}
	_ = remoteFile.Chmod(0o600)
	_ = localFile.Chmod(0o600)

	recorder := &stereoCallRecorder{
		startedAt:  now,
		finalPath:  uniqueVoiceRecordingPath(directory, now),
		remotePath: remoteFile.Name(),
		localPath:  localFile.Name(),
		remoteFile: remoteFile,
		localFile:  localFile,
		remotePCM:  make(chan []byte, voiceRecordingQueueDepth),
		localPCM:   make(chan []byte, voiceRecordingQueueDepth),
		stop:       make(chan struct{}),
	}
	recorder.writers.Add(2)
	go recorder.writePCM(remoteFile, recorder.remotePCM)
	go recorder.writePCM(localFile, recorder.localPCM)
	return recorder, nil
}

func uniqueVoiceRecordingPath(directory string, now time.Time) string {
	base := "通话录音-" + now.Format("20060102-150405")
	for suffix := 0; ; suffix++ {
		name := base + ".wav"
		if suffix > 0 {
			name = fmt.Sprintf("%s-%d.wav", base, suffix+1)
		}
		path := filepath.Join(directory, name)
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return path
		}
	}
}

func (r *stereoCallRecorder) PushRemote(input []byte) {
	r.push(input, r.remotePCM, &r.droppedRemote)
}

func (r *stereoCallRecorder) PushLocal(input []byte) {
	r.push(input, r.localPCM, &r.droppedLocal)
}

func (r *stereoCallRecorder) push(input []byte, output chan<- []byte, dropped *atomic.Uint64) {
	length := len(input) &^ 1
	if length == 0 {
		return
	}
	select {
	case <-r.stop:
		return
	default:
	}
	frame := append([]byte(nil), input[:length]...)
	select {
	case output <- frame:
	default:
		dropped.Add(1)
	}
}

func (r *stereoCallRecorder) writePCM(file *os.File, frames <-chan []byte) {
	defer r.writers.Done()
	for {
		select {
		case frame := <-frames:
			r.writeFrame(file, frame)
		case <-r.stop:
			for {
				select {
				case frame := <-frames:
					r.writeFrame(file, frame)
				default:
					return
				}
			}
		}
	}
}

func (r *stereoCallRecorder) writeFrame(file *os.File, frame []byte) {
	if len(frame) == 0 {
		return
	}
	if _, err := file.Write(frame); err != nil {
		r.errorMu.Lock()
		if r.writerErr == nil {
			r.writerErr = err
		}
		r.errorMu.Unlock()
	}
}

func (r *stereoCallRecorder) Stop() (voiceRecordingStatus, error) {
	r.stopOnce.Do(func() { close(r.stop) })
	r.writers.Wait()

	remoteCloseErr := r.remoteFile.Close()
	localCloseErr := r.localFile.Close()
	r.errorMu.Lock()
	writerErr := r.writerErr
	r.errorMu.Unlock()
	if writerErr == nil {
		writerErr = remoteCloseErr
	}
	if writerErr == nil {
		writerErr = localCloseErr
	}

	status := voiceRecordingStatus{
		Running:             false,
		StartedAt:           &r.startedAt,
		FileName:            filepath.Base(r.finalPath),
		Path:                r.finalPath,
		DroppedRemoteFrames: r.droppedRemote.Load(),
		DroppedLocalFrames:  r.droppedLocal.Load(),
	}
	stoppedAt := time.Now()
	status.StoppedAt = &stoppedAt
	defer func() {
		_ = os.Remove(r.remotePath)
		_ = os.Remove(r.localPath)
	}()

	if writerErr != nil {
		status.LastError = fmt.Sprintf("写入录音缓存失败: %v", writerErr)
		return status, errors.New(status.LastError)
	}
	frames, err := mergeMonoPCMToStereoWAV(r.remotePath, r.localPath, r.finalPath, voiceSampleRate)
	if err != nil {
		status.LastError = err.Error()
		return status, err
	}
	status.DurationMilliseconds = int64(frames) * 1000 / int64(voiceSampleRate)
	return status, nil
}

func mergeMonoPCMToStereoWAV(remotePath, localPath, finalPath string, sampleRate uint32) (uint64, error) {
	remote, err := os.Open(remotePath)
	if err != nil {
		return 0, fmt.Errorf("读取对方语音缓存失败: %w", err)
	}
	defer remote.Close()
	local, err := os.Open(localPath)
	if err != nil {
		return 0, fmt.Errorf("读取本机语音缓存失败: %w", err)
	}
	defer local.Close()

	remoteInfo, err := remote.Stat()
	if err != nil {
		return 0, err
	}
	localInfo, err := local.Stat()
	if err != nil {
		return 0, err
	}
	remoteSamples := uint64(remoteInfo.Size() / 2)
	localSamples := uint64(localInfo.Size() / 2)
	frames := max(remoteSamples, localSamples)
	dataSize := frames * uint64(voiceRecordingChannels) * uint64(voiceRecordingBitsPerSample/8)
	if dataSize > math.MaxUint32-36 {
		return 0, errors.New("录音时间过长，已超过 WAV 4 GB 上限")
	}

	output, err := os.OpenFile(finalPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, fmt.Errorf("创建 WAV 录音失败: %w", err)
	}
	complete := false
	defer func() {
		_ = output.Close()
		if !complete {
			_ = os.Remove(finalPath)
		}
	}()
	if err := writeWAVHeader(output, sampleRate, uint32(dataSize)); err != nil {
		return 0, err
	}

	const blockSamples = 4096
	remoteBuffer := make([]byte, blockSamples*2)
	localBuffer := make([]byte, blockSamples*2)
	interleaved := make([]byte, blockSamples*4)
	for written := uint64(0); written < frames; {
		count := min(uint64(blockSamples), frames-written)
		remoteBlock := remoteBuffer[:count*2]
		localBlock := localBuffer[:count*2]
		clear(remoteBlock)
		clear(localBlock)
		_, _ = io.ReadFull(remote, remoteBlock)
		_, _ = io.ReadFull(local, localBlock)
		stereoBlock := interleaved[:count*4]
		for index := uint64(0); index < count; index++ {
			binary.LittleEndian.PutUint16(stereoBlock[index*4:index*4+2], binary.LittleEndian.Uint16(remoteBlock[index*2:index*2+2]))
			binary.LittleEndian.PutUint16(stereoBlock[index*4+2:index*4+4], binary.LittleEndian.Uint16(localBlock[index*2:index*2+2]))
		}
		if _, err := output.Write(stereoBlock); err != nil {
			return 0, fmt.Errorf("写入 WAV 录音失败: %w", err)
		}
		written += count
	}
	if err := output.Sync(); err != nil {
		return 0, fmt.Errorf("保存 WAV 录音失败: %w", err)
	}
	if err := output.Close(); err != nil {
		return 0, fmt.Errorf("关闭 WAV 录音失败: %w", err)
	}
	complete = true
	return frames, nil
}

func writeWAVHeader(output io.Writer, sampleRate uint32, dataSize uint32) error {
	byteRate := sampleRate * uint32(voiceRecordingChannels) * uint32(voiceRecordingBitsPerSample/8)
	blockAlign := voiceRecordingChannels * (voiceRecordingBitsPerSample / 8)
	header := make([]byte, 44)
	copy(header[0:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:8], dataSize+36)
	copy(header[8:12], "WAVE")
	copy(header[12:16], "fmt ")
	binary.LittleEndian.PutUint32(header[16:20], 16)
	binary.LittleEndian.PutUint16(header[20:22], 1)
	binary.LittleEndian.PutUint16(header[22:24], voiceRecordingChannels)
	binary.LittleEndian.PutUint32(header[24:28], sampleRate)
	binary.LittleEndian.PutUint32(header[28:32], byteRate)
	binary.LittleEndian.PutUint16(header[32:34], blockAlign)
	binary.LittleEndian.PutUint16(header[34:36], voiceRecordingBitsPerSample)
	copy(header[36:40], "data")
	binary.LittleEndian.PutUint32(header[40:44], dataSize)
	if _, err := output.Write(header); err != nil {
		return fmt.Errorf("写入 WAV 文件头失败: %w", err)
	}
	return nil
}

func voiceRecordingsDirectory() (string, error) {
	directory, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("读取用户数据目录失败: %w", err)
	}
	return filepath.Join(directory, "DJOneHub", "Recordings"), nil
}
