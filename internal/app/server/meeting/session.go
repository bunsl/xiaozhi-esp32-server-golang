package meeting

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	defaultStorageDir = "data/meetings"
	defaultMaxBytes   = int64(512 * 1024 * 1024)
)

type StartRequest struct {
	SampleRate      int    `json:"sample_rate"`
	Channels        int    `json:"channels"`
	Format          string `json:"format"`
	FrameDurationMs int    `json:"frame_duration_ms,omitempty"`
}

type SessionConfig struct {
	StorageDir string
	MaxBytes   int64
}

type Result struct {
	DeviceID      string    `json:"device_id"`
	MeetingID     string    `json:"meeting_id"`
	Path          string    `json:"path"`
	SampleRate    int       `json:"sample_rate"`
	Channels      int       `json:"channels"`
	Frames        uint64    `json:"frames"`
	MissingFrames uint64    `json:"missing_frames,omitempty"`
	Bytes         int64     `json:"bytes"`
	DurationMs    int64     `json:"duration_ms"`
	StartedAt     time.Time `json:"started_at"`
	EndedAt       time.Time `json:"ended_at"`
}

// Session writes interleaved PCM to a WAV file without retaining the meeting in memory.
type Session struct {
	mu sync.Mutex

	deviceID  string
	meetingID string
	path      string
	tmpPath   string
	file      *os.File
	startedAt time.Time
	endedAt   time.Time

	sampleRate int
	channels   int
	maxBytes   int64
	bytes      int64
	frames     uint64
	missing    uint64
	nextSeq    uint32
	stopped    bool
	result     Result
}

func NewSession(config SessionConfig, deviceID, meetingID string, request StartRequest) (*Session, error) {
	if request.Format != PCMFormat {
		return nil, fmt.Errorf("unsupported meeting format: %q", request.Format)
	}
	if request.SampleRate < 8000 || request.SampleRate > 48000 {
		return nil, fmt.Errorf("unsupported sample rate: %d", request.SampleRate)
	}
	if request.Channels < 1 || request.Channels > 2 {
		return nil, fmt.Errorf("unsupported channel count: %d", request.Channels)
	}
	devicePart := safeComponent(deviceID)
	meetingPart := safeComponent(meetingID)
	if devicePart == "" || meetingPart == "" {
		return nil, fmt.Errorf("device_id and meeting_id are required")
	}
	storageDir := strings.TrimSpace(config.StorageDir)
	if storageDir == "" {
		storageDir = defaultStorageDir
	}
	if config.MaxBytes <= 0 {
		config.MaxBytes = defaultMaxBytes
	}
	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		return nil, fmt.Errorf("create meeting storage directory: %w", err)
	}

	startedAt := time.Now().UTC()
	base := fmt.Sprintf("%s_%s_%s", devicePart, meetingPart, startedAt.Format("20060102T150405.000000000Z"))
	path := filepath.Join(storageDir, base+".wav")
	tmpPath := path + ".part"
	file, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o640)
	if err != nil {
		return nil, fmt.Errorf("create meeting WAV: %w", err)
	}
	if err := writeWavHeader(file, request.SampleRate, request.Channels, 0); err != nil {
		file.Close()
		os.Remove(tmpPath)
		return nil, fmt.Errorf("write meeting WAV header: %w", err)
	}

	return &Session{
		deviceID:   deviceID,
		meetingID:  meetingID,
		path:       path,
		tmpPath:    tmpPath,
		file:       file,
		startedAt:  startedAt,
		sampleRate: request.SampleRate,
		channels:   request.Channels,
		maxBytes:   config.MaxBytes,
	}, nil
}

func (s *Session) WriteFrame(frame Frame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.file == nil {
		return fmt.Errorf("meeting session is stopped")
	}
	if frame.Sequence < s.nextSeq {
		return fmt.Errorf("meeting frame sequence moved backwards: got=%d expected>=%d", frame.Sequence, s.nextSeq)
	}
	bytesPerSampleFrame := 2 * s.channels
	if len(frame.Payload) == 0 || len(frame.Payload)%bytesPerSampleFrame != 0 {
		return fmt.Errorf("meeting PCM payload must align to %d-channel samples: %d", s.channels, len(frame.Payload))
	}
	if s.bytes+int64(len(frame.Payload)) > s.maxBytes {
		return fmt.Errorf("meeting exceeds maximum size: %d", s.maxBytes)
	}
	if frame.Sequence > s.nextSeq {
		s.missing += uint64(frame.Sequence - s.nextSeq)
	}
	if _, err := s.file.Write(frame.Payload); err != nil {
		return fmt.Errorf("write meeting PCM: %w", err)
	}
	s.bytes += int64(len(frame.Payload))
	s.frames++
	s.nextSeq = frame.Sequence + 1
	return nil
}

func (s *Session) Stop() (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return s.result, nil
	}
	s.endedAt = time.Now().UTC()
	if err := writeWavHeader(s.file, s.sampleRate, s.channels, uint32(s.bytes)); err != nil {
		_ = s.file.Close()
		return Result{}, fmt.Errorf("finalize meeting WAV header: %w", err)
	}
	if err := s.file.Sync(); err != nil {
		_ = s.file.Close()
		return Result{}, fmt.Errorf("sync meeting WAV: %w", err)
	}
	if err := s.file.Close(); err != nil {
		return Result{}, fmt.Errorf("close meeting WAV: %w", err)
	}
	if err := os.Rename(s.tmpPath, s.path); err != nil {
		return Result{}, fmt.Errorf("publish meeting WAV: %w", err)
	}
	s.stopped = true
	s.result = Result{
		DeviceID:      s.deviceID,
		MeetingID:     s.meetingID,
		Path:          s.path,
		SampleRate:    s.sampleRate,
		Channels:      s.channels,
		Frames:        s.frames,
		MissingFrames: s.missing,
		Bytes:         s.bytes,
		DurationMs:    s.endedAt.Sub(s.startedAt).Milliseconds(),
		StartedAt:     s.startedAt,
		EndedAt:       s.endedAt,
	}
	return s.result, nil
}

func safeComponent(value string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(value) {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	component := strings.Trim(b.String(), "._")
	if component == "" || component == "." || component == ".." {
		return ""
	}
	return component
}

func writeWavHeader(file *os.File, sampleRate, channels int, dataBytes uint32) error {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	header := make([]byte, 44)
	copy(header[0:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:8], 36+dataBytes)
	copy(header[8:12], "WAVE")
	copy(header[12:16], "fmt ")
	binary.LittleEndian.PutUint32(header[16:20], 16)
	binary.LittleEndian.PutUint16(header[20:22], 1)
	binary.LittleEndian.PutUint16(header[22:24], uint16(channels))
	binary.LittleEndian.PutUint32(header[24:28], uint32(sampleRate))
	byteRate := uint32(sampleRate * channels * 2)
	binary.LittleEndian.PutUint32(header[28:32], byteRate)
	binary.LittleEndian.PutUint16(header[32:34], uint16(channels*2))
	binary.LittleEndian.PutUint16(header[34:36], 16)
	copy(header[36:40], "data")
	binary.LittleEndian.PutUint32(header[40:44], dataBytes)
	if _, err := file.WriteAt(header, 0); err != nil {
		return err
	}
	_, err := file.Seek(0, io.SeekEnd)
	return err
}
