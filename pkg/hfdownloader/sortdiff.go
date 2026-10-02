// SPDX-License-Identifier: Apache-2.0

package hfdownloader

// SortDiffusion keeps the two stores apart: a finished hub download of a
// diffusion repo has its model blobs moved into the ComfyUI tree
// (<diffusion>/<category>/<file>) and each hub blob becomes a symlink to the
// moved file, so the HF snapshot keeps working. Same filesystem: renames only.

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// SortMove is one planned or done blob move.
type SortMove struct {
	Repo string `json:"repo"`
	Blob string `json:"blob"`
	Dest string `json:"dest"`
	Size int64  `json:"size"`
	Note string `json:"note,omitempty"`
}

var comfyDirs = map[string]string{
	"diffusion_models": "diffusion_models", "unet": "diffusion_models", "checkpoints": "checkpoints",
	"vae": "vae", "text_encoders": "text_encoders", "clip": "text_encoders", "loras": "loras",
	"clip_vision": "clip_vision", "latent_upscale_models": "latent_upscale_models",
	"upscale_models": "upscale_models", "model_patches": "model_patches",
}

var sortModelExt = map[string]bool{
	".safetensors": true, ".gguf": true, ".sft": true, ".pth": true, ".pt": true,
	".ckpt": true, ".bin": true, ".onnx": true, ".pkl": true,
}

// readmeSaysDiffusion reads the model card front matter of a snapshot.
func readmeSaysDiffusion(snap string) bool {
	f, err := os.Open(filepath.Join(snap, "README.md"))
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<16), 1<<20)
	if !sc.Scan() || strings.TrimSpace(sc.Text()) != "---" {
		return false
	}
	for n := 0; sc.Scan() && n < 400; n++ {
		l := strings.ToLower(strings.TrimSpace(sc.Text()))
		if l == "---" {
			break
		}
		l = strings.Trim(strings.TrimPrefix(l, "- "), `"' `)
		for _, k := range []string{
			"pipeline_tag: text-to-image", "pipeline_tag: image-to-image", "pipeline_tag: text-to-video",
			"pipeline_tag: image-to-video", "pipeline_tag: video-to-video", "library_name: diffusers",
			"diffusers", "comfyui", "diffusion-single-file", "text-to-image", "text-to-video", "image-to-video",
		} {
			if l == k {
				return true
			}
		}
	}
	return false
}

// repoIsDiffusion decides from local data only. A GGUF header wins over the
// model card: an LLM-architecture GGUF repo is never diffusion.
func repoIsDiffusion(snap string, files []HubFile) bool {
	for _, f := range files {
		if strings.HasSuffix(strings.ToLower(f.Path), ".gguf") && !isMMProj(f.Path) {
			if m, err := readGGUFMetaCached(f.Blob); err == nil {
				return diffusionArch[m.Architecture]
			}
		}
	}
	return readmeSaysDiffusion(snap)
}

func repoDownloadBusy(blobs string) bool {
	ents, err := os.ReadDir(blobs)
	if err != nil {
		return true
	}
	for _, e := range ents {
		n := e.Name()
		if strings.HasPrefix(n, "tmp-") || strings.HasSuffix(n, ".incomplete") || strings.HasSuffix(n, ".part") {
			return true
		}
	}
	return false
}

// SortDiffusion moves the model blobs of finished diffusion repos into
// diffusionDir. dryRun only plans.
func (c *HFCache) SortDiffusion(diffusionDir string, dryRun bool, logf func(string, ...any)) ([]SortMove, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	files, err := c.SnapshotFiles()
	if err != nil {
		return nil, err
	}
	diffReal := diffusionDir
	if r, err := filepath.EvalSymlinks(diffusionDir); err == nil {
		diffReal = r
	}
	var moves []SortMove
	for repo, fs := range files {
		rd, err := c.Repo(repo, RepoTypeModel)
		if err != nil || len(fs) == 0 {
			continue
		}
		if ref, _ := rd.ReadRef("main"); ref == "" || repoDownloadBusy(rd.BlobsDir()) {
			continue
		}
		var todo []HubFile
		for _, f := range fs {
			if !sortModelExt[strings.ToLower(filepath.Ext(f.Path))] || f.Size < 1<<20 || strings.HasPrefix(f.Blob, diffReal+string(filepath.Separator)) {
				continue
			}
			todo = append(todo, f)
		}
		if len(todo) == 0 {
			continue
		}
		snap := filepath.Dir(fs[0].Snapshot)
		for d := filepath.Dir(fs[0].Path); d != "." && d != "/"; d = filepath.Dir(d) {
			snap = filepath.Dir(snap)
		}
		if !repoIsDiffusion(snap, fs) {
			continue
		}
		owner := strings.Replace(repo, "/", "--", 1)
		for _, f := range todo {
			cat := ""
			parts := strings.Split(f.Path, "/")
			for _, p := range parts[:len(parts)-1] {
				if v, ok := comfyDirs[strings.ToLower(p)]; ok {
					cat = v
					break
				}
			}
			if cat == "" {
				cat = ComfyCategory(filepath.Base(f.Path))
			}
			dest := filepath.Join(diffusionDir, "maestro", owner, filepath.FromSlash(f.Path))
			if cat != "" {
				dest = filepath.Join(diffusionDir, cat, filepath.Base(f.Path))
				if _, err := os.Lstat(dest); err == nil {
					dest = filepath.Join(diffusionDir, "maestro", owner, filepath.FromSlash(f.Path))
				}
			}
			mv := SortMove{Repo: repo, Blob: f.Blob, Dest: dest, Size: f.Size}
			if _, err := os.Lstat(dest); err == nil {
				mv.Note = "destination exists, left in hub"
				moves = append(moves, mv)
				continue
			}
			if !dryRun {
				if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
					mv.Note = err.Error()
				} else if err := os.Rename(f.Blob, dest); err != nil {
					mv.Note = err.Error()
				} else if err := os.Symlink(dest, f.Blob); err != nil {
					os.Rename(dest, f.Blob) // put it back: never leave the hub without the name
					mv.Note = err.Error()
				} else {
					logf("sorted %s:%s -> %s", repo, f.Path, dest)
				}
			}
			moves = append(moves, mv)
		}
	}
	return moves, nil
}
