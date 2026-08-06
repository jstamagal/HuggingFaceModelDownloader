// Copyright 2025
// SPDX-License-Identifier: Apache-2.0

package smartdl

import (
	"strings"
	"testing"
)

// TestGGUFShardCollapsing verifies that multi-part quants are presented as a
// single entry per quant with the combined size, across the naming styles
// used by bartowski (gguf-split suffix), unsloth (quant-named subdirectory),
// and mradermacher (raw .partNofM splits).
func TestGGUFShardCollapsing(t *testing.T) {
	t.Run("gguf-split suffix style", func(t *testing.T) {
		files := []FileInfo{
			{Name: "Model-70B-Q6_K-00001-of-00002.gguf", Path: "Model-70B-Q6_K-00001-of-00002.gguf", Size: 30_000_000_000},
			{Name: "Model-70B-Q6_K-00002-of-00002.gguf", Path: "Model-70B-Q6_K-00002-of-00002.gguf", Size: 27_000_000_000},
			{Name: "Model-70B-Q4_K_M.gguf", Path: "Model-70B-Q4_K_M.gguf", Size: 40_000_000_000},
		}
		info := analyzeGGUF(files)
		if len(info.Quantizations) != 2 {
			t.Fatalf("expected 2 quant groups, got %d: %+v", len(info.Quantizations), info.Quantizations)
		}
		var q6 *GGUFQuantization
		for i := range info.Quantizations {
			if info.Quantizations[i].Name == "Q6_K" {
				q6 = &info.Quantizations[i]
			}
		}
		if q6 == nil {
			t.Fatal("Q6_K group missing")
		}
		if q6.FileCount != 2 {
			t.Errorf("Q6_K FileCount = %d, want 2", q6.FileCount)
		}
		if q6.TotalSize != 57_000_000_000 {
			t.Errorf("Q6_K TotalSize = %d, want 57e9", q6.TotalSize)
		}
		// Shards must be ordered.
		if !strings.Contains(q6.Files[0].Name, "00001-of") {
			t.Errorf("first shard = %q, want 00001-of first", q6.Files[0].Name)
		}
	})

	t.Run("unsloth quant subdirectory style", func(t *testing.T) {
		files := []FileInfo{
			{Name: "DeepSeek-R1-UD-IQ1_S-00001-of-00003.gguf", Path: "DeepSeek-R1-UD-IQ1_S/DeepSeek-R1-UD-IQ1_S-00001-of-00003.gguf", Size: 45_000_000_000},
			{Name: "DeepSeek-R1-UD-IQ1_S-00002-of-00003.gguf", Path: "DeepSeek-R1-UD-IQ1_S/DeepSeek-R1-UD-IQ1_S-00002-of-00003.gguf", Size: 45_000_000_000},
			{Name: "DeepSeek-R1-UD-IQ1_S-00003-of-00003.gguf", Path: "DeepSeek-R1-UD-IQ1_S/DeepSeek-R1-UD-IQ1_S-00003-of-00003.gguf", Size: 41_000_000_000},
			{Name: "DeepSeek-R1-Q2_K_XL-00001-of-00005.gguf", Path: "Q2_K_XL/DeepSeek-R1-Q2_K_XL-00001-of-00005.gguf", Size: 48_000_000_000},
			{Name: "DeepSeek-R1-Q2_K_XL-00002-of-00005.gguf", Path: "Q2_K_XL/DeepSeek-R1-Q2_K_XL-00002-of-00005.gguf", Size: 48_000_000_000},
		}
		info := analyzeGGUF(files)
		if len(info.Quantizations) != 2 {
			t.Fatalf("expected 2 quant groups, got %d", len(info.Quantizations))
		}
		names := map[string]int{}
		for _, q := range info.Quantizations {
			names[q.Name] = q.FileCount
		}
		if names["IQ1_S"] != 3 {
			t.Errorf("IQ1_S FileCount = %d, want 3 (groups: %v)", names["IQ1_S"], names)
		}
		if names["Q2_K_XL"] != 2 {
			t.Errorf("Q2_K_XL FileCount = %d, want 2 (groups: %v)", names["Q2_K_XL"], names)
		}
	})

	t.Run("subdirectory carries quant when filenames do not", func(t *testing.T) {
		files := []FileInfo{
			{Name: "model-00001-of-00002.gguf", Path: "Q4_K_M/model-00001-of-00002.gguf", Size: 9_000_000_000},
			{Name: "model-00002-of-00002.gguf", Path: "Q4_K_M/model-00002-of-00002.gguf", Size: 8_200_000_000},
		}
		info := analyzeGGUF(files)
		if len(info.Quantizations) != 1 {
			t.Fatalf("expected 1 quant group, got %d", len(info.Quantizations))
		}
		q := info.Quantizations[0]
		if q.Name != "Q4_K_M" {
			t.Errorf("Name = %q, want Q4_K_M (from directory)", q.Name)
		}
		if q.FileCount != 2 || q.TotalSize != 17_200_000_000 {
			t.Errorf("FileCount = %d TotalSize = %d", q.FileCount, q.TotalSize)
		}
	})

	t.Run("mradermacher part-split style", func(t *testing.T) {
		files := []FileInfo{
			{Name: "Model.i1-Q4_K_M.gguf.part1of2", Path: "Model.i1-Q4_K_M.gguf.part1of2", Size: 20_000_000_000},
			{Name: "Model.i1-Q4_K_M.gguf.part2of2", Path: "Model.i1-Q4_K_M.gguf.part2of2", Size: 18_000_000_000},
			{Name: "Model.i1-Q2_K.gguf", Path: "Model.i1-Q2_K.gguf", Size: 15_000_000_000},
		}
		info := analyzeGGUF(files)
		if len(info.Quantizations) != 2 {
			t.Fatalf("expected 2 quant groups, got %d: %+v", len(info.Quantizations), info.Quantizations)
		}
		names := map[string]int{}
		for _, q := range info.Quantizations {
			names[q.Name] = q.FileCount
		}
		if names["Q4_K_M"] != 2 {
			t.Errorf("Q4_K_M FileCount = %d, want 2 (groups: %v)", names["Q4_K_M"], names)
		}
		if names["Q2_K"] != 1 {
			t.Errorf("Q2_K FileCount = %d, want 1", names["Q2_K"])
		}
	})

	t.Run("selectable item label and files for shards", func(t *testing.T) {
		files := []FileInfo{
			{Name: "m-Q4_K_M-00001-of-00003.gguf", Path: "m-Q4_K_M-00001-of-00003.gguf", Size: 6_000_000_000},
			{Name: "m-Q4_K_M-00002-of-00003.gguf", Path: "m-Q4_K_M-00002-of-00003.gguf", Size: 6_000_000_000},
			{Name: "m-Q4_K_M-00003-of-00003.gguf", Path: "m-Q4_K_M-00003-of-00003.gguf", Size: 5_200_000_000},
		}
		items := GGUFToSelectableItems(analyzeGGUF(files))
		if len(items) != 1 {
			t.Fatalf("expected 1 item, got %d", len(items))
		}
		item := items[0]
		if item.Label != "Q4_K_M (3 files)" {
			t.Errorf("Label = %q, want \"Q4_K_M (3 files)\"", item.Label)
		}
		if item.Size != 17_200_000_000 {
			t.Errorf("Size = %d, want combined size", item.Size)
		}
		if len(item.Files) != 3 {
			t.Errorf("Files = %v, want all 3 shards", item.Files)
		}
		if item.FilterValue != "q4_k_m" {
			t.Errorf("FilterValue = %q", item.FilterValue)
		}
	})

	t.Run("distinct single-file quants stay separate", func(t *testing.T) {
		files := []FileInfo{
			{Name: "m.Q4_K_M.gguf", Path: "m.Q4_K_M.gguf", Size: 1},
			{Name: "m.Q5_K_M.gguf", Path: "m.Q5_K_M.gguf", Size: 2},
			{Name: "m.IQ4_XS.gguf", Path: "m.IQ4_XS.gguf", Size: 3},
		}
		info := analyzeGGUF(files)
		if len(info.Quantizations) != 3 {
			t.Fatalf("expected 3 quant groups, got %d", len(info.Quantizations))
		}
	})
}

func TestStripGGUFShardSuffix(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		isShard bool
	}{
		{"m-Q6_K-00001-of-00002.gguf", "m-Q6_K.gguf", true},
		{"m-00002-of-00009.gguf", "m.gguf", true},
		{"m.Q8_0.gguf.part1of2", "m.Q8_0.gguf", true},
		{"m.Q8_0.gguf", "m.Q8_0.gguf", false},
		{"m-Q4_K_M.gguf", "m-Q4_K_M.gguf", false},
	}
	for _, tt := range tests {
		got, shard := stripGGUFShardSuffix(tt.in)
		if got != tt.want || shard != tt.isShard {
			t.Errorf("stripGGUFShardSuffix(%q) = (%q, %v), want (%q, %v)", tt.in, got, shard, tt.want, tt.isShard)
		}
	}
}
