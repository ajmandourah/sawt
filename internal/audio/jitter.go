// Package audio provides the jitter buffer for smoothing audio playback.
package audio

import (
	"container/heap"
	"log"
	"sync"
	"time"
)

// JitterPacket represents a buffered audio packet.
type JitterPacket struct {
	Sequence int64
	Samples  []int16
	IsLast   bool
}

// JitterHeap is a min-heap of JitterPackets sorted by sequence number.
type JitterHeap []*JitterPacket

func (h JitterHeap) Len() int { return len(h) }
func (h JitterHeap) Less(i, j int) bool {
	return h[i].Sequence < h[j].Sequence
}
func (h JitterHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}
func (h *JitterHeap) Push(x interface{}) {
	*h = append(*h, x.(*JitterPacket))
}
func (h *JitterHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

// JitterBuffer smooths audio playback by buffering packets and handling loss.
type JitterBuffer struct {
	mu      sync.Mutex
	heap    JitterHeap
	seq     int64       // current expected sequence
	delay   time.Duration
	bufSize int         // max packets to buffer
	running bool
	silence []int16   // silence samples for loss concealment
	sink    Sink      // underlying audio sink
	samplesPerFrame int  // samples per frame for timing
}

// NewJitterBuffer creates a new jitter buffer.
func NewJitterBuffer(sink Sink, delayMs, bufFrames, samplesPerFrame int) *JitterBuffer {
	jb := &JitterBuffer{
		heap:    make(JitterHeap, 0),
		seq:     -1,
		delay:   time.Duration(delayMs) * time.Millisecond,
		bufSize: bufFrames,
		silence: make([]int16, samplesPerFrame),
		sink:    sink,
		samplesPerFrame: samplesPerFrame,
	}
	heap.Init(&jb.heap)
	return jb
}

// AddPacket adds a packet to the jitter buffer.
func (jb *JitterBuffer) AddPacket(seq int64, samples []int16, isLast bool) {
	jb.mu.Lock()
	defer jb.mu.Unlock()

	pkt := &JitterPacket{
		Sequence: seq,
		Samples:  samples,
		IsLast:   isLast,
	}

	// Add to heap
	heap.Push(&jb.heap, pkt)

	// Start processing if not running
	if !jb.running {
		jb.running = true
		if jb.seq == -1 || len(jb.heap) == 1 {
			jb.seq = pkt.Sequence
		}
		go jb.process()
	}
}

// process runs the jitter buffer processing loop.
func (jb *JitterBuffer) process() {
	// Initial delay to fill buffer
	time.Sleep(jb.delay)

	for {
		jb.mu.Lock()
		if len(jb.heap) == 0 {
			jb.mu.Unlock()
			jb.running = false
			return
		}

		pkt := jb.heap[0]

		// Skip old packets (delayed beyond buffer)
		if pkt.Sequence < jb.seq {
			heap.Pop(&jb.heap)
			jb.mu.Unlock()
			continue
		}

		// Handle missing packets (loss concealment)
		if pkt.Sequence > jb.seq {
			// Gap detected - send silence
			log.Printf("JitterBuffer: gap detected, expected %d, got %d", jb.seq, pkt.Sequence)
			for jb.seq < pkt.Sequence {
				jb.sink.SendAudio(jb.silence)
				jb.seq++
			}
			jb.mu.Unlock()
			time.Sleep(time.Duration(jb.samplesPerFrame/(SampleRate/1000)) * time.Millisecond)
			continue
		}

		// Process current packet
		heap.Pop(&jb.heap)
		jb.seq++
		jb.mu.Unlock()

		// Send packet to sink
		jb.sink.SendAudio(pkt.Samples)

		// Timing: sleep based on sample count
		if pkt.IsLast {
			jb.running = false
			return
		}
		time.Sleep(time.Duration(jb.samplesPerFrame/(SampleRate/1000)) * time.Millisecond)
	}
}

// Flush sends any remaining packets in the buffer.
func (jb *JitterBuffer) Flush() {
	jb.mu.Lock()
	defer jb.mu.Unlock()

	for jb.heap.Len() > 0 {
		pkt := heap.Pop(&jb.heap).(*JitterPacket)
		jb.sink.SendAudio(pkt.Samples)
	}
}

// JitterSink wraps JitterBuffer to implement the Sink interface.
type JitterSink struct {
	jb *JitterBuffer
	seq int64
}

// NewJitterSink creates a new JitterSink.
func NewJitterSink(jb *JitterBuffer) *JitterSink {
	return &JitterSink{jb: jb}
}

// OpenAudio opens the audio channel (no-op for jitter buffer).
func (js *JitterSink) OpenAudio() {
	js.seq = 0
}

// CloseAudio closes the audio channel (flushes buffer).
func (js *JitterSink) CloseAudio() {
	js.jb.Flush()
}

// SendAudio adds a packet to the jitter buffer.
func (js *JitterSink) SendAudio(samples []int16) bool {
	js.jb.AddPacket(js.seq, samples, false)
	js.seq++
	return true
}
