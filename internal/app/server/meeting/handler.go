package meeting

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	meetingdata "xiaozhi-esp32-server-golang/internal/data/meeting"
	"xiaozhi-esp32-server-golang/internal/util"
)

const (
	MessageTypeStart      = "meeting.start"
	MessageTypeStop       = "meeting.stop"
	MessageTypeReady      = "meeting.ready"
	MessageTypeError      = "meeting.error"
	MessageTypeTranscript = "meeting.transcript"
)

type Handler struct {
	Upgrader        websocket.Upgrader
	SessionConfig   SessionConfig
	MaxFramePayload int
	records         *meetingdata.Client
}

func NewHandler(storageDir string) *Handler {
	return &Handler{
		Upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin:     func(*http.Request) bool { return true },
		},
		SessionConfig:   SessionConfig{StorageDir: storageDir},
		MaxFramePayload: 1024 * 1024,
		records:         meetingdata.NewClient(util.GetBackendURL(), util.GetManagerAuthToken()),
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !websocket.IsWebSocketUpgrade(r) {
		http.Error(w, "meeting endpoint requires WebSocket upgrade", http.StatusUpgradeRequired)
		return
	}
	deviceID := firstNonEmpty(r.Header.Get("Device-Id"), r.Header.Get("device-id"), r.URL.Query().Get("device_id"), r.URL.Query().Get("device-id"))
	if strings.TrimSpace(deviceID) == "" {
		http.Error(w, "device_id is required", http.StatusBadRequest)
		return
	}
	conn, err := h.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("会议 WebSocket 升级失败: %v", err)
		return
	}
	defer conn.Close()
	var writeMu sync.Mutex
	writeJSON := func(value interface{}) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.WriteJSON(value)
	}
	writeError := func(message string) {
		_ = writeJSON(map[string]string{"type": MessageTypeError, "error": message})
	}

	var session *Session
	var transcriber *Transcriber
	defer func() {
		if session != nil {
			if result, stopErr := session.Stop(); stopErr != nil {
				log.Printf("会议连接断开，WAV 收尾失败: device=%s error=%v", deviceID, stopErr)
			} else {
				log.Printf("会议连接断开，已保存 WAV: device=%s meeting=%s path=%s bytes=%d", deviceID, result.MeetingID, result.Path, result.Bytes)
				h.finalize(result, transcriber)
			}
		}
	}()

	for {
		messageType, data, readErr := conn.ReadMessage()
		if readErr != nil {
			return
		}
		switch messageType {
		case websocket.TextMessage:
			var envelope struct {
				Type        string        `json:"type"`
				MeetingID   string        `json:"meeting_id"`
				Audio       StartRequest  `json:"audio"`
				AudioParams *StartRequest `json:"audio_params"`
				StartRequest
			}
			if err := json.Unmarshal(data, &envelope); err != nil {
				writeError("invalid JSON: " + err.Error())
				continue
			}
			switch envelope.Type {
			case MessageTypeStart:
				if session != nil {
					writeError("meeting is already active")
					continue
				}
				request := envelope.StartRequest
				if request.Format == "" && envelope.AudioParams != nil {
					request = *envelope.AudioParams
				}
				if request.Format == "" {
					request = envelope.Audio
				}
				newSession, startErr := NewSession(h.SessionConfig, deviceID, envelope.MeetingID, request)
				if startErr != nil {
					writeError(startErr.Error())
					continue
				}
				session = newSession
				go h.saveRecord(meetingdata.Record{
					DeviceID: deviceID, MeetingID: envelope.MeetingID, Status: "recording",
					AudioPath: newSession.path, SampleRate: request.SampleRate, Channels: request.Channels,
					StartedAt: newSession.startedAt,
				})
				transcriber, err = NewTranscriber(deviceID, func(text, speaker string, isFinal bool) error {
					return writeJSON(map[string]interface{}{
						"type": MessageTypeTranscript, "meeting_id": envelope.MeetingID,
						"text": text, "speaker": speaker, "is_final": isFinal,
					})
				})
				if err != nil {
					log.Printf("会议实时转写未启用: device=%s error=%v", deviceID, err)
					transcriber = nil
				}
				_ = writeJSON(map[string]interface{}{
					"type":        MessageTypeReady,
					"meeting_id":  envelope.MeetingID,
					"sample_rate": request.SampleRate,
					"channels":    request.Channels,
					"format":      request.Format,
				})
			case MessageTypeStop:
				if session == nil {
					writeError("meeting is not active")
					continue
				}
				result, stopErr := session.Stop()
				if stopErr != nil {
					writeError(stopErr.Error())
					return
				}
				_ = writeJSON(map[string]interface{}{"type": MessageTypeStop, "result": result})
				session = nil
				h.finalize(result, transcriber)
				transcriber = nil
				return
			default:
				writeError(fmt.Sprintf("unsupported meeting message type: %q", envelope.Type))
			}
		case websocket.BinaryMessage:
			if session == nil {
				writeError("send meeting.start before audio")
				continue
			}
			frame, decodeErr := DecodeFrame(data, h.MaxFramePayload)
			if decodeErr != nil {
				writeError(decodeErr.Error())
				continue
			}
			if writeErr := session.WriteFrame(frame); writeErr != nil {
				writeError(writeErr.Error())
				return
			}
			if transcriber != nil {
				transcriber.Feed(frame, session.channels)
			}
		default:
			writeError("unsupported WebSocket message type")
		}
	}
}

func (h *Handler) finalize(result Result, transcriber *Transcriber) {
	go func() {
		record := meetingdata.Record{
			DeviceID: result.DeviceID, MeetingID: result.MeetingID, Status: "transcribing",
			AudioPath: result.Path, SampleRate: result.SampleRate, Channels: result.Channels,
			DurationMs: result.DurationMs, Frames: result.Frames, MissingFrames: result.MissingFrames,
			StartedAt: result.StartedAt, EndedAt: &result.EndedAt,
		}
		if transcriber == nil {
			record.Status = "completed"
			h.saveRecord(record)
			return
		}
		transcriber.Close()
		record.Transcript, record.Segments = transcriberSnapshot(transcriber)
		record.SpeakerCount = countSpeakers(record.Segments)
		record.Status = "summarizing"
		h.saveRecord(record)

		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		summary, err := transcriber.Summarize(ctx, record.Transcript)
		cancel()
		if err != nil {
			record.Status = "completed"
			record.Error = "AI 纪要生成失败: " + err.Error()
		} else {
			record.Status = "completed"
			record.Summary = summary
		}
		h.saveRecord(record)
	}()
}

func countSpeakers(segments []meetingdata.Segment) int {
	seen := make(map[string]struct{})
	for _, segment := range segments {
		name := strings.TrimSpace(segment.SpeakerName)
		if name != "" {
			seen[name] = struct{}{}
		}
	}
	return len(seen)
}

func transcriberSnapshot(transcriber *Transcriber) (string, []meetingdata.Segment) {
	transcript, source := transcriber.Snapshot()
	segments := make([]meetingdata.Segment, len(source))
	for i, segment := range source {
		segments[i] = meetingdata.Segment{
			SpeakerID: segment.SpeakerID, SpeakerName: segment.SpeakerName, Text: segment.Text,
			StartMs: segment.StartMs, EndMs: segment.EndMs, Confidence: segment.Confidence,
		}
	}
	return transcript, segments
}

func (h *Handler) saveRecord(record meetingdata.Record) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := h.records.Upsert(ctx, record); err != nil {
		log.Printf("保存会议记录失败: device=%s meeting=%s error=%v", record.DeviceID, record.MeetingID, err)
	}
}

func (h *Handler) writeError(conn *websocket.Conn, message string) {
	_ = conn.WriteJSON(map[string]string{"type": MessageTypeError, "error": message})
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
