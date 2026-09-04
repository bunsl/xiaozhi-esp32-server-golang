package meeting

import (
	"encoding/binary"
	"fmt"
)

const (
	// PCMFormat is signed 16-bit little-endian interleaved PCM.
	PCMFormat = "pcm_s16le"

	frameMagic       = "EAMF"
	frameVersion     = 1
	frameEncodingPCM = 0
	FrameHeaderSize  = 24
)

// Frame is one timestamped meeting audio packet. Payload is interleaved PCM.
type Frame struct {
	Sequence    uint32
	TimestampMs uint64
	Payload     []byte
}

// EncodeFrame serializes a meeting frame for one WebSocket binary message.
func EncodeFrame(sequence uint32, timestampMs uint64, payload []byte) []byte {
	data := make([]byte, FrameHeaderSize+len(payload))
	copy(data[0:4], frameMagic)
	data[4] = frameVersion
	data[5] = frameEncodingPCM
	binary.LittleEndian.PutUint32(data[6:10], sequence)
	binary.LittleEndian.PutUint64(data[10:18], timestampMs)
	binary.LittleEndian.PutUint32(data[18:22], uint32(len(payload)))
	copy(data[FrameHeaderSize:], payload)
	return data
}

// DecodeFrame validates and parses a meeting binary frame.
func DecodeFrame(data []byte, maxPayload int) (Frame, error) {
	if len(data) < FrameHeaderSize {
		return Frame{}, fmt.Errorf("meeting frame too short: %d", len(data))
	}
	if string(data[0:4]) != frameMagic {
		return Frame{}, fmt.Errorf("invalid meeting frame magic")
	}
	if data[4] != frameVersion {
		return Frame{}, fmt.Errorf("unsupported meeting frame version: %d", data[4])
	}
	if data[5] != frameEncodingPCM {
		return Frame{}, fmt.Errorf("unsupported meeting frame encoding: %d", data[5])
	}
	payloadLen := binary.LittleEndian.Uint32(data[18:22])
	if payloadLen > uint32(len(data)-FrameHeaderSize) || int(payloadLen) != len(data)-FrameHeaderSize {
		return Frame{}, fmt.Errorf("meeting frame payload length mismatch: header=%d actual=%d", payloadLen, len(data)-FrameHeaderSize)
	}
	if maxPayload > 0 && payloadLen > uint32(maxPayload) {
		return Frame{}, fmt.Errorf("meeting frame payload too large: %d", payloadLen)
	}
	if payloadLen == 0 || payloadLen%2 != 0 {
		return Frame{}, fmt.Errorf("meeting PCM payload must be non-empty and 16-bit aligned: %d", payloadLen)
	}

	return Frame{
		Sequence:    binary.LittleEndian.Uint32(data[6:10]),
		TimestampMs: binary.LittleEndian.Uint64(data[10:18]),
		Payload:     data[FrameHeaderSize:],
	}, nil
}
