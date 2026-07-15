// Copyright 2025
// SPDX-License-Identifier: Apache-2.0

package hfdownloader

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestSearchModels(t *testing.T) {
	var gotQuery url.Values
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id": "owner/model", "author": "owner", "downloads": 1234,
			"likes": 42, "pipeline_tag": "text-generation",
			"library_name": "transformers", "gated": "manual",
			"tags": []string{"transformers", "gguf"},
		}})
	}))
	defer srv.Close()

	gated := true
	results, err := SearchModels(context.Background(), ModelSearchOptions{
		Query: "llama", Author: "owner", PipelineTag: "text-generation",
		Library: "transformers", Gated: &gated, Sort: "updated", Limit: 25,
		Token: "secret", Endpoint: srv.URL, HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatalf("SearchModels: %v", err)
	}
	if len(results) != 1 || results[0].ID != "owner/model" {
		t.Fatalf("results = %#v", results)
	}
	if results[0].Gated != "manual" {
		t.Fatalf("gated = %q", results[0].Gated)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	wants := map[string]string{
		"search": "llama", "author": "owner", "pipeline_tag": "text-generation",
		"library": "transformers", "gated": "true", "sort": "lastModified",
		"direction": "-1", "limit": "25",
	}
	for key, want := range wants {
		if got := gotQuery.Get(key); got != want {
			t.Errorf("query %s = %q, want %q", key, got, want)
		}
	}
	if len(gotQuery["expand[]"]) == 0 {
		t.Error("expand[] was not sent")
	}
}

func TestSearchModelsErrors(t *testing.T) {
	t.Run("limit", func(t *testing.T) {
		_, err := SearchModels(context.Background(), ModelSearchOptions{Limit: 1001})
		if err == nil {
			t.Fatal("expected limit error")
		}
	})

	t.Run("status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "slow down", http.StatusTooManyRequests)
		}))
		defer srv.Close()
		_, err := SearchModels(context.Background(), ModelSearchOptions{Endpoint: srv.URL, HTTPClient: srv.Client()})
		if err == nil {
			t.Fatal("expected API error")
		}
		apiErr, ok := err.(*APIError)
		if !ok || apiErr.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("error = %#v", err)
		}
	})
}

func TestGatedStatus(t *testing.T) {
	for raw, want := range map[string]GatedStatus{
		`null`: "", `false`: "", `true`: "gated", `"manual"`: "manual",
	} {
		var got GatedStatus
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatalf("unmarshal %s: %v", raw, err)
		}
		if got != want {
			t.Errorf("unmarshal %s = %q, want %q", raw, got, want)
		}
	}
}
