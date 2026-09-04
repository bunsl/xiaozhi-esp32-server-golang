package meeting

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
)

const (
	MessageTypeStart = "meeting.start"
	MessageTypeStop  = "meeting.stop"
	MessageTypeReady = "meeting.ready"
	MessageTypeError = "meeting.error"
)

type Handler struct {
	Upgrader        websocket.Upgrader
	SessionConfig   SessionConfig
	MaxFramePayload int
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

	var session *Session
	defer func() {
		if session != nil {
			if result, stopErr := session.Stop(); stopErr != nil {
				log.Printf("会议连接断开，WAV 收尾失败: device=%s error=%v", deviceID, stopErr)
			} else {
				log.Printf("会议连接断开，已保存 WAV: device=%s meeting=%s path=%s bytes=%d", deviceID, result.MeetingID, result.Path, result.Bytes)
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
				h.writeError(conn, "invalid JSON: "+err.Error())
				continue
			}
			switch envelope.Type {
			case MessageTypeStart:
				if session != nil {
					h.writeError(conn, "meeting is already active")
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
					h.writeError(conn, startErr.Error())
					continue
				}
				session = newSession
				_ = conn.WriteJSON(map[string]interface{}{
					"type":        MessageTypeReady,
					"meeting_id":  envelope.MeetingID,
					"sample_rate": request.SampleRate,
					"channels":    request.Channels,
					"format":      request.Format,
				})
			case MessageTypeStop:
				if session == nil {
					h.writeError(conn, "meeting is not active")
					continue
				}
				result, stopErr := session.Stop()
				if stopErr != nil {
					h.writeError(conn, stopErr.Error())
					return
				}
				_ = conn.WriteJSON(map[string]interface{}{"type": MessageTypeStop, "result": result})
				return
			default:
				h.writeError(conn, fmt.Sprintf("unsupported meeting message type: %q", envelope.Type))
			}
		case websocket.BinaryMessage:
			if session == nil {
				h.writeError(conn, "send meeting.start before audio")
				continue
			}
			frame, decodeErr := DecodeFrame(data, h.MaxFramePayload)
			if decodeErr != nil {
				h.writeError(conn, decodeErr.Error())
				continue
			}
			if writeErr := session.WriteFrame(frame); writeErr != nil {
				h.writeError(conn, writeErr.Error())
				return
			}
		default:
			h.writeError(conn, "unsupported WebSocket message type")
		}
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
