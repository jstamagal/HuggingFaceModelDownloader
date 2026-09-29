// SPDX-License-Identifier: Apache-2.0

package hfdownloader

// Adopt turns files that already exist on disk (hf download --local-dir,
// LM Studio's models/<owner>/<repo>/, app checkpoint dumps, ...) into real
// Hugging Face cache entries without re-downloading them.
//
// Matching is by content, not by name: every local file is hashed and looked
// up in the repo tree(s) by LFS sha256 (large files) or git blob sha1 (small
// files). A file is only moved once upstream vouches for its exact bytes, so
// renamed files (a flat app ckpts/ dir) resolve to their real repo path and
// corrupt/partial files are never adopted.

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// AdoptMode selects how a verified local file is put into blobs/.
type AdoptMode string

const (
	AdoptMove     AdoptMode = "move"     // rename into blobs/ (instant on the same filesystem)
	AdoptHardlink AdoptMode = "hardlink" // hardlink into blobs/, original stays
	AdoptCopy     AdoptMode = "copy"     // copy into blobs/, original stays
)

// AdoptOptions configures an adopt run.
type AdoptOptions struct {
	// Repos are candidate repo IDs. Every local file is matched against all
	// of them. Required.
	Repos []string
	// Dir is the local directory to adopt from (walked recursively).
	Dir string
	// Files optionally restricts adoption to these paths (absolute or
	// relative to Dir). Empty means every regular file under Dir.
	Files []string

	Mode    AdoptMode
	DryRun  bool
	NoFetch bool // don't download missing small (non-LFS) files of the chosen commit
	// LeaveLink replaces each adopted original with a symlink to its snapshot
	// path, so whatever read the old location keeps working.
	LeaveLink bool
	// HistoryDepth is how many commits back to search when a file is not in
	// HEAD (0 = HEAD only).
	HistoryDepth int
	Jobs         int // parallel hashers

	Token    string
	Endpoint string
	Proxy    *ProxyConfig
	Log      func(format string, args ...any)
}

// AdoptedFile is one local file's outcome.
type AdoptedFile struct {
	Local    string `json:"local"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256,omitempty"`
	Repo     string `json:"repo,omitempty"`
	Commit   string `json:"commit,omitempty"`
	RepoPath string `json:"repo_path,omitempty"`
	Status   string `json:"status"` // adopted | duplicate | would-adopt | unmatched | skipped | error
	Note     string `json:"note,omitempty"`
}

// AdoptRepoResult summarises one repo.
type AdoptRepoResult struct {
	Repo    string   `json:"repo"`
	Commit  string   `json:"commit"`
	IsHead  bool     `json:"is_head"`
	Files   int      `json:"files"`
	Bytes   int64    `json:"bytes"`
	Fetched []string `json:"fetched,omitempty"`
}

// AdoptResult is the full report.
type AdoptResult struct {
	Files []AdoptedFile        `json:"files"`
	Repos []AdoptRepoResult    `json:"repos"`
	Head  map[string]string    `json:"-"`
	trees map[string]*repoTree // repo -> HEAD tree (debug)
}

// remoteFile is one file of a repo tree at a commit.
type remoteFile struct {
	Path   string
	Size   int64
	GitOid string // git blob sha1 of the content (non-LFS) or of the pointer (LFS)
	SHA256 string // LFS sha256; empty for non-LFS
}

type repoTree struct {
	Repo   string
	Commit string
	Files  []remoteFile
	bySHA  map[string][]int // lfs sha256 -> indexes
	byGit  map[string][]int // git oid (non-LFS only) -> indexes
}

func newRepoTree(repo, commit string, files []remoteFile) *repoTree {
	t := &repoTree{Repo: repo, Commit: commit, Files: files, bySHA: map[string][]int{}, byGit: map[string][]int{}}
	for i, f := range files {
		if f.SHA256 != "" {
			t.bySHA[f.SHA256] = append(t.bySHA[f.SHA256], i)
		} else if f.GitOid != "" {
			t.byGit[f.GitOid] = append(t.byGit[f.GitOid], i)
		}
	}
	return t
}

// match returns the repo paths whose content equals the local hashes.
func (t *repoTree) match(h localHash) []remoteFile {
	var idx []int
	if h.SHA256 != "" {
		idx = append(idx, t.bySHA[h.SHA256]...)
	}
	if h.GitOid != "" {
		idx = append(idx, t.byGit[h.GitOid]...)
	}
	out := make([]remoteFile, 0, len(idx))
	for _, i := range idx {
		out = append(out, t.Files[i])
	}
	return out
}

type localHash struct {
	Path   string
	Size   int64
	SHA256 string
	GitOid string // only computed for files small enough to be non-LFS
}

// Files above this size are always LFS on the Hub, so git-sha1 is useless.
const adoptGitOidMax = 50 << 20

type hubAPI struct {
	ctx      context.Context
	httpc    *http.Client
	token    string
	endpoint string
}

func (a *hubAPI) getJSON(u string, v any) error {
	for attempt := 0; ; attempt++ {
		req, _ := http.NewRequestWithContext(a.ctx, "GET", u, nil)
		addAuth(req, a.token)
		resp, err := a.httpc.Do(req)
		if err != nil {
			return err
		}
		if resp.StatusCode == 429 && attempt < 5 {
			resp.Body.Close()
			wait := time.Duration(2<<attempt) * time.Second
			if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
				wait = time.Duration(s) * time.Second
			}
			select {
			case <-time.After(wait):
				continue
			case <-a.ctx.Done():
				return a.ctx.Err()
			}
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
			return &APIError{StatusCode: resp.StatusCode, Status: resp.Status, URL: u, Message: strings.TrimSpace(string(body))}
		}
		return json.NewDecoder(resp.Body).Decode(v)
	}
}

// getPaged GETs u and returns the decoded page plus the rel="next" URL.
func (a *hubAPI) getPaged(u string, v any) (string, error) {
	for attempt := 0; ; attempt++ {
		req, _ := http.NewRequestWithContext(a.ctx, "GET", u, nil)
		addAuth(req, a.token)
		resp, err := a.httpc.Do(req)
		if err != nil {
			return "", err
		}
		if resp.StatusCode == 429 && attempt < 6 {
			resp.Body.Close()
			wait := time.Duration(2<<attempt) * time.Second
			if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
				wait = time.Duration(s) * time.Second
			}
			select {
			case <-time.After(wait):
				continue
			case <-a.ctx.Done():
				return "", a.ctx.Err()
			}
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
			return "", &APIError{StatusCode: resp.StatusCode, Status: resp.Status, URL: u, Message: strings.TrimSpace(string(body))}
		}
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			return "", err
		}
		return nextLink(resp.Header.Get("Link")), nil
	}
}

// nextLink extracts the rel="next" target of an RFC 8288 Link header.
func nextLink(h string) string {
	for _, part := range strings.Split(h, ",") {
		if !strings.Contains(part, `rel="next"`) {
			continue
		}
		if i, j := strings.Index(part, "<"), strings.Index(part, ">"); i >= 0 && j > i {
			return part[i+1 : j]
		}
	}
	return ""
}

// tree lists every file of repo@rev with one recursive, paginated listing
// (the per-directory walk costs one request per folder).
func (a *hubAPI) tree(repo, rev string) (*repoTree, error) {
	var files []remoteFile
	u := fmt.Sprintf("%s/api/models/%s/tree/%s?recursive=true", getEndpoint(a.endpoint), repo, rev)
	var nodes []hfNode
	for u != "" {
		var page []hfNode
		next, err := a.getPaged(u, &page)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, page...)
		u = next
	}
	err := func() error {
		for _, n := range nodes {
			if err := a.addNode(repo, n, &files); err != nil {
				return err
			}
		}
		return nil
	}()
	if err != nil {
		return nil, err
	}
	return newRepoTree(repo, rev, files), nil
}

func (a *hubAPI) addNode(repo string, n hfNode, files *[]remoteFile) error {
	{
		if n.Type != "file" && n.Type != "blob" {
			return nil
		}
		if unsafeRepoPath(n.Path) {
			return fmt.Errorf("refusing unsafe path from repo tree: %q", n.Path)
		}
		f := remoteFile{Path: n.Path, Size: n.Size, GitOid: n.Oid}
		if n.LFS != nil {
			f.SHA256 = n.LFS.Sha256
			if f.SHA256 == "" {
				f.SHA256 = n.LFS.Oid
			}
			if n.LFS.Size > 0 {
				f.Size = n.LFS.Size
			}
			if strings.Trim(f.SHA256, "*") == "" {
				return fmt.Errorf("%s: hub masked the file hashes (gated repo) - a token with access is required", repo)
			}
		}
		*files = append(*files, f)
		return nil
	}
}

func (a *hubAPI) headCommit(repo string) (string, error) {
	info, err := fetchRepoInfo(a.ctx, a.httpc, a.token, a.endpoint, Job{Repo: repo, Revision: "main"})
	if err != nil {
		return "", err
	}
	if len(info.SHA) != 40 {
		return "", fmt.Errorf("%s: hub returned no commit sha for main", repo)
	}
	return info.SHA, nil
}

func (a *hubAPI) commits(repo string, limit int) ([]string, error) {
	var out []string
	u := fmt.Sprintf("%s/api/models/%s/commits/main?limit=%d", getEndpoint(a.endpoint), repo, min(limit, 100))
	for u != "" && len(out) < limit {
		var cs []struct {
			ID string `json:"id"`
		}
		next, err := a.getPaged(u, &cs)
		if err != nil {
			return out, err
		}
		if len(cs) == 0 {
			break
		}
		for _, c := range cs {
			out = append(out, c.ID)
		}
		u = next
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (a *hubAPI) fetchRaw(repo, commit, path, dst string) error {
	u := rawURL(a.endpoint, Job{Repo: repo, Revision: commit}, path)
	req, _ := http.NewRequestWithContext(a.ctx, "GET", u, nil)
	addAuth(req, a.token)
	resp, err := a.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return &APIError{StatusCode: resp.StatusCode, Status: resp.Status, URL: u}
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// hashLocal computes sha256 (always) and git blob sha1 (small files) in one pass.
func hashLocal(path string, size int64) (localHash, error) {
	f, err := os.Open(path)
	if err != nil {
		return localHash{}, err
	}
	defer f.Close()
	h256 := sha256.New()
	var w io.Writer = h256
	var h1 = sha1.New()
	small := size <= adoptGitOidMax
	if small {
		fmt.Fprintf(h1, "blob %d\x00", size)
		w = io.MultiWriter(h256, h1)
	}
	buf := make([]byte, 4<<20)
	n, err := io.CopyBuffer(w, f, buf)
	if err != nil {
		return localHash{}, err
	}
	if n != size {
		return localHash{}, fmt.Errorf("%s changed size while hashing (%d != %d)", path, n, size)
	}
	lh := localHash{Path: path, Size: size, SHA256: hex.EncodeToString(h256.Sum(nil))}
	if small {
		lh.GitOid = hex.EncodeToString(h1.Sum(nil))
	}
	return lh, nil
}

// hashCache remembers hashes by (dev, inode, size, mtime) so re-runs and the
// watcher skip re-reading hundreds of GB. Stored as JSON in the cache root.
type hashCache struct {
	path  string
	mu    sync.Mutex
	m     map[string]localHash
	dirty bool
}

func loadHashCache(path string) *hashCache {
	hc := &hashCache{path: path, m: map[string]localHash{}}
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &hc.m)
	}
	return hc
}

func hashKey(path string) (string, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	dev, ino := fileDevIno(fi)
	if ino == 0 {
		return fmt.Sprintf("%s:%d:%d", path, fi.Size(), fi.ModTime().UnixNano()), true
	}
	return fmt.Sprintf("%d:%d:%d:%d", dev, ino, fi.Size(), fi.ModTime().UnixNano()), true
}

func (hc *hashCache) get(path string) (localHash, bool) {
	k, ok := hashKey(path)
	if !ok {
		return localHash{}, false
	}
	hc.mu.Lock()
	defer hc.mu.Unlock()
	h, ok := hc.m[k]
	h.Path = path
	return h, ok
}

func (hc *hashCache) put(h localHash) {
	k, ok := hashKey(h.Path)
	if !ok {
		return
	}
	st := h
	st.Path = ""
	hc.mu.Lock()
	hc.m[k] = st
	hc.dirty = true
	hc.mu.Unlock()
}

func (hc *hashCache) save() {
	hc.mu.Lock()
	defer hc.mu.Unlock()
	if !hc.dirty {
		return
	}
	b, _ := json.Marshal(hc.m)
	tmp := hc.path + ".tmp"
	if os.WriteFile(tmp, b, 0644) == nil {
		os.Rename(tmp, hc.path)
	}
}

// adoptSkipName reports local files that are never model content.
func adoptSkipName(name string) bool {
	l := strings.ToLower(name)
	return strings.HasSuffix(l, ".part") || strings.HasSuffix(l, ".incomplete") ||
		strings.HasSuffix(l, ".lock") || strings.HasPrefix(l, "downloading_") || l == ".ds_store"
}

func (o *AdoptOptions) logf(format string, args ...any) {
	if o.Log != nil {
		o.Log(format, args...)
	}
}

// Adopt runs the adoption described by opts against cache.
func (c *HFCache) Adopt(ctx context.Context, opts AdoptOptions) (*AdoptResult, error) {
	if len(opts.Repos) == 0 {
		return nil, errors.New("adopt: at least one --repo is required")
	}
	for _, r := range opts.Repos {
		if err := validateRepoID(r); err != nil {
			return nil, err
		}
	}
	if opts.Mode == "" {
		opts.Mode = AdoptMove
	}
	if opts.Jobs <= 0 {
		opts.Jobs = 4
	}
	dir, err := filepath.Abs(opts.Dir)
	if err != nil {
		return nil, err
	}

	// 1. Collect local files (regular files only; symlinks are already views).
	type lf struct {
		path string
		size int64
	}
	var locals []lf
	res := &AdoptResult{Head: map[string]string{}}
	addLocal := func(p string, info os.FileInfo) {
		if !info.Mode().IsRegular() {
			return
		}
		if adoptSkipName(info.Name()) {
			res.Files = append(res.Files, AdoptedFile{Local: p, Size: info.Size(), Status: "skipped", Note: "partial/lock file"})
			return
		}
		locals = append(locals, lf{p, info.Size()})
	}
	if len(opts.Files) > 0 {
		for _, f := range opts.Files {
			p := f
			if !filepath.IsAbs(p) {
				p = filepath.Join(dir, p)
			}
			info, err := os.Lstat(p)
			if err != nil {
				return nil, err
			}
			addLocal(p, info)
		}
	} else {
		err = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() && p != dir && (info.Name() == ".cache" || info.Name() == ".git") {
				return filepath.SkipDir
			}
			addLocal(p, info)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if len(locals) == 0 {
		return res, nil
	}

	// 2. Hash in parallel (disk-bound; big files dominate).
	sort.Slice(locals, func(i, j int) bool { return locals[i].size > locals[j].size })
	hashes := make([]localHash, len(locals))
	herrs := make([]error, len(locals))
	var wg sync.WaitGroup
	next := make(chan int)
	var hashedBytes int64
	var mu sync.Mutex
	var total int64
	for _, l := range locals {
		total += l.size
	}
	start := time.Now()
	hc := loadHashCache(filepath.Join(c.Root, ".adopt-hashes.json"))
	defer hc.save()
	for w := 0; w < opts.Jobs; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				if h, ok := hc.get(locals[i].path); ok {
					hashes[i] = h
				} else {
					hashes[i], herrs[i] = hashLocal(locals[i].path, locals[i].size)
					if herrs[i] == nil {
						hc.put(hashes[i])
					}
				}
				mu.Lock()
				hashedBytes += locals[i].size
				opts.logf("hashed %s (%s) [%s/%s]", filepath.Base(locals[i].path), humanBytes(locals[i].size), humanBytes(hashedBytes), humanBytes(total))
				mu.Unlock()
			}
		}()
	}
	for i := range locals {
		select {
		case next <- i:
		case <-ctx.Done():
			close(next)
			wg.Wait()
			return nil, ctx.Err()
		}
	}
	close(next)
	wg.Wait()
	hc.save()
	if secs := time.Since(start).Seconds(); secs > 0 {
		opts.logf("hashed %s in %.0fs (%s/s)", humanBytes(total), secs, humanBytes(int64(float64(total)/secs)))
	}

	// 3. Fetch HEAD trees; walk history only while files remain unmatched.
	api := &hubAPI{ctx: ctx, httpc: buildHTTPClientWithProxy(opts.Proxy), token: opts.Token, endpoint: opts.Endpoint}
	type hit struct {
		repo   string
		commit string
		file   remoteFile
		depth  int // 0 = HEAD
	}
	hits := make([][]hit, len(locals)) // all (repo, commit) hits, newest first per repo
	trees := map[string][]*repoTree{}  // repo -> trees newest first
	matchAll := func(t *repoTree, depth int) int {
		n := 0
		for i, h := range hashes {
			if herrs[i] != nil {
				continue
			}
			for _, rf := range t.match(h) {
				hits[i] = append(hits[i], hit{t.Repo, t.Commit, rf, depth})
				n++
			}
		}
		return n
	}
	unmatched := func() int {
		n := 0
		for i := range locals {
			if herrs[i] == nil && len(hits[i]) == 0 {
				n++
			}
		}
		return n
	}
	// HEAD trees of all candidates in parallel; matching stays in repo order
	// so the first-listed repo wins content that several repos share.
	headTrees := make([]*repoTree, len(opts.Repos))
	headErrs := make([]error, len(opts.Repos))
	{
		sem := make(chan struct{}, 8)
		var twg sync.WaitGroup
		for k, repo := range opts.Repos {
			twg.Add(1)
			go func(k int, repo string) {
				defer twg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				head, err := api.headCommit(repo)
				if err == nil {
					headTrees[k], err = api.tree(repo, head)
				}
				headErrs[k] = err
			}(k, repo)
		}
		twg.Wait()
	}
	var live []string
	for k, repo := range opts.Repos {
		if err := headErrs[k]; err != nil {
			if len(opts.Repos) == 1 {
				return nil, fmt.Errorf("%s: %w", repo, err)
			}
			opts.logf("%s: skipped (%v)", repo, err)
			continue
		}
		t := headTrees[k]
		res.Head[repo] = t.Commit
		trees[repo] = []*repoTree{t}
		live = append(live, repo)
		if n := matchAll(t, 0); n > 0 || len(opts.Repos) <= 4 {
			opts.logf("%s@%s: %d files, %d local matches at HEAD", repo, t.Commit[:8], len(t.Files), n)
		}
	}
	opts.Repos = live
	// History: with many candidates, only walk repos that already matched at
	// HEAD - a stale file almost always comes from a repo still in use.
	matchedAtHead := map[string]bool{}
	for i := range hits {
		for _, h := range hits[i] {
			matchedAtHead[h.repo] = true
		}
	}
	if opts.HistoryDepth > 0 && unmatched() > 0 {
		for _, repo := range opts.Repos {
			if unmatched() == 0 {
				break
			}
			if len(opts.Repos) > 4 && !matchedAtHead[repo] {
				continue
			}
			cs, err := api.commits(repo, opts.HistoryDepth+1)
			if err != nil {
				opts.logf("%s: commit history unavailable: %v", repo, err)
				continue
			}
			for d, cmt := range cs {
				if cmt == res.Head[repo] {
					continue
				}
				if unmatched() == 0 {
					break
				}
				t, err := api.tree(repo, cmt)
				if err != nil {
					opts.logf("%s@%s: %v", repo, cmt[:8], err)
					continue
				}
				trees[repo] = append(trees[repo], t)
				if n := matchAll(t, d); n > 0 {
					opts.logf("%s@%s: %d matches in history", repo, cmt[:8], n)
				}
			}
		}
	}

	// 4. Per repo pick ONE snapshot commit: the newest commit that contains
	// every local file matched to that repo. Files that exist only in older
	// commits go to their own newest commit (refs/main still -> chosen).
	byRepoFiles := map[string][]int{}
	for i := range locals {
		if herrs[i] != nil {
			res.Files = append(res.Files, AdoptedFile{Local: locals[i].path, Size: locals[i].size, Status: "error", Note: herrs[i].Error()})
			continue
		}
		if len(hits[i]) == 0 {
			res.Files = append(res.Files, AdoptedFile{Local: locals[i].path, Size: locals[i].size, SHA256: hashes[i].SHA256, Status: "unmatched"})
			continue
		}
		byRepoFiles[hits[i][0].repo] = append(byRepoFiles[hits[i][0].repo], i)
	}

	for _, repo := range opts.Repos {
		idx := byRepoFiles[repo]
		if len(idx) == 0 {
			continue
		}
		chosen := ""
		for _, t := range trees[repo] { // newest first
			all := true
			for _, i := range idx {
				if len(t.match(hashes[i])) == 0 {
					all = false
					break
				}
			}
			if all {
				chosen = t.Commit
				break
			}
		}
		if chosen == "" {
			chosen = res.Head[repo]
			opts.logf("%s: no single commit holds all local files; spreading across snapshots, refs/main -> HEAD", repo)
		}
		var chosenTree *repoTree
		for _, t := range trees[repo] {
			if t.Commit == chosen {
				chosenTree = t
			}
		}

		rd, _ := c.Repo(repo, RepoTypeModel)
		rr := AdoptRepoResult{Repo: repo, Commit: chosen, IsHead: chosen == res.Head[repo]}
		inSnapshot := map[string]bool{}

		for _, i := range idx {
			h := hashes[i]
			// Prefer a hit in the chosen commit; prefer a path whose basename
			// equals the local name when content is duplicated in the repo.
			var pick *hit
			for k := range hits[i] {
				hk := &hits[i][k]
				if hk.repo != repo {
					continue
				}
				better := pick == nil ||
					(hk.commit == chosen && pick.commit != chosen) ||
					(hk.commit == pick.commit && filepath.Base(hk.file.Path) == filepath.Base(h.Path) && filepath.Base(pick.file.Path) != filepath.Base(h.Path))
				if better {
					pick = hk
				}
			}
			af := AdoptedFile{Local: h.Path, Size: h.Size, SHA256: h.SHA256, Repo: repo, Commit: pick.commit, RepoPath: pick.file.Path}
			if pick.commit == chosen {
				inSnapshot[pick.file.Path] = true
			}
			rr.Files++
			rr.Bytes += h.Size
			if opts.DryRun {
				af.Status = "would-adopt"
				res.Files = append(res.Files, af)
				continue
			}
			if err := rd.EnsureDirs(); err != nil {
				return nil, err
			}
			st, err := c.adoptOne(rd, h, pick.commit, pick.file.Path, opts)
			if err != nil {
				af.Status, af.Note = "error", err.Error()
			} else {
				af.Status = st
				// Other paths in the chosen commit with identical content
				// (Q8_0 shard == Q6_K shard) share the blob.
				for _, rf := range chosenTree.match(h) {
					if rf.Path != pick.file.Path && !inSnapshot[rf.Path] {
						if _, err := os.Lstat(rd.SnapshotPath(chosen, rf.Path)); err == nil {
							inSnapshot[rf.Path] = true
						}
					}
				}
			}
			res.Files = append(res.Files, af)
		}

		// Pull the small files of the chosen commit (README, configs,
		// tokenizer, ...) so the snapshot is what a real download produces.
		if chosenTree != nil && !opts.NoFetch {
			for _, rf := range chosenTree.Files {
				if rf.SHA256 != "" || inSnapshot[rf.Path] {
					continue
				}
				if _, err := os.Lstat(rd.SnapshotPath(chosen, rf.Path)); err == nil {
					continue
				}
				if opts.DryRun {
					rr.Fetched = append(rr.Fetched, rf.Path)
					continue
				}
				if err := rd.EnsureDirs(); err != nil {
					return nil, err
				}
				tmp := rd.IncompletePath("adopt-" + strconv.Itoa(os.Getpid()) + "-" + hex.EncodeToString([]byte(rf.Path))[:16])
				if err := api.fetchRaw(repo, chosen, rf.Path, tmp); err != nil {
					os.Remove(tmp)
					opts.logf("%s: fetch %s: %v", repo, rf.Path, err)
					continue
				}
				if _, err := rd.StoreDownloadedFile(tmp, rf.Path, chosen, "", "", false); err != nil {
					opts.logf("%s: store %s: %v", repo, rf.Path, err)
					continue
				}
				rr.Fetched = append(rr.Fetched, rf.Path)
			}
		}

		if !opts.DryRun {
			// refs/main must point at a commit whose snapshot exists.
			if cur, _ := rd.ReadRef("main"); cur == "" || cur == chosen || !dirExists(rd.SnapshotDir(cur)) || rr.IsHead {
				if err := rd.WriteRef("main", chosen); err != nil {
					return nil, err
				}
			} else {
				opts.logf("%s: refs/main already -> %s, snapshot %s added alongside", repo, cur[:8], chosen[:8])
			}
		}
		res.Repos = append(res.Repos, rr)
	}
	return res, nil
}

// adoptOne stores one verified local file as a blob + snapshot link.
func (c *HFCache) adoptOne(rd *RepoDir, h localHash, commit, repoPath string, opts AdoptOptions) (string, error) {
	blob := rd.BlobPath(h.SHA256)
	status := "adopted"
	if fi, err := os.Stat(blob); err == nil {
		if fi.Size() != h.Size {
			return "", fmt.Errorf("existing blob %s has wrong size %d", blob, fi.Size())
		}
		status = "duplicate"
		// Same inode already? nothing to free.
		if same, _ := sameFile(blob, h.Path); !same && opts.Mode == AdoptMove {
			if err := os.Remove(h.Path); err != nil {
				return "", err
			}
		}
	} else {
		// Re-check the source didn't change since hashing.
		fi, err := os.Stat(h.Path)
		if err != nil {
			return "", err
		}
		if fi.Size() != h.Size {
			return "", fmt.Errorf("source changed size since hashing")
		}
		switch opts.Mode {
		case AdoptMove:
			if err := os.Rename(h.Path, blob); err != nil {
				var le *os.LinkError
				if errors.As(err, &le) && strings.Contains(le.Err.Error(), "cross-device") {
					return "", fmt.Errorf("source is on another filesystem than the cache; use --mode copy")
				}
				return "", err
			}
		case AdoptHardlink:
			if err := os.Link(h.Path, blob); err != nil {
				return "", err
			}
		case AdoptCopy:
			tmp := blob + ".incomplete"
			if err := copyFile(h.Path, tmp); err != nil {
				os.Remove(tmp)
				return "", err
			}
			if err := os.Rename(tmp, blob); err != nil {
				return "", err
			}
		default:
			return "", fmt.Errorf("unknown adopt mode %q", opts.Mode)
		}
		os.Chmod(blob, 0644)
	}
	if err := rd.createSnapshotSymlink(commit, repoPath, h.SHA256); err != nil {
		return "", err
	}
	if err := rd.CreateFriendlySymlink(commit, repoPath, ""); err != nil {
		return "", err
	}
	if opts.LeaveLink && opts.Mode == AdoptMove {
		if _, err := os.Lstat(h.Path); errors.Is(err, os.ErrNotExist) {
			abs, _ := filepath.Abs(rd.SnapshotPath(commit, repoPath))
			if err := os.Symlink(abs, h.Path); err != nil {
				return status, fmt.Errorf("adopted, but leave-link failed: %w", err)
			}
		}
	}
	return status, nil
}

func sameFile(a, b string) (bool, error) {
	fa, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(fa, fb), nil
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// InferRepoFromPath returns owner/name from the last two path components
// (LM Studio, `hf download --local-dir owner/name`, hfdownloader models/).
func InferRepoFromPath(dir string) (string, bool) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	name := filepath.Base(abs)
	owner := filepath.Base(filepath.Dir(abs))
	id := owner + "/" + name
	if validateRepoID(id) != nil {
		return "", false
	}
	return id, true
}
