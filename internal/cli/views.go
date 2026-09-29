// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/bodaay/HuggingFaceModelDownloader/pkg/hfdownloader"
)

type viewsFlags struct {
	cacheDir, root, diffusion, maestro string
}

func (v *viewsFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&v.cacheDir, "cache-dir", "", "Hugging Face cache directory (default: config cache-dir, HF_HOME, ~/.cache/huggingface)")
	cmd.Flags().StringVar(&v.root, "views", "", "Views root (default: <cache-dir>/../views)")
	cmd.Flags().StringVar(&v.diffusion, "diffusion", "", "ComfyUI models dir to link diffusion files into (default: <cache-dir>/../diffusion if it exists)")
	cmd.Flags().StringVar(&v.maestro, "maestro", "", "Maestro/Wan2GP flat ckpts dir to ingest and classify for ComfyUI")
}

func (v *viewsFlags) resolve() (*hfdownloader.HFCache, hfdownloader.ViewsOptions) {
	cache := hfdownloader.NewHFCache(resolveCacheDir(v.cacheDir), hfdownloader.DefaultStaleTimeout)
	parent := filepath.Dir(filepath.Clean(cache.Root))
	opts := hfdownloader.ViewsOptions{Root: v.root, DiffusionDir: v.diffusion, MaestroDir: v.maestro}
	if opts.Root == "" {
		opts.Root = filepath.Join(parent, "views")
	}
	if opts.DiffusionDir == "" {
		if fi, err := os.Stat(filepath.Join(parent, "diffusion")); err == nil && fi.IsDir() {
			opts.DiffusionDir = filepath.Join(parent, "diffusion")
		}
	}
	return cache, opts
}

func newViewsCmd(ro *RootOpts) *cobra.Command {
	var vf viewsFlags
	cmd := &cobra.Command{
		Use:   "views",
		Short: "Build per-app symlink views (ollama, lmstudio, hipfire, comfyui) from the hub cache",
		Long: `Views rebuilds per-application directory layouts on top of the hub cache.
No bytes are copied: every entry is a symlink into hub/.../snapshots or blobs.

  <views>/ollama/     point OLLAMA_MODELS here. Every GGUF quant of every repo
                      is "hf.co/<owner>/<repo>:<QUANT>" (mmproj bundled).
  <views>/lmstudio/   point LM Studio's models folder here (<owner>/<repo>/*.gguf)
  <views>/hipfire/    point HIPFIRE_MODELS_DIR here (hipfire-models/* repos)
  <diffusion>/<cat>/  ComfyUI models dir, fed from the Maestro ckpts view

Only links the builder made are removed on rebuild. Real files you drop into
a view are left alone; run 'hfdownloader watch' to adopt them automatically.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cache, opts := vf.resolve()
			if !ro.Quiet {
				opts.Log = func(f string, a ...any) { fmt.Fprintf(os.Stderr, "  "+f+"\n", a...) }
			}
			rep, err := cache.BuildViews(opts)
			if ro.JSONOut && rep != nil {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				enc.Encode(rep)
				return err
			}
			if rep != nil {
				fmt.Printf("views: %s\n  ollama models: %d\n  lmstudio files: %d\n  hipfire files: %d\n  comfyui links: %d\n  stale removed: %d\n",
					opts.Root, rep.OllamaModels, rep.LMStudioFiles, rep.HipfireFiles, rep.ComfyLinks, rep.Removed)
				for _, s := range rep.Skipped {
					fmt.Printf("  skipped: %s\n", s)
				}
			}
			return err
		},
	}
	vf.bind(cmd)
	return cmd
}
