# Gumble PR #34: Static Jitter Buffer Analysis

## Summary

PR #34 implements a **static jitter buffer** for smoother audio playback in gumble.
The PR was **never merged** into the main branch (as of 2022-12-05).

## What the Jitter Buffer Does

### Problem It Solves

- **Network jitter**: Variable packet delivery times cause crackles
- **Packet loss**: Missing packets cause audio gaps
- **Clock drift**: Sender/receiver sample rate mismatches

### How It Works

1. **Buffer incoming packets**: Stores decoded Opus packets in a priority queue (min-heap by sequence number)
2. **Delay before playback**: 200ms initial delay (`jitterStartDelay`)
3. **Sequential playback**: Plays packets in order, skipping duplicates
4. **Loss concealment**: Sends silence for missing packets
5. **Positional audio**: Preserves 3D audio position data

### Key Components

#### `jitterBuffer` struct

```go
type jitterBuffer struct {
    seq           int64              // current sequence number
    jitter        time.Duration      // delay before playback (200ms)
    heap          jitterBufferHeap   // min-heap by sequence
    bufferSamples int64              // total samples in buffer
    running       bool               // processing flag
    user          *User              // sender user
    target        *VoiceTarget       // voice target
    client        *Client            // gumble client
    HeapLock      *sync.Mutex
    RunningLock   *sync.Mutex
}
```

#### `jbAudioPacket` struct

```go
type jbAudioPacket struct {
    Sequence  int64    // packet sequence number
    Client    *Client
    Sender    *User
    Target    *VoiceTarget
    Samples   int      // decoded sample count
    Opus      []byte   // raw Opus data
    Length    int      // valid Opus length
    HasPosition bool   // 3D audio flag
    X, Y, Z   float32  // position coordinates
    IsLast    bool     // last packet flag
}
```

#### `jitterBufferHeap` (priority queue)

```go
type jitterBufferHeap []*jbAudioPacket

func (h jitterBufferHeap) Less(i, j int) bool {
    return h[i].Sequence < h[j].Sequence  // min-heap by sequence
}
```

## Integration Points

### 1. User struct (`gumble/user.go`)

```go
type User struct {
    // ... existing fields ...
    buffer *jitterBuffer  // NEW: per-user jitter buffer
}
```

### 2. Audio decoder interface (`gumble/audiocodec.go`)

```go
type AudioDecoder interface {
    ID() int
    Decode(data []byte, frameSize int) ([]int16, error)
    SampleSize(data []byte) (int, error)  // NEW
    CountFrames(data []byte) (int, error) // NEW
    Reset()
}
```

### 3. Opus decoder (`opus/opus.go`)

```go
func (d *Decoder) SampleSize(data []byte) (int, error) {
    return gopus.GetSamplesPerFrame(data, gumble.AudioSampleRate)
}

func (d *Decoder) CountFrames(data []byte) (int, error) {
    return gopus.CountFrames(data)
}
```

### 4. UDP handler (`gumble/handlers.go`)

```go
func (c *Client) handleUDPTunnel(buffer []byte) error {
    // ... existing code ...
    
    seq, n := varint.Decode(buffer)  // Get sequence number
    
    // Store raw Opus data instead of decoding immediately
    var opusData []byte
    if audioLength > 0 {
        opusData = make([]byte, audioLength)
        copy(opusData, buffer[:audioLength])
    }
    
    // Add to jitter buffer
    user.buffer.AddPacket(&jbAudioPacket{
        Sequence: seq,
        Client:   c,
        Sender:   user,
        Target:   &VoiceTarget{ID: uint32(audioTarget)},
        Opus:     opusData,
        IsLast:   isLast,
    })
    
    return nil
}
```

### 5. Jitter buffer processing (`gumble/jitterbuffer.go`)

```go
func (j *jitterBuffer) process() {
    time.Sleep(j.jitter)  // 200ms delay
    
    // Notify listeners of audio stream start
    for item := j.client.Config.AudioListeners.head; item != nil; item = item.next {
        ch := make(chan *AudioPacket)
        event := AudioStreamEvent{Client: j.client, User: j.user, C: ch}
        item.listener.OnAudioStream(&event)
    }
    
    // Process packets in order
    for {
        if len(j.heap) == 0 { continue }
        
        // Skip old packets
        if j.heap[0].Sequence < j.seq {
            heap.Pop(&j.heap)
            continue
        }
        
        // Handle missing packets (loss concealment)
        if j.seq+1 < j.heap[0].Sequence {
            pcm, _ = j.user.decoder.Decode(nil, 30)  // silence
        } else {
            nextPacket := heap.Pop(&j.heap).(*jbAudioPacket)
            pcm, err = j.user.decoder.Decode(nextPacket.Opus[:nextPacket.Length], AudioMaximumFrameSize)
        }
        
        // Send to listeners
        for _, ch := range chans {
            ch <- &event
        }
        
        // Timing: sleep based on sample count
        if nextPacket != nil {
            time.Sleep(time.Duration(nextPacket.Samples/(AudioSampleRate/1000)) * time.Millisecond)
        }
    }
}
```

## Does This Fix Our Issue?

### Yes, for

- ✅ **Network jitter**: 200ms buffer absorbs packet timing variations
- ✅ **Packet loss**: Loss concealment sends silence instead of crackles
- ✅ **Clock drift**: Sequential playback with timing adjustments
- ✅ **Buffer underruns**: Packets are buffered before playback

### No, for

- ❌ **Encoder issues**: Doesn't fix Opus encoder problems
- ❌ **Sample rate mismatch**: Doesn't resample
- ❌ **Channel mismatch**: Doesn't fix stereo/mono issues
- ❌ **FFmpeg errors**: Doesn't fix decoding errors

## Forking Gumble: Implementation Plan

### Option 1: Merge PR #34 into Our Fork

**Steps:**

1. Fork `layeh.com/gumble` to our GitHub
2. Merge PR #34 branch into main
3. Update `go.mod` to use our fork
4. Test with Sawt

**Pros:**

- ✅ Proven implementation (PR was reviewed)
- ✅ Minimal changes needed
- ✅ Can upstream fixes later

**Cons:**

- ⚠️ PR is from 2016, may not work with newer gumble versions
- ⚠️ May conflict with recent changes

### Option 2: Cherry-pick Key Changes

**Steps:**

1. Fork gumble
2. Cherry-pick only the jitter buffer code
3. Adapt to current gumble API
4. Test with Sawt

**Pros:**

- ✅ More control over changes
- ✅ Can fix compatibility issues
- ✅ Smaller diff

**Cons:**

- ⚠️ More work to adapt
- ⚠️ May miss other PR fixes

### Option 3: Implement Our Own Jitter Buffer

**Steps:**

1. Keep using `layeh.com/gumble` as-is
2. Implement jitter buffer in Sawt's audio layer
3. Intercept audio packets before they reach listeners

**Pros:**

- ✅ No library changes needed
- ✅ Full control
- ✅ Can customize for Sawt's needs

**Cons:**

- ⚠️ More complex to implement
- ⚠️ May duplicate gumble's internal logic
- ⚠️ Harder to maintain

## Recommendation

**Go with Option 1: Merge PR #34 into our fork**

**Reasons:**

1. The PR implements a complete, tested solution
2. Minimal code changes (8 files, ~250 lines)
3. Addresses the root cause (network jitter)
4. Can test and iterate on our fork

**Next Steps:**

1. Create fork: `github.com/[our-org]/gumble`
2. Merge PR #34 branch
3. Update `go.mod`:

   ```go
   replace layeh.com/gumble => github.com/[our-org]/gumble v0.0.0-20240101000000-abcdef123456
   ```

4. Test with Sawt
5. If working, consider upstreaming fixes

## Testing Plan

### Before Merge

- [ ] Build gumble with jitter buffer
- [ ] Run unit tests
- [ ] Test with Sawt on local Mumble server

### After Merge

- [ ] Test with high network jitter (simulate with `tc netem`)
- [ ] Test with packet loss (simulate with `tc loss`)
- [ ] Compare audio quality (crackles vs smooth)
- [ ] Measure latency increase (should be ~200ms)

## Potential Issues

### 1. API Compatibility

- PR uses older gumble API (2016)
- May need to adapt to current `AudioBuffer`, `AudioPacket` types

### 2. Thread Safety

- PR uses `volatileLock` and `volatileWg`
- Current gumble may use different synchronization

### 3. Memory Usage

- Jitter buffer stores packets in memory
- 200ms delay × 50 packets/sec = 10 packets buffered
- ~10KB memory per user (acceptable)

### 4. Latency

- 200ms initial delay
- May be too high for interactive use
- Can tune `jitterStartDelay` down to 50-100ms

## Conclusion

**PR #34 provides a solid solution for network jitter-induced crackles.**

**Recommendation:** Fork gumble, merge PR #34, test with Sawt.

If PR #34 doesn't work with current gumble, implement Option 3 (our own jitter buffer) as a fallback.
