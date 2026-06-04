// Package pipeline - streaming support structures for audio and text chunk parsing.
package pipeline

import (
	"io"
	"math"
	"sync"
	"time"
)

// AudioStreamReader wraps an audio stream and provides real-time chunking and VAD.
type AudioStreamReader struct {
	mu           sync.Mutex
	reader       io.Reader
	sampleRate   int
	bitDepth     int
	channels     int
	silenceDur   time.Duration
	energyThresh float64
}

// NewAudioStreamReader creates a new reader for raw PCM audio.
func NewAudioStreamReader(r io.Reader, sampleRate, bitDepth, channels int, silenceDur time.Duration) *AudioStreamReader {
	return &AudioStreamReader{
		reader:       r,
		sampleRate:   sampleRate,
		bitDepth:     bitDepth,
		channels:     channels,
		silenceDur:   silenceDur,
		energyThresh: 0.05, // default energy threshold for speech detection
	}
}

// ReadChunks reads from the stream and streams raw PCM chunks or notifies on silence segments (VAD).
func (asr *AudioStreamReader) ReadChunks(callback func([]byte, bool)) error {
	asr.mu.Lock()
	r := asr.reader
	asr.mu.Unlock()

	// Read 100ms frames
	frameSize := asr.sampleRate * (asr.bitDepth / 8) * asr.channels / 10 // 100ms frame
	buf := make([]byte, frameSize)

	var silenceAccum time.Duration

	for {
		n, err := io.ReadFull(r, buf)
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			return err
		}

		energy := calculateRMSRawPCM(buf[:n], asr.bitDepth)
		isSilence := energy < asr.energyThresh

		if isSilence {
			silenceAccum += 100 * time.Millisecond
		} else {
			silenceAccum = 0
		}

		isSegmentComplete := silenceAccum >= asr.silenceDur
		if isSegmentComplete {
			silenceAccum = 0
		}

		callback(buf[:n], isSegmentComplete)
	}

	return nil
}

// SetEnergyThreshold configures the sensitivity for Voice Activity Detection.
func (asr *AudioStreamReader) SetEnergyThreshold(thresh float64) {
	asr.mu.Lock()
	defer asr.mu.Unlock()
	asr.energyThresh = thresh
}

// calculateRMSRawPCM calculates the Root Mean Square (RMS) energy of PCM frames to detect silence.
func calculateRMSRawPCM(data []byte, bitDepth int) float64 {
	if len(data) == 0 {
		return 0
	}

	var sum float64
	var count int

	if bitDepth == 16 {
		for i := 0; i < len(data)-1; i += 2 {
			sample := int16(data[i]) | (int16(data[i+1]) << 8)
			val := float64(sample) / 32768.0
			sum += val * val
			count++
		}
	} else {
		// 8-bit PCM (unsigned by default, bias 128)
		for _, b := range data {
			val := (float64(b) - 128.0) / 128.0
			sum += val * val
			count++
		}
	}

	if count == 0 {
		return 0
	}
	return math.Sqrt(sum / float64(count))
}
