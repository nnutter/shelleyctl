package models_test

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nnutter/shelleyctl/internal/models"
)

func serverPort(t *testing.T, server *httptest.Server) int {
	t.Helper()
	_, value, err := net.SplitHostPort(server.Listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(value)
	require.NoError(t, err)
	return port
}

func TestConfigureResumesWithoutDuplicatesOrResettingTuning(t *testing.T) {
	var providerCalls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { providerCalls.Add(1) }))
	defer provider.Close()
	// Independent API fixture, not the client's writable model type.
	stored := fmt.Sprintf(`[{"model_id":"tuned","display_name":"My tuned model","provider_type":"openai-responses","endpoint":%q,"api_key":"old-fixture-key","model_name":"one","max_tokens":731,"tags":"slug","reasoning_effort":"high","reasoning_replay":"none","image_support":"yes","reasoning_support":"yes","reasoning_map":"{\"high\":\"deep\"}"},{"model_id":"unrelated","provider_type":"gemini","endpoint":"https://other.example","model_name":"other"}]`, provider.URL)
	puts, posts := 0, 0
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodGet:
			_, _ = io.WriteString(w, stored)
		case http.MethodPut:
			puts++
			var body map[string]jsontext.Value
			if !assert.NoError(t, json.UnmarshalRead(r.Body, &body)) {
				return
			}
			if r.URL.Path == "/api/custom-models/created" {
				assert.JSONEq(t, `"two"`, string(body["model_name"]))
				assert.JSONEq(t, `"new-fixture-key"`, string(body["api_key"]))
				return
			}
			assert.Equal(t, "/api/custom-models/tuned", r.URL.Path)
			for field, want := range map[string]string{"display_name": `"My tuned model"`, "api_key": `"new-fixture-key"`, "max_tokens": "731", "tags": `"slug"`, "reasoning_effort": `"high"`, "reasoning_replay": `"none"`, "image_support": `"yes"`, "reasoning_support": `"yes"`, "reasoning_map": `"{\"high\":\"deep\"}"`} {
				assert.JSONEq(t, want, string(body[field]), field)
			}
		case http.MethodPost:
			posts++
			var body map[string]jsontext.Value
			if !assert.NoError(t, json.UnmarshalRead(r.Body, &body)) {
				return
			}
			assert.JSONEq(t, `"two"`, string(body["model_name"]))
			assert.JSONEq(t, `"new-fixture-key"`, string(body["api_key"]))
			body["model_id"] = jsontext.Value(`"created"`)
			data, err := json.Marshal(body)
			if !assert.NoError(t, err) {
				return
			}
			stored = strings.TrimSuffix(stored, "]") + "," + string(data) + "]"
			// The save completed, but the caller did not receive success.
			http.Error(w, "new-fixture-key", http.StatusServiceUnavailable)
		default:
			assert.Fail(t, "unexpected model operation", r.Method)
		}
	}))
	defer server.Close()
	config := models.Config{APIURL: provider.URL + "/", APIType: "openai-responses", APIKey: "new-fixture-key", Names: []string{"one", "two", "two"}}
	err := models.Configure(t.Context(), serverPort(t, server), config)
	require.ErrorContains(t, err, "HTTP 503")
	require.NotContains(t, err.Error(), config.APIKey)
	require.NoError(t, models.Configure(t.Context(), serverPort(t, server), config))
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 3, puts)
	require.Equal(t, 1, posts, "retry discovers the saved model instead of creating another")
	require.Zero(t, providerCalls.Load(), "saving models must not spend provider credits")
}

func TestConfigureRejectsAmbiguousModelsBeforeMutations(t *testing.T) {
	var mutations atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mutations.Add(1)
		}
		_, _ = io.WriteString(w, `[{"model_id":"a","provider_type":"openai","endpoint":"https://api.example/v1","model_name":"one"},{"model_id":"b","provider_type":"openai","endpoint":"https://api.example/v1/","model_name":"one"}]`)
	}))
	defer server.Close()
	err := models.Configure(t.Context(), serverPort(t, server), models.Config{APIURL: "https://api.example/v1", APIType: "openai", APIKey: "fixture-key", Names: []string{"new", "one"}})
	require.ErrorContains(t, err, "multiple matching")
	require.Zero(t, mutations.Load())
}

func TestConfigureDoesNotFollowRedirectsOrExposeResponseSecrets(t *testing.T) {
	var leaked atomic.Int32
	foreign := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked.Add(1) }))
	defer foreign.Close()
	for _, mode := range []string{"redirect", "error", "invalid JSON", "oversized list"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					switch mode {
					case "invalid JSON":
						_, _ = io.WriteString(w, `{"api_key":"fixture-key"`)
					case "oversized list":
						_, _ = io.WriteString(w, strings.Repeat(" ", (4<<20)+1))
					default:
						_, _ = io.WriteString(w, "[]")
					}
					return
				}
				if mode == "redirect" {
					http.Redirect(w, r, foreign.URL, http.StatusTemporaryRedirect)
					return
				}
				http.Error(w, "fixture-key", http.StatusBadRequest)
			}))
			defer server.Close()
			err := models.Configure(t.Context(), serverPort(t, server), models.Config{APIURL: "https://api.example/v1", APIType: "openai", APIKey: "fixture-key", Names: []string{"one"}})
			require.Error(t, err)
			require.NotContains(t, err.Error(), "fixture-key")
		})
	}
	require.Zero(t, leaked.Load())
}
