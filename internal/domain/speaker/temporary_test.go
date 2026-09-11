package speaker

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestEncodePCM16WAV(t *testing.T) {
	wav := encodePCM16WAV([]float32{-1, 0, 1}, 16000)
	if len(wav) != 50 {
		t.Fatalf("unexpected WAV size: %d", len(wav))
	}
	if string(wav[:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		t.Fatalf("invalid WAV header: %q %q", wav[:4], wav[8:12])
	}
	if got := binary.LittleEndian.Uint32(wav[24:28]); got != 16000 {
		t.Fatalf("unexpected sample rate: %d", got)
	}
	if got := int16(binary.LittleEndian.Uint16(wav[44:46])); got > -32766 {
		t.Fatalf("negative sample was not encoded: %d", got)
	}
	if got := int16(binary.LittleEndian.Uint16(wav[48:50])); got < 32766 {
		t.Fatalf("positive sample was not encoded: %d", got)
	}
}

func TestTemporarySpeakerLabels(t *testing.T) {
	tests := map[int]string{
		0:  "发言人 A",
		25: "发言人 Z",
		26: "发言人 AA",
		27: "发言人 AB",
	}
	for index, want := range tests {
		if got := TemporarySpeakerName(index); got != want {
			t.Fatalf("index %d: got %q, want %q", index, got, want)
		}
	}
}

func TestTemporarySpeakerRequests(t *testing.T) {
	var registered bool
	var deleted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/speaker/register":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("parse multipart form: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			file, _, err := r.FormFile("audio")
			if err != nil {
				t.Errorf("read audio: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			defer file.Close()
			if r.FormValue("speaker_name") != "发言人 A" {
				t.Errorf("unexpected speaker name: %q", r.FormValue("speaker_name"))
			}
			registered = true
		case r.Method == http.MethodDelete:
			if r.URL.Query().Get("uid") != "meeting-1" {
				t.Errorf("unexpected uid: %q", r.URL.Query().Get("uid"))
			}
			deleted = true
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	ctx := context.Background()
	if err := RegisterTemporarySpeaker(ctx, server.URL, "meeting-1", "agent-1",
		"speaker-a", "发言人 A", "sample-a", make([]float32, 1600), 16000); err != nil {
		t.Fatalf("register temporary speaker: %v", err)
	}
	if err := DeleteTemporarySpeaker(ctx, server.URL, "meeting-1", "agent-1",
		"speaker-a"); err != nil {
		t.Fatalf("delete temporary speaker: %v", err)
	}
	if !registered || !deleted {
		t.Fatalf("requests not observed: registered=%v deleted=%v", registered, deleted)
	}
}

func TestStreamingClientPassesMeetingUID(t *testing.T) {
	queryCh := make(chan string, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queryCh <- r.URL.RawQuery
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.WriteJSON(map[string]string{"type": "connection"})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	client := NewStreamingClient(server.URL)
	if err := client.Connect(16000, "agent-1", "meeting-1", 0.55); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()
	select {
	case query := <-queryCh:
		if query != "sample_rate=16000&agent_id=agent-1&uid=meeting-1&threshold=0.550000" {
			t.Fatalf("unexpected query: %q", query)
		}
	case <-time.After(time.Second):
		t.Fatal("websocket request not observed")
	}
}
