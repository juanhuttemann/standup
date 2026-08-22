// Package catalog fetches the model catalog that login's provider list is
// built from, caches it on disk, and lists what an OpenAI-compatible endpoint
// serves. Endpoints and model ids are deployment facts: they are fetched at
// run time, never committed.
package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"charm.land/catwalk/pkg/catwalk"
)

// URL is the hosted catalog. catwalk.New() defaults to localhost, so the
// address is always passed explicitly.
const URL = "https://catwalk.charm.land"

const (
	cacheFile    = "providers.json"
	fetchTimeout = 10 * time.Second
	// maxModelsBytes bounds an endpoint's model list the way internal/update
	// bounds a download: a hostile or broken host cannot exhaust memory.
	maxModelsBytes = 4 << 20
)

// Provider is a catwalk provider reduced to what login needs and already
// mapped onto a standup adapter. Unsupported protocols never appear.
type Provider struct {
	Name    string
	ID      string
	Adapter string // "openai" or "anthropic"
	BaseURL string // empty when catwalk carries a $VAR placeholder
	Models  []Model
}

// Model is one selectable model.
type Model struct{ ID, Name string }

// Source records which answer a Catalog came from.
type Source int

// The two places a catalog can come from.
const (
	SourceNetwork Source = iota + 1
	SourceCache
)

// Catalog is the provider list plus its provenance, so login can say it is
// showing cached providers instead of presenting stale ones as current.
type Catalog struct {
	Providers []Provider
	Source    Source
	FetchErr  error // why the network answer was not used; nil when it was
}

// Client fetches and caches the catalog. URL and Dir are fields so tests
// point at an httptest server and a temporary directory.
type Client struct {
	URL string
	Dir string
}

// Load prefers a live fetch and falls back to the cached catalog. It fails
// only when neither answers; login still offers its custom-endpoint path
// then, because a self-hosted endpoint is in no catalog anyway.
func Load(ctx context.Context, dir string) (Catalog, error) {
	return Client{URL: URL, Dir: dir}.Load(ctx)
}

func (c Client) Load(ctx context.Context) (Catalog, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	cached, cacheErr := c.readCache()
	etag := ""
	if cacheErr == nil {
		etag = catwalk.Etag(cached)
	}

	fetched, fetchErr := catwalk.NewWithURL(c.URL).GetProviders(ctx, etag)
	switch {
	case fetchErr == nil:
		if err := c.writeCache(fetched); err != nil {
			return Catalog{}, err
		}
		return Catalog{Providers: supported(fetched), Source: SourceNetwork}, nil
	case errors.Is(fetchErr, catwalk.ErrNotModified) && cacheErr == nil:
		providers, err := decode(cached)
		if err != nil {
			return Catalog{}, err
		}
		return Catalog{Providers: providers, Source: SourceCache}, nil
	case cacheErr == nil:
		providers, err := decode(cached)
		if err != nil {
			return Catalog{}, err
		}
		return Catalog{Providers: providers, Source: SourceCache, FetchErr: fetchErr}, nil
	}
	return Catalog{}, fmt.Errorf("%w, and no cached catalog in %s: %w", fetchErr, c.Dir, cacheErr)
}

func decode(b []byte) ([]Provider, error) {
	var providers []catwalk.Provider
	if err := json.Unmarshal(b, &providers); err != nil {
		return nil, fmt.Errorf("read cached catalog: %w", err)
	}
	return supported(providers), nil
}

// supported keeps the providers standup can actually talk to. catwalk's Type
// is the wire protocol, which is exactly the question the two adapters ask.
func supported(providers []catwalk.Provider) []Provider {
	out := make([]Provider, 0, len(providers))
	for _, p := range providers {
		adapter, ok := adapterFor(p.Type)
		if !ok {
			continue
		}
		out = append(out, Provider{
			Name:    p.Name,
			ID:      string(p.ID),
			Adapter: adapter,
			BaseURL: endpointOf(p),
			Models:  models(p),
		})
	}
	return out
}

func adapterFor(t catwalk.Type) (string, bool) {
	switch t {
	case catwalk.TypeOpenAI, catwalk.TypeOpenAICompat, catwalk.TypeOpenRouter, catwalk.TypeVercel:
		return "openai", true
	case catwalk.TypeAnthropic:
		return "anthropic", true
	}
	return "", false
}

// endpointOf blanks the placeholders catwalk carries for providers whose SDK
// has a built-in default (for example "$OPENAI_API_ENDPOINT"). standup needs
// a real base URL, so login asks for those.
func endpointOf(p catwalk.Provider) string {
	if strings.HasPrefix(p.APIEndpoint, "$") {
		return ""
	}
	return p.APIEndpoint
}

// models leads with the provider's own recommendation, so the default row of
// the picker is the model that provider would pick.
func models(p catwalk.Provider) []Model {
	out := make([]Model, 0, len(p.Models))
	for _, m := range p.Models {
		out = append(out, Model{ID: m.ID, Name: m.Name})
	}
	slices.SortStableFunc(out, func(a, b Model) int {
		switch p.DefaultLargeModelID {
		case a.ID:
			return -1
		case b.ID:
			return 1
		}
		return 0
	})
	return out
}

func (c Client) cachePath() string { return filepath.Join(c.Dir, cacheFile) }

func (c Client) readCache() ([]byte, error) { return os.ReadFile(c.cachePath()) }

// writeCache replaces the cache atomically: a torn file would be read back as
// a corrupt catalog on the next offline run.
func (c Client) writeCache(providers []catwalk.Provider) error {
	b, err := json.Marshal(providers)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(c.Dir, cacheFile+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(b); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := tmp.Chmod(0o600); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return replaceCatalogFile(c.cachePath(), tmp.Name())
}

// EndpointModels asks an OpenAI-compatible endpoint what it serves. Local and
// self-hosted endpoints are in no catalog, and the machine itself is the only
// authority on what has actually been pulled.
func EndpointModels(ctx context.Context, baseURL, apiKey string) ([]Model, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := (&http.Client{Timeout: fetchTimeout}).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("endpoint answered %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxModelsBytes))
	if err != nil {
		return nil, err
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("endpoint did not answer with a model list: %w", err)
	}
	models := make([]Model, 0, len(list.Data))
	for _, m := range list.Data {
		if m.ID != "" {
			models = append(models, Model{ID: m.ID, Name: m.ID})
		}
	}
	if len(models) == 0 {
		return nil, errors.New("endpoint listed no models")
	}
	slices.SortFunc(models, func(a, b Model) int { return strings.Compare(a.ID, b.ID) })
	return models, nil
}
