**BUG FOUND: Double LoadURLs Call**

**Root Cause:**
The `LoadURLs()` function is called TWICE during startup:
1. Inside `store.New()` via `loadURLs()` (store.go:74)
2. Explicitly in `cmd/sawt/main.go:117`

**Exact Code Path:**
```
cmd/sawt/main.go:114: apiStore := store.New(cfg.MusicDir, "ffprobe", cfg.DataDir)
  → store.go:74: s.loadURLs()
    → store.go:424: s.LoadURLs(urlPath)  // FIRST CALL - appends to trackOrder
  → store.go:391: LoadURLs() returns

cmd/sawt/main.go:117: apiStore.LoadURLs(filepath.Join(cfg.DataDir, "urls.json"))
  → store.go:391: LoadURLs() AGAIN - appends to trackOrder AGAIN  // DUPLICATE!
```

**Result:**
- Before restart: 1 entry (correct)
- After restart: 2 entries (duplicate in `trackOrder`, though `s.tracks` map only has one entry)

**Secondary Issue:**
`AddTrack()` (store.go:152-157) also unconditionally appends to `trackOrder` without checking for duplicates. If `handleAddURL` is called twice for the same URL (creating different IDs), it would add both to `trackOrder`. But the primary bug is the double `LoadURLs` call.

**Fix:**
Remove line 117 from `cmd/sawt/main.go` since `store.New()` already handles loading URLs.

Now I'll use a planner agent to design the fix implementation.