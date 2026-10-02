// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

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
	cmd.Flags().StringVar(&v.maestro, "maestro", "", "Maestro/Wan2GP flat ckpts dir to ingest and classify for ComfyUI (default: <views>/maestro if it exists)")
}

func (v *viewsFlags) resolve() (*hfdownloader.HFCache, hfdownloader.ViewsOptions) {
	cache := hfdownloader.NewHFCache(resolveCacheDir(v.cacheDir), hfdownloader.DefaultStaleTimeout)
	root := filepath.Clean(cache.Root)
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r // ~/.cache/huggingface -> /srv/models/huggingface: views sit beside the real store
	}
	parent := filepath.Dir(root)
	opts := hfdownloader.ViewsOptions{Root: v.root, DiffusionDir: v.diffusion, MaestroDir: v.maestro}
	if opts.Root == "" {
		opts.Root = filepath.Join(parent, "views")
	}
	if opts.DiffusionDir == "" {
		if fi, err := os.Stat(filepath.Join(parent, "diffusion")); err == nil && fi.IsDir() {
			opts.DiffusionDir = filepath.Join(parent, "diffusion")
		}
	}
	if opts.MaestroDir == "" {
		if fi, err := os.Stat(filepath.Join(opts.Root, "maestro")); err == nil && fi.IsDir() {
			opts.MaestroDir = filepath.Join(opts.Root, "maestro")
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
  <views>/hipfire/    point HIPFIRE_MODELS_DIR here (hipfire-models/* repos, hardlinks)
  <views>/maestro/    Maestro/Wan2GP flat ckpts (symlink app/ckpts here)
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

func newWatchCmd(ro *RootOpts) *cobra.Command {
	var vf viewsFlags
	var reposFile, endpoint string
	var settle, every time.Duration
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Keep views current and adopt model files dropped into any view",
		Long: `Watch rebuilds the views whenever the hub changes and ingests real files
that appear inside a view:

  <views>/lmstudio/<o>/<r>/x.gguf   adopted into hub as o/r, replaced by a link
  <views>/ollama (ollama pull hf.co/...)   pulled layers moved into the hub
  <views>/inbox/, <views>/hipfire/  repo found by Hub search, verified by hash
  --maestro dir, <diffusion>/<cat>/ adopted, a symlink left at the same path

Nothing is moved unless the Hub vouches for the exact bytes. Unmatched files
(civitai downloads, private merges) are left where they are.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cache, vopts := vf.resolve()
			opts := hfdownloader.WatchOptions{
				Views: vopts, Settle: settle, Rebuild: every,
				Token: resolveHubToken(ro, cache.Root), Endpoint: resolveHubEndpoint(endpoint),
				Log: func(f string, a ...any) {
					fmt.Fprintf(os.Stderr, "%s "+f+"\n", append([]any{time.Now().Format("15:04:05")}, a...)...)
				},
			}
			if reposFile != "" {
				b, err := os.ReadFile(reposFile)
				if err != nil {
					return err
				}
				for _, l := range strings.Split(string(b), "\n") {
					if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
						opts.MaestroRepos = append(opts.MaestroRepos, l)
					}
				}
			}
			return cache.Watch(cmd.Context(), opts)
		},
	}
	vf.bind(cmd)
	cmd.Flags().StringVar(&reposFile, "maestro-repos", "", "Candidate repo list for files Maestro downloads (one owner/name per line)")
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "Hub endpoint")
	cmd.Flags().DurationVar(&settle, "settle", 10*time.Second, "A new file must be unchanged this long before it is ingested")
	cmd.Flags().DurationVar(&every, "rebuild-every", 5*time.Minute, "Periodic full views rebuild")
	return cmd
}

func newMountCmd(ro *RootOpts) *cobra.Command {
	var vf viewsFlags
	var upper, reposFile, endpoint string
	var allowOther, debug bool
	var settle time.Duration
	cmd := &cobra.Command{
		Use:   "mount MOUNTPOINT",
		Short: "FUSE mount showing every app its own layout of the model stores",
		Long: `Mount serves one filesystem with a directory per app, computed live from
the hub (<cache-dir>/hub), your own files (<cache-dir>/local/<owner>/<repo>/)
and the ComfyUI store (--diffusion):

  ollama/     OLLAMA_MODELS. hf.co/<o>/<r>:<QUANT> and local/<o>/<r>:<QUANT>
  lmstudio/   LM Studio models folder (<owner>/<repo>/*.gguf)
  hipfire/    HIPFIRE_MODELS_DIR (hipfire-models/* repos)
  maestro/    Maestro/Wan2GP ckpts (writable layer)
  inbox/      drop anything here; the Hub is searched for it

Files written into a view (a download, an mv, ollama pull) land in the upper
dir and, once closed and settled, are verified against the Hub and moved into
the right store. Finished diffusion downloads in the hub are moved into the
ComfyUI store. Unmatched files stay in the upper dir and stay visible.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cache, vopts := vf.resolve()
			if upper == "" {
				upper = filepath.Join(filepath.Dir(vopts.Root), ".upper")
			}
			logf := func(f string, a ...any) {
				fmt.Fprintf(os.Stderr, "%s "+f+"\n", append([]any{time.Now().Format("15:04:05")}, a...)...)
			}
			opts := hfdownloader.MountOptions{
				MountPoint: args[0], UpperDir: upper, DiffusionDir: vopts.DiffusionDir,
				AllowOther: allowOther, Debug: debug,
				Watch: hfdownloader.WatchOptions{
					Settle: settle, Token: resolveHubToken(ro, cache.Root),
					Endpoint: resolveHubEndpoint(endpoint), Log: logf,
				},
			}
			if reposFile == "" {
				if h, err := os.UserConfigDir(); err == nil {
					if _, err := os.Stat(filepath.Join(h, "hfdownloader", "maestro_repos.txt")); err == nil {
						reposFile = filepath.Join(h, "hfdownloader", "maestro_repos.txt")
					}
				}
			}
			if reposFile != "" {
				b, err := os.ReadFile(reposFile)
				if err != nil {
					return err
				}
				for _, l := range strings.Split(string(b), "\n") {
					if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
						opts.Watch.MaestroRepos = append(opts.Watch.MaestroRepos, l)
					}
				}
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return cache.Mount(ctx, opts)
		},
	}
	vf.bind(cmd)
	cmd.Flags().StringVar(&upper, "upper", "", "Writable layer (default: <cache-dir>/../.upper)")
	cmd.Flags().BoolVar(&allowOther, "allow-other", false, "Let other users (system ollama) use the mount; needs user_allow_other in /etc/fuse.conf")
	cmd.Flags().BoolVar(&debug, "debug", false, "Log every FUSE request")
	cmd.Flags().StringVar(&reposFile, "maestro-repos", "", "Candidate repo list for files Maestro downloads (default: ~/.config/hfdownloader/maestro_repos.txt)")
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "Hub endpoint")
	cmd.Flags().DurationVar(&settle, "settle", 10*time.Second, "A written file must be closed and unchanged this long before ingest")
	return cmd
}
