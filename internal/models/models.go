// Package models configures custom models through Shelley's local HTTP API.
package models

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Config supplies one provider endpoint and key for a set of model names.
// Keys must not appear in logs, launchers, or lifecycle records.
type Config struct {
	APIURL  string
	APIType string
	APIKey  string
	Names   []string
}

// Validate rejects incomplete configuration before any instance side effects.
func (c Config) Validate() error {
	if !validAPIURL(c.APIURL) {
		return errors.New("API URL must be an absolute HTTP or HTTPS URL without credentials, query, or fragment")
	}
	switch c.APIType {
	case "openai-responses", "openai", "anthropic", "gemini":
	default:
		return errors.New("API type must be openai-responses, openai, anthropic, or gemini")
	}
	if strings.TrimSpace(c.APIKey) == "" || strings.ContainsAny(c.APIKey, "\r\n") {
		return errors.New("API key must be a nonempty single line")
	}
	if len(c.Names) == 0 {
		return errors.New("at least one model name is required")
	}
	for _, name := range c.Names {
		if !validModelName(name) {
			return errors.New("model names must be nonempty and must not contain surrounding whitespace or line breaks")
		}
	}
	return nil
}

func validAPIURL(value string) bool {
	u, err := url.Parse(value)
	if err != nil {
		return false
	}
	switch u.Scheme {
	case "https", "http":
	default:
		return false
	}
	if u.Hostname() == "" {
		return false
	}
	if u.User != nil {
		return false
	}
	if u.RawQuery != "" {
		return false
	}
	return u.Fragment == ""
}

func validModelName(value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	if strings.TrimSpace(value) != value {
		return false
	}
	return !strings.ContainsAny(value, "\r\n")
}

// model mirrors the writable fields of the pinned Shelley custom-model API.
// Carry existing settings through PUT so key rotation does not reset UI tuning.
type model struct {
	ID               string `json:"model_id,omitempty"`
	DisplayName      string `json:"display_name"`
	ProviderType     string `json:"provider_type"`
	Endpoint         string `json:"endpoint"`
	APIKey           string `json:"api_key"`
	ModelName        string `json:"model_name"`
	MaxTokens        int64  `json:"max_tokens"`
	Tags             string `json:"tags"`
	ReasoningEffort  string `json:"reasoning_effort"`
	ReasoningReplay  string `json:"reasoning_replay"`
	ImageSupport     string `json:"image_support"`
	ReasoningSupport string `json:"reasoning_support"`
	ReasoningMap     string `json:"reasoning_map"`
}

func (m model) matches(c Config, name string) bool {
	if m.ProviderType != c.APIType {
		return false
	}
	if m.ModelName != name {
		return false
	}
	return strings.TrimRight(m.Endpoint, "/") == strings.TrimRight(c.APIURL, "/")
}

func matchingModels(existing []model, c Config) (map[string]model, error) {
	updates := make(map[string]model)
	for _, name := range c.Names {
		matches := 0
		for _, m := range existing {
			if !m.matches(c, name) {
				continue
			}
			matches++
			if m.ID == "" {
				return nil, errors.New("matching Shelley model has no identifier")
			}
			updates[name] = m
		}
		if matches > 1 {
			return nil, errors.New("multiple matching custom models exist; resolve duplicates in Shelley before configuring")
		}
	}
	return updates, nil
}

// Configure upserts matching endpoint/type/name tuples without deleting models.
// Callers must verify container ownership and running state before supplying its
// host loopback port. Requests never test or contact the configured provider.
func Configure(ctx context.Context, port int, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if port < 1 {
		return errors.New("invalid Shelley backend port")
	}
	if port > 65535 {
		return errors.New("invalid Shelley backend port")
	}
	client := &http.Client{
		Timeout:       10 * time.Second,
		Transport:     &http.Transport{Proxy: nil},
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer client.CloseIdleConnections()
	base := fmt.Sprintf("http://127.0.0.1:%d/api/custom-models", port)
	data, err := request(ctx, client, http.MethodGet, base, nil)
	if err != nil {
		return err
	}
	var existing []model
	if err := json.Unmarshal(data, &existing); err != nil {
		return errors.New("cannot decode Shelley's custom model list")
	}
	updates, err := matchingModels(existing, c)
	if err != nil {
		return err
	}
	return applyModels(ctx, client, base, c, updates)
}

func applyModels(ctx context.Context, client *http.Client, base string, c Config, updates map[string]model) error {
	seen := make(map[string]bool)
	for _, name := range c.Names {
		if seen[name] {
			continue
		}
		seen[name] = true
		m, found := updates[name]
		method, target := http.MethodPost, base
		if found {
			method, target = http.MethodPut, base+"/"+url.PathEscape(m.ID)
		} else {
			m = model{DisplayName: name, ProviderType: c.APIType, Endpoint: strings.TrimRight(c.APIURL, "/"), ModelName: name}
		}
		m.APIKey = c.APIKey
		body, err := json.Marshal(m)
		if err != nil {
			return errors.New("cannot encode Shelley model settings")
		}
		if _, err := request(ctx, client, method, target, body); err != nil {
			return fmt.Errorf("model configuration stopped; repeat with the same options to resume: %w", err)
		}
	}
	return nil
}

func request(ctx context.Context, client *http.Client, method, target string, body []byte) (data []byte, result error) {
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("cannot construct local Shelley request")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("cannot contact local Shelley API; check the instance and retry")
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			if result == nil {
				result = errors.New("cannot close local Shelley response")
			}
		}
	}()
	if !successful(response.StatusCode) {
		return nil, fmt.Errorf("local Shelley API returned HTTP %d for %s", response.StatusCode, method)
	}
	if method != http.MethodGet {
		return nil, nil
	}
	return readList(response.Body)
}

func successful(code int) bool {
	if code < 200 {
		return false
	}
	return code < 300
}

func readList(body io.Reader) ([]byte, error) {
	const limit = 4 << 20
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, errors.New("cannot read Shelley's custom model list")
	}
	if len(data) > limit {
		return nil, errors.New("custom model list exceeds the response limit")
	}
	return data, nil
}
