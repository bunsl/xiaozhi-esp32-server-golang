package meeting

import (
	"context"
	"time"

	httpclient "xiaozhi-esp32-server-golang/internal/components/http"
)

type Segment struct {
	SpeakerID   string  `json:"speaker_id"`
	SpeakerName string  `json:"speaker_name"`
	Text        string  `json:"text"`
	StartMs     int64   `json:"start_ms"`
	EndMs       int64   `json:"end_ms"`
	Confidence  float32 `json:"confidence"`
}

type Record struct {
	DeviceID      string     `json:"device_id"`
	MeetingID     string     `json:"meeting_id"`
	Status        string     `json:"status"`
	AudioPath     string     `json:"audio_path"`
	SampleRate    int        `json:"sample_rate"`
	Channels      int        `json:"channels"`
	DurationMs    int64      `json:"duration_ms"`
	Frames        uint64     `json:"frames"`
	MissingFrames uint64     `json:"missing_frames"`
	Transcript    string     `json:"transcript"`
	Summary       string     `json:"summary"`
	SpeakerCount  int        `json:"speaker_count"`
	Error         string     `json:"error"`
	StartedAt     time.Time  `json:"started_at"`
	EndedAt       *time.Time `json:"ended_at,omitempty"`
	Segments      []Segment  `json:"segments,omitempty"`
}

type Client struct {
	client *httpclient.ManagerClient
}

func NewClient(baseURL, token string) *Client {
	return &Client{client: httpclient.NewManagerClient(httpclient.ManagerClientConfig{
		BaseURL: baseURL, AuthToken: token, Timeout: 10 * time.Second, MaxRetries: 3,
	})}
}

func (c *Client) Upsert(ctx context.Context, record Record) error {
	return c.client.DoRequest(ctx, httpclient.RequestOptions{
		Method: "POST", Path: "/api/internal/meetings", Body: record,
	})
}
