// Copyright 2025
// SPDX-License-Identifier: Apache-2.0

package hfdownloader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Segmented downloading (aria2c-style).
//
// Hugging Face's CDN throttles per TCP connection, so a single large file is
// split into fixed-size byte-range segments fetched over many parallel
// HTTP/1.1 connections. A work queue keeps every connection busy until the
// last segment finishes (no idle tail from a static equal split), and a small
// sidecar state file records completed segments so an interrupted download
// resumes without re-fetching finished ranges.
//
// Layout on disk while a download is in flight:
//
//	<dst>.part       preallocated to the final size; segments are written
//	                 in place at their offsets
//	<dst>.parts.json completed-segment bitmap + partial progress
//
// On success the state file is removed and <dst>.part is renamed to <dst>.
const (
	minSegmentBytes    = 8 << 20  // 8 MiB
	maxSegmentBytes    = 64 << 20 // 64 MiB
	targetSegmentCount = 64
)

// testSegmentBytes overrides segment sizing in tests (0 = disabled).
var testSegmentBytes int64

// errRangeNotSupported signals that the server answered a Range request with
// 200 OK, so segmented downloading is impossible for this URL.
var errRangeNotSupported = errors.New("server does not support range requests")

// resolvedURL caches the post-redirect download URL for one file so every
// segment request goes straight to the CDN instead of paying a hub redirect
// round-trip per segment. Hugging Face signs the redirect target, so when the
// signature expires (403/410 mid-download) the cache is re-resolved once and
// the segment retried.
type resolvedURL struct {
	mu       sync.Mutex
	origin   string // the hub /resolve/ URL
	final    string // post-redirect URL ("" until resolved)
	size     int64  // Content-Length reported for the full file (0 if unknown)
	noRanges bool   // server answered 200 to a Range probe
}

// resolve returns the cached final URL, following redirects manually on first
// use. The Authorization header is only ever sent to the origin host — the
// signed CDN URL must not receive the token (and Go itself strips it on
// cross-host redirects, which is exactly why manual resolution is needed to
// learn the final URL).
func (r *resolvedURL) resolve(ctx context.Context, httpc *http.Client, token string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.final != "" {
		return r.final, nil
	}

	noRedirect := *httpc
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	origin, err := neturl.Parse(r.origin)
	if err != nil {
		return "", err
	}

	cur := r.origin
	for hop := 0; hop < 8; hop++ {
		// Probe with HEAD and WITHOUT a Range header: Hugging Face's Xet CAS
		// bridge signs the requested range into the redirect URL, so a URL
		// obtained with "Range: bytes=0-0" only ever serves that one range
		// ("Auth failed: invalid range"). A HEAD-derived URL serves any range.
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, cur, nil)
		if err != nil {
			return "", err
		}
		curURL := req.URL
		if curURL.Host == origin.Host {
			addAuth(req, token)
		} else {
			req.Header.Set("User-Agent", "hfdownloader/2")
		}

		resp, err := noRedirect.Do(req)
		if err != nil {
			return "", err
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()

		switch {
		case resp.StatusCode >= 300 && resp.StatusCode < 400:
			loc := resp.Header.Get("Location")
			if loc == "" {
				return "", fmt.Errorf("redirect without Location from %s", cur)
			}
			next, err := curURL.Parse(loc)
			if err != nil {
				return "", fmt.Errorf("bad redirect Location %q: %w", loc, err)
			}
			cur = next.String()
		case resp.StatusCode == http.StatusOK:
			if resp.ContentLength > 0 {
				r.size = resp.ContentLength
			}
			// Only trust an explicit "none"; a missing Accept-Ranges header is
			// inconclusive and the segment fetch path handles a 200 answer to
			// a ranged GET by falling back to a sequential download.
			r.noRanges = strings.EqualFold(strings.TrimSpace(resp.Header.Get("Accept-Ranges")), "none")
			r.final = cur
			return cur, nil
		default:
			return "", fmt.Errorf("resolve %s: unexpected status %s", cur, resp.Status)
		}
	}
	return "", fmt.Errorf("too many redirects resolving %s", r.origin)
}

// invalidate drops the cached URL (e.g. after a signed URL expires).
func (r *resolvedURL) invalidate() {
	r.mu.Lock()
	r.final = ""
	r.mu.Unlock()
}

// segmentSizeFor picks a segment size from the file size alone, so segment
// boundaries are stable across runs even when --connections changes. (The old
// engine derived part boundaries from the connection count, which silently
// corrupted resumed downloads when -c differed between runs.)
func segmentSizeFor(size int64) int64 {
	if testSegmentBytes > 0 {
		return testSegmentBytes
	}
	s := size / targetSegmentCount
	if s < minSegmentBytes {
		return minSegmentBytes
	}
	if s > maxSegmentBytes {
		return maxSegmentBytes
	}
	return s
}

// segmentState is the JSON sidecar that makes segmented downloads resumable.
type segmentState struct {
	Version     int           `json:"version"`
	Size        int64         `json:"size"`
	SegmentSize int64         `json:"segmentSize"`
	Completed   []int         `json:"completed"`
	Partial     map[int]int64 `json:"partial,omitempty"` // segment -> valid prefix bytes
}

func statePathFor(dst string) string { return dst + ".parts.json" }

func loadSegmentState(path string) *segmentState {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var st segmentState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil
	}
	return &st
}

func (st *segmentState) save(path string) error {
	sort.Ints(st.Completed)
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// segmentTracker guards resumable-download state shared by the workers.
type segmentTracker struct {
	mu        sync.Mutex
	state     *segmentState
	statePath string
	completed map[int]bool
}

func newSegmentTracker(st *segmentState, statePath string) *segmentTracker {
	done := make(map[int]bool, len(st.Completed))
	for _, idx := range st.Completed {
		done[idx] = true
	}
	return &segmentTracker{state: st, statePath: statePath, completed: done}
}

func (t *segmentTracker) isComplete(idx int) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.completed[idx]
}

func (t *segmentTracker) partialBytes(idx int) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state.Partial[idx]
}

// markComplete records idx as done and persists the state file so a crash
// after this point never re-downloads the segment.
func (t *segmentTracker) markComplete(idx int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.completed[idx] {
		return
	}
	t.completed[idx] = true
	t.state.Completed = append(t.state.Completed, idx)
	delete(t.state.Partial, idx)
	_ = t.state.save(t.statePath)
}

// setPartial records how many leading bytes of segment idx are valid on disk.
// Kept in memory; persisted by persist() on interruption.
func (t *segmentTracker) setPartial(idx int, n int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.completed[idx] {
		return
	}
	if t.state.Partial == nil {
		t.state.Partial = make(map[int]int64)
	}
	if n > 0 {
		t.state.Partial[idx] = n
	}
}

func (t *segmentTracker) persist() {
	t.mu.Lock()
	defer t.mu.Unlock()
	_ = t.state.save(t.statePath)
}

// countingWriter adds every written byte to a shared atomic counter so the
// progress ticker reports live byte counts without stat()ing files.
type countingWriter struct {
	w     io.Writer
	total *atomic.Int64
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	if n > 0 {
		cw.total.Add(int64(n))
	}
	return n, err
}

// cleanupLegacyParts removes .part-NN files left behind by the pre-3.3 static
// split engine; their boundaries depended on the old connection count and
// cannot be trusted.
func cleanupLegacyParts(dst string) {
	matches, err := filepath.Glob(dst + ".part-*")
	if err != nil {
		return
	}
	for _, m := range matches {
		if strings.HasSuffix(m, ".json") || strings.HasSuffix(m, ".json.tmp") {
			continue
		}
		_ = os.Remove(m)
	}
}

// downloadMultipart downloads a file as parallel byte-range segments.
// Falls back to downloadSingle when the size is unknown or the server turns
// out not to honor Range requests.
func downloadMultipart(ctx context.Context, httpc *http.Client, token string, job Job, cfg Settings, it PlanItem, dst string, emit func(ProgressEvent)) error {
	// Resolve the redirect chain once up front. This also probes range
	// support and learns the file size when the plan didn't know it.
	src := &resolvedURL{origin: it.URL}
	if _, err := src.resolve(ctx, httpc, token); err != nil {
		return err
	}
	if it.Size <= 0 && src.size > 0 {
		it.Size = src.size
	}
	if it.Size <= 0 || src.noRanges {
		return downloadSingle(ctx, httpc, token, job, cfg, it, dst, emit)
	}

	segSize := segmentSizeFor(it.Size)
	numSegs := int((it.Size + segSize - 1) / segSize)
	if numSegs <= 1 {
		return downloadSingle(ctx, httpc, token, job, cfg, it, dst, emit)
	}
	workers := cfg.Concurrency
	if workers <= 0 {
		workers = DefaultConnections
	}
	if workers > numSegs {
		workers = numSegs
	}

	cleanupLegacyParts(dst)

	partPath := dst + ".part"
	statePath := statePathFor(dst)

	f, err := os.OpenFile(partPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}

	st := loadSegmentState(statePath)
	staleState := false
	if st != nil && (st.Version != 1 || st.Size != it.Size || st.SegmentSize != segSize || fi.Size() != it.Size) {
		st = nil // stale state from a different file/version; start over
		staleState = true
	}
	if st == nil {
		if staleState {
			// The .part bytes belonged to the discarded state (e.g. the remote
			// file changed size between runs) and cannot be trusted.
			if err := f.Truncate(0); err != nil {
				return fmt.Errorf("reset stale part file %s: %w", partPath, err)
			}
			fi, err = f.Stat()
			if err != nil {
				return err
			}
		}
		st = &segmentState{Version: 1, Size: it.Size, SegmentSize: segSize}
		// A pre-existing .part smaller than the target is a valid prefix from
		// an interrupted single-connection download: convert it to completed
		// segments instead of throwing the bytes away.
		if have := fi.Size(); have > 0 && have < it.Size {
			whole := int(have / segSize)
			for i := 0; i < whole; i++ {
				st.Completed = append(st.Completed, i)
			}
			if rem := have - int64(whole)*segSize; rem > 0 {
				st.Partial = map[int]int64{whole: rem}
			}
		}
		if err := f.Truncate(it.Size); err != nil {
			return fmt.Errorf("preallocate %s: %w", partPath, err)
		}
		if err := st.save(statePath); err != nil {
			return fmt.Errorf("write segment state: %w", err)
		}
	}

	tracker := newSegmentTracker(st, statePath)

	// Progress counter starts at the resumed byte count.
	var written atomic.Int64
	var resumed int64
	for idx := range tracker.completed {
		resumed += segmentLength(idx, segSize, it.Size)
	}
	for idx, n := range st.Partial {
		if !tracker.completed[idx] {
			resumed += n
		}
	}
	written.Store(resumed)
	if resumed > 0 {
		emit(ProgressEvent{Event: "file_progress", Path: it.RelativePath, Downloaded: resumed, Total: it.Size})
	}

	// Work queue of pending segments.
	queue := make(chan int, numSegs)
	for idx := 0; idx < numSegs; idx++ {
		if !tracker.isComplete(idx) {
			queue <- idx
		}
	}
	close(queue)

	segCtx, cancelSegs := context.WithCancel(ctx)
	defer cancelSegs()

	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
	)
	fail := func(err error) {
		errOnce.Do(func() {
			firstErr = err
			cancelSegs()
		})
	}

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range queue {
				select {
				case <-segCtx.Done():
					return
				default:
				}
				if err := downloadSegment(segCtx, httpc, token, cfg, it, src, f, idx, segSize, tracker, &written, emit); err != nil {
					fail(err)
					return
				}
			}
		}()
	}

	// Progress ticker: report the live byte counter while segments download.
	tickerDone := make(chan struct{})
	var tickerWG sync.WaitGroup
	tickerWG.Add(1)
	go func() {
		defer tickerWG.Done()
		t := time.NewTicker(200 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-segCtx.Done():
				return
			case <-tickerDone:
				return
			case <-t.C:
				emit(ProgressEvent{Event: "file_progress", Path: it.RelativePath, Downloaded: written.Load(), Total: it.Size})
			}
		}
	}()

	wg.Wait()
	close(tickerDone)
	tickerWG.Wait()

	// Persist partial progress before reporting any failure so the next run
	// resumes from where we stopped.
	if ctx.Err() != nil {
		tracker.persist()
		return ctx.Err()
	}
	if firstErr != nil {
		if errors.Is(firstErr, errRangeNotSupported) {
			// The server ignored Range. Throw away the sparse segment file —
			// its byte layout cannot be trusted — and stream sequentially.
			f.Close()
			_ = os.Remove(partPath)
			_ = os.Remove(statePath)
			return downloadSingle(ctx, httpc, token, job, cfg, it, dst, emit)
		}
		tracker.persist()
		return firstErr
	}

	// All segments accounted for.
	emit(ProgressEvent{Event: "file_progress", Path: it.RelativePath, Downloaded: it.Size, Total: it.Size})
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(partPath, dst); err != nil {
		return err
	}
	_ = os.Remove(statePath)
	return nil
}

// segmentLength returns the byte length of segment idx.
func segmentLength(idx int, segSize, total int64) int64 {
	start := int64(idx) * segSize
	end := start + segSize
	if end > total {
		end = total
	}
	return end - start
}

// downloadSegment fetches one byte-range segment into f at its offset,
// resuming from any previously recorded partial progress and retrying with
// backoff on transient errors.
func downloadSegment(ctx context.Context, httpc *http.Client, token string, cfg Settings, it PlanItem, src *resolvedURL, f *os.File, idx int, segSize int64, tracker *segmentTracker, written *atomic.Int64, emit func(ProgressEvent)) error {
	start := int64(idx) * segSize
	end := start + segmentLength(idx, segSize, it.Size) - 1
	pos := tracker.partialBytes(idx)
	if start+pos > end {
		// Partial already covers the segment (boundary rounding); finish it.
		tracker.markComplete(idx)
		return nil
	}

	retry := newRetry(cfg)
	var lastErr error

	for attempt := 0; attempt <= cfg.Retries; attempt++ {
		if err := ctx.Err(); err != nil {
			tracker.setPartial(idx, pos)
			return err
		}

		srcURL, err := src.resolve(ctx, httpc, token)
		if err != nil {
			tracker.setPartial(idx, pos)
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srcURL, nil)
		if err != nil {
			return err
		}
		if srcURL == it.URL {
			addAuth(req, token)
		} else {
			req.Header.Set("User-Agent", "hfdownloader/2")
		}
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start+pos, end))

		resp, err := httpc.Do(req)
		switch {
		case err != nil:
			lastErr = err
		case resp.StatusCode == http.StatusOK:
			// Server ignored the Range header entirely.
			resp.Body.Close()
			tracker.setPartial(idx, pos)
			return errRangeNotSupported
		case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusUnauthorized:
			// Signed CDN URL likely expired; re-resolve and retry.
			resp.Body.Close()
			src.invalidate()
			lastErr = fmt.Errorf("segment %d: status %s (re-resolving URL)", idx, resp.Status)
		case resp.StatusCode != http.StatusPartialContent:
			lastErr = fmt.Errorf("segment %d: unexpected status %s", idx, resp.Status)
			resp.Body.Close()
		default:
			want := end - (start + pos) + 1
			ow := io.NewOffsetWriter(f, start+pos)
			n, cerr := io.Copy(&countingWriter{w: ow, total: written}, io.LimitReader(resp.Body, want))
			resp.Body.Close()
			pos += n
			if cerr == nil && n == want {
				tracker.markComplete(idx)
				return nil
			}
			if cerr == nil {
				cerr = io.ErrUnexpectedEOF // short body
			}
			lastErr = cerr
		}

		if attempt < cfg.Retries {
			emit(ProgressEvent{Event: "retry", Path: it.RelativePath, Attempt: attempt + 1, Message: lastErr.Error()})
			if !sleepCtx(ctx, retry.Next()) {
				tracker.setPartial(idx, pos)
				return ctx.Err()
			}
		}
	}
	tracker.setPartial(idx, pos)
	return fmt.Errorf("segment %d (%d-%d): %w", idx, start, end, lastErr)
}
