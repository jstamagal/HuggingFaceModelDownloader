// SPDX-License-Identifier: Apache-2.0

package hfdownloader

// Views: per-application directory layouts built as symlink trees on top of
// the hub cache. The hub (hub/models--o--r/{blobs,snapshots,refs}) is the only
// place bytes live; every view is disposable and rebuilt from it.
//
//   ollama/    OLLAMA_MODELS layout: manifests/hf.co/<o>/<r>/<QUANT> + blobs/sha256-<x>
//              (model layers are symlinks to hub blobs - the Hub LFS sha256
//              IS Ollama's digest, so no bytes are duplicated)
//   lmstudio/  LM Studio downloadsFolder: <o>/<r>/<file>.gguf
//   hipfire/   flat hipfire models dir for hipfire-models/* repos
//   comfyui    <diffusion>/<category>/<file>, classified from the Maestro view
//
// Only symlinks the builder made are ever removed; real files dropped into a
// view are left for `watch` to adopt.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ViewsOptions configures a views build.
type ViewsOptions struct {
	Root         string // views root, e.g. /srv/models/views
	DiffusionDir string // ComfyUI models dir, e.g. /srv/models/diffusion ("" = skip)
	MaestroDir   string // Maestro flat ckpts view ("" = skip comfy sync)
	Log          func(format string, args ...any)
}

// ViewsReport summarises a build.
type ViewsReport struct {
	OllamaModels  int      `json:"ollama_models"`
	LMStudioFiles int      `json:"lmstudio_files"`
	HipfireFiles  int      `json:"hipfire_files"`
	ComfyLinks    int      `json:"comfy_links"`
	Removed       int      `json:"removed_stale"`
	Skipped       []string `json:"skipped,omitempty"`
}

// HubFile is one file of a repo's current snapshot.
type HubFile struct {
	Repo     string
	Path     string // path inside the repo
	Snapshot string // absolute snapshot symlink path
	Blob     string // absolute blob path
	SHA      string // blob name (sha256 for LFS files)
	Size     int64
	ModTime  time.Time
}

// SnapshotFiles lists every file of every model repo at refs/main (or the
// newest snapshot when refs/main is missing).
func (c *HFCache) SnapshotFiles() (map[string][]HubFile, error) {
	ents, err := os.ReadDir(c.HubDir())
	if err != nil {
		return nil, err
	}
	out := map[string][]HubFile{}
	for _, e := range ents {
		name := e.Name()
		if !e.IsDir() || !strings.HasPrefix(name, "models--") {
			continue
		}
		parts := strings.SplitN(strings.TrimPrefix(name, "models--"), "--", 2)
		if len(parts) != 2 {
			continue
		}
		repo := parts[0] + "/" + parts[1]
		rd, err := c.Repo(repo, RepoTypeModel)
		if err != nil {
			continue
		}
		commit, _ := rd.ReadRef("main")
		if commit == "" || !dirExists(rd.SnapshotDir(commit)) {
			commit = newestSnapshot(rd)
		}
		if commit == "" {
			continue
		}
		snap := rd.SnapshotDir(commit)
		filepath.Walk(snap, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			blob, err := filepath.EvalSymlinks(p)
			if err != nil {
				return nil // dangling snapshot link
			}
			fi, err := os.Stat(blob)
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(snap, p)
			out[repo] = append(out[repo], HubFile{
				Repo: repo, Path: filepath.ToSlash(rel), Snapshot: p, Blob: blob,
				SHA: filepath.Base(blob), Size: fi.Size(), ModTime: fi.ModTime(),
			})
			return nil
		})
	}
	return out, nil
}

func newestSnapshot(rd *RepoDir) string {
	ents, err := os.ReadDir(rd.SnapshotsDir())
	if err != nil {
		return ""
	}
	var best string
	var bt time.Time
	for _, e := range ents {
		if fi, err := e.Info(); err == nil && e.IsDir() && fi.ModTime().After(bt) {
			best, bt = e.Name(), fi.ModTime()
		}
	}
	return best
}

var (
	quantRe      = regexp.MustCompile(`(?i)(?:^|[-_.])((?:I?Q\d(?:_[A-Z0-9]+)*)|(?:MXFP4(?:_MOE)?)|(?:NVFP4)|(?:TQ\d_\d)|BF16|F16|F32|FP16|FP8)(?:[-_.]|$)`)
	shardRe      = regexp.MustCompile(`-(\d{5})-of-(\d{5})\.gguf$`)
	ollamaTagBad = regexp.MustCompile(`[^A-Za-z0-9_.-]`)
)

func isMMProj(p string) bool {
	b := strings.ToLower(filepath.Base(p))
	return strings.HasPrefix(b, "mmproj") || strings.Contains(b, "-mmproj") || strings.Contains(b, ".mmproj")
}

// ggufQuant returns the quant label of a GGUF path (file name, then parent dir).
func ggufQuant(p string) string {
	base := shardRe.ReplaceAllString(filepath.Base(p), ".gguf")
	base = strings.TrimSuffix(base, ".gguf")
	if m := quantRe.FindAllStringSubmatch(base, -1); len(m) > 0 {
		return strings.ToUpper(m[len(m)-1][1])
	}
	if m := quantRe.FindStringSubmatch(filepath.Base(filepath.Dir(p))); m != nil {
		return strings.ToUpper(m[1])
	}
	return ""
}

func (o *ViewsOptions) logf(f string, a ...any) {
	if o.Log != nil {
		o.Log(f, a...)
	}
}

// managedSet records the symlinks/files a view builder owns, so a rebuild can
// remove what it made without touching anything else.
type managedSet struct {
	path string
	old  map[string]bool
	now  map[string]bool
}

const viewsManifestName = ".hfd-managed.json"

func loadManaged(dir string) *managedSet {
	m := &managedSet{path: filepath.Join(dir, viewsManifestName), old: map[string]bool{}, now: map[string]bool{}}
	var l []string
	if b, err := os.ReadFile(m.path); err == nil && json.Unmarshal(b, &l) == nil {
		for _, p := range l {
			m.old[p] = true
		}
	}
	return m
}

// finish removes previously managed entries not produced this run.
func (m *managedSet) finish() (int, error) {
	n := 0
	for p := range m.old {
		if m.now[p] {
			continue
		}
		fi, err := os.Lstat(p)
		if err != nil {
			continue
		}
		// Only remove symlinks, hardlinks into the hub, and our own small
		// generated files.
		if fi.Mode()&os.ModeSymlink != 0 || fi.Size() < 1<<20 || fileLinkCount(fi) > 1 {
			if os.Remove(p) == nil {
				n++
				pruneEmptyDirs(filepath.Dir(p), filepath.Dir(m.path))
			}
		}
	}
	l := make([]string, 0, len(m.now))
	for p := range m.now {
		l = append(l, p)
	}
	sort.Strings(l)
	b, _ := json.MarshalIndent(l, "", " ")
	return n, os.WriteFile(m.path, b, 0644)
}

func pruneEmptyDirs(dir, stop string) {
	for dir != stop && strings.HasPrefix(dir, stop) {
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// ensureLink makes link point at target (absolute). A real file already at
// link is never replaced.
func ensureLink(link, target string) (bool, error) {
	if fi, err := os.Lstat(link); err == nil {
		if fi.Mode()&os.ModeSymlink == 0 {
			return false, nil // real file: owned by the user / watch
		}
		if cur, _ := os.Readlink(link); cur == target {
			return true, nil
		}
		os.Remove(link)
	}
	if err := os.MkdirAll(filepath.Dir(link), 0755); err != nil {
		return false, err
	}
	return true, os.Symlink(target, link)
}

// ensureHardlink makes link a hardlink of blob, for apps that skip symlinks
// (hipfire). A file already there that is not blob is only replaced when it
// is itself a hub hardlink (link count > 1); a user's real file is left alone.
func ensureHardlink(link, blob string) (bool, error) {
	bfi, err := os.Stat(blob)
	if err != nil {
		return false, err
	}
	if fi, err := os.Lstat(link); err == nil {
		if os.SameFile(fi, bfi) {
			return true, nil
		}
		if fi.Mode()&os.ModeSymlink == 0 && fileLinkCount(fi) < 2 {
			return false, nil
		}
		os.Remove(link)
	}
	if err := os.MkdirAll(filepath.Dir(link), 0755); err != nil {
		return false, err
	}
	return true, os.Link(blob, link)
}

// BuildViews rebuilds every view from the hub cache.
func (c *HFCache) BuildViews(opts ViewsOptions) (*ViewsReport, error) {
	files, err := c.SnapshotFiles()
	if err != nil {
		return nil, err
	}
	rep := &ViewsReport{}
	if err := c.buildOllama(files, opts, rep); err != nil {
		return rep, fmt.Errorf("ollama view: %w", err)
	}
	if err := buildLMStudio(files, opts, rep); err != nil {
		return rep, fmt.Errorf("lmstudio view: %w", err)
	}
	if err := buildHipfire(files, opts, rep); err != nil {
		return rep, fmt.Errorf("hipfire view: %w", err)
	}
	if opts.DiffusionDir != "" && opts.MaestroDir != "" {
		if err := syncComfyFromMaestro(opts, rep); err != nil {
			return rep, fmt.Errorf("comfyui view: %w", err)
		}
	}
	return rep, nil
}

// ---- ollama ---------------------------------------------------------------

// GGUF architectures that are diffusion/video models (ComfyUI-GGUF), which
// no LLM runtime can load.
var diffusionArch = map[string]bool{
	"wan": true, "ltx2": true, "ltxv": true, "flux": true, "sd1": true, "sdxl": true, "sd3": true,
	"qwen_image": true, "qwen_image21": true, "hidream": true, "hyvid": true, "cosmos": true, "lumina2": true, "chroma": true,
}

var errNotLLM = fmt.Errorf("diffusion gguf")

type ollamaLayer struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

type ollamaManifest struct {
	SchemaVersion int           `json:"schemaVersion"`
	MediaType     string        `json:"mediaType"`
	Config        ollamaLayer   `json:"config"`
	Layers        []ollamaLayer `json:"layers"`
}

// ollamaPlanned is one generated Ollama model: manifest + config blob, with
// layers pointing at existing files (hub blobs or local files).
type ollamaPlanned struct {
	Repo, Tag    string
	Layers       []HubFile // model shards, then projector
	Manifest     []byte
	Config       []byte
	ConfigDigest string // hex sha256 of Config
}

// planOllama groups every repo's GGUFs into Ollama models (one per quant,
// shards joined, mmproj bundled). Diffusion-arch GGUFs are dropped.
func planOllama(files map[string][]HubFile, skipped *[]string) []ollamaPlanned {
	repos := make([]string, 0, len(files))
	for r := range files {
		repos = append(repos, r)
	}
	sort.Strings(repos)
	var out []ollamaPlanned
	for _, repo := range repos {
		type group struct {
			quant  string
			shards []HubFile
		}
		groups := map[string]*group{}
		var projs []HubFile
		for _, f := range files[repo] {
			lp := strings.ToLower(f.Path)
			if !strings.HasSuffix(lp, ".gguf") || len(f.SHA) != 64 || strings.Contains(lp, "imatrix") {
				continue
			}
			if isMMProj(f.Path) {
				projs = append(projs, f)
				continue
			}
			if strings.HasPrefix(strings.ToLower(filepath.Base(f.Path)), "mtp-") {
				continue // speculative draft head, not a standalone model
			}
			logical := shardRe.ReplaceAllString(f.Path, ".gguf")
			g := groups[logical]
			if g == nil {
				g = &group{quant: ggufQuant(f.Path)}
				groups[logical] = g
			}
			g.shards = append(g.shards, f)
		}
		if len(groups) == 0 {
			continue
		}
		var proj *HubFile
		sort.Slice(projs, func(i, j int) bool { return projs[i].Path < projs[j].Path })
		for i := range projs {
			if proj == nil || strings.Contains(strings.ToUpper(projs[i].Path), "F16") && !strings.Contains(strings.ToUpper(proj.Path), "F16") {
				proj = &projs[i]
			}
		}
		logicals := make([]string, 0, len(groups))
		for l := range groups {
			logicals = append(logicals, l)
		}
		sort.Strings(logicals)
		byTag := map[string]int{}
		for _, l := range logicals {
			byTag[groups[l].quant]++
		}
		for _, l := range logicals {
			g := groups[l]
			sort.Slice(g.shards, func(i, j int) bool { return g.shards[i].Path < g.shards[j].Path })
			tag := g.quant
			if tag == "" || byTag[tag] > 1 {
				tag = strings.TrimSuffix(filepath.Base(l), ".gguf")
			}
			tag = ollamaTagBad.ReplaceAllString(tag, "_")
			if len(tag) > 120 {
				tag = tag[:120]
			}
			if len(g.shards) > 1 {
				if m := shardRe.FindStringSubmatch(g.shards[0].Path); m == nil || fmt.Sprintf("%05d", len(g.shards)) != m[2] {
					if skipped != nil {
						*skipped = append(*skipped, fmt.Sprintf("ollama: %s %s incomplete shard set (%d present)", repo, tag, len(g.shards)))
					}
					continue
				}
			}
			m, err := ollamaModel(repo, tag, g.quant, g.shards, proj)
			if err == errNotLLM {
				continue
			} else if err != nil {
				if skipped != nil {
					*skipped = append(*skipped, fmt.Sprintf("ollama: %s:%s: %v", repo, tag, err))
				}
				continue
			}
			out = append(out, m)
		}
	}
	return out
}

func ollamaModel(repo, tag, quant string, shards []HubFile, proj *HubFile) (ollamaPlanned, error) {
	var layers []ollamaLayer
	var diffIDs []string
	var params uint64
	arch := ""
	pl := ollamaPlanned{Repo: repo, Tag: tag}
	for i, s := range shards {
		meta, err := ReadGGUFMeta(s.Blob)
		if err != nil {
			return pl, err
		}
		params += meta.Params
		if i == 0 {
			arch = meta.Architecture
			if diffusionArch[arch] {
				return pl, errNotLLM
			}
		}
		layers = append(layers, ollamaLayer{"application/vnd.ollama.image.model", "sha256:" + s.SHA, s.Size})
		diffIDs = append(diffIDs, "sha256:"+s.SHA)
		pl.Layers = append(pl.Layers, s)
	}
	if proj != nil {
		layers = append(layers, ollamaLayer{"application/vnd.ollama.image.projector", "sha256:" + proj.SHA, proj.Size})
		diffIDs = append(diffIDs, "sha256:"+proj.SHA)
		pl.Layers = append(pl.Layers, *proj)
	}
	if quant == "" {
		quant = "unknown"
	}
	cfg := map[string]any{
		"model_format":   "gguf",
		"model_family":   arch,
		"model_families": []string{arch},
		"model_type":     humanParams(params),
		"file_type":      quant,
		"architecture":   "amd64",
		"os":             "linux",
		"rootfs":         map[string]any{"type": "layers", "diff_ids": diffIDs},
	}
	pl.Config, _ = json.Marshal(cfg)
	sum := sha256.Sum256(pl.Config)
	pl.ConfigDigest = hex.EncodeToString(sum[:])
	man := ollamaManifest{
		SchemaVersion: 2,
		MediaType:     "application/vnd.docker.distribution.manifest.v2+json",
		Config:        ollamaLayer{"application/vnd.docker.container.image.v1+json", "sha256:" + pl.ConfigDigest, int64(len(pl.Config))},
		Layers:        layers,
	}
	pl.Manifest, _ = json.Marshal(man)
	return pl, nil
}

func (c *HFCache) buildOllama(files map[string][]HubFile, opts ViewsOptions, rep *ViewsReport) error {
	root := filepath.Join(opts.Root, "ollama")
	blobs := filepath.Join(root, "blobs")
	if err := os.MkdirAll(blobs, 0775); err != nil {
		return err
	}
	os.MkdirAll(filepath.Join(root, "manifests"), 0775)
	ms := loadManaged(root)
	for _, m := range planOllama(files, &rep.Skipped) {
		if err := writeOllamaModel(root, m, ms); err != nil {
			rep.Skipped = append(rep.Skipped, fmt.Sprintf("ollama: %s:%s: %v", m.Repo, m.Tag, err))
			continue
		}
		rep.OllamaModels++
	}
	n, err := ms.finish()
	rep.Removed += n
	return err
}

func writeOllamaModel(root string, m ollamaPlanned, ms *managedSet) error {
	blobs := filepath.Join(root, "blobs")
	for _, s := range m.Layers {
		link := filepath.Join(blobs, "sha256-"+s.SHA)
		if ok, err := ensureLink(link, s.Blob); err != nil {
			return err
		} else if ok {
			ms.now[link] = true
		}
	}
	cpath := filepath.Join(blobs, "sha256-"+m.ConfigDigest)
	if _, err := os.Stat(cpath); err != nil {
		if err := os.WriteFile(cpath, m.Config, 0644); err != nil {
			return err
		}
	}
	ms.now[cpath] = true
	mpath := filepath.Join(root, "manifests", "hf.co", filepath.FromSlash(m.Repo), m.Tag)
	if fi, err := os.Lstat(mpath); err == nil && !ms.old[mpath] && fi.Mode().IsRegular() {
		return nil // a real `ollama pull` manifest: leave it alone
	}
	if cur, err := os.ReadFile(mpath); err != nil || string(cur) != string(m.Manifest) {
		if err := os.MkdirAll(filepath.Dir(mpath), 0775); err != nil {
			return err
		}
		if err := os.WriteFile(mpath, m.Manifest, 0664); err != nil {
			return err
		}
	}
	ms.now[mpath] = true
	return nil
}

// ---- lmstudio ---------------------------------------------------------------

func buildLMStudio(files map[string][]HubFile, opts ViewsOptions, rep *ViewsReport) error {
	root := filepath.Join(opts.Root, "lmstudio")
	if err := os.MkdirAll(root, 0755); err != nil {
		return err
	}
	ms := loadManaged(root)
	for repo, fs := range files {
		for _, f := range fs {
			if !strings.HasSuffix(strings.ToLower(f.Path), ".gguf") {
				continue
			}
			if meta, err := ReadGGUFMeta(f.Blob); err == nil && diffusionArch[meta.Architecture] {
				continue // ComfyUI/Maestro material; LM Studio would list it as an LLM
			}
			// LM Studio indexes exactly <owner>/<repo>/<file>; flatten subdirs
			// (quant folders, split_files/...) so every file is found.
			link := filepath.Join(root, filepath.FromSlash(repo), filepath.Base(f.Path))
			ok, err := ensureLink(link, f.Snapshot)
			if err != nil {
				return err
			}
			if ok {
				ms.now[link] = true
				rep.LMStudioFiles++
			}
		}
	}
	n, err := ms.finish()
	rep.Removed += n
	return err
}

// ---- hipfire ----------------------------------------------------------------

func buildHipfire(files map[string][]HubFile, opts ViewsOptions, rep *ViewsReport) error {
	root := filepath.Join(opts.Root, "hipfire")
	if err := os.MkdirAll(root, 0755); err != nil {
		return err
	}
	ms := loadManaged(root)
	for repo, fs := range files {
		if !strings.HasPrefix(repo, "hipfire-models/") {
			continue
		}
		for _, f := range fs {
			b := strings.ToLower(filepath.Base(f.Path))
			if b == "readme.md" || b == ".gitattributes" || strings.HasSuffix(b, ".json") {
				continue
			}
			// hipfire lists regular files only; a hardlink costs no space.
			link := filepath.Join(root, filepath.Base(f.Path))
			ok, err := ensureHardlink(link, f.Blob)
			if err != nil {
				return err
			}
			if ok {
				ms.now[link] = true
				rep.HipfireFiles++
			}
		}
	}
	n, err := ms.finish()
	rep.Removed += n
	return err
}

// ---- comfyui <- maestro ------------------------------------------------------

// ComfyCategory maps a Maestro checkpoint (path relative to ckpts/) to a
// ComfyUI models/ category, or "" when ComfyUI has no use for it
// (preprocessor weights, tokenizer/config files).
func ComfyCategory(rel string) string {
	l := strings.ToLower(rel)
	ext := strings.ToLower(filepath.Ext(l))
	if ext != ".safetensors" && ext != ".gguf" && ext != ".sft" {
		return ""
	}
	dir := strings.ToLower(filepath.ToSlash(filepath.Dir(rel)))
	base := filepath.Base(l)
	switch {
	case strings.HasPrefix(dir, "llm"):
		return "" // chat LLMs: served by the LLM views
	case strings.Contains(base, "lora"):
		return "loras"
	case strings.Contains(base, "upscaler"):
		return "latent_upscale_models"
	case strings.Contains(base, "vision"):
		return "clip_vision"
	case strings.Contains(base, "vae") || strings.Contains(base, "vocoder"):
		return "vae"
	case strings.Contains(base, "embeddings_connector") || strings.Contains(base, "text_embedding_projection"):
		return "text_encoders"
	case dir != ".":
		top := strings.SplitN(dir, "/", 2)[0]
		for _, p := range []string{"qwen", "gemma", "t5", "clip", "llama", "umt5"} {
			if strings.HasPrefix(top, p) {
				return "text_encoders"
			}
		}
		if strings.HasPrefix(top, "minimax_h3") && strings.Contains(base, "qwen3vl") {
			return "text_encoders"
		}
		return ""
	}
	return "diffusion_models"
}

func syncComfyFromMaestro(opts ViewsOptions, rep *ViewsReport) error {
	ms := loadManaged(opts.DiffusionDir)
	err := filepath.Walk(opts.MaestroDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() && (info.Name() == ".cache" || info.Name() == "bin") {
			return filepath.SkipDir
		}
		if info.Mode()&os.ModeSymlink == 0 && !info.Mode().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(opts.MaestroDir, p)
		cat := ComfyCategory(rel)
		if cat == "" {
			return nil
		}
		target, err := filepath.EvalSymlinks(p)
		if err != nil {
			return nil
		}
		// Link to the snapshot path when Maestro's entry is a hub link, so
		// the ComfyUI entry shows where it came from.
		if info.Mode()&os.ModeSymlink != 0 {
			if t, err := os.Readlink(p); err == nil && filepath.IsAbs(t) {
				target = t
			}
		}
		name := filepath.Base(p)
		if g := strings.ToLower(name); g == "model.safetensors" || g == "diffusion_pytorch_model.safetensors" || strings.HasPrefix(g, "model-0") {
			name = filepath.Base(filepath.Dir(p)) + "_" + name
		}
		link := filepath.Join(opts.DiffusionDir, cat, name)
		if existing, err := filepath.EvalSymlinks(link); err == nil {
			if a, e1 := os.Stat(existing); e1 == nil {
				if b, e2 := os.Stat(target); e2 == nil && os.SameFile(a, b) {
					if fi, _ := os.Lstat(link); fi != nil && fi.Mode()&os.ModeSymlink != 0 && ms.old[link] {
						ms.now[link] = true
					}
					rep.ComfyLinks++
					return nil
				}
			}
		}
		ok, err := ensureLink(link, target)
		if err != nil {
			return err
		}
		if ok {
			ms.now[link] = true
			rep.ComfyLinks++
		}
		return nil
	})
	if err != nil {
		return err
	}
	n, err := ms.finish()
	rep.Removed += n
	return err
}
