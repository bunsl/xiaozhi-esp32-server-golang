package meeting

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/spf13/viper"

	"xiaozhi-esp32-server-golang/internal/domain/asr"
	userconfig "xiaozhi-esp32-server-golang/internal/domain/config"
	configtypes "xiaozhi-esp32-server-golang/internal/domain/config/types"
	"xiaozhi-esp32-server-golang/internal/domain/llm"
	"xiaozhi-esp32-server-golang/internal/domain/speaker"
	log "xiaozhi-esp32-server-golang/logger"
)

const (
	meetingAudioQueueDepth = 500
	utteranceAudioDepth    = 1600
	meetingPreRollMs       = int64(200)
	meetingSilenceMs       = int64(600)
	meetingMaxUtteranceMs  = int64(30_000)
)

type TranscriptSegment struct {
	SpeakerID   string  `json:"speaker_id"`
	SpeakerName string  `json:"speaker_name"`
	Text        string  `json:"text"`
	StartMs     int64   `json:"start_ms"`
	EndMs       int64   `json:"end_ms"`
	Confidence  float32 `json:"confidence"`
}

type transcriptSender func(text, speaker string, isFinal bool, startMs, endMs int64) error

type audioPacket struct {
	pcm     []float32
	startMs int64
	endMs   int64
}

type utteranceSession struct {
	index      int
	startMs    int64
	endMs      int64
	audio      chan []float32
	pcm        []float32
	speakerPCM []float32
	endReady   chan struct{}
	previous   <-chan struct{}
	completed  chan struct{}
	latestEnd  atomic.Int64
	dropped    atomic.Uint64
}

type Transcriber struct {
	ctx       context.Context
	cancel    context.CancelFunc
	input     chan audioPacket
	done      chan struct{}
	send      transcriptSender
	config    configtypes.UConfig
	deviceID  string
	meetingID string
	asrConfig map[string]interface{}

	mu         sync.Mutex
	closed     bool
	startStamp uint64
	lastStamp  uint64
	lastEndMs  int64
	segments   []TranscriptSegment
	workers    sync.WaitGroup
	asrSlots   chan struct{}

	speakerMu         sync.Mutex
	temporarySpeakers []string
}

func NewTranscriber(deviceID, meetingID string, send transcriptSender) (*Transcriber, error) {
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
	if deviceConfig.Asr.Provider == "funasr" &&
		strings.EqualFold(strings.TrimSpace(stringConfig(asrConfig, "mode")), "offline") {
		asrConfig["mode"] = "2pass"
	}
	provider, err := asr.NewAsrProvider(deviceConfig.Asr.Provider, asrConfig)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create meeting ASR: %w", err)
	}
	_ = provider.Close()
	t := &Transcriber{
		ctx: ctx, cancel: cancel, input: make(chan audioPacket, meetingAudioQueueDepth),
		done: make(chan struct{}), send: send, config: deviceConfig, deviceID: deviceID,
		meetingID: meetingID, asrConfig: asrConfig, asrSlots: make(chan struct{}, 2),
	}
	go t.process()
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
	startMs := int64(frame.TimestampMs - t.startStamp)
	durationMs := int64(len(pcm)) * 1000 / 16000
	t.lastEndMs = startMs + durationMs
	select {
	case t.input <- audioPacket{pcm: pcm, startMs: startMs, endMs: startMs + durationMs}:
	default:
		log.Warnf("会议转写输入队列已满，丢弃音频帧: meeting=%s at=%dms",
			t.meetingID, startMs)
	}
	t.mu.Unlock()
}

func (t *Transcriber) Close() {
	t.mu.Lock()
	if !t.closed {
		t.closed = true
		close(t.input)
	}
	t.mu.Unlock()
	select {
	case <-t.done:
	case <-time.After(30 * time.Second):
		t.cancel()
		select {
		case <-t.done:
		case <-time.After(2 * time.Second):
		}
	}
	t.cancel()
}

func (t *Transcriber) Snapshot() (string, []TranscriptSegment) {
	t.mu.Lock()
	defer t.mu.Unlock()
	segments := append([]TranscriptSegment(nil), t.segments...)
	sort.SliceStable(segments, func(i, j int) bool {
		return segments[i].StartMs < segments[j].StartMs
	})
	parts := make([]string, 0, len(segments))
	for _, segment := range segments {
		parts = append(parts, fmt.Sprintf("[%s-%s] %s：%s",
			formatMeetingOffset(segment.StartMs), formatMeetingOffset(segment.EndMs),
			segment.SpeakerName, segment.Text))
	}
	return strings.Join(parts, "\n"), segments
}

func formatMeetingOffset(value int64) string {
	if value < 0 {
		value = 0
	}
	totalSeconds := value / 1000
	return fmt.Sprintf("%02d:%02d", totalSeconds/60, totalSeconds%60)
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

func (t *Transcriber) process() {
	defer close(t.done)

	preRollLimit := meetingDurationConfig("meeting.pre_roll_ms", meetingPreRollMs)
	silenceLimit := meetingDurationConfig("meeting.silence_ms", meetingSilenceMs)
	maxUtterance := meetingDurationConfig("meeting.max_utterance_ms", meetingMaxUtteranceMs)
	previous := make(chan struct{})
	close(previous)
	var active *utteranceSession
	var preRoll []audioPacket
	var silenceMs int64
	utteranceIndex := 0

	for packet := range t.input {
		preRoll = append(preRoll, packet)
		for len(preRoll) > 0 && packet.endMs-preRoll[0].startMs > preRollLimit {
			preRoll = preRoll[1:]
		}

		voice := isMeetingSpeech(packet.pcm)
		if active == nil {
			if !voice {
				continue
			}
			active = t.startUtterance(utteranceIndex, preRoll[0].startMs, previous)
			utteranceIndex++
			for index, buffered := range preRoll {
				t.feedUtterance(active, buffered.pcm, voice && index == len(preRoll)-1)
			}
			preRoll = preRoll[:0]
			silenceMs = 0
			continue
		}

		t.feedUtterance(active, packet.pcm, voice)
		if voice {
			silenceMs = 0
		} else {
			silenceMs += packet.endMs - packet.startMs
		}
		if silenceMs < silenceLimit && packet.endMs-active.startMs < maxUtterance {
			continue
		}

		endMs := packet.endMs - silenceMs
		if endMs <= active.startMs {
			endMs = packet.endMs
		}
		t.finishUtterance(active, endMs)
		previous = active.completed
		active = nil
		silenceMs = 0
	}

	if active != nil {
		endMs := t.currentElapsedMs()
		if endMs <= active.startMs {
			endMs = active.startMs + 1
		}
		t.finishUtterance(active, endMs)
	}
	t.workers.Wait()
	t.cleanupTemporarySpeakers()
}

func (t *Transcriber) startUtterance(index int, startMs int64,
	previous <-chan struct{}) *utteranceSession {
	session := &utteranceSession{
		index: index, startMs: startMs, audio: make(chan []float32, utteranceAudioDepth),
		endReady: make(chan struct{}), previous: previous, completed: make(chan struct{}),
	}
	t.workers.Add(1)
	go t.runUtterance(session)
	return session
}

func (t *Transcriber) feedUtterance(session *utteranceSession, pcm []float32, speakerAudio bool) {
	if session == nil || len(pcm) == 0 {
		return
	}
	session.pcm = append(session.pcm, pcm...)
	if speakerAudio {
		session.speakerPCM = append(session.speakerPCM, pcm...)
	}
	session.latestEnd.Store(session.startMs + int64(len(session.pcm))*1000/16000)
	select {
	case session.audio <- pcm:
	case <-t.ctx.Done():
	default:
		session.dropped.Add(1)
	}
}

func (t *Transcriber) finishUtterance(session *utteranceSession, endMs int64) {
	if session == nil {
		return
	}
	session.endMs = endMs
	close(session.audio)
	close(session.endReady)
}

func (t *Transcriber) runUtterance(session *utteranceSession) {
	defer t.workers.Done()
	defer close(session.completed)

	select {
	case t.asrSlots <- struct{}{}:
		defer func() { <-t.asrSlots }()
	case <-t.ctx.Done():
		return
	}

	segmentCtx, segmentCancel := context.WithCancel(t.ctx)
	defer segmentCancel()
	recognitionDone := make(chan struct{})
	defer close(recognitionDone)
	go func() {
		select {
		case <-session.endReady:
		case <-recognitionDone:
			return
		case <-t.ctx.Done():
			return
		}
		select {
		case <-time.After(10 * time.Second):
			segmentCancel()
		case <-recognitionDone:
		case <-t.ctx.Done():
		}
	}()

	config := make(map[string]interface{}, len(t.asrConfig))
	for key, value := range t.asrConfig {
		config[key] = value
	}
	provider, err := asr.NewAsrProvider(t.config.Asr.Provider, config)
	if err != nil {
		log.Warnf("会议分句 ASR 创建失败: meeting=%s segment=%d error=%v",
			t.meetingID, session.index, err)
		for range session.audio {
		}
		return
	}
	defer provider.Close()
	results, err := provider.StreamingRecognize(segmentCtx, session.audio)
	if err != nil {
		log.Warnf("会议分句 ASR 启动失败: meeting=%s segment=%d error=%v",
			t.meetingID, session.index, err)
		for range session.audio {
		}
		return
	}

	var finalText string
	var lastPartial string
	for result := range results {
		if result.Error != nil {
			log.Warnf("会议分句 ASR 失败: meeting=%s segment=%d error=%v",
				t.meetingID, session.index, result.Error)
			continue
		}
		text := strings.TrimSpace(result.Text)
		if text == "" {
			continue
		}
		if result.IsFinal {
			finalText = text
			continue
		}
		lastPartial = text
		if t.send != nil {
			_ = t.send(text, "正在识别", false, session.startMs, session.latestEnd.Load())
		}
	}
	<-session.endReady
	if finalText == "" {
		finalText = lastPartial
	}
	if finalText == "" {
		return
	}

	<-session.previous
	speakerID, speakerName, confidence := t.identifySpeaker(session.speakerPCM)
	if speakerName == "" {
		speakerName = "未知发言人"
	}
	if t.send != nil {
		_ = t.send(finalText, speakerName, true, session.startMs, session.endMs)
	}
	t.mu.Lock()
	t.segments = append(t.segments, TranscriptSegment{
		SpeakerID: speakerID, SpeakerName: speakerName, Text: finalText,
		StartMs: session.startMs, EndMs: session.endMs, Confidence: confidence,
	})
	t.mu.Unlock()
	if dropped := session.dropped.Load(); dropped > 0 {
		log.Warnf("会议分句 ASR 丢弃音频块: meeting=%s segment=%d dropped=%d",
			t.meetingID, session.index, dropped)
	}
}

func (t *Transcriber) currentElapsedMs() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.startStamp == 0 || t.lastStamp < t.startStamp {
		return 0
	}
	return t.lastEndMs
}

func isMeetingSpeech(pcm []float32) bool {
	if len(pcm) == 0 {
		return false
	}
	var sum float64
	for _, sample := range pcm {
		value := float64(sample)
		sum += value * value
	}
	rms := math.Sqrt(sum / float64(len(pcm)))
	threshold := viper.GetFloat64("meeting.vad_rms_threshold")
	if threshold <= 0 {
		threshold = 0.008
	}
	return rms >= threshold
}

func meetingDurationConfig(key string, fallback int64) int64 {
	value := viper.GetInt64(key)
	if value <= 0 {
		return fallback
	}
	return value
}

func (t *Transcriber) identifySpeaker(pcm []float32) (string, string, float32) {
	if len(pcm) == 0 || !viper.GetBool("voice_identify.enable") {
		return "", "", 0
	}
	baseURL := voiceIdentifyBaseURL()
	if baseURL == "" {
		return "", "", 0
	}

	t.speakerMu.Lock()
	defer t.speakerMu.Unlock()

	knownThreshold := viper.GetFloat64("meeting.speaker_threshold")
	if knownThreshold <= 0 {
		knownThreshold = viper.GetFloat64("voice_identify.threshold")
	}
	if knownThreshold <= 0 {
		knownThreshold = 0.6
	}
	agentID := t.speakerAgentID()
	result := identifyMeetingVoice(t.ctx, baseURL, "", agentID, knownThreshold, pcm)
	if result != nil && result.Identified {
		if _, configured := t.config.VoiceIdentify[result.SpeakerName]; configured {
			return result.SpeakerID, result.SpeakerName, result.Confidence
		}
	}

	clusterThreshold := viper.GetFloat64("meeting.diarization_threshold")
	if clusterThreshold <= 0 {
		clusterThreshold = 0.55
	}
	if len(t.temporarySpeakers) > 0 {
		result = identifyMeetingVoice(t.ctx, baseURL, t.meetingID, agentID,
			clusterThreshold, pcm)
		if result != nil && result.Identified {
			for _, temporaryID := range t.temporarySpeakers {
				if result.SpeakerID == temporaryID {
					return result.SpeakerID, result.SpeakerName, result.Confidence
				}
			}
		}
	}

	index := len(t.temporarySpeakers)
	speakerID := speaker.TemporarySpeakerID(t.meetingID, index)
	speakerName := speaker.TemporarySpeakerName(index)
	registerCtx, registerCancel := context.WithTimeout(t.ctx, 8*time.Second)
	defer registerCancel()
	if err := speaker.RegisterTemporarySpeaker(registerCtx, baseURL, t.meetingID,
		agentID, speakerID, speakerName, speakerID+"-0", pcm, 16000); err != nil {
		log.Warnf("会议临时声纹注册失败: meeting=%s speaker=%s error=%v",
			t.meetingID, speakerName, err)
		return "", "", 0
	}
	t.temporarySpeakers = append(t.temporarySpeakers, speakerID)
	return speakerID, speakerName, 1
}

func identifyMeetingVoice(parent context.Context, baseURL, uid, agentID string,
	threshold float64, pcm []float32) *speaker.IdentifyResult {
	config := map[string]interface{}{
		"base_url":  baseURL,
		"threshold": threshold,
	}
	if uid != "" {
		config["uid"] = uid
	}
	provider, err := speaker.GetSpeakerProvider(config)
	if err != nil {
		return nil
	}
	defer provider.Close()
	ctx, cancel := context.WithTimeout(parent, 6*time.Second)
	defer cancel()
	if err := provider.StartStreaming(ctx, 16000, agentID); err != nil {
		return nil
	}
	for offset := 0; offset < len(pcm); offset += 3200 {
		end := offset + 3200
		if end > len(pcm) {
			end = len(pcm)
		}
		if err := provider.SendAudioChunk(ctx, pcm[offset:end]); err != nil {
			return nil
		}
	}
	result, err := provider.FinishAndIdentify(ctx)
	if err != nil {
		return nil
	}
	return result
}

func (t *Transcriber) cleanupTemporarySpeakers() {
	t.speakerMu.Lock()
	speakerIDs := append([]string(nil), t.temporarySpeakers...)
	t.speakerMu.Unlock()
	if len(speakerIDs) == 0 {
		return
	}
	baseURL := voiceIdentifyBaseURL()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	for _, speakerID := range speakerIDs {
		if err := speaker.DeleteTemporarySpeaker(ctx, baseURL, t.meetingID,
			t.speakerAgentID(), speakerID); err != nil {
			log.Warnf("清理会议临时声纹失败: meeting=%s speaker_id=%s error=%v",
				t.meetingID, speakerID, err)
		}
	}
}

func (t *Transcriber) speakerAgentID() string {
	if agentID := strings.TrimSpace(t.config.AgentId); agentID != "" {
		return agentID
	}
	return "meeting-" + t.deviceID
}

func voiceIdentifyBaseURL() string {
	if value := strings.TrimSpace(os.Getenv("VOICE_IDENTIFY_BASE_URL")); value != "" {
		return value
	}
	return strings.TrimSpace(viper.GetString("voice_identify.base_url"))
}

func stringConfig(config map[string]interface{}, key string) string {
	value, _ := config[key].(string)
	return value
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
