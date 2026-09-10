package meeting

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/spf13/viper"

	"xiaozhi-esp32-server-golang/internal/domain/asr"
	asrtypes "xiaozhi-esp32-server-golang/internal/domain/asr/types"
	userconfig "xiaozhi-esp32-server-golang/internal/domain/config"
	configtypes "xiaozhi-esp32-server-golang/internal/domain/config/types"
	"xiaozhi-esp32-server-golang/internal/domain/llm"
	"xiaozhi-esp32-server-golang/internal/domain/speaker"
)

type TranscriptSegment struct {
	SpeakerID   string  `json:"speaker_id"`
	SpeakerName string  `json:"speaker_name"`
	Text        string  `json:"text"`
	StartMs     int64   `json:"start_ms"`
	EndMs       int64   `json:"end_ms"`
	Confidence  float32 `json:"confidence"`
}

type transcriptSender func(text, speaker string, isFinal bool) error

type Transcriber struct {
	ctx      context.Context
	cancel   context.CancelFunc
	provider asr.AsrProvider
	audio    chan []float32
	done     chan struct{}
	send     transcriptSender
	config   configtypes.UConfig

	mu          sync.Mutex
	closed      bool
	startStamp  uint64
	lastStamp   uint64
	segmentFrom int64
	segments    []TranscriptSegment
	utterance   []float32
}

func NewTranscriber(deviceID string, send transcriptSender) (*Transcriber, error) {
	ctx, cancel := context.WithCancel(context.Background())
	configProvider, err := userconfig.GetProvider(viper.GetString("config_provider.type"))
	if err != nil {
		cancel()
		return nil, fmt.Errorf("load meeting config provider: %w", err)
	}
	configCtx, configCancel := context.WithTimeout(ctx, 5*time.Second)
	deviceConfig, err := configProvider.GetUserConfig(configCtx, deviceID)
	configCancel()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("load meeting device config: %w", err)
	}
	asrConfig := make(map[string]interface{}, len(deviceConfig.Asr.Config)+2)
	for key, value := range deviceConfig.Asr.Config {
		asrConfig[key] = value
	}
	asrConfig["sample_rate"] = 16000
	asrConfig["auto_end"] = false
	provider, err := asr.NewAsrProvider(deviceConfig.Asr.Provider, asrConfig)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create meeting ASR: %w", err)
	}
	audio := make(chan []float32, 200)
	results, err := provider.StreamingRecognize(ctx, audio)
	if err != nil {
		_ = provider.Close()
		cancel()
		return nil, fmt.Errorf("start meeting ASR: %w", err)
	}
	t := &Transcriber{
		ctx: ctx, cancel: cancel, provider: provider, audio: audio, done: make(chan struct{}),
		send: send, config: deviceConfig,
	}
	go t.consume(results)
	return t, nil
}

func (t *Transcriber) Feed(frame Frame, channels int) {
	pcm := downmixS16LE(frame.Payload, channels)
	if len(pcm) == 0 {
		return
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	if t.startStamp == 0 {
		t.startStamp = frame.TimestampMs
	}
	t.lastStamp = frame.TimestampMs
	if viper.GetBool("voice_identify.enable") && len(t.config.VoiceIdentify) > 0 {
		const maxSpeakerSamples = 16000 * 30
		if len(t.utterance)+len(pcm) > maxSpeakerSamples {
			drop := len(t.utterance) + len(pcm) - maxSpeakerSamples
			t.utterance = append(t.utterance[:0], t.utterance[drop:]...)
		}
		t.utterance = append(t.utterance, pcm...)
	}
	select {
	case t.audio <- pcm:
	default:
	}
	t.mu.Unlock()
}

func (t *Transcriber) Close() {
	t.mu.Lock()
	if !t.closed {
		t.closed = true
		close(t.audio)
	}
	t.mu.Unlock()
	select {
	case <-t.done:
	case <-time.After(8 * time.Second):
		t.cancel()
		select {
		case <-t.done:
		case <-time.After(2 * time.Second):
		}
	}
	t.cancel()
	_ = t.provider.Close()
}

func (t *Transcriber) Snapshot() (string, []TranscriptSegment) {
	t.mu.Lock()
	defer t.mu.Unlock()
	segments := append([]TranscriptSegment(nil), t.segments...)
	parts := make([]string, 0, len(segments))
	for _, segment := range segments {
		parts = append(parts, segment.Text)
	}
	return strings.Join(parts, "\n"), segments
}

func (t *Transcriber) Summarize(ctx context.Context, transcript string) (string, error) {
	if strings.TrimSpace(transcript) == "" {
		return "", nil
	}
	provider, err := llm.GetLLMProvider(t.config.Llm.Provider, t.config.Llm.Config)
	if err != nil {
		return "", err
	}
	defer provider.Close()
	messages := []*schema.Message{
		schema.SystemMessage("你是会议纪要助手。请用中文输出简洁、准确的结构化纪要，包含主题、关键结论、行动项和待确认事项；不得编造转写中没有的信息。"),
		schema.UserMessage(transcript),
	}
	var summary strings.Builder
	for message := range provider.ResponseWithContext(ctx, "meeting-summary", messages, nil) {
		if message == nil {
			continue
		}
		if errText := llm.LLMErrorMessage(message); errText != "" {
			return "", fmt.Errorf("%s", errText)
		}
		summary.WriteString(message.Content)
	}
	return strings.TrimSpace(summary.String()), nil
}

func (t *Transcriber) consume(results <-chan asrtypes.StreamingResult) {
	defer close(t.done)
	for result := range results {
		if result.Error != nil || strings.TrimSpace(result.Text) == "" {
			continue
		}
		if !result.IsFinal {
			if t.send != nil {
				_ = t.send(result.Text, "正在识别", false)
			}
			continue
		}
		t.mu.Lock()
		utterance := append([]float32(nil), t.utterance...)
		t.utterance = t.utterance[:0]
		endMs := int64(0)
		if t.startStamp > 0 && t.lastStamp >= t.startStamp {
			endMs = int64(t.lastStamp - t.startStamp)
		}
		t.mu.Unlock()

		speakerID, speakerName, confidence := t.identifySpeaker(utterance)
		if speakerName == "" {
			speakerName = "未知发言人"
		}
		if t.send != nil {
			_ = t.send(result.Text, speakerName, true)
		}
		t.mu.Lock()
		t.segments = append(t.segments, TranscriptSegment{
			SpeakerID: speakerID, SpeakerName: speakerName, Text: strings.TrimSpace(result.Text),
			StartMs: t.segmentFrom, EndMs: endMs,
			Confidence: confidence,
		})
		t.segmentFrom = endMs
		t.mu.Unlock()
	}
}

func (t *Transcriber) identifySpeaker(pcm []float32) (string, string, float32) {
	if len(pcm) == 0 || !viper.GetBool("voice_identify.enable") || len(t.config.VoiceIdentify) == 0 {
		return "", "", 0
	}
	baseURL := strings.TrimSpace(viper.GetString("voice_identify.base_url"))
	if baseURL == "" {
		return "", "", 0
	}
	config := map[string]interface{}{"base_url": baseURL}
	if viper.IsSet("voice_identify.threshold") {
		config["threshold"] = viper.GetFloat64("voice_identify.threshold")
	}
	provider, err := speaker.GetSpeakerProvider(config)
	if err != nil {
		return "", "", 0
	}
	defer provider.Close()
	ctx, cancel := context.WithTimeout(t.ctx, 12*time.Second)
	defer cancel()
	if err := provider.StartStreaming(ctx, 16000, t.config.AgentId); err != nil {
		return "", "", 0
	}
	for offset := 0; offset < len(pcm); offset += 3200 {
		end := offset + 3200
		if end > len(pcm) {
			end = len(pcm)
		}
		if err := provider.SendAudioChunk(ctx, pcm[offset:end]); err != nil {
			return "", "", 0
		}
	}
	result, err := provider.FinishAndIdentify(ctx)
	if err != nil || result == nil || !result.Identified {
		return "", "", 0
	}
	if _, configured := t.config.VoiceIdentify[result.SpeakerName]; !configured {
		return "", "", 0
	}
	return result.SpeakerID, result.SpeakerName, result.Confidence
}

func downmixS16LE(payload []byte, channels int) []float32 {
	if channels < 1 || channels > 2 || len(payload)%(channels*2) != 0 {
		return nil
	}
	samples := len(payload) / (channels * 2)
	result := make([]float32, samples)
	for i := 0; i < samples; i++ {
		offset := i * channels * 2
		value := int32(int16(binary.LittleEndian.Uint16(payload[offset : offset+2])))
		if channels == 2 {
			value += int32(int16(binary.LittleEndian.Uint16(payload[offset+2 : offset+4])))
			value /= 2
		}
		result[i] = float32(value) / 32768.0
	}
	return result
}
