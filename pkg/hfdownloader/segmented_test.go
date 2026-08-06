// Copyright 2025
// SPDX-License-Identifier: Apache-2.0

package hfdownloader

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// setTestSegmentSize overrides the segment size for the duration of a test.
func setTestSegmentSize(t *testing.T, n int64) {
	t.Helper()
	old := testSegmentBytes
	testSegmentBytes = n
	t.Cleanup(func() { testSegmentBytes = old })
}

// patternBytes builds a deterministic payload where each byte is its index mod 251.
func patternBytes(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

// rangeServer serves Range requests over full and records the Range headers seen.
func rangeServer(t *testing.T, full []byte) (*httptest.Server, func() []string) {
	t.Helper()
	var (
		mu   sync.Mutex
		hdrs []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", strconv.Itoa(len(full)))
			w.Header().Set("Accept-Ranges", "bytes")
			return
		}
		mu.Lock()
		hdrs = append(hdrs, r.Header.Get("Range"))
		mu.Unlock()
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(full))
	}))
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), hdrs...)
	}
}

func TestSegmentSizeFor(t *testing.T) {
	tests := []struct {
		size int64
		want int64
	}{
		{100 << 20, minSegmentBytes},                              // small file clamps to min
		{targetSegmentCount * (16 << 20), 16 << 20},               // mid-size lands between clamps
		{1 << 40, maxSegmentBytes},                                // huge file clamps to max
		{targetSegmentCount*minSegmentBytes + 1, minSegmentBytes}, // boundary
	}
	for _, tt := range tests {
		if got := segmentSizeFor(tt.size); got != tt.want {
			t.Errorf("segmentSizeFor(%d) = %d, want %d", tt.size, got, tt.want)
		}
	}
}

// TestDownloadSegmented_CompletesAndCleansUp verifies a plain segmented
// download: correct bytes, no leftover .part/.parts.json files.
func TestDownloadSegmented_CompletesAndCleansUp(t *testing.T) {
	tmpDir := t.TempDir()
	full := patternBytes(10000)
	setTestSegmentSize(t, 2500)

	dst := filepath.Join(tmpDir, "blobs", "tmp-seg")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}

	srv, ranges := rangeServer(t, full)
	defer srv.Close()

	it := PlanItem{RelativePath: "seg.bin", URL: srv.URL + "/seg.bin", Size: int64(len(full)), AcceptRanges: true}
	err := downloadMultipart(context.Background(), srv.Client(), "", Job{Repo: "o/r"}, Settings{Concurrency: 4, Retries: 0}, it, dst, func(ProgressEvent) {})
	if err != nil {
		t.Fatalf("downloadMultipart: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, full) {
		t.Errorf("content mismatch: got %d bytes, want %d", len(got), len(full))
	}
	if _, err := os.Stat(dst + ".part"); !os.IsNotExist(err) {
		t.Error(".part not cleaned up")
	}
	if _, err := os.Stat(statePathFor(dst)); !os.IsNotExist(err) {
		t.Error(".parts.json not cleaned up")
	}
	if got := len(ranges()); got != 4 {
		t.Errorf("expected 4 range requests, got %d", got)
	}
}

// TestDownloadSegmented_ResumeSkipsCompletedSegments verifies that segments
// recorded as complete in the state file are not re-requested and that the
// final bytes are correct.
func TestDownloadSegmented_ResumeSkipsCompletedSegments(t *testing.T) {
	tmpDir := t.TempDir()
	const segSize = 2500
	full := patternBytes(10000)
	setTestSegmentSize(t, segSize)

	dst := filepath.Join(tmpDir, "blobs", "tmp-resume")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}

	// Simulate an interrupted run: segments 0 and 2 already written, segment 1
	// half done, segment 3 untouched.
	part := make([]byte, len(full))
	copy(part[0:segSize], full[0:segSize])
	copy(part[segSize:segSize+segSize/2], full[segSize:segSize+segSize/2])
	copy(part[2*segSize:3*segSize], full[2*segSize:3*segSize])
	if err := os.WriteFile(dst+".part", part, 0o644); err != nil {
		t.Fatal(err)
	}
	st := &segmentState{
		Version: 1, Size: int64(len(full)), SegmentSize: segSize,
		Completed: []int{0, 2},
		Partial:   map[int]int64{1: segSize / 2},
	}
	if err := st.save(statePathFor(dst)); err != nil {
		t.Fatal(err)
	}

	srv, ranges := rangeServer(t, full)
	defer srv.Close()

	it := PlanItem{RelativePath: "resume.bin", URL: srv.URL + "/resume.bin", Size: int64(len(full)), AcceptRanges: true}
	err := downloadMultipart(context.Background(), srv.Client(), "", Job{Repo: "o/r"}, Settings{Concurrency: 4, Retries: 0}, it, dst, func(ProgressEvent) {})
	if err != nil {
		t.Fatalf("downloadMultipart: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, full) {
		t.Error("content mismatch after resume")
	}

	seen := ranges()
	// Completed segments 0 and 2 must not be requested at all.
	for _, r := range seen {
		var rs, re int64
		fmt.Sscanf(r, "bytes=%d-%d", &rs, &re)
		if rs >= 0 && re < segSize {
			t.Errorf("completed segment 0 was re-requested: %s", r)
		}
		if rs >= 2*segSize && re < 3*segSize {
			t.Errorf("completed segment 2 was re-requested: %s", r)
		}
	}
	// Segment 1 must resume from its recorded partial offset.
	wantResume := fmt.Sprintf("bytes=%d-%d", segSize+segSize/2, 2*segSize-1)
	found := false
	for _, r := range seen {
		if r == wantResume {
			found = true
		}
	}
	if !found {
		t.Errorf("segment 1 not resumed from partial offset: want %q in %v", wantResume, seen)
	}
}

// TestDownloadSegmented_AdoptsSingleDownloadPrefix verifies that a plain
// prefix .part left behind by an interrupted single-connection download is
// converted into completed segments instead of being discarded.
func TestDownloadSegmented_AdoptsSingleDownloadPrefix(t *testing.T) {
	tmpDir := t.TempDir()
	const segSize = 2500
	full := patternBytes(10000)
	setTestSegmentSize(t, segSize)

	dst := filepath.Join(tmpDir, "blobs", "tmp-prefix")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	// Prefix covering segment 0 entirely and 600 bytes of segment 1.
	if err := os.WriteFile(dst+".part", full[:segSize+600], 0o644); err != nil {
		t.Fatal(err)
	}

	srv, ranges := rangeServer(t, full)
	defer srv.Close()

	it := PlanItem{RelativePath: "prefix.bin", URL: srv.URL + "/prefix.bin", Size: int64(len(full)), AcceptRanges: true}
	err := downloadMultipart(context.Background(), srv.Client(), "", Job{Repo: "o/r"}, Settings{Concurrency: 2, Retries: 0}, it, dst, func(ProgressEvent) {})
	if err != nil {
		t.Fatalf("downloadMultipart: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, full) {
		t.Error("content mismatch after prefix adoption")
	}
	for _, r := range ranges() {
		var rs, re int64
		fmt.Sscanf(r, "bytes=%d-%d", &rs, &re)
		if rs < segSize {
			t.Errorf("prefix bytes were re-requested: %s", r)
		}
	}
}

// TestDownloadSegmented_StaleStateRestarts verifies that a state file whose
// recorded size disagrees with the plan is discarded rather than trusted.
func TestDownloadSegmented_StaleStateRestarts(t *testing.T) {
	tmpDir := t.TempDir()
	full := patternBytes(10000)
	setTestSegmentSize(t, 2500)

	dst := filepath.Join(tmpDir, "blobs", "tmp-stale")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	// State from a previous revision of the file with a different size.
	if err := os.WriteFile(dst+".part", make([]byte, 5000), 0o644); err != nil {
		t.Fatal(err)
	}
	st := &segmentState{Version: 1, Size: 5000, SegmentSize: 2500, Completed: []int{0, 1}}
	if err := st.save(statePathFor(dst)); err != nil {
		t.Fatal(err)
	}

	srv, _ := rangeServer(t, full)
	defer srv.Close()

	it := PlanItem{RelativePath: "stale.bin", URL: srv.URL + "/stale.bin", Size: int64(len(full)), AcceptRanges: true}
	err := downloadMultipart(context.Background(), srv.Client(), "", Job{Repo: "o/r"}, Settings{Concurrency: 4, Retries: 0}, it, dst, func(ProgressEvent) {})
	if err != nil {
		t.Fatalf("downloadMultipart: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, full) {
		t.Error("content mismatch after stale-state restart")
	}
}

// TestDownloadSegmented_FallsBackWhenRangeIgnored verifies that a server
// answering 200 OK to Range requests triggers a clean fallback to the
// sequential single-connection path and still produces correct bytes.
func TestDownloadSegmented_FallsBackWhenRangeIgnored(t *testing.T) {
	tmpDir := t.TempDir()
	full := patternBytes(10000)
	setTestSegmentSize(t, 2500)

	dst := filepath.Join(tmpDir, "blobs", "tmp-norange")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", strconv.Itoa(len(full)))
			return
		}
		// Ignore Range entirely.
		w.Header().Set("Content-Length", strconv.Itoa(len(full)))
		w.WriteHeader(http.StatusOK)
		w.Write(full)
	}))
	defer srv.Close()

	it := PlanItem{RelativePath: "norange.bin", URL: srv.URL + "/norange.bin", Size: int64(len(full)), AcceptRanges: true}
	err := downloadMultipart(context.Background(), srv.Client(), "", Job{Repo: "o/r"}, Settings{Concurrency: 4, Retries: 0}, it, dst, func(ProgressEvent) {})
	if err != nil {
		t.Fatalf("downloadMultipart: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, full) {
		t.Error("content mismatch after range fallback")
	}
	if _, err := os.Stat(statePathFor(dst)); !os.IsNotExist(err) {
		t.Error("state file not cleaned up after fallback")
	}
}

// TestDownloadSegmented_RemovesLegacyPartFiles verifies that .part-NN files
// from the old engine are removed rather than left to rot next to the blob.
func TestDownloadSegmented_RemovesLegacyPartFiles(t *testing.T) {
	tmpDir := t.TempDir()
	full := patternBytes(10000)
	setTestSegmentSize(t, 2500)

	dst := filepath.Join(tmpDir, "blobs", "tmp-legacy")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := os.WriteFile(fmt.Sprintf("%s.part-%02d", dst, i), []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	srv, _ := rangeServer(t, full)
	defer srv.Close()

	it := PlanItem{RelativePath: "legacy.bin", URL: srv.URL + "/legacy.bin", Size: int64(len(full)), AcceptRanges: true}
	err := downloadMultipart(context.Background(), srv.Client(), "", Job{Repo: "o/r"}, Settings{Concurrency: 4, Retries: 0}, it, dst, func(ProgressEvent) {})
	if err != nil {
		t.Fatalf("downloadMultipart: %v", err)
	}
	legacy, _ := filepath.Glob(dst + ".part-*")
	if len(legacy) != 0 {
		t.Errorf("legacy part files not cleaned up: %v", legacy)
	}
}
