// Package anthropic is a minimal client for the Anthropic Messages API,
// just enough to send a single-turn completion request and get back the
// text response - used by `fastci analyze` to ask Claude to diagnose a
// captured test failure. It intentionally avoids a dependency on the full
// Anthropic SDK for this one call.
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// DefaultModel is used when Client.Model is empty.
const DefaultModel = "claude-sonnet-5"

const defaultBaseURL = "https://api.anthropic.com"
const apiVersion = "2023-06-01"
const defaultTimeout = 60 * time.Second

// Client calls the Anthropic Messages API.
type Client struct {
	APIKey string
	Model  string // defaults to DefaultModel if empty

	// BaseURL and HTTPClient are overridable for tests; production callers
	// should leave both zero.
	BaseURL    string
	HTTPClient *http.Client
}

// CreateMessage sends a single user-turn request (with an optional system
// prompt) and returns the concatenated text of the response.
func (c *Client) CreateMessage(ctx context.Context, system, userPrompt string, maxTokens int) (string, error) {
	if c.APIKey == "" {
		return "", fmt.Errorf("anthropic: no API key set")
	}

	reqBody := map[string]any{
		"model":      c.model(),
		"max_tokens": maxTokens,
		"messages": []map[string]string{
			{"role": "user", "content": userPrompt},
		},
	}
	if system != "" {
		reqBody["system"] = system
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("anthropic: encoding request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("anthropic: building request: %w", err)
	}
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", apiVersion)
	req.Header.Set("content-type", "application/json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("anthropic: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("anthropic: reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("anthropic: %s", apiErrorMessage(resp.Status, respBody))
	}

	var parsed struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("anthropic: parsing response: %w", err)
	}

	var text string
	for _, block := range parsed.Content {
		if block.Type == "text" {
			text += block.Text
		}
	}
	if text == "" {
		return "", fmt.Errorf("anthropic: response had no text content")
	}
	return text, nil
}

// apiErrorMessage extracts the API's own error message from a non-200
// response body, falling back to the raw body if it isn't the expected
// {"error": {"message": "..."}} shape.
func apiErrorMessage(status string, body []byte) string {
	var apiErr struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &apiErr) == nil && apiErr.Error.Message != "" {
		return fmt.Sprintf("%s: %s", status, apiErr.Error.Message)
	}
	return fmt.Sprintf("%s: %s", status, string(body))
}

func (c *Client) model() string {
	if c.Model != "" {
		return c.Model
	}
	return DefaultModel
}

func (c *Client) baseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return defaultBaseURL
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: defaultTimeout}
}
