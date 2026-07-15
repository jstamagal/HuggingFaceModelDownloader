// Copyright 2025
// SPDX-License-Identifier: Apache-2.0

package hfdownloader

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ModelSearchOptions controls a Hugging Face Hub model search.
type ModelSearchOptions struct {
	Query       string
	Author      string
	PipelineTag string
	Library     string
	Gated       *bool
	Sort        string
	Limit       int
	Token       string
	Endpoint    string
	Proxy       *ProxyConfig

	// HTTPClient is primarily useful to callers that need a custom transport or
	// to tests using an httptest server. When nil, the standard downloader HTTP
	// client (including proxy support) is used.
	HTTPClient *http.Client
}

// ModelSearchResult is the compact model metadata returned by Hub search.
type ModelSearchResult struct {
	ID            string      `json:"id"`
	Author        string      `json:"author,omitempty"`
	Downloads     int64       `json:"downloads,omitempty"`
	Likes         int64       `json:"likes,omitempty"`
	TrendingScore float64     `json:"trendingScore,omitempty"`
	LastModified  string      `json:"lastModified,omitempty"`
	CreatedAt     string      `json:"createdAt,omitempty"`
	PipelineTag   string      `json:"pipeline_tag,omitempty"`
	LibraryName   string      `json:"library_name,omitempty"`
	Gated         GatedStatus `json:"gated,omitempty"`
	Private       bool        `json:"private,omitempty"`
	Disabled      bool        `json:"disabled,omitempty"`
	Tags          []string    `json:"tags,omitempty"`
}

// GatedStatus normalizes the Hub's polymorphic gated field. The API can return
// false, true, null, or a mode string such as "manual" or "auto".
type GatedStatus string

// UnmarshalJSON implements json.Unmarshaler.
func (g *GatedStatus) UnmarshalJSON(data []byte) error {
	if string(data) == "null" || string(data) == "false" {
		*g = ""
		return nil
	}
	if string(data) == "true" {
		*g = "gated"
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("decode gated status: %w", err)
	}
	*g = GatedStatus(value)
	return nil
}

// SearchModels searches model repositories using the Hugging Face Hub API.
func SearchModels(ctx context.Context, opts ModelSearchOptions) ([]ModelSearchResult, error) {
	if opts.Limit <= 0 {
		opts.Limit = 50
	}
	if opts.Limit > 1000 {
		return nil, fmt.Errorf("search limit must be between 1 and 1000")
	}

	values := url.Values{}
	setQueryValue(values, "search", opts.Query)
	setQueryValue(values, "author", opts.Author)
	setQueryValue(values, "pipeline_tag", opts.PipelineTag)
	setQueryValue(values, "library", opts.Library)
	if opts.Gated != nil {
		values.Set("gated", strconv.FormatBool(*opts.Gated))
	}
	if sortKey := hubSortKey(opts.Sort); sortKey != "" {
		values.Set("sort", sortKey)
		values.Set("direction", "-1")
	}
	values.Set("limit", strconv.Itoa(opts.Limit))
	for _, field := range []string{
		"author", "downloads", "likes", "trendingScore", "lastModified",
		"createdAt", "pipeline_tag", "library_name", "gated", "private",
		"disabled", "tags",
	} {
		values.Add("expand[]", field)
	}

	reqURL := getEndpoint(opts.Endpoint) + "/api/models?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	addAuth(req, opts.Token)

	httpc := opts.HTTPClient
	if httpc == nil {
		httpc, err = BuildHTTPClient(opts.Proxy)
		if err != nil {
			return nil, fmt.Errorf("build search HTTP client: %w", err)
		}
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search Hugging Face models: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &APIError{StatusCode: resp.StatusCode, Status: resp.Status, URL: reqURL}
	}

	var results []ModelSearchResult
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, fmt.Errorf("decode Hugging Face model search: %w", err)
	}
	return results, nil
}

func setQueryValue(values url.Values, key, value string) {
	if value = strings.TrimSpace(value); value != "" {
		values.Set(key, value)
	}
}

func hubSortKey(sort string) string {
	switch strings.ToLower(strings.TrimSpace(sort)) {
	case "", "trending", "trendingscore", "trending_score":
		return "trendingScore"
	case "downloads":
		return "downloads"
	case "likes":
		return "likes"
	case "updated", "lastmodified", "last_modified":
		return "lastModified"
	case "created", "createdat", "created_at":
		return "createdAt"
	default:
		return strings.TrimSpace(sort)
	}
}
