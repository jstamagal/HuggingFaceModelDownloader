# HFDownloader CLI Reference

Complete command-line reference for `hfdownloader`.

---

## Table of Contents

- [Installation](#installation)
- [Quick Reference](#quick-reference)
- [Global Flags](#global-flags)
- [Commands](#commands)
  - [search](#search)
  - [download](#download)
  - [serve](#serve)
  - [analyze](#analyze)
  - [list](#list)
  - [cache](#cache)
  - [info](#info)
  - [rebuild](#rebuild)
  - [mirror](#mirror)
  - [proxy](#proxy)
  - [config](#config)
  - [version](#version)
- [Environment Variables](#environment-variables)
- [Configuration File](#configuration-file)
- [Examples](#examples)

---

## Installation

```bash
# One-liner install (Linux/macOS/WSL)
bash <(curl -sSL https://g.bodaay.io/hfd) -i

# Or build from source
# Requires Go 1.25+
git clone https://github.com/bodaay/HuggingFaceModelDownloader
cd HuggingFaceModelDownloader
go build -o hfdownloader ./cmd/hfdownloader
```

---

## Quick Reference

```bash
# Download a model
hfdownloader download owner/model

# Download specific quantizations
hfdownloader download TheBloke/Mistral-7B-Instruct-v0.2-GGUF:q4_k_m,q5_k_m

# Download a dataset
hfdownloader download facebook/flores --dataset

# Analyze before downloading
hfdownloader analyze owner/model

# Search and browse models interactively
hfdownloader search llama

# Start web UI
hfdownloader serve

# List downloaded repos
hfdownloader list

# Browse and clean downloaded repos interactively
hfdownloader cache

# Show repo details
hfdownloader info Mistral-7B
```

---

## Global Flags

These flags work with all commands:

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--token` | `-t` | string | `$HF_TOKEN` | HuggingFace access token |
| `--json` | | bool | `false` | Emit machine-readable JSON events |
| `--quiet` | `-q` | bool | `false` | Minimal output |
| `--verbose` | `-v` | bool | `false` | Debug output |
| `--config` | | string | | Path to config file (JSON/YAML) |
| `--log-file` | | string | | Write logs to file |
| `--log-level` | | string | `info` | Log level: debug, info, warn, error |
| `--theme` | | string | `auto` | TUI background theme: auto, light, dark |
| `--color` | | string | `auto` | Color output: auto, always, never |

`--color auto` honors the standard `NO_COLOR` environment variable. If a
remote shell injects `NO_COLOR=1` even though its terminal supports color, use
`--color always` or set `HF_COLOR=always`.

### Authentication

```bash
# Via flag
hfdownloader download meta-llama/Llama-2-7b -t hf_xxxxx

# Via environment variable (recommended)
export HF_TOKEN=hf_xxxxx
hfdownloader download meta-llama/Llama-2-7b
```

---

## Commands

### search

Search, filter, and browse model repositories on the Hugging Face Hub in a
full-screen TUI. Selecting a result analyzes the repository, then opens the
smart download selector when variants or components are available.

```text
hfdownloader search [query] [flags]
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--author` | string | | Filter by author or organization |
| `--pipeline` | string | | Filter by pipeline task |
| `--library` | string | | Filter by library |
| `--gated` | string | `all` | Access filter: `all`, `open`, or `gated` |
| `--sort` | string | `trending` | `trending`, `downloads`, `likes`, `updated`, or `created` |
| `--limit` | int | `50` | Maximum results per search (1-1000) |
| `--endpoint` | string | `https://huggingface.co` | Custom Hub endpoint |

TUI keys:

| Key | Action |
|-----|--------|
| `/` | Focus the search field |
| `Up` / `Down`, `j` / `k` | Move through results |
| `s` / `t` / `l` / `g` | Cycle sort, task, library, and access filters |
| `c` | Copy the interactive analyze command |
| `Enter` | Inspect the selected model |
| `q` | Quit |

Use the global `--json` flag to return search results without starting the TUI.

```bash
hfdownloader search
hfdownloader search llama
hfdownloader search mistral --library transformers --sort downloads
hfdownloader search --pipeline text-to-image --gated open
hfdownloader search llama --json
```

### download

Download models or datasets from HuggingFace Hub.

**This is the default command** — runs when no subcommand is specified.

```
hfdownloader download [REPO] [flags]
hfdownloader [REPO] [flags]              # Same as above
```

#### Repository Selection

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--repo` | `-r` | string | | Repository ID (owner/name) |
| `--dataset` | | bool | `false` | Treat as dataset |
| `--revision` | `-b` | string | `main` | Branch, tag, or commit |
| `--filters` | `-F` | strings | | Comma-separated LFS filters |
| `--exclude` | `-E` | strings | | Patterns to exclude |
| `--exact` | | bool | `false` | Match filters against whole name segments, not substrings (e.g. `-F q6_k` matches `Q6_K` but not `Q6_K_XL`) |
| `--append-filter-subdir` | | bool | `false` | Create subdirs per filter |

#### Performance

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--connections` | `-c` | int | `16` | Connections per file |
| `--max-active` | | int | `3` | Max concurrent downloads |
| `--multipart-threshold` | | string | `32MiB` | Min size for multipart |

#### Reliability

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--retries` | | int | `4` | Retry attempts |
| `--backoff-initial` | | string | `400ms` | Initial retry delay |
| `--backoff-max` | | string | `10s` | Max retry delay |
| `--verify` | | string | `size` | Verification: none, size, etag, sha256 |
| `--stale-timeout` | | string | `5m` | Timeout for stale downloads |

#### Output

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--cache-dir` | | string | `~/.cache/huggingface` | HF cache directory (default layout) |
| `--local-dir` | | string | | Download real files (not HF cache symlinks) into this directory, `huggingface-cli`-style |
| `--endpoint` | | string | `https://huggingface.co` | Custom endpoint (mirrors) |
| `--no-manifest` | | bool | `false` | Don't write hfd.yaml manifest |
| `--no-friendly` | | bool | `false` | Don't create friendly symlinks |
| `--dry-run` | | bool | `false` | Plan only, no download |
| `--plan-format` | | string | `table` | Plan format: table, json |

#### Proxy

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--proxy` | `-x` | string | | Proxy URL (http://, https://, socks5://) |
| `--proxy-user` | | string | | Proxy authentication username |
| `--proxy-pass` | | string | | Proxy authentication password |
| `--no-env-proxy` | | bool | `false` | Ignore HTTP_PROXY/HTTPS_PROXY env vars |

#### Flat-file output (v2.x compatibility)

Use these when you want real files at a user-specified path instead of the
HF cache's blobs + symlink layout. `--local-dir` is the preferred, non-legacy
name; `--legacy -o <dir>` continues to work for v2.x users and is not going
away.

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--local-dir` | | string | | Flat-file mode; download real files into this directory |
| `--legacy` | | bool | `false` | Enable v2.x flat directory structure (defaults to `Models/` or `Datasets/`) |
| `--output` | `-o` | string | | Output directory for `--legacy` mode |

`--local-dir <path>` and `--legacy -o <path>` are equivalent. They are
mutually exclusive on a single command line.

#### Examples

```bash
# Basic download
hfdownloader download TheBloke/Mistral-7B-Instruct-v0.2-GGUF

# Filter syntax in repo name
hfdownloader download TheBloke/Mistral-7B-Instruct-v0.2-GGUF:q4_k_m,q5_k_m

# Or use --filters flag
hfdownloader download TheBloke/Mistral-7B-Instruct-v0.2-GGUF -F q4_k_m,q5_k_m

# Exclude files
hfdownloader download owner/repo -E ".md,.txt,fp16"

# Specific branch
hfdownloader download CompVis/stable-diffusion-v1-4 -b fp16

# Download dataset
hfdownloader download facebook/flores --dataset

# High-speed download
hfdownloader download owner/repo -c 16 --max-active 4

# Dry run (preview files)
hfdownloader download owner/repo --dry-run
hfdownloader download owner/repo --dry-run --plan-format json

# Use mirror (e.g., for China)
hfdownloader download owner/repo --endpoint https://hf-mirror.com

# Private/gated models
hfdownloader download meta-llama/Llama-2-7b -t hf_xxxxx

# Strict verification
hfdownloader download owner/repo --verify sha256

# Download via proxy
hfdownloader download owner/repo --proxy http://proxy:8080

# Download via authenticated SOCKS5 proxy
hfdownloader download owner/repo --proxy socks5://localhost:1080 \
  --proxy-user myuser --proxy-pass mypassword

# Put real files (not HF cache symlinks) in a directory of your choice
hfdownloader download owner/repo --local-dir ./my-model
# Equivalent v2.x form (still supported)
hfdownloader download owner/repo --legacy -o ./my-model
```

---

### serve

Start HTTP server with Web UI, REST API, and WebSocket support.

```
hfdownloader serve [flags]
```

#### Flags

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--addr` | | string | `0.0.0.0` | Bind address |
| `--port` | `-p` | int | `8080` | Port |
| `--cache-dir` | | string | `~/.cache/huggingface` | Cache directory |
| `--connections` | `-c` | int | `16` | Connections per file |
| `--max-active` | | int | `3` | Max concurrent downloads |
| `--multipart-threshold` | | string | `32MiB` | Min size for multipart |
| `--verify` | | string | `size` | Verification mode |
| `--retries` | | int | `4` | Retry attempts |
| `--endpoint` | | string | | Custom HF endpoint |
| `--auth-user` | | string | | Basic auth username |
| `--auth-pass` | | string | | Basic auth password |
| `--models-dir` | | string | `./Models` | Legacy models directory |
| `--datasets-dir` | | string | `./Datasets` | Legacy datasets directory |

#### Examples

```bash
# Start with defaults (port 8080)
hfdownloader serve

# Custom port
hfdownloader serve --port 3000

# With authentication
hfdownloader serve --auth-user admin --auth-pass secret123

# With HuggingFace token
hfdownloader serve -t hf_xxxxx

# Use mirror
hfdownloader serve --endpoint https://hf-mirror.com

# High-performance settings
hfdownloader serve -c 16 --max-active 8
```

#### Server Features

- **Web UI** at `http://localhost:8080`
  - Analyze page: auto-detect model type, show files/sizes
  - Jobs page: real-time download progress
  - Cache browser: view downloaded repos
- **REST API** at `/api/*` endpoints
- **WebSocket** at `/api/ws` for live updates

---

### analyze

Analyze a HuggingFace repository without downloading. Auto-detects whether it's a model or dataset.

```
hfdownloader analyze <repo> [flags]
```

#### Flags

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--interactive` | `-i` | bool | `false` | Launch interactive TUI to pick files/quantizations to download |
| `--endpoint` | | string | | Custom HF endpoint |
| `--format` | | string | `text` | Output: text, json |

#### Auto-Detection

- Tries model API first, falls back to dataset API
- If repo exists as both, prompts you to choose

#### Detected Types

| Type | Detection |
|------|-----------|
| GGUF | `.gguf` files |
| Transformers | `config.json` with model architecture |
| Diffusers | `model_index.json` |
| LoRA | `adapter_config.json` |
| GPTQ | `quantize_config.json` |
| AWQ | AWQ config in `config.json` |
| ONNX | `.onnx` files or `onnx/` directory |
| Audio | Audio-specific configs |
| Vision | Vision-specific configs |
| Multimodal | Multiple modality configs |
| Dataset | Dataset configs |

#### Examples

```bash
# Analyze model
hfdownloader analyze TheBloke/Mistral-7B-Instruct-v0.2-GGUF

# Analyze dataset
hfdownloader analyze facebook/flores

# JSON output
hfdownloader analyze owner/repo --format json

# Use mirror
hfdownloader analyze owner/repo --endpoint https://hf-mirror.com
```

#### Sample Output

```
Repository: TheBloke/Mistral-7B-Instruct-v0.2-GGUF
Type:       GGUF Model
Files:      12 files (4.2 GiB total)

GGUF Quantizations:
  Q2_K      2.1 GiB  ★★☆☆☆  ~2.8 GiB RAM  Smallest, lowest quality
  Q4_K_M    3.8 GiB  ★★★★☆  ~4.7 GiB RAM  Good balance (recommended)
  Q5_K_M    4.5 GiB  ★★★★★  ~5.4 GiB RAM  High quality
  Q8_0      7.2 GiB  ★★★★★  ~8.3 GiB RAM  Near-lossless

Transformers Analysis:
  Architecture:  MistralForCausalLM
  Parameters:    7.24B
  Context:       32768 tokens
  Vocabulary:    32000
```

---

### list

List models, datasets, and Spaces found directly in the standard Hub cache,
including downloads created by `hf`, `huggingface-cli`, and Python libraries.

```
hfdownloader list [flags]
```

#### Flags

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--cache-dir` | | string | `~/.cache/huggingface` | Cache directory |
| `--type` | | string | | Filter: model, dataset, space |
| `--sort` | | string | `name` | Sort: name, size, date |
| `--format` | | string | `table` | Output: table, json |
| `--scan` | | bool | `false` | Compatibility flag; direct scanning is now the default |
| `--manifests-only` | | bool | `false` | Only show repositories with hfdownloader manifests |

#### Examples

```bash
# List all
hfdownloader list

# Models only
hfdownloader list --type model

# Sort by size
hfdownloader list --sort size

# JSON output
hfdownloader list --format json

# Restrict output to hfdownloader-managed downloads
hfdownloader list --manifests-only
```

#### Sample Output

```
Downloaded Repositories

TYPE     REPO                                    SIZE      BRANCH   DATE
model    TheBloke/Mistral-7B-Instruct-v0.2-GGUF  4.2 GiB   main     2024-01-15
model    meta-llama/Llama-3-8B-Instruct          16.1 GiB  main     2024-01-14
dataset  facebook/flores                         128 MiB   main     2024-01-10

Total: 3 repositories (20.4 GiB)
```

---

### cache

Browse models, datasets, and Spaces in the local Hugging Face Hub cache and
remove data you no longer need. The interactive browser is a tree: repository
rows are parents and independently removable GGUF, safetensors, ONNX, and other
model payloads are children. Numbered shards are grouped as one artifact.
Select a repository row to delete everything beneath it, or select individual
artifact/quant rows to keep the rest of the repository.

Use the arrow keys or `j`/`k` to move, left/right or `h`/`l` to collapse and
expand, `space` to select, `/` to search, `t` to filter by type, `s` to change
sorting, and `d` to delete. Deletion always requires a second confirmation.
Passing an output/filter flag switches the command to non-interactive output,
which makes it easy to use in scripts.

```
hfdownloader cache [flags]
hfdownloader cache delete [TYPE:]OWNER/NAME... [flags]
```

#### Flags

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--cache-dir` | string | `~/.cache/huggingface` | Cache directory |
| `--type` | string | | Filter: model, dataset, space |
| `--search` | string | | Filter repository names in non-interactive output |
| `--sort` | string | `size` | Sort: size, name, recent |
| `--format` | string | `table` | Non-interactive output: table, json |

The `delete` subcommand accepts `--yes` for automation and `--force` to override
active-download protection. Prefix the repository with its type when the same
`owner/name` exists as more than one Hub repository type.

#### Examples

```bash
# Interactive cleanup browser
hfdownloader cache

# Script-friendly inventory
hfdownloader cache --sort size --format json

# Explicit, confirmed deletion for automation
hfdownloader cache delete model:TheBloke/Mistral-7B-GGUF --yes
```

---

### info

Show detailed information about a downloaded repository.

Information is read from the standard Hub cache when no hfdownloader manifest
exists, so repositories downloaded by the official `hf` command are supported.

```
hfdownloader info <repo> [flags]
```

#### Flags

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--cache-dir` | | string | `~/.cache/huggingface` | Cache directory |
| `--format` | | string | `text` | Output: text, json |

#### Examples

```bash
# Full repo name
hfdownloader info TheBloke/Mistral-7B-Instruct-v0.2-GGUF

# Partial match
hfdownloader info Mistral-7B

# JSON output
hfdownloader info Mistral-7B --format json
```

#### Sample Output

```
Repository: TheBloke/Mistral-7B-Instruct-v0.2-GGUF
Type:       model
Branch:     main
Commit:     a1b2c3d4e5f6...

Files:      12
Total Size: 4.2 GiB
Downloaded: 2024-01-15 10:30:45

Paths:
  Friendly: ~/.cache/huggingface/models/TheBloke/Mistral-7B-Instruct-v0.2-GGUF/
  Cache:    ~/.cache/huggingface/hub/models--TheBloke--Mistral-7B-Instruct-v0.2-GGUF/

Original Command:
  hfdownloader download TheBloke/Mistral-7B-Instruct-v0.2-GGUF -F q4_k_m

Files:
  NAME                                    SIZE      LFS
  config.json                             1.2 KiB   no
  mistral-7b-instruct-v0.2.Q4_K_M.gguf    4.1 GiB   yes
  README.md                               8.5 KiB   no
```

---

### rebuild

Regenerate friendly view symlinks from hub cache.

```
hfdownloader rebuild [flags]
```

Use after downloading with the official HuggingFace Python library, or after manually modifying the cache.

#### Flags

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--cache-dir` | | string | `~/.cache/huggingface` | Cache directory |
| `--clean` | | bool | `false` | Remove orphaned symlinks |
| `--write-script` | | bool | `false` | Write standalone rebuild.sh |

#### Examples

```bash
# Rebuild symlinks
hfdownloader rebuild

# Clean orphaned links
hfdownloader rebuild --clean

# Write standalone script
hfdownloader rebuild --write-script
```

#### Sample Output

```json
{
  "repos_scanned": 5,
  "symlinks_created": 23,
  "symlinks_updated": 2,
  "orphans_removed": 1,
  "errors": []
}
```

---

### mirror

Sync HuggingFace cache between locations.

```
hfdownloader mirror <subcommand> [flags]
```

#### Subcommands

##### mirror target add

Add a named mirror target.

```
hfdownloader mirror target add <name> <path> [flags]
```

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--description` | `-d` | string | | Target description |

```bash
hfdownloader mirror target add office /mnt/nas/hfcache -d "Office NAS"
hfdownloader mirror target add usb /media/usb/hfcache
```

##### mirror target list

List configured targets.

```
hfdownloader mirror target list [flags]
```

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--format` | | string | `table` | Output: table, json |

```bash
hfdownloader mirror target list
```

##### mirror target remove

Remove a mirror target.

```
hfdownloader mirror target remove <name>
```

```bash
hfdownloader mirror target remove usb
```

##### mirror diff

Show differences between local and target.

```
hfdownloader mirror diff <target> [flags]
```

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--cache-dir` | | string | `~/.cache/huggingface` | Local cache |
| `--format` | | string | `table` | Output: table, json |
| `--repo` | | string | | Filter by repo name |

```bash
hfdownloader mirror diff office
hfdownloader mirror diff office --repo Mistral
```

##### mirror push

Push local repos to target.

```
hfdownloader mirror push <target> [flags]
```

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--cache-dir` | | string | `~/.cache/huggingface` | Local cache |
| `--repo` | | string | | Filter by repo name |
| `--dry-run` | | bool | `false` | Preview only |
| `--verify` | | bool | `false` | Verify SHA256 after copy |
| `--delete` | | bool | `false` | Delete repos not in source |
| `--force` | | bool | `false` | Re-copy incomplete repos |

```bash
# Preview
hfdownloader mirror push office --dry-run

# Push all
hfdownloader mirror push office

# Push specific repo
hfdownloader mirror push office --repo Mistral-7B

# With verification
hfdownloader mirror push office --verify
```

##### mirror pull

Pull repos from target to local.

```
hfdownloader mirror pull <target> [flags]
```

Same flags as `mirror push`.

```bash
hfdownloader mirror pull office
hfdownloader mirror pull office --repo Llama
```

---

### proxy

Manage and test proxy configuration.

```
hfdownloader proxy <subcommand> [flags]
```

#### Subcommands

##### proxy test

Test proxy connectivity by making a test request.

```
hfdownloader proxy test [flags]
```

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--proxy` | `-x` | string | | Proxy URL (required) |
| `--proxy-user` | | string | | Proxy username |
| `--proxy-pass` | | string | | Proxy password |
| `--url` | | string | `https://huggingface.co/api/whoami` | URL to test |
| `--timeout` | | string | `30s` | Connection timeout |

```bash
# Test HTTP proxy
hfdownloader proxy test --proxy http://proxy.corp.com:8080

# Test with authentication
hfdownloader proxy test --proxy http://proxy.corp.com:8080 \
  --proxy-user myuser --proxy-pass mypassword

# Test SOCKS5 proxy
hfdownloader proxy test --proxy socks5://localhost:1080

# Test against specific URL
hfdownloader proxy test --proxy http://proxy:8080 --url https://example.com
```

##### proxy info

Show current proxy configuration from environment variables.

```
hfdownloader proxy info [flags]
```

```bash
# Show proxy info
hfdownloader proxy info

# JSON output
hfdownloader proxy info --json
```

#### Sample Output

```
Proxy Configuration:

Environment Variables:
  HTTP_PROXY:    http://proxy.corp.com:8080
  HTTPS_PROXY:   http://proxy.corp.com:8080
  NO_PROXY:      localhost,.internal.com

Effective Proxy: http://proxy.corp.com:8080
```

#### Using Proxy with Downloads

Proxy settings can be specified via CLI flags, config file, or environment variables.

##### CLI Flags

```bash
# Basic proxy
hfdownloader download meta-llama/Llama-2-7b --proxy http://proxy:8080

# With authentication
hfdownloader download meta-llama/Llama-2-7b \
  --proxy http://proxy:8080 \
  --proxy-user myuser \
  --proxy-pass mypassword

# SOCKS5 proxy
hfdownloader download meta-llama/Llama-2-7b --proxy socks5://localhost:1080

# Ignore environment proxy
hfdownloader download meta-llama/Llama-2-7b --no-env-proxy
```

##### Configuration File

```yaml
# ~/.config/hfdownloader.yaml
proxy:
  url: http://proxy.corp.com:8080
  username: myuser
  password: mypassword
  no_proxy: localhost,.internal.com,10.0.0.0/8
  no_env_proxy: false
  insecure_skip_verify: false
```

##### Environment Variables

| Variable | Description |
|----------|-------------|
| `HTTP_PROXY` / `http_proxy` | Proxy for HTTP requests |
| `HTTPS_PROXY` / `https_proxy` | Proxy for HTTPS requests |
| `ALL_PROXY` / `all_proxy` | Fallback for all protocols |
| `NO_PROXY` / `no_proxy` | Comma-separated bypass list |

##### Supported Proxy Types

| Type | URL Format | Description |
|------|------------|-------------|
| HTTP | `http://host:port` | Standard HTTP proxy |
| HTTPS | `https://host:port` | TLS-encrypted proxy |
| SOCKS5 | `socks5://host:port` | SOCKS5 TCP tunneling |
| SOCKS5h | `socks5h://host:port` | SOCKS5 with remote DNS |

##### NO_PROXY Patterns

The `no_proxy` setting supports:

- Exact hostnames: `localhost`, `myserver.local`
- Domain suffixes: `.internal.com` (matches `*.internal.com`)
- CIDR ranges: `10.0.0.0/8`, `192.168.0.0/16`
- Wildcard: `*` (bypass all)

---

### config

Manage configuration file.

```
hfdownloader config <subcommand>
```

#### Subcommands

##### config init

Create default configuration file.

```
hfdownloader config init [flags]
```

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--force` | `-f` | bool | `false` | Overwrite existing |
| `--yaml` | | bool | `false` | Create YAML instead of JSON |

```bash
hfdownloader config init
hfdownloader config init --yaml
hfdownloader config init -f
```

##### config show

Display current configuration.

```bash
hfdownloader config show
```

##### config path

Print configuration file path.

```bash
hfdownloader config path
# Output: /home/user/.config/hfdownloader.json
```

#### Default Configuration

```json
{
  "connections": 16,
  "max-active": 3,
  "multipart-threshold": "32MiB",
  "verify": "size",
  "retries": 4,
  "backoff-initial": "400ms",
  "backoff-max": "10s",
  "token": ""
}
```

---

### version

Show version information.

```
hfdownloader version [flags]
```

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--short` | `-s` | bool | `false` | Version number only |

```bash
hfdownloader version
# hfdownloader v3.4.2
# Go:      go1.21.0
# OS/Arch: darwin/arm64
# Commit:  abc123
# Built:   2024-01-15T10:30:00Z

hfdownloader version -s
# 3.0.0
```

---

## Environment Variables

| Variable | Description |
|----------|-------------|
| `HF_TOKEN` | HuggingFace access token |
| `HF_HOME` | Override `~/.cache/huggingface` root |
| `HF_HUB_CACHE` | Override just the `hub/` directory |
| `HF_COLOR` | Color output: `auto`, `always`, or `never` |
| `COLORFGBG` | Optional foreground/background palette hint used by auto theme |
| `NO_COLOR` | Standard color opt-out honored by `--color auto` |
| `HTTP_PROXY` / `http_proxy` | Proxy for HTTP requests |
| `HTTPS_PROXY` / `https_proxy` | Proxy for HTTPS requests |
| `ALL_PROXY` / `all_proxy` | Fallback proxy for all protocols |
| `NO_PROXY` / `no_proxy` | Comma-separated proxy bypass list |

```bash
export HF_TOKEN=hf_xxxxx
export HF_HOME=/mnt/data/huggingface

# Proxy configuration
export HTTPS_PROXY=http://proxy.corp.com:8080
export NO_PROXY=localhost,.internal.com
```

### SSH color troubleshooting

SSH does not inherently disable Bubble Tea colors. It forwards terminal
capabilities through environment variables, so a remote `TERM=dumb`, missing
terminfo entry, or `NO_COLOR=1` can produce monochrome output. Check the remote
session with:

```bash
printf 'TERM=%s COLORTERM=%s NO_COLOR=%s\n' "$TERM" "$COLORTERM" "$NO_COLOR"
```

For a color-capable terminal reported as `xterm-256color`, either remove an
unwanted `NO_COLOR` setting or explicitly override it:

```bash
hfdownloader --color always search llama
# Persistent for hfdownloader only:
export HF_COLOR=always
```

If background detection is wrong through a relay or multiplexer, use
`--theme light` or `--theme dark`. Auto mode also understands common
`COLORFGBG` values such as `15;0` (dark) and `0;15` (light).

---

## Configuration File

Configuration is loaded from (in order):
1. `--config` flag path
2. `~/.config/hfdownloader.json`
3. `~/.config/hfdownloader.yaml`

### JSON Format

```json
{
  "token": "hf_xxxxx",
  "connections": 16,
  "max-active": 3,
  "multipart-threshold": "32MiB",
  "verify": "size",
  "retries": 4,
  "backoff-initial": "400ms",
  "backoff-max": "10s",
  "endpoint": "",
  "proxy": {
    "url": "http://proxy.corp.com:8080",
    "username": "myuser",
    "password": "mypassword",
    "no_proxy": "localhost,.internal.com",
    "no_env_proxy": false,
    "insecure_skip_verify": false
  }
}
```

### YAML Format

```yaml
token: hf_xxxxx
connections: 16
max-active: 3
multipart-threshold: 32MiB
verify: size
retries: 4
backoff-initial: 400ms
backoff-max: 10s
endpoint: ""

# Proxy configuration
proxy:
  url: http://proxy.corp.com:8080
  username: myuser
  password: mypassword
  no_proxy: localhost,.internal.com
  no_env_proxy: false
  insecure_skip_verify: false
```

---

## Examples

### Download Workflows

```bash
# 1. Analyze → Select → Download
hfdownloader analyze TheBloke/Mistral-7B-Instruct-v0.2-GGUF
hfdownloader download TheBloke/Mistral-7B-Instruct-v0.2-GGUF:q4_k_m

# 2. Download entire model
hfdownloader download meta-llama/Llama-3-8B-Instruct

# 3. Download with filters and excludes
hfdownloader download owner/repo -F safetensors -E ".md,fp16"

# 4. High-speed download
hfdownloader download owner/repo -c 16 --max-active 8

# 5. Resume interrupted download
hfdownloader download owner/repo  # Automatically resumes
```

### Server Workflows

```bash
# 1. Basic server
hfdownloader serve

# 2. Production server
hfdownloader serve \
  --port 8080 \
  --auth-user admin \
  --auth-pass secure123 \
  -t hf_xxxxx

# 3. Mirror server
hfdownloader serve --endpoint https://hf-mirror.com
```

### Cache Management

```bash
# View downloads
hfdownloader list --sort size

# Interactively select repositories and reclaim their disk space
hfdownloader cache

# Get details
hfdownloader info Mistral-7B

# Rebuild after Python downloads
hfdownloader rebuild --clean

# Mirror to backup
hfdownloader mirror target add backup /mnt/backup/hf
hfdownloader mirror push backup --verify
```

### CI/CD Integration

```bash
# JSON output for parsing
hfdownloader download owner/repo --json 2>&1 | jq '.event'

# Dry run for planning
hfdownloader download owner/repo --dry-run --plan-format json

# Quiet mode for scripts
hfdownloader download owner/repo -q
```

---

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | General error |
| 2 | Invalid arguments |
| 130 | Interrupted (Ctrl+C) |

---

## Signal Handling

- `SIGINT` (Ctrl+C): Graceful shutdown, saves progress
- `SIGTERM`: Same as SIGINT

Interrupted downloads can be resumed by running the same command again.

---

## See Also

- [V3 Features Documentation](V3_FEATURES.md)
- [REST API Documentation](API.md)
- [Main README](../README.md)
- [GitHub Issues](https://github.com/bodaay/HuggingFaceModelDownloader/issues)
