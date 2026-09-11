package meeting

import (
	"encoding/binary"
	"math"
	"testing"
)

func TestDownmixS16LE(t *testing.T) {
	payload := make([]byte, 8)
	put := func(dst []byte, value int16) {
		binary.LittleEndian.PutUint16(dst, uint16(value))
	}
	put(payload[0:2], 12000)
	put(payload[2:4], 4000)
	put(payload[4:6], -8000)
	put(payload[6:8], -4000)

	got := downmixS16LE(payload, 2)
	want := []float32{8000.0 / 32768.0, -6000.0 / 32768.0}
	if len(got) != len(want) {
		t.Fatalf("samples = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if math.Abs(float64(got[i]-want[i])) > 0.00001 {
			t.Fatalf("sample[%d] = %f, want %f", i, got[i], want[i])
		}
	}
}

func TestDownmixRejectsUnalignedPayload(t *testing.T) {
	if got := downmixS16LE([]byte{1, 2, 3}, 2); got != nil {
		t.Fatalf("expected nil for unaligned stereo payload")
	}
}

func TestIsMeetingSpeech(t *testing.T) {
	quiet := make([]float32, 320)
	if isMeetingSpeech(quiet) {
		t.Fatal("silence must not be classified as speech")
	}
	voice := make([]float32, 320)
	for i := range voice {
		if i%2 == 0 {
			voice[i] = 0.05
		} else {
			voice[i] = -0.05
		}
	}
	if !isMeetingSpeech(voice) {
		t.Fatal("voiced frame must be classified as speech")
	}
}

func TestSnapshotIncludesTimelineAndSpeaker(t *testing.T) {
	transcriber := &Transcriber{
		segments: []TranscriptSegment{{
			SpeakerName: "发言人 A",
			Text:        "确认本周目标",
			StartMs:     1230,
			EndMs:       4560,
		}},
	}
	transcript, segments := transcriber.Snapshot()
	if len(segments) != 1 {
		t.Fatalf("segments = %d, want 1", len(segments))
	}
	if transcript != "[00:01-00:04] 发言人 A：确认本周目标" {
		t.Fatalf("unexpected transcript: %q", transcript)
	}
}
