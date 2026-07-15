// Copyright 2025
// SPDX-License-Identifier: Apache-2.0

package cli

import "testing"

func TestNewSearchCmd(t *testing.T) {
	cmd := newSearchCmd(t.Context(), &RootOpts{})
	if cmd.Use != "search [query]" {
		t.Fatalf("Use = %q", cmd.Use)
	}
	for _, flag := range []string{"author", "pipeline", "library", "gated", "sort", "limit", "endpoint"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("missing --%s", flag)
		}
	}
	if err := cmd.Args(cmd, []string{"one", "two"}); err == nil {
		t.Error("expected too-many-arguments error")
	}
}

func TestParseSearchAccess(t *testing.T) {
	all, err := parseSearchAccess("all")
	if err != nil || all != nil {
		t.Fatalf("all = %v, %v", all, err)
	}
	open, err := parseSearchAccess("open")
	if err != nil || open == nil || *open {
		t.Fatalf("open = %v, %v", open, err)
	}
	gated, err := parseSearchAccess("gated")
	if err != nil || gated == nil || !*gated {
		t.Fatalf("gated = %v, %v", gated, err)
	}
	if _, err := parseSearchAccess("secret"); err == nil {
		t.Fatal("expected invalid-access error")
	}
}

func TestValidSearchSort(t *testing.T) {
	for _, value := range []string{"trending", "downloads", "likes", "updated", "created"} {
		if !validSearchSort(value) {
			t.Errorf("%q should be valid", value)
		}
	}
	if validSearchSort("stars") {
		t.Error("stars should be invalid")
	}
}
