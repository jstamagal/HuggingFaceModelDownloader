// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bodaay/HuggingFaceModelDownloader/pkg/hfdownloader"
)

func newAdoptCmd(ro *RootOpts) *cobra.Command {
	var (
		cacheDir  string
		repos     []string
		mode      string
		dryRun    bool
		noFetch   bool
		leaveLink bool
		history   int
		jobs      int
		endpoint  string
		reposFile string
		search    bool
	)
	cmd := &cobra.Command{
		Use:   "adopt DIR [FILE...]",
		Short: "Move already-downloaded files into the hub cache (no re-download)",
		Long: `Adopt turns files that are already on disk into real Hugging Face cache
entries: hub/models--owner--repo/{blobs,snapshots,refs}. Nothing is downloaded
except small metadata files (README, configs, tokenizer) of the matched commit.

Every local file is hashed and matched by CONTENT against the repo tree on the
Hub (LFS sha256 for weights, git blob sha1 for small files). A file is only
moved once the Hub vouches for its exact bytes, so:
  - renamed files (flat app checkpoint dirs) land at their real repo path
  - truncated/corrupt files are left alone and reported "unmatched"
  - files from an older revision are found by walking commit history

Sources this covers:
  hf download --local-dir, LM Studio models/<owner>/<repo>/, llama.cpp -hf
  downloads, app ckpts/ dumps (pass every candidate --repo).

If --repo is omitted, it is inferred from DIR's last two path components
(owner/repo), which is how LM Studio and --local-dir lay things out.

Examples:
  hfdownloader adopt ~/.lmstudio/models/bartowski/Foo-GGUF --dry-run
  hfdownloader adopt ./ckpts -r DeepBeepMeep/Wan2.1 -r DeepBeepMeep/LTX-2 --leave-link`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cache := hfdownloader.NewHFCache(resolveCacheDir(cacheDir), hfdownloader.DefaultStaleTimeout)
			if reposFile != "" {
				b, err := os.ReadFile(reposFile)
				if err != nil {
					return err
				}
				for _, l := range strings.Split(string(b), "\n") {
					if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
						repos = append(repos, l)
					}
				}
			}
			token := resolveHubToken(ro, cache.Root)
			endpoint = resolveHubEndpoint(endpoint)
			if search {
				repos = append(repos, searchCandidates(cmd, args, token, endpoint)...)
			}
			if len(repos) == 0 {
				id, ok := hfdownloader.InferRepoFromPath(args[0])
				if !ok {
					return fmt.Errorf("cannot infer owner/repo from %q; pass --repo or --search", args[0])
				}
				repos = []string{id}
			}
			opts := hfdownloader.AdoptOptions{
				Repos: repos, Dir: args[0], Files: args[1:],
				Mode: hfdownloader.AdoptMode(mode), DryRun: dryRun, NoFetch: noFetch,
				LeaveLink: leaveLink, HistoryDepth: history, Jobs: jobs,
				Token: token, Endpoint: endpoint,
			}
			if !ro.Quiet {
				opts.Log = func(f string, a ...any) { fmt.Fprintf(os.Stderr, "  "+f+"\n", a...) }
			}
			res, err := cache.Adopt(cmd.Context(), opts)
			if err != nil {
				return err
			}
			if ro.JSONOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(res)
			}
			counts := map[string]int{}
			for _, f := range res.Files {
				counts[f.Status]++
				if ro.Quiet && f.Status != "error" && f.Status != "unmatched" {
					continue
				}
				switch f.Status {
				case "adopted", "duplicate", "would-adopt":
					fmt.Printf("%-11s %s -> %s@%s:%s\n", f.Status, f.Local, f.Repo, short(f.Commit), f.RepoPath)
				default:
					note := ""
					if f.Note != "" {
						note = "  (" + f.Note + ")"
					}
					fmt.Printf("%-11s %s%s\n", f.Status, f.Local, note)
				}
			}
			for _, r := range res.Repos {
				head := "HEAD"
				if !r.IsHead {
					head = "older revision"
				}
				fmt.Printf("\n%s @ %s (%s): %d files, %s", r.Repo, short(r.Commit), head, r.Files, humanSize(r.Bytes))
				if len(r.Fetched) > 0 {
					verb := "fetched"
					if dryRun {
						verb = "would fetch"
					}
					fmt.Printf(", %s %d small files", verb, len(r.Fetched))
				}
				fmt.Println()
			}
			fmt.Printf("\n")
			for _, k := range []string{"adopted", "duplicate", "would-adopt", "unmatched", "skipped", "error"} {
				if counts[k] > 0 {
					fmt.Printf("%s=%d ", k, counts[k])
				}
			}
			fmt.Println()
			if counts["error"] > 0 {
				return fmt.Errorf("%d file(s) failed", counts["error"])
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&cacheDir, "cache-dir", "", "Hugging Face cache directory (default: config cache-dir, HF_HOME, ~/.cache/huggingface)")
	cmd.Flags().StringArrayVarP(&repos, "repo", "r", nil, "Candidate repo owner/name (repeatable). Default: inferred from DIR")
	cmd.Flags().StringVar(&reposFile, "repos-file", "", "File with candidate repo IDs, one per line (# comments ok)")
	cmd.Flags().StringVar(&mode, "mode", "move", "How to place verified files into blobs/: move, hardlink, copy")
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "Hash and match only; change nothing")
	cmd.Flags().BoolVar(&noFetch, "no-fetch", false, "Don't download the small non-LFS files of the matched commit")
	cmd.Flags().BoolVar(&leaveLink, "leave-link", false, "After a move, leave a symlink at the original path pointing into the cache")
	cmd.Flags().IntVar(&history, "history", 50, "Commits of history to search for files not in HEAD (0 = HEAD only)")
	cmd.Flags().IntVarP(&jobs, "jobs", "j", 4, "Parallel hashers")
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "Hub endpoint (default https://huggingface.co)")
	cmd.Flags().BoolVarP(&search, "search", "s", false, "Find candidate repos by Hub search on the dir and weight file names (adds to --repo)")
	return cmd
}

// searchCandidates runs Hub search on DIR's name, owner/name path guess and
// every weight file's name. Wrong candidates cost only API calls: adoption
// still matches by content.
func searchCandidates(cmd *cobra.Command, args []string, token, endpoint string) []string {
	var names []string
	if id, ok := hfdownloader.InferRepoFromPath(args[0]); ok {
		names = append(names, filepath.Base(id))
	}
	files := args[1:]
	if len(files) == 0 {
		filepath.WalkDir(args[0], func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() {
				files = append(files, p)
			}
			return nil
		})
	}
	for _, f := range files {
		switch strings.ToLower(filepath.Ext(f)) {
		case ".gguf", ".safetensors", ".sft", ".bin", ".pt", ".pth", ".ckpt", ".onnx", ".mq4":
			names = append(names, filepath.Base(f))
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		ids, err := hfdownloader.GuessRepos(cmd.Context(), n, token, endpoint)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  search %s: %v\n", n, err)
		}
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	fmt.Fprintf(os.Stderr, "  search: %d candidate repos from %d names\n", len(out), len(names))
	return out
}

// resolveHubToken: --token > HF_TOKEN > config token > <cache>/token.
func resolveHubToken(ro *RootOpts, cacheRoot string) string {
	if t := strings.TrimSpace(ro.Token); t != "" {
		return t
	}
	if t := strings.TrimSpace(os.Getenv("HF_TOKEN")); t != "" {
		return t
	}
	if cfg := loadConfigMap(); cfg != nil {
		if v, ok := cfg["token"].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	if b, err := os.ReadFile(cacheRoot + "/token"); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}

func resolveHubEndpoint(endpoint string) string {
	if endpoint != "" {
		return endpoint
	}
	if cfg := loadConfigMap(); cfg != nil {
		if v, ok := cfg["endpoint"].(string); ok {
			return v
		}
	}
	return ""
}

func short(c string) string {
	if len(c) > 8 {
		return c[:8]
	}
	return c
}
