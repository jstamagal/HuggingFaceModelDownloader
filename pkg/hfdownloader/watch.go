// SPDX-License-Identifier: Apache-2.0

package hfdownloader

// Watch keeps the views alive: anything real that lands in a view (a file
// you mv in, an app download, an `ollama pull hf.co/...`) is adopted into the
// hub by content and the views are rebuilt. Files that cannot be matched to a
// Hub repo (civitai loras, private merges) stay exactly where they are.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// WatchOptions configures Watch.
type WatchOptions struct {
	Views        ViewsOptions
	MaestroRepos []string      // candidate repos for files Maestro downloads
	Settle       time.Duration // file must be unchanged this long before ingest
	Rebuild      time.Duration // periodic full rebuild (catches plain hfdownloader downloads)
	Token        string
	Endpoint     string
	Log          func(format string, args ...any)
}

func (o *WatchOptions) logf(f string, a ...any) {
	if o.Log != nil {
		o.Log(f, a...)
	}
}

// Watch runs until ctx is cancelled.
func (c *HFCache) Watch(ctx context.Context, opts WatchOptions) error {
	if opts.Settle == 0 {
		opts.Settle = 10 * time.Second
	}
	if opts.Rebuild == 0 {
		opts.Rebuild = 5 * time.Minute
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()

	roots := []string{
		filepath.Join(opts.Views.Root, "lmstudio"),
		filepath.Join(opts.Views.Root, "hipfire"),
		filepath.Join(opts.Views.Root, "ollama", "manifests"),
		filepath.Join(opts.Views.Root, "inbox"),
	}
	if opts.Views.MaestroDir != "" {
		roots = append(roots, opts.Views.MaestroDir)
	}
	if opts.Views.DiffusionDir != "" {
		roots = append(roots, opts.Views.DiffusionDir)
	}
	addTree := func(dir string) {
		filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err != nil || !info.IsDir() {
				return nil
			}
			if n := info.Name(); p != dir && (n == ".cache" || n == "bin" || n == ".git") {
				return filepath.SkipDir
			}
			w.Add(p)
			return nil
		})
	}
	for _, r := range roots {
		os.MkdirAll(r, 0755)
		addTree(r)
	}
	// Watch the real hub path (the cache dir may be a symlink) down to the
	// snapshot dirs: a finished download shows up as a new snapshot link or
	// refs/main write. blobs/ is skipped - .incomplete churn is not a change.
	hub := c.HubDir()
	if r, err := filepath.EvalSymlinks(hub); err == nil {
		hub = r
	}
	addHub := func(dir string) {
		filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err != nil || !info.IsDir() {
				return nil
			}
			if info.Name() == "blobs" || info.Name() == ".locks" {
				return filepath.SkipDir
			}
			w.Add(p)
			return nil
		})
	}
	addHub(hub)

	rebuild := func(why string) {
		rep, err := c.BuildViews(opts.Views)
		if err != nil {
			opts.logf("views rebuild (%s): %v", why, err)
			return
		}
		opts.logf("views rebuilt (%s): ollama=%d lmstudio=%d hipfire=%d comfy=%d removed=%d",
			why, rep.OllamaModels, rep.LMStudioFiles, rep.HipfireFiles, rep.ComfyLinks, rep.Removed)
	}
	rebuild("start")
	// Anything real already sitting in the views gets a pass at start.
	pending := map[string]time.Time{}
	for _, r := range roots {
		filepath.Walk(r, func(p string, info os.FileInfo, err error) error {
			if err == nil && info.Mode().IsRegular() && ingestCandidate(p, info) {
				pending[p] = time.Now()
			}
			if err == nil && info.IsDir() && (info.Name() == ".cache" || info.Name() == "bin") {
				return filepath.SkipDir
			}
			return nil
		})
	}

	var mu sync.Mutex
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	full := time.NewTicker(opts.Rebuild)
	defer full.Stop()
	hubDirty := false
	var hubLast time.Time
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-w.Errors:
			opts.logf("watch error: %v", err)
		case ev := <-w.Events:
			if strings.HasPrefix(ev.Name, hub+string(filepath.Separator)) {
				if ev.Op&fsnotify.Create != 0 {
					if fi, err := os.Lstat(ev.Name); err == nil && fi.IsDir() {
						addHub(ev.Name)
					}
				}
				hubDirty, hubLast = true, time.Now()
				continue
			}
			info, err := os.Lstat(ev.Name)
			if err != nil {
				continue
			}
			if info.IsDir() && ev.Op&fsnotify.Create != 0 {
				addTree(ev.Name)
				filepath.Walk(ev.Name, func(p string, fi os.FileInfo, err error) error {
					if err == nil && fi.Mode().IsRegular() && ingestCandidate(p, fi) {
						mu.Lock()
						pending[p] = time.Now()
						mu.Unlock()
					}
					return nil
				})
				continue
			}
			if info.Mode().IsRegular() && ingestCandidate(ev.Name, info) {
				mu.Lock()
				pending[ev.Name] = time.Now()
				mu.Unlock()
			}
		case <-full.C:
			rebuild("periodic")
		case <-tick.C:
			if hubDirty && time.Since(hubLast) >= 3*time.Second {
				hubDirty = false
				rebuild("hub changed")
			}
			var ready []string
			mu.Lock()
			for p, t := range pending {
				if time.Since(t) >= opts.Settle {
					ready = append(ready, p)
					delete(pending, p)
				}
			}
			mu.Unlock()
			if len(ready) == 0 {
				continue
			}
			changed := false
			for _, p := range ready {
				if c.ingest(ctx, p, opts) {
					changed = true
				}
			}
			if changed {
				rebuild("ingest")
			}
		}
	}
}

// ingestCandidate filters out builder-owned and in-progress files.
func ingestCandidate(p string, info os.FileInfo) bool {
	n := info.Name()
	if adoptSkipName(n) || strings.HasPrefix(n, ".") || strings.HasPrefix(n, "put_") ||
		strings.HasSuffix(n, ".tmp") || strings.Contains(n, ".part") || info.Size() == 0 ||
		fileLinkCount(info) > 1 { // hardlink of a hub blob (hipfire view)
		return false
	}
	if strings.Contains(filepath.ToSlash(p), "/manifests/") {
		return true
	}
	switch strings.ToLower(filepath.Ext(n)) {
	case ".gguf", ".safetensors", ".sft", ".bin", ".pt", ".pth", ".ckpt", ".onnx", ".mq4", ".hfq", ".pkl":
		return true
	}
	return false
}

// ingest adopts one real file dropped into a view. Returns true if the hub
// changed.
func (c *HFCache) ingest(ctx context.Context, p string, opts WatchOptions) bool {
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	// Still growing? requeue next round by returning (Watch re-adds on write).
	time.Sleep(time.Second)
	if fi2, err := os.Lstat(p); err != nil || fi2.Size() != fi.Size() {
		return false
	}
	v := opts.Views
	var repos []string
	leaveLink := false
	under := func(root string) (string, bool) {
		if root == "" {
			return "", false
		}
		rel, err := filepath.Rel(root, p)
		return rel, err == nil && !strings.HasPrefix(rel, "..")
	}
	lmRoot := filepath.Join(v.Root, "lmstudio")
	manRoot := filepath.Join(v.Root, "ollama", "manifests")
	switch {
	case func() bool { _, ok := under(manRoot); return ok }():
		return c.ingestOllamaManifest(ctx, p, opts)
	case func() bool {
		rel, ok := under(lmRoot)
		return ok && strings.Count(rel, string(filepath.Separator)) >= 2
	}():
		rel, _ := under(lmRoot)
		parts := strings.SplitN(filepath.ToSlash(rel), "/", 3)
		repos = []string{parts[0] + "/" + parts[1]}
	case func() bool { _, ok := under(v.MaestroDir); return ok }():
		repos = opts.MaestroRepos
		leaveLink = true // Maestro reads this exact path
	case func() bool { _, ok := under(v.DiffusionDir); return ok }():
		leaveLink = true // ComfyUI reads this exact path
	}
	if len(repos) == 0 {
		g, err := GuessRepos(ctx, p, opts.Token, opts.Endpoint)
		if err != nil {
			opts.logf("ingest %s: repo search: %v", p, err)
		}
		repos = g
	}
	if len(repos) == 0 {
		opts.logf("ingest %s: no candidate repo, left in place", p)
		return false
	}
	res, err := c.Adopt(ctx, AdoptOptions{
		Repos: repos, Dir: filepath.Dir(p), Files: []string{p},
		Mode: AdoptMove, LeaveLink: leaveLink, HistoryDepth: 20, Jobs: 1,
		Token: opts.Token, Endpoint: opts.Endpoint,
	})
	if err != nil {
		opts.logf("ingest %s: %v", p, err)
		return false
	}
	for _, f := range res.Files {
		switch f.Status {
		case "adopted", "duplicate":
			opts.logf("ingest %s -> %s@%s:%s", p, f.Repo, f.Commit[:8], f.RepoPath)
			return true
		default:
			opts.logf("ingest %s: %s %s (left in place)", p, f.Status, f.Note)
		}
	}
	return false
}

// ingestOllamaManifest handles `ollama pull hf.co/<o>/<r>:<tag>`: the pulled
// layers are HF LFS blobs, so they move into the hub and become links.
func (c *HFCache) ingestOllamaManifest(ctx context.Context, p string, opts WatchOptions) bool {
	man := filepath.Join(opts.Views.Root, "ollama", "manifests")
	rel, _ := filepath.Rel(man, p)
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) != 4 || parts[0] != "hf.co" {
		return false // registry.ollama.ai models are not on the Hub
	}
	var m ollamaManifest
	b, err := os.ReadFile(p)
	if err != nil || json.Unmarshal(b, &m) != nil {
		return false
	}
	blobs := filepath.Join(opts.Views.Root, "ollama", "blobs")
	var files []string
	for _, l := range m.Layers {
		if l.MediaType != "application/vnd.ollama.image.model" && l.MediaType != "application/vnd.ollama.image.projector" {
			continue
		}
		bp := filepath.Join(blobs, strings.Replace(l.Digest, ":", "-", 1))
		if fi, err := os.Lstat(bp); err == nil && fi.Mode().IsRegular() {
			files = append(files, bp)
		}
	}
	if len(files) == 0 {
		return false
	}
	res, err := c.Adopt(ctx, AdoptOptions{
		Repos: []string{parts[1] + "/" + parts[2]}, Dir: blobs, Files: files,
		Mode: AdoptMove, LeaveLink: true, HistoryDepth: 20, Jobs: 2,
		Token: opts.Token, Endpoint: opts.Endpoint,
	})
	if err != nil {
		opts.logf("ingest ollama %s: %v", rel, err)
		return false
	}
	n := 0
	for _, f := range res.Files {
		if f.Status == "adopted" || f.Status == "duplicate" {
			n++
		}
	}
	opts.logf("ingest ollama %s: %d/%d layers moved into hub", rel, n, len(files))
	return n > 0
}
