// Copyright 2025
// SPDX-License-Identifier: Apache-2.0

package hfdownloader

import "testing"

func TestParseRepoRef(t *testing.T) {
	tests := []struct {
		in   string
		want RepoRef
	}{
		// Plain form
		{"owner/name", RepoRef{Repo: "owner/name"}},
		{"  owner/name  ", RepoRef{Repo: "owner/name"}},

		// hf:// URIs
		{"hf://owner/name", RepoRef{Repo: "owner/name"}},
		{"hf://owner/name/model.gguf", RepoRef{Repo: "owner/name", Path: "model.gguf"}},
		{"hf://owner/name/sub/dir/model.gguf", RepoRef{Repo: "owner/name", Path: "sub/dir/model.gguf"}},
		{"hf://models/owner/name", RepoRef{Repo: "owner/name"}},
		{"hf://datasets/owner/name", RepoRef{Repo: "owner/name", IsDataset: true}},
		{"hf://datasets/owner/name/data/train.parquet", RepoRef{Repo: "owner/name", IsDataset: true, Path: "data/train.parquet"}},
		{"hf://owner/name@dev", RepoRef{Repo: "owner/name", Revision: "dev"}},
		{"hf://owner/name@refs%2Fpr%2F1/model.bin", RepoRef{Repo: "owner/name", Revision: "refs/pr/1", Path: "model.bin"}},
		{"HF://owner/name", RepoRef{Repo: "owner/name"}},

		// Hub URLs
		{"https://huggingface.co/owner/name", RepoRef{Repo: "owner/name"}},
		{"https://huggingface.co/owner/name/", RepoRef{Repo: "owner/name"}},
		{"http://huggingface.co/owner/name", RepoRef{Repo: "owner/name"}},
		{"https://hf.co/owner/name", RepoRef{Repo: "owner/name"}},
		{"https://huggingface.co/datasets/owner/name", RepoRef{Repo: "owner/name", IsDataset: true}},
		{"https://huggingface.co/owner/name/tree/main", RepoRef{Repo: "owner/name"}},
		{"https://huggingface.co/owner/name/tree/gptq-4bit", RepoRef{Repo: "owner/name", Revision: "gptq-4bit"}},
		{"https://huggingface.co/owner/name/blob/main/config.json", RepoRef{Repo: "owner/name", Path: "config.json"}},
		{"https://huggingface.co/owner/name/resolve/main/model-00001-of-00002.safetensors",
			RepoRef{Repo: "owner/name", Path: "model-00001-of-00002.safetensors"}},
		{"https://huggingface.co/owner/name/resolve/main/model.gguf?download=true",
			RepoRef{Repo: "owner/name", Path: "model.gguf"}},
		{"https://huggingface.co/owner/name/resolve/refs%2Fpr%2F1/model.gguf",
			RepoRef{Repo: "owner/name", Revision: "refs/pr/1", Path: "model.gguf"}},
		{"https://huggingface.co/owner/name/blob/dev/sub/dir/tokenizer.json",
			RepoRef{Repo: "owner/name", Revision: "dev", Path: "sub/dir/tokenizer.json"}},
		{"https://huggingface.co/datasets/owner/name/resolve/main/data/train-00000.parquet",
			RepoRef{Repo: "owner/name", IsDataset: true, Path: "data/train-00000.parquet"}},
		// Non-content suffixes are ignored
		{"https://huggingface.co/owner/name/discussions", RepoRef{Repo: "owner/name"}},

		// Real-world examples
		{"https://huggingface.co/bartowski/Meta-Llama-3.1-8B-Instruct-GGUF/resolve/main/Meta-Llama-3.1-8B-Instruct-Q4_K_M.gguf",
			RepoRef{Repo: "bartowski/Meta-Llama-3.1-8B-Instruct-GGUF", Path: "Meta-Llama-3.1-8B-Instruct-Q4_K_M.gguf"}},
		{"hf://datasets/HuggingFaceFW/fineweb", RepoRef{Repo: "HuggingFaceFW/fineweb", IsDataset: true}},
	}

	for _, tt := range tests {
		got, err := ParseRepoRef(tt.in)
		if err != nil {
			t.Errorf("ParseRepoRef(%q) error: %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseRepoRef(%q) = %+v, want %+v", tt.in, got, tt.want)
		}
	}
}

func TestParseRepoRefErrors(t *testing.T) {
	for _, in := range []string{
		"",
		"just-a-name",
		"hf://",
		"hf://owner",
		"hf://datasets/owner",
		"https://example.com/owner/name",
		"https://huggingface.co/",
		"https://huggingface.co/onlyowner",
	} {
		if _, err := ParseRepoRef(in); err == nil {
			t.Errorf("ParseRepoRef(%q) succeeded, want error", in)
		}
	}
}

func TestLooksLikeRepoURI(t *testing.T) {
	for in, want := range map[string]bool{
		"hf://owner/name":             true,
		"HF://owner/name":             true,
		"https://huggingface.co/o/n":  true,
		"http://huggingface.co/o/n":   true,
		"owner/name":                  false,
		"owner/name:q4_k_m":           false,
		"TheBloke/Mistral-7B:q4,q5_0": false,
	} {
		if got := LooksLikeRepoURI(in); got != want {
			t.Errorf("LooksLikeRepoURI(%q) = %v, want %v", in, got, want)
		}
	}
}
