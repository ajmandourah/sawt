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
	mu              sync.Mutex
	cond            *sync.Cond // signaled when new packets arrive
	heap            JitterHeap
	seq             int64 // current expected sequence
	delay           time.Duration
	running         bool
	silence         []int16 // silence samples for loss concealment
	sink            Sink    // underlying audio sink
	samplesPerFrame int     // samples per frame for timing
}

// NewJitterBuffer creates a new jitter buffer.
func NewJitterBuffer(sink Sink, delayMs, samplesPerFrame int) *JitterBuffer {
	jb := &JitterBuffer{
		heap:            make(JitterHeap, 0),
		seq:             -1,
		delay:           time.Duration(delayMs) * time.Millisecond,
		silence:         make([]int16, samplesPerFrame),
		sink:            sink,
		samplesPerFrame: samplesPerFrame,
	}
	jb.cond = sync.NewCond(&jb.mu)
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
	log.Printf("JitterBuffer: added packet %d, heap size=%d", seq, jb.heap.Len())

	// Start processing if not running
	if !jb.running {
		jb.running = true
		if jb.seq == -1 {
			jb.seq = pkt.Sequence
		}
		log.Printf("JitterBuffer: starting process goroutine")
		go jb.process()
	}

	// Signal waiting goroutine
	jb.cond.Signal()
}

// process runs the jitter buffer processing loop.
func (jb *JitterBuffer) process() {
	log.Printf("JitterBuffer: process starting, delay=%v", jb.delay)

	// Initial delay to fill buffer
	time.Sleep(jb.delay)
	log.Printf("JitterBuffer: delay complete, heap size=%d", jb.heap.Len())

	for {
		jb.mu.Lock()

		// Wait for packets or shutdown
		for len(jb.heap) == 0 {
			if !jb.running {
				jb.mu.Unlock()
				log.Printf("JitterBuffer: process stopping (not running)")
				return
			}
			log.Printf("JitterBuffer: waiting for packets...")
			jb.cond.Wait()
			log.Printf("JitterBuffer: woke up, heap size=%d", jb.heap.Len())
		}

		pkt := jb.heap[0]

		// Skip old packets (delayed beyond buffer)
		if pkt.Sequence < jb.seq {
			log.Printf("JitterBuffer: skipping old packet %d (expected %d)", pkt.Sequence, jb.seq)
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
			continue
		}

		// Process current packet
		heap.Pop(&jb.heap)
		jb.seq++
		jb.mu.Unlock()

		// Send packet to sink (no timing - engine handles timing)
		log.Printf("JitterBuffer: sending packet %d to sink", pkt.Sequence)
		jb.sink.SendAudio(pkt.Samples)

		// Check if last packet
		if pkt.IsLast {
			log.Printf("JitterBuffer: last packet received, stopping")
			jb.mu.Lock()
			jb.running = false
			jb.mu.Unlock()
			return
		}
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
	jb   *JitterBuffer
	sink Sink
	seq  int64
}

// NewJitterSink creates a new JitterSink.
func NewJitterSink(jb *JitterBuffer, sink Sink) *JitterSink {
	return &JitterSink{jb: jb, sink: sink}
}

// OpenAudio opens the audio channel (delegates to underlying sink).
func (js *JitterSink) OpenAudio() {
	js.sink.OpenAudio()
}

// CloseAudio closes the audio channel (flushes buffer).
func (js *JitterSink) CloseAudio() {
	js.jb.Flush()
	js.sink.CloseAudio()
}

// SendAudio adds a packet to the jitter buffer.
func (js *JitterSink) SendAudio(samples []int16) bool {
	log.Printf("JitterSink: SendAudio called, samples=%d", len(samples))
	js.jb.AddPacket(js.seq, samples, false)
	js.seq++
	return true
}
