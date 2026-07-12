// Package audio provides volume control for the audio pipeline.
// Volume is applied as PCM gain (multiplied into int16 samples) before
// the data reaches the Mumble sink, making it source-agnostic.
package audio

import (
	"math"
	"sync"
)

const (
	DefaultVolume = 50  // percentage
	MinVolume     = 0   // mute
	MaxVolume     = 200 // double gain
)

// VolumeController manages the global audio volume.
type VolumeController struct {
	mu   sync.RWMutex
	gain float64 // 0.0 = mute, 1.0 = 100%, 2.0 = 200%
}

// NewVolumeController creates a controller at the given percentage (0–200).
func NewVolumeController(percent float64) *VolumeController {
	gain := clamp(percent/100.0, 0, 2)
	return &VolumeController{gain: gain}
}

// SetVolume sets the volume to a percentage (0–200). Clamped to range.
func (vc *VolumeController) SetVolume(percent float64) float64 {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	vc.gain = clamp(percent/100.0, 0, 2)
	return percent
}

// GetVolume returns the current volume as a percentage (0–200).
func (vc *VolumeController) GetVolume() float64 {
	vc.mu.RLock()
	defer vc.mu.RUnlock()
	return vc.gain * 100
}

// GetGain returns the raw gain multiplier (0.0–2.0).
func (vc *VolumeController) GetGain() float64 {
	vc.mu.RLock()
	defer vc.mu.RUnlock()
	return vc.gain
}

// Mute sets volume to 0%.
func (vc *VolumeController) Mute() {
	vc.SetVolume(0)
}

// Unmute restores volume to the last non-zero level (100%).
func (vc *VolumeController) Unmute() {
	vc.SetVolume(100)
}

// IsMuted returns true if volume is at 0%.
func (vc *VolumeController) IsMuted() bool {
	vc.mu.RLock()
	defer vc.mu.RUnlock()
	return vc.gain == 0
}

// volumeSink wraps a Sink and applies volume gain to every audio frame.
type volumeSink struct {
	inner Sink
	ctrl  *VolumeController
}

func (v *volumeSink) OpenAudio() {
	v.inner.OpenAudio()
}

func (v *volumeSink) CloseAudio() {
	v.inner.CloseAudio()
}

func (v *volumeSink) SendAudio(samples []int16) bool {
	gain := v.ctrl.GetGain()
	if gain == 1.0 {
		// No scaling needed — fast path.
		return v.inner.SendAudio(samples)
	}

	scaled := make([]int16, len(samples))
	for i, s := range samples {
		val := float64(s) * gain
		if val > 32767 {
			val = 32767
		} else if val < -32768 {
			val = -32768
		}
		scaled[i] = int16(math.Round(val))
	}
	return v.inner.SendAudio(scaled)
}

// clamp limits v to [lo, hi].
func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
