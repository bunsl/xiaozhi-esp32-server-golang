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
