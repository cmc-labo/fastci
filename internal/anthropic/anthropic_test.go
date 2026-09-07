package anthropic_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hpscript/fastci/internal/anthropic"
)

func TestCreateMessageSendsExpectedRequestAndParsesResponse(t *testing.T) {
	var gotPath, gotAPIKey, gotVersion string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("content-type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]string{
				{"type": "text", "text": "likely cause: nil pointer in foo.go"},
			},
		})
	}))
	defer srv.Close()

	c := &anthropic.Client{APIKey: "test-key", Model: "claude-sonnet-5", BaseURL: srv.URL}
	text, err := c.CreateMessage(context.Background(), "you are a helpful assistant", "why did this fail?", 512)
	if err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	if text != "likely cause: nil pointer in foo.go" {
		t.Errorf("text = %q, want the mocked response text", text)
	}
	if gotPath != "/v1/messages" {
		t.Errorf("path = %q, want /v1/messages", gotPath)
	}
	if gotAPIKey != "test-key" {
		t.Errorf("x-api-key header = %q, want test-key", gotAPIKey)
	}
	if gotVersion == "" {
		t.Error("anthropic-version header missing")
	}
	if gotBody["model"] != "claude-sonnet-5" {
		t.Errorf("request model = %v, want claude-sonnet-5", gotBody["model"])
	}
	if gotBody["system"] != "you are a helpful assistant" {
		t.Errorf("request system = %v, want the given system prompt", gotBody["system"])
	}
}

func TestCreateMessageMissingAPIKey(t *testing.T) {
	c := &anthropic.Client{}
	if _, err := c.CreateMessage(context.Background(), "", "hello", 100); err == nil {
		t.Error("CreateMessage with no APIKey should return an error")
	}
}

func TestCreateMessageSurfacesAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"type": "authentication_error", "message": "invalid x-api-key"},
		})
	}))
	defer srv.Close()

	c := &anthropic.Client{APIKey: "bad-key", BaseURL: srv.URL}
	_, err := c.CreateMessage(context.Background(), "", "hello", 100)
	if err == nil {
		t.Fatal("CreateMessage should return an error on a non-200 response")
	}
	if !strings.Contains(err.Error(), "invalid x-api-key") {
		t.Errorf("error = %q, want it to surface the API's own error message", err.Error())
	}
}

func TestCreateMessageDefaultModel(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]string{{"type": "text", "text": "ok"}},
		})
	}))
	defer srv.Close()

	c := &anthropic.Client{APIKey: "k", BaseURL: srv.URL} // Model left empty
	if _, err := c.CreateMessage(context.Background(), "", "hi", 10); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}
	if gotBody["model"] != anthropic.DefaultModel {
		t.Errorf("request model = %v, want default %q", gotBody["model"], anthropic.DefaultModel)
	}
}
