package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"standup/internal/catalog"
	"standup/internal/config"
)

// scriptedUI answers queued prompts, so the whole flow runs without a
// terminal. It applies each caller's Validate the way promptui does, which is
// what makes the validation branches testable at all.
type scriptedUI struct {
	answers  []string
	abortAt  int // index of the prompt the user quits at; -1 for none
	calls    []string
	items    [][]string
	rejected []string
}

func (s *scriptedUI) next(kind string) (string, error) {
	if len(s.calls) == s.abortAt {
		s.calls = append(s.calls, kind)
		return "", errLoginAborted
	}
	s.calls = append(s.calls, kind)
	if len(s.answers) == 0 {
		return "", errLoginAborted // promptui reports an exhausted stdin as EOF
	}
	answer := s.answers[0]
	s.answers = s.answers[1:]
	return answer, nil
}

func (s *scriptedUI) Select(_ string, items []string) (int, error) {
	s.items = append(s.items, items)
	answer, err := s.next("select")
	if err != nil {
		return 0, err
	}
	for i, item := range items {
		if item == answer {
			return i, nil
		}
	}
	return 0, errors.New("scriptedUI: no item matching " + answer)
}

func (s *scriptedUI) Input(_ string, validate func(string) error) (string, error) {
	return s.validated("input", validate)
}

func (s *scriptedUI) Secret(_ string, validate func(string) error) (string, error) {
	return s.validated("secret", validate)
}

// validated models promptui: an answer the Validate func rejects is not an
// error, it is re-asked. An E2E run proved the earlier double wrong — it
// returned the validation error, a path the real binary can never take.
func (s *scriptedUI) validated(kind string, validate func(string) error) (string, error) {
	for {
		answer, err := s.next(kind)
		if err != nil {
			return "", err
		}
		if validate == nil {
			return answer, nil
		}
		if err := validate(answer); err == nil {
			return answer, nil
		}
		s.rejected = append(s.rejected, answer)
	}
}

func loginHarness(t *testing.T, script *scriptedUI, providers []catalog.Provider) (*cobra.Command, *bytes.Buffer, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("STANDUP_CONFIG_DIR", dir)
	for _, key := range []string{"OPENAI_BASE_URL", "OPENAI_MODEL", "OPENAI_API_KEY", "ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL", "ANTHROPIC_API_KEY"} {
		t.Setenv(key, "")
	}
	swap(t, &loginInteractive, func() bool { return true })
	swap(t, &ui, loginUI(script))
	swap(t, &catalogLoad, func(context.Context, string) (catalog.Catalog, error) {
		return catalog.Catalog{Providers: providers, Source: catalog.SourceNetwork}, nil
	})
	swap(t, &modelCheck, func(context.Context, config.Config) error { return nil })
	_, root, buf := newHarness(t, &fakeAssistant{})
	return root, buf, dir
}

func swap[T any](t *testing.T, target *T, replacement T) {
	t.Helper()
	old := *target
	*target = replacement
	t.Cleanup(func() { *target = old })
}

func compatible() []catalog.Provider {
	return []catalog.Provider{{
		Name: "Compatible", ID: "compatible", Adapter: "openai",
		BaseURL: "https://compatible.example.test/v1",
		Models:  []catalog.Model{{ID: "big", Name: "Big"}, {ID: "small", Name: "Small"}},
	}}
}

func env(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ".env"))
	require.NoError(t, err)
	return string(b)
}

func TestLoginRefusesWithoutATerminal(t *testing.T) {
	script := &scriptedUI{abortAt: -1}
	root, _, dir := loginHarness(t, script, compatible())
	swap(t, &loginInteractive, func() bool { return false })

	root.SetArgs([]string{"login"})
	err := root.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config set", "the scripted alternative is named")
	_, statErr := os.Stat(filepath.Join(dir, ".env"))
	assert.True(t, os.IsNotExist(statErr), "a refused login writes nothing")
}

func TestLoginSavesAndVerifiesACatalogProvider(t *testing.T) {
	script := &scriptedUI{answers: []string{"Compatible", "sk-secret-value", "Small (small)"}, abortAt: -1}
	root, buf, dir := loginHarness(t, script, compatible())

	root.SetArgs([]string{"login"})
	require.NoError(t, root.Execute())

	contents := env(t, dir)
	assert.Contains(t, contents, "OPENAI_BASE_URL=https://compatible.example.test/v1")
	assert.Contains(t, contents, "OPENAI_MODEL=small")
	assert.Contains(t, contents, "OPENAI_API_KEY=sk-secret-value")
	info, err := os.Stat(filepath.Join(dir, ".env"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	assert.Contains(t, buf.String(), "ok   provider openai in "+filepath.Join(dir, "config.yaml"))
	assert.Contains(t, buf.String(), "ok   endpoint and model in "+filepath.Join(dir, ".env"))
	assert.Contains(t, buf.String(), "ok   model answers")
	assert.NotContains(t, buf.String(), "sk-secret-value", "a pasted key is never echoed")
	assert.Equal(t, []string{"select", "secret", "select"}, script.calls, "a catalogued endpoint is not asked for")
}

func TestLoginAsksForABaseURLWhenTheCatalogHasNone(t *testing.T) {
	providers := compatible()
	providers[0].BaseURL = ""
	script := &scriptedUI{answers: []string{"Compatible", "https://typed.example.test/v1", "k", "Big (big)"}, abortAt: -1}
	root, _, dir := loginHarness(t, script, providers)

	root.SetArgs([]string{"login"})
	require.NoError(t, root.Execute())
	assert.Contains(t, env(t, dir), "OPENAI_BASE_URL=https://typed.example.test/v1")
	assert.Equal(t, []string{"select", "input", "secret", "select"}, script.calls)
}

func TestLoginKeepsAnEmptyKeyOutOfTheEnvFile(t *testing.T) {
	script := &scriptedUI{answers: []string{"Compatible", "", "Big (big)"}, abortAt: -1}
	root, _, dir := loginHarness(t, script, compatible())

	root.SetArgs([]string{"login"})
	require.NoError(t, root.Execute())
	assert.NotContains(t, env(t, dir), "OPENAI_API_KEY", "a local endpoint needs no key")
}

func TestLoginReAsksForAnAnthropicKey(t *testing.T) {
	providers := compatible()
	providers[0].Adapter = "anthropic"
	script := &scriptedUI{answers: []string{"Compatible", "", "sk-ant", "Big (big)"}, abortAt: -1}
	root, _, dir := loginHarness(t, script, providers)

	root.SetArgs([]string{"login"})
	require.NoError(t, root.Execute())
	assert.Equal(t, []string{""}, script.rejected, "the anthropic provider cannot go without a key")
	assert.Contains(t, env(t, dir), "ANTHROPIC_API_KEY=sk-ant")
}

func TestLoginListsACustomEndpointsModels(t *testing.T) {
	script := &scriptedUI{answers: []string{customEndpointItem, "http://localhost:11434/v1", "", "served-model"}, abortAt: -1}
	root, _, dir := loginHarness(t, script, compatible())
	swap(t, &endpointModels, func(context.Context, string, string) ([]catalog.Model, error) {
		return []catalog.Model{{ID: "served-model", Name: "served-model"}}, nil
	})

	root.SetArgs([]string{"login"})
	require.NoError(t, root.Execute())
	contents := env(t, dir)
	assert.Contains(t, contents, "OPENAI_BASE_URL=http://localhost:11434/v1")
	assert.Contains(t, contents, "OPENAI_MODEL=served-model")
	assert.Equal(t, []string{"select", "input", "secret", "select"}, script.calls)
}

func TestLoginFallsBackToATypedModelID(t *testing.T) {
	script := &scriptedUI{answers: []string{customEndpointItem, "http://localhost:11434/v1", "", "hand-typed"}, abortAt: -1}
	root, buf, dir := loginHarness(t, script, compatible())
	swap(t, &endpointModels, func(context.Context, string, string) ([]catalog.Model, error) {
		return nil, errors.New("connection refused")
	})

	root.SetArgs([]string{"login"})
	require.NoError(t, root.Execute())
	assert.Contains(t, env(t, dir), "OPENAI_MODEL=hand-typed")
	assert.Contains(t, buf.String(), "note endpoint did not list models")
	assert.Equal(t, []string{"select", "input", "secret", "input"}, script.calls)
}

func TestLoginReAsksForABaseURLWithNoScheme(t *testing.T) {
	script := &scriptedUI{answers: []string{customEndpointItem, "localhost:11434", "http://localhost:11434/v1", "", "typed"}, abortAt: -1}
	root, _, dir := loginHarness(t, script, compatible())
	swap(t, &endpointModels, func(context.Context, string, string) ([]catalog.Model, error) {
		return nil, errors.New("connection refused")
	})

	root.SetArgs([]string{"login"})
	require.NoError(t, root.Execute())
	assert.Equal(t, []string{"localhost:11434"}, script.rejected, "a host with no scheme is re-asked, not fatal")
	assert.Contains(t, env(t, dir), "OPENAI_BASE_URL=http://localhost:11434/v1")
}

func TestLoginWritesNothingWhenStdinRunsOut(t *testing.T) {
	script := &scriptedUI{answers: []string{"Compatible"}, abortAt: -1}
	root, _, dir := loginHarness(t, script, compatible())

	root.SetArgs([]string{"login"})
	require.NoError(t, root.Execute(), "an exhausted stdin reads as a quit, like Ctrl-D")
	_, statErr := os.Stat(filepath.Join(dir, ".env"))
	assert.True(t, os.IsNotExist(statErr))
}

func TestLoginQuitsCleanlyAtEveryStep(t *testing.T) {
	for step := 0; step < 3; step++ {
		t.Run(strconv.Itoa(step), func(t *testing.T) {
			script := &scriptedUI{answers: []string{"Compatible", "k", "Big (big)"}, abortAt: step}
			root, _, dir := loginHarness(t, script, compatible())

			root.SetArgs([]string{"login"})
			require.NoError(t, root.Execute(), "quitting is not an error")
			_, statErr := os.Stat(filepath.Join(dir, ".env"))
			assert.True(t, os.IsNotExist(statErr), "an abandoned login writes nothing")
		})
	}
}

func TestLoginKeepsSettingsWhenVerificationFails(t *testing.T) {
	script := &scriptedUI{answers: []string{"Compatible", "sk-live", "Big (big)"}, abortAt: -1}
	root, buf, dir := loginHarness(t, script, compatible())
	swap(t, &modelCheck, func(context.Context, config.Config) error {
		return errors.New("endpoint rejected the request — check OPENAI_MODEL")
	})

	root.SetArgs([]string{"login"})
	require.Error(t, root.Execute())
	assert.Contains(t, env(t, dir), "OPENAI_MODEL=big", "what the user typed is not discarded")
	assert.Contains(t, buf.String(), "fail model answers")
	assert.Contains(t, buf.String(), "settings kept")
}

func TestLoginSaysWhenTheCatalogIsCached(t *testing.T) {
	script := &scriptedUI{answers: []string{"Compatible", "k", "Big (big)"}, abortAt: -1}
	root, buf, _ := loginHarness(t, script, compatible())
	swap(t, &catalogLoad, func(context.Context, string) (catalog.Catalog, error) {
		return catalog.Catalog{Providers: compatible(), Source: catalog.SourceCache, FetchErr: errors.New("no route to host")}, nil
	})

	root.SetArgs([]string{"login"})
	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "note using the cached model catalog")
	assert.Contains(t, buf.String(), "no route to host")
}

func TestLoginOffersTheCustomEndpointWithNoCatalogAtAll(t *testing.T) {
	script := &scriptedUI{answers: []string{customEndpointItem, "http://localhost:11434/v1", "", "typed"}, abortAt: -1}
	root, buf, dir := loginHarness(t, script, nil)
	swap(t, &catalogLoad, func(context.Context, string) (catalog.Catalog, error) {
		return catalog.Catalog{}, errors.New("no route to host, and no cached catalog")
	})
	swap(t, &endpointModels, func(context.Context, string, string) ([]catalog.Model, error) {
		return nil, errors.New("connection refused")
	})

	root.SetArgs([]string{"login"})
	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "note no model catalog available")
	assert.Contains(t, env(t, dir), "OPENAI_MODEL=typed")
	require.Len(t, script.items, 1)
	assert.Equal(t, []string{customEndpointItem}, script.items[0], "the custom endpoint is always selectable")
}

func TestLoginWarnsWhenTheEnvironmentOverridesWhatItWrote(t *testing.T) {
	script := &scriptedUI{answers: []string{"Compatible", "k", "Big (big)"}, abortAt: -1}
	root, buf, _ := loginHarness(t, script, compatible())
	t.Setenv("OPENAI_BASE_URL", "http://elsewhere.example.test/v1")

	root.SetArgs([]string{"login"})
	require.NoError(t, root.Execute())
	assert.Contains(t, buf.String(), "note OPENAI_BASE_URL is set in your environment and wins over")
	assert.Contains(t, buf.String(), "run: unset OPENAI_BASE_URL")
}

// Three shadowed variables are one problem, not three: a line each buried the
// one thing to do under repetition of the same sentence.
func TestLoginWarnsOnceForEveryShadowedSetting(t *testing.T) {
	script := &scriptedUI{answers: []string{"Compatible", "k", "Big (big)"}, abortAt: -1}
	root, buf, _ := loginHarness(t, script, compatible())
	t.Setenv("OPENAI_BASE_URL", "http://elsewhere.example.test/v1")
	t.Setenv("OPENAI_MODEL", "other")
	t.Setenv("OPENAI_API_KEY", "other")

	root.SetArgs([]string{"login"})
	require.NoError(t, root.Execute())
	out := buf.String()
	assert.Equal(t, 1, strings.Count(out, "note "), "one note, not one per variable")
	assert.Contains(t, out, "OPENAI_BASE_URL, OPENAI_MODEL and OPENAI_API_KEY are set in your environment")
	assert.Contains(t, out, "run: unset OPENAI_BASE_URL OPENAI_MODEL OPENAI_API_KEY")
}

func TestJoinWords(t *testing.T) {
	assert.Equal(t, "A", joinWords([]string{"A"}))
	assert.Equal(t, "A and B", joinWords([]string{"A", "B"}))
	assert.Equal(t, "A, B and C", joinWords([]string{"A", "B", "C"}))
}

func TestLoginTrimsAPastedRequestPathFromTheBaseURL(t *testing.T) {
	script := &scriptedUI{answers: []string{customEndpointItem, "https://api.example.test/v1/models", "k", "served"}, abortAt: -1}
	root, buf, dir := loginHarness(t, script, compatible())
	swap(t, &endpointModels, func(_ context.Context, base, _ string) ([]catalog.Model, error) {
		assert.Equal(t, "https://api.example.test/v1", base, "the endpoint is asked at the root")
		return []catalog.Model{{ID: "served", Name: "served"}}, nil
	})

	root.SetArgs([]string{"login"})
	require.NoError(t, root.Execute())
	assert.Empty(t, script.rejected, "a pasted request path is trimmed, never re-asked")
	assert.Contains(t, buf.String(), "note https://api.example.test/v1/models is a request path; using https://api.example.test/v1")
	assert.Contains(t, env(t, dir), "OPENAI_BASE_URL=https://api.example.test/v1\n")
}
