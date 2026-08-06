// Copyright 2025
// SPDX-License-Identifier: Apache-2.0

package hfdownloader

import (
	"fmt"
	"net/url"
	"strings"
)

// RepoRef is a parsed repository reference. It captures everything a
// Hugging Face identifier can carry: the repo id, whether it is a dataset,
// an optional revision, and an optional file path inside the repo.
type RepoRef struct {
	Repo      string // "owner/name"
	IsDataset bool
	Revision  string // "" when unspecified
	Path      string // file path within the repo, "" when the whole repo is meant
}

// ParseRepoRef accepts every identifier form Hugging Face hands out and
// normalizes it to a RepoRef:
//
//	owner/name
//	hf://owner/name[/path/to/file]
//	hf://models/owner/name[/path]
//	hf://datasets/owner/name[/path]
//	hf://owner/name@revision[/path]           (huggingface_hub fsspec style)
//	https://huggingface.co/owner/name
//	https://huggingface.co/owner/name/tree/rev[/path]
//	https://huggingface.co/owner/name/blob/rev/path
//	https://huggingface.co/owner/name/resolve/rev/path[?download=true]
//	https://huggingface.co/datasets/owner/name[...]
//	https://hf.co/...                          (short domain)
//
// Plain "owner/name" strings pass through unchanged.
func ParseRepoRef(s string) (RepoRef, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return RepoRef{}, fmt.Errorf("empty repo reference")
	}

	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "hf://"):
		return parseHFURI(s[len("hf://"):])
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"):
		return parseHubURL(s)
	}

	// Plain owner/name.
	if !IsValidModelName(s) {
		return RepoRef{}, fmt.Errorf("invalid repo id %q (expected owner/name or an hf:// / huggingface.co URL)", s)
	}
	return RepoRef{Repo: s}, nil
}

// parseHFURI parses the remainder of an hf:// URI (after the scheme).
// Forms: [models/|datasets/]owner/name[@revision][/path...]
func parseHFURI(rest string) (RepoRef, error) {
	rest = strings.Trim(rest, "/")
	if rest == "" {
		return RepoRef{}, fmt.Errorf("empty hf:// URI")
	}
	segs := strings.Split(rest, "/")

	ref := RepoRef{}
	switch strings.ToLower(segs[0]) {
	case "datasets":
		ref.IsDataset = true
		segs = segs[1:]
	case "models":
		segs = segs[1:]
	}
	if len(segs) < 2 {
		return RepoRef{}, fmt.Errorf("hf:// URI must include owner/name (got %q)", rest)
	}

	owner, name := segs[0], segs[1]
	// huggingface_hub fsspec allows an @revision suffix on the repo segment,
	// with the revision possibly percent-encoded (refs%2Fpr%2F1).
	if at := strings.Index(name, "@"); at >= 0 {
		rev := name[at+1:]
		name = name[:at]
		if decoded, err := url.PathUnescape(rev); err == nil {
			rev = decoded
		}
		ref.Revision = rev
	}
	ref.Repo = owner + "/" + name
	if len(segs) > 2 {
		ref.Path = strings.Join(segs[2:], "/")
	}
	if !IsValidModelName(ref.Repo) {
		return RepoRef{}, fmt.Errorf("invalid repo id %q in hf:// URI", ref.Repo)
	}
	return ref, nil
}

// parseHubURL parses full https://huggingface.co/... URLs.
func parseHubURL(s string) (RepoRef, error) {
	u, err := url.Parse(s)
	if err != nil {
		return RepoRef{}, fmt.Errorf("invalid URL %q: %w", s, err)
	}
	host := strings.ToLower(u.Hostname())
	switch host {
	case "huggingface.co", "www.huggingface.co", "hf.co":
	default:
		return RepoRef{}, fmt.Errorf("unsupported host %q (expected huggingface.co)", u.Hostname())
	}

	segs := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	// Decode each segment individually so a %2F inside a revision does not
	// create phantom path segments.
	for i := range segs {
		if dec, err := url.PathUnescape(segs[i]); err == nil {
			segs[i] = dec
		}
	}

	ref := RepoRef{}
	if len(segs) > 0 && strings.EqualFold(segs[0], "datasets") {
		ref.IsDataset = true
		segs = segs[1:]
	}
	if len(segs) < 2 || segs[0] == "" || segs[1] == "" {
		return RepoRef{}, fmt.Errorf("URL %q does not name a repo (owner/name)", s)
	}
	ref.Repo = segs[0] + "/" + segs[1]
	if !IsValidModelName(ref.Repo) {
		return RepoRef{}, fmt.Errorf("invalid repo id %q in URL", ref.Repo)
	}
	segs = segs[2:]

	// Optional /tree/<rev>[/path], /blob/<rev>/<path>, /resolve/<rev>/<path>,
	// /raw/<rev>/<path>. Anything else (e.g. /discussions) is ignored.
	if len(segs) >= 2 {
		switch strings.ToLower(segs[0]) {
		case "tree", "blob", "resolve", "raw":
			ref.Revision = segs[1]
			if len(segs) > 2 {
				ref.Path = strings.Join(segs[2:], "/")
			}
		}
	}
	if ref.Revision == "main" {
		ref.Revision = "" // default branch; same as unspecified
	}
	return ref, nil
}

// LooksLikeRepoURI reports whether s uses one of the URI schemes ParseRepoRef
// understands (as opposed to a plain owner/name string). Callers use this to
// decide whether legacy "repo:filter" colon splitting applies.
func LooksLikeRepoURI(s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(lower, "hf://") ||
		strings.HasPrefix(lower, "http://") ||
		strings.HasPrefix(lower, "https://")
}
