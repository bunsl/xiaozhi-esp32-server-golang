package meeting

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	payload := []byte{0x01, 0x00, 0xff, 0x7f}
	encoded := EncodeFrame(42, 123456, payload)

	frame, err := DecodeFrame(encoded, 1024)
	if err != nil {
		t.Fatalf("DecodeFrame() error = %v", err)
	}
	if frame.Sequence != 42 || frame.TimestampMs != 123456 {
		t.Fatalf("frame metadata = %+v", frame)
	}
	if string(frame.Payload) != string(payload) {
		t.Fatalf("payload = %v, want %v", frame.Payload, payload)
	}
}

func TestDecodeFrameRejectsCorruptionAndOversize(t *testing.T) {
	valid := EncodeFrame(1, 0, []byte{0, 0})

	tests := []struct {
		name string
		data []byte
		max  int
	}{
		{name: "short", data: valid[:FrameHeaderSize-1], max: 1024},
		{name: "bad magic", data: append([]byte("BAD!"), valid[4:]...), max: 1024},
		{name: "odd pcm payload", data: EncodeFrame(1, 0, []byte{0}), max: 1024},
		{name: "oversize", data: valid, max: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeFrame(tt.data, tt.max); err == nil {
				t.Fatal("DecodeFrame() unexpectedly succeeded")
			}
		})
	}
}

func TestSessionWritesWav(t *testing.T) {
	root := t.TempDir()
	session, err := NewSession(SessionConfig{StorageDir: root}, "device/1", "meeting-1", StartRequest{
		SampleRate:      16000,
		Channels:        2,
		Format:          PCMFormat,
		FrameDurationMs: 20,
	})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}

	if err := session.WriteFrame(Frame{Sequence: 0, TimestampMs: 0, Payload: make([]byte, 640)}); err != nil {
		t.Fatalf("WriteFrame(first) error = %v", err)
	}
	if err := session.WriteFrame(Frame{Sequence: 1, TimestampMs: 20, Payload: make([]byte, 640)}); err != nil {
		t.Fatalf("WriteFrame(second) error = %v", err)
	}
	result, err := session.Stop()
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	if result.Frames != 2 || result.Bytes != 1280 {
		t.Fatalf("result = %+v", result)
	}
	if result.Path == "" || filepath.Ext(result.Path) != ".wav" {
		t.Fatalf("result path = %q", result.Path)
	}
	wav, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(wav[:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		t.Fatalf("invalid WAV signature: %q %q", wav[:4], wav[8:12])
	}
	if got := binary.LittleEndian.Uint16(wav[22:24]); got != 2 {
		t.Fatalf("channels = %d, want 2", got)
	}
	if got := binary.LittleEndian.Uint32(wav[40:44]); got != 1280 {
		t.Fatalf("data size = %d, want 1280", got)
	}
	if got := int64(len(wav)); got != 44+1280 {
		t.Fatalf("file size = %d, want %d", got, 44+1280)
	}
}

func TestNewSessionRejectsUnsupportedFormat(t *testing.T) {
	_, err := NewSession(SessionConfig{StorageDir: t.TempDir()}, "device", "meeting", StartRequest{
		SampleRate: 16000,
		Channels:   2,
		Format:     "opus",
	})
	if err == nil {
		t.Fatal("NewSession() unexpectedly accepted opus")
	}
}
