package speaker

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxSpeakerErrorBody = 4096

var temporarySpeakerHTTPClient = &http.Client{Timeout: 10 * time.Second}

// RegisterTemporarySpeaker stores one meeting-scoped voice sample in voice-server.
// The caller must delete the temporary speaker when the meeting is finalized.
func RegisterTemporarySpeaker(ctx context.Context, baseURL, uid, agentID, speakerID,
	speakerName, uuid string, pcm []float32, sampleRate int) error {
	if len(pcm) == 0 || sampleRate <= 0 {
		return fmt.Errorf("temporary speaker audio is empty")
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	fields := map[string]string{
		"uid":          uid,
		"agent_id":     agentID,
		"speaker_id":   speakerID,
		"speaker_name": speakerName,
		"uuid":         uuid,
	}
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			return err
		}
	}
	audioPart, err := writer.CreateFormFile("audio", "meeting-speaker.wav")
	if err != nil {
		return err
	}
	if _, err := audioPart.Write(encodePCM16WAV(pcm, sampleRate)); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}

	endpoint := strings.TrimRight(baseURL, "/") + "/api/v1/speaker/register"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return doTemporarySpeakerRequest(req)
}

// DeleteTemporarySpeaker removes all samples belonging to a meeting-scoped speaker.
func DeleteTemporarySpeaker(ctx context.Context, baseURL, uid, agentID, speakerID string) error {
	endpoint := strings.TrimRight(baseURL, "/") + "/api/v1/speaker/" +
		url.PathEscape(speakerID) + "?uid=" + url.QueryEscape(uid) +
		"&agent_id=" + url.QueryEscape(agentID)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	return doTemporarySpeakerRequest(req)
}

func doTemporarySpeakerRequest(req *http.Request) error {
	response, err := temporarySpeakerHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil
	}

	data, _ := io.ReadAll(io.LimitReader(response.Body, maxSpeakerErrorBody))
	var payload struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(data, &payload)
	if payload.Error == "" {
		payload.Error = strings.TrimSpace(string(data))
	}
	return fmt.Errorf("voice-server returned %s: %s", response.Status, payload.Error)
}

func encodePCM16WAV(pcm []float32, sampleRate int) []byte {
	const (
		channels      = 1
		bitsPerSample = 16
		headerSize    = 44
	)
	dataSize := len(pcm) * 2
	wav := make([]byte, headerSize+dataSize)
	copy(wav[0:4], "RIFF")
	binary.LittleEndian.PutUint32(wav[4:8], uint32(36+dataSize))
	copy(wav[8:12], "WAVE")
	copy(wav[12:16], "fmt ")
	binary.LittleEndian.PutUint32(wav[16:20], 16)
	binary.LittleEndian.PutUint16(wav[20:22], 1)
	binary.LittleEndian.PutUint16(wav[22:24], channels)
	binary.LittleEndian.PutUint32(wav[24:28], uint32(sampleRate))
	byteRate := sampleRate * channels * bitsPerSample / 8
	binary.LittleEndian.PutUint32(wav[28:32], uint32(byteRate))
	binary.LittleEndian.PutUint16(wav[32:34], channels*bitsPerSample/8)
	binary.LittleEndian.PutUint16(wav[34:36], bitsPerSample)
	copy(wav[36:40], "data")
	binary.LittleEndian.PutUint32(wav[40:44], uint32(dataSize))
	for i, sample := range pcm {
		clamped := math.Max(-1, math.Min(1, float64(sample)))
		value := int16(clamped * 32767)
		binary.LittleEndian.PutUint16(wav[headerSize+i*2:], uint16(value))
	}
	return wav
}

func TemporarySpeakerName(index int) string {
	if index < 0 {
		index = 0
	}
	name := ""
	for {
		name = string(rune('A'+index%26)) + name
		index = index/26 - 1
		if index < 0 {
			break
		}
	}
	return "发言人 " + name
}

func TemporarySpeakerID(meetingID string, index int) string {
	return "__meeting_" + meetingID + "_" + strconv.Itoa(index)
}
