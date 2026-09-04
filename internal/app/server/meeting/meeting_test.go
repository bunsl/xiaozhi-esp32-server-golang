package meeting

import (
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
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
		{name: "bad version", data: mutateByte(valid, 4, 2), max: 1024},
		{name: "bad encoding", data: mutateByte(valid, 5, 2), max: 1024},
		{name: "bad reserved", data: mutateByte(valid, 22, 1), max: 1024},
		{name: "bad payload length", data: mutatePayloadLength(valid, 100), max: 1024},
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

func mutateByte(data []byte, offset int, value byte) []byte {
	mutated := append([]byte(nil), data...)
	mutated[offset] = value
	return mutated
}

func mutatePayloadLength(data []byte, length uint32) []byte {
	mutated := append([]byte(nil), data...)
	binary.LittleEndian.PutUint32(mutated[18:22], length)
	return mutated
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

func TestSessionTracksMissingFramesAndRejectsBackwardsOrOversize(t *testing.T) {
	session, err := NewSession(SessionConfig{StorageDir: t.TempDir(), MaxBytes: 640}, "device", "meeting", StartRequest{
		SampleRate: 16000,
		Channels:   2,
		Format:     PCMFormat,
	})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if err := session.WriteFrame(Frame{Sequence: 2, Payload: make([]byte, 4)}); err != nil {
		t.Fatalf("WriteFrame(gap) error = %v", err)
	}
	if err := session.WriteFrame(Frame{Sequence: 1, Payload: make([]byte, 4)}); err == nil {
		t.Fatal("WriteFrame(backwards) unexpectedly succeeded")
	}
	if err := session.WriteFrame(Frame{Sequence: 3, Payload: make([]byte, 638)}); err == nil {
		t.Fatal("WriteFrame(oversize) unexpectedly succeeded")
	}
	result, err := session.Stop()
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if result.MissingFrames != 2 || result.Frames != 1 {
		t.Fatalf("result = %+v", result)
	}
	second, err := session.Stop()
	if err != nil || second.Path != result.Path {
		t.Fatalf("idempotent Stop() = %+v, %v", second, err)
	}
}

func TestNewSessionRejectsInvalidParameters(t *testing.T) {
	tests := []StartRequest{
		{SampleRate: 7999, Channels: 1, Format: PCMFormat},
		{SampleRate: 48001, Channels: 1, Format: PCMFormat},
		{SampleRate: 16000, Channels: 0, Format: PCMFormat},
		{SampleRate: 16000, Channels: 3, Format: PCMFormat},
	}
	for _, request := range tests {
		if _, err := NewSession(SessionConfig{StorageDir: t.TempDir()}, "device", "meeting", request); err == nil {
			t.Fatalf("NewSession(%+v) unexpectedly succeeded", request)
		}
	}
	if _, err := NewSession(SessionConfig{StorageDir: t.TempDir()}, "../", "meeting", StartRequest{
		SampleRate: 16000,
		Channels:   1,
		Format:     PCMFormat,
	}); err == nil {
		t.Fatal("NewSession(invalid device) unexpectedly succeeded")
	}
	storageFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(storageFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := NewSession(SessionConfig{StorageDir: storageFile}, "device", "meeting", StartRequest{
		SampleRate: 16000,
		Channels:   1,
		Format:     PCMFormat,
	}); err == nil {
		t.Fatal("NewSession(file storage) unexpectedly succeeded")
	}
}

func TestHandlerStartAudioStop(t *testing.T) {
	server := httptest.NewServer(NewHandler(t.TempDir()))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/xiaozhi/meeting/v1/"
	header := http.Header{"Device-Id": []string{"device-test"}}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer conn.Close()

	if err := conn.WriteJSON(map[string]interface{}{
		"type":        MessageTypeStart,
		"meeting_id":  "meeting-test",
		"sample_rate": 16000,
		"channels":    2,
		"format":      PCMFormat,
	}); err != nil {
		t.Fatalf("start write error = %v", err)
	}
	var ready map[string]interface{}
	if err := conn.ReadJSON(&ready); err != nil {
		t.Fatalf("ready read error = %v", err)
	}
	if ready["type"] != MessageTypeReady {
		t.Fatalf("ready = %#v", ready)
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, EncodeFrame(0, 0, make([]byte, 640))); err != nil {
		t.Fatalf("audio write error = %v", err)
	}
	if err := conn.WriteJSON(map[string]string{"type": MessageTypeStop}); err != nil {
		t.Fatalf("stop write error = %v", err)
	}
	var stopped struct {
		Type   string `json:"type"`
		Result Result `json:"result"`
	}
	if err := conn.ReadJSON(&stopped); err != nil {
		t.Fatalf("stop read error = %v", err)
	}
	if stopped.Type != MessageTypeStop || stopped.Result.Bytes != 640 {
		t.Fatalf("stop = %+v", stopped)
	}
	if _, err := os.Stat(stopped.Result.Path); err != nil {
		t.Fatalf("WAV does not exist: %v", err)
	}
}

func TestHandlerRequiresDeviceID(t *testing.T) {
	server := httptest.NewServer(NewHandler(t.TempDir()))
	defer server.Close()
	response, err := http.Get(server.URL + "/xiaozhi/meeting/v1/")
	if err != nil {
		t.Fatalf("GET() error = %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != 426 {
		t.Fatalf("status = %d, want 426", response.StatusCode)
	}
}

func TestHandlerReportsProtocolErrors(t *testing.T) {
	server := httptest.NewServer(NewHandler(t.TempDir()))
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/xiaozhi/meeting/v1/"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Device-Id": []string{"device-test"}})
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.BinaryMessage, []byte("audio-before-start")); err != nil {
		t.Fatalf("audio write error = %v", err)
	}
	var response map[string]string
	if err := conn.ReadJSON(&response); err != nil {
		t.Fatalf("error response read error = %v", err)
	}
	if response["type"] != MessageTypeError {
		t.Fatalf("response = %#v", response)
	}
	if err := conn.WriteMessage(websocket.TextMessage, []byte("{")); err != nil {
		t.Fatalf("malformed JSON write error = %v", err)
	}
	if err := conn.ReadJSON(&response); err != nil {
		t.Fatalf("malformed JSON response read error = %v", err)
	}
	if err := conn.WriteJSON(map[string]string{"type": MessageTypeStop}); err != nil {
		t.Fatalf("stop-before-start write error = %v", err)
	}
	if err := conn.ReadJSON(&response); err != nil {
		t.Fatalf("stop-before-start response read error = %v", err)
	}
}
