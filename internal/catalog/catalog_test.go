package catalog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sample is an invented catalog: real endpoints and model ids are deployment
// facts and never belong in the repo, fixtures included.
func sample() []catwalk.Provider {
	return []catwalk.Provider{
		{
			Name: "Compatible", ID: "compatible", Type: catwalk.TypeOpenAICompat,
			APIEndpoint: "https://compatible.example.test/v1", APIKey: "$COMPATIBLE_API_KEY",
			DefaultLargeModelID: "big",
			Models:              []catwalk.Model{{ID: "small", Name: "Small"}, {ID: "big", Name: "Big"}},
		},
		{
			Name: "Placeholder", ID: "placeholder", Type: catwalk.TypeOpenAI,
			APIEndpoint: "$PLACEHOLDER_API_ENDPOINT",
			Models:      []catwalk.Model{{ID: "only", Name: "Only"}},
		},
		{
			Name: "Messages", ID: "messages", Type: catwalk.TypeAnthropic,
			APIEndpoint: "https://messages.example.test",
			Models:      []catwalk.Model{{ID: "m", Name: "M"}},
		},
		{Name: "Routed", ID: "routed", Type: catwalk.TypeOpenRouter, APIEndpoint: "https://routed.example.test/v1"},
		{Name: "Edged", ID: "edged", Type: catwalk.TypeVercel, APIEndpoint: "https://edged.example.test/v1"},
		{Name: "Gemini", ID: "gemini", Type: catwalk.TypeGoogle, APIEndpoint: "https://g.example.test"},
		{Name: "Azure", ID: "azure", Type: catwalk.TypeAzure},
		{Name: "Bedrock", ID: "bedrock", Type: catwalk.TypeBedrock},
		{Name: "Vertex", ID: "vertex", Type: catwalk.TypeVertexAI},
	}
}

func serve(t *testing.T, providers []catwalk.Provider) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v2/providers", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(providers))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func names(providers []Provider) []string {
	out := make([]string, len(providers))
	for i, p := range providers {
		out[i] = p.Name
	}
	return out
}

func TestLoadFetchesMapsAndCaches(t *testing.T) {
	dir := t.TempDir()
	srv := serve(t, sample())

	got, err := Client{URL: srv.URL, Dir: dir}.Load(context.Background())
	require.NoError(t, err)
	assert.Equal(t, SourceNetwork, got.Source)
	require.NoError(t, got.FetchErr)

	assert.Equal(t, []string{"Compatible", "Placeholder", "Messages", "Routed", "Edged"}, names(got.Providers),
		"google, azure, bedrock and vertex speak neither of the two supported protocols")
	assert.Equal(t, "openai", got.Providers[0].Adapter)
	assert.Equal(t, "https://compatible.example.test/v1", got.Providers[0].BaseURL)
	assert.Equal(t, "anthropic", got.Providers[2].Adapter)
	assert.Equal(t, "openai", got.Providers[3].Adapter, "openrouter speaks the OpenAI wire protocol")
	assert.Equal(t, "openai", got.Providers[4].Adapter, "so does vercel")

	info, err := os.Stat(filepath.Join(dir, cacheFile))
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		// Windows has no Unix permission bits; every file reads as 0666.
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "the atomic write leaves no temporary file behind")
}

func TestLoadPutsTheProvidersDefaultModelFirst(t *testing.T) {
	srv := serve(t, sample())
	got, err := Client{URL: srv.URL, Dir: t.TempDir()}.Load(context.Background())
	require.NoError(t, err)
	require.Len(t, got.Providers[0].Models, 2)
	assert.Equal(t, "big", got.Providers[0].Models[0].ID, "the provider's own recommendation leads")
}

// catwalk names the variable a provider authenticates with, or carries none
// at all. That is the only signal for whether login must insist on a key, and
// an accepted empty key means a 401 several prompts later.
func TestLoadCarriesWhetherAProviderNeedsAKey(t *testing.T) {
	srv := serve(t, sample())
	got, err := Client{URL: srv.URL, Dir: t.TempDir()}.Load(context.Background())
	require.NoError(t, err)
	assert.True(t, got.Providers[0].NeedsKey, "a provider naming an API key variable needs one")
	assert.False(t, got.Providers[3].NeedsKey, "one naming none does not")
}

func TestLoadBlanksAPlaceholderEndpoint(t *testing.T) {
	srv := serve(t, sample())
	got, err := Client{URL: srv.URL, Dir: t.TempDir()}.Load(context.Background())
	require.NoError(t, err)
	assert.Empty(t, got.Providers[1].BaseURL, "a $VAR placeholder is not a URL; login must ask for one")
}

func TestLoadFallsBackToTheCache(t *testing.T) {
	dir := t.TempDir()
	srv := serve(t, sample())
	_, err := Client{URL: srv.URL, Dir: dir}.Load(context.Background())
	require.NoError(t, err)
	srv.Close()

	got, err := Client{URL: srv.URL, Dir: dir}.Load(context.Background())
	require.NoError(t, err)
	assert.Equal(t, SourceCache, got.Source)
	require.Error(t, got.FetchErr, "login says why it is showing cached providers")
	assert.Equal(t, []string{"Compatible", "Placeholder", "Messages", "Routed", "Edged"}, names(got.Providers))
}

func TestLoadNotModifiedUsesTheCache(t *testing.T) {
	dir := t.TempDir()
	srv := serve(t, sample())
	_, err := Client{URL: srv.URL, Dir: dir}.Load(context.Background())
	require.NoError(t, err)
	srv.Close()

	notModified := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}))
	t.Cleanup(notModified.Close)

	got, err := Client{URL: notModified.URL, Dir: dir}.Load(context.Background())
	require.NoError(t, err)
	assert.Equal(t, SourceCache, got.Source)
	assert.NoError(t, got.FetchErr, "an unchanged catalog is not a failure")
	assert.Len(t, got.Providers, 5)
}

func TestLoadWithoutNetworkOrCache(t *testing.T) {
	srv := serve(t, sample())
	srv.Close()
	_, err := Client{URL: srv.URL, Dir: t.TempDir()}.Load(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no cached catalog")
}

func TestEndpointModels(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		_, err := w.Write([]byte(`{"object":"list","data":[{"id":"zulu"},{"id":"alpha"}]}`))
		assert.NoError(t, err)
	}))
	t.Cleanup(srv.Close)

	models, err := EndpointModels(context.Background(), srv.URL+"/v1/", "k")
	require.NoError(t, err)
	assert.Equal(t, []Model{{ID: "alpha", Name: "alpha"}, {ID: "zulu", Name: "zulu"}}, models, "ids are sorted")
	assert.Equal(t, "/v1/models", gotPath)
	assert.Equal(t, "Bearer k", gotAuth)

	_, err = EndpointModels(context.Background(), srv.URL, "")
	require.NoError(t, err)
	assert.Empty(t, gotAuth, "a local endpoint needs no key and must not be sent an empty one")
}

func TestEndpointModelsRejectsAnswersItCannotUse(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"server error": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
		"not json":     func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<html>")) },
		"no models":    func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"data":[]}`)) },
	}
	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(handler)
			t.Cleanup(srv.Close)
			_, err := EndpointModels(context.Background(), srv.URL, "")
			require.Error(t, err)
		})
	}
}
