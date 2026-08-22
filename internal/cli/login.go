package cli

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/manifoldco/promptui"
	"github.com/manifoldco/promptui/list"
	"github.com/spf13/cobra"

	"standup/internal/catalog"
	"standup/internal/config"
)

// customEndpointItem is the last row of the provider list. Local and
// self-hosted servers are in no catalog, so typing an endpoint is a
// first-class choice rather than a fallback.
const customEndpointItem = "custom OpenAI-compatible endpoint (Ollama, LM Studio, self-hosted)"

// baseURLLabel names the root rather than a request path: pasting the models
// or completions URL is the mistake this prompt exists to avoid.
const baseURLLabel = "base URL (endpoint root, usually ending in /v1)"

// loginUI is the terminal front end. It is an interface because promptui
// needs a real TTY: the branching around placeholder endpoints, absent model
// lists and quitting mid-flow is only testable behind a seam.
type loginUI interface {
	Select(label string, items []string) (int, error)
	Input(label string, validate func(string) error) (string, error)
	Secret(label string, validate func(string) error) (string, error)
}

// Seams: login must be runnable with no terminal, no network and no endpoint.
var (
	ui               loginUI = promptUI{}
	loginInteractive         = interactive
	catalogLoad              = catalog.Load
	endpointModels           = catalog.EndpointModels
	saveProvider             = config.SaveProvider
)

// errLoginAborted carries a user quit up to the one place that turns it into
// a silent exit 0, so every prompt does not have to repeat the check.
var errLoginAborted = errors.New("login aborted")

type promptUI struct{}

// The default templates are deliberate: promptui's readline ANSI writer
// panics while parsing the 24-bit sequences a custom template would carry
// (see truecolorSupported in color.go).
func (promptUI) Select(label string, items []string) (int, error) {
	i, _, err := (&promptui.Select{
		Label:             label,
		Items:             items,
		Size:              12,
		StartInSearchMode: true,
		Searcher:          searcher(items),
	}).Run()
	return i, err
}

// No Default and no AllowEdit: promptui keeps a rejected entry in the line
// buffer, so the answer to "that is an API path, use X" was appended to the
// bad value rather than replacing it, and the concatenation was saved.
func (promptUI) Input(label string, validate func(string) error) (string, error) {
	return (&promptui.Prompt{Label: label, Validate: validate}).Run()
}

func (promptUI) Secret(label string, validate func(string) error) (string, error) {
	return (&promptui.Prompt{Label: label, Mask: '*', Validate: validate}).Run()
}

// searcher is case-insensitive substring matching, which is all a list of 35
// providers or a provider's 267 models needs.
func searcher(items []string) list.Searcher {
	return func(input string, index int) bool {
		return strings.Contains(strings.ToLower(items[index]), strings.ToLower(input))
	}
}

func loginSelect(label string, items []string) (int, error) {
	i, err := ui.Select(label, items)
	if aborted(err) {
		return 0, errLoginAborted
	}
	return i, err
}

func loginInput(label string, validate func(string) error) (string, error) {
	value, err := ui.Input(label, validate)
	if aborted(err) {
		return "", errLoginAborted
	}
	return value, err
}

func loginSecret(label string, validate func(string) error) (string, error) {
	value, err := ui.Secret(label, validate)
	if aborted(err) {
		return "", errLoginAborted
	}
	return value, err
}

func runLogin(cmd *cobra.Command, d Deps) error {
	if !loginInteractive() {
		return errors.New("login needs a terminal; set the provider with config set instead")
	}
	rep := &reporter{out: cmd.OutOrStdout()}
	sel, err := loginChoose(cmd, rep, loginProviders(cmd, rep))
	if errors.Is(err, errLoginAborted) {
		return rep.err
	}
	if err != nil {
		return err
	}
	return loginApply(cmd, d, rep, sel)
}

// loginProviders never fails: with neither network nor cache the custom
// endpoint is still a complete answer.
func loginProviders(cmd *cobra.Command, rep *reporter) []catalog.Provider {
	loaded, err := fetchCatalog(cmd)
	if err != nil {
		rep.printf("note no model catalog available (%v); type an endpoint instead\n", err)
		return nil
	}
	if loaded.Source == catalog.SourceCache && loaded.FetchErr != nil {
		rep.printf("note using the cached model catalog (fetch failed: %v)\n", loaded.FetchErr)
	}
	return loaded.Providers
}

func fetchCatalog(cmd *cobra.Command) (catalog.Catalog, error) {
	dir, err := config.WriteDir()
	if err != nil {
		return catalog.Catalog{}, err
	}
	var loaded catalog.Catalog
	err = spin("fetching model catalog", func() error {
		loaded, err = catalogLoad(cmd.Context(), dir)
		return err
	})
	return loaded, err
}

func loginChoose(cmd *cobra.Command, rep *reporter, providers []catalog.Provider) (config.ProviderSelection, error) {
	items := make([]string, 0, len(providers)+1)
	for _, p := range providers {
		items = append(items, p.Name)
	}
	items = append(items, customEndpointItem)

	i, err := loginSelect("provider", items)
	if err != nil {
		return config.ProviderSelection{}, err
	}
	if i == len(providers) {
		return loginCustom(cmd, rep)
	}
	return loginCatalogProvider(cmd, rep, providers[i])
}

func loginCustom(cmd *cobra.Command, rep *reporter) (config.ProviderSelection, error) {
	base, err := loginBaseURL(rep)
	if err != nil {
		return config.ProviderSelection{}, err
	}
	key, err := loginKey("openai")
	if err != nil {
		return config.ProviderSelection{}, err
	}
	model, err := loginModel(loginEndpointModels(cmd, rep, base, key))
	if err != nil {
		return config.ProviderSelection{}, err
	}
	return config.ProviderSelection{Provider: "openai", BaseURL: base, Model: model, APIKey: key}, nil
}

// loginEndpointModels asks the endpoint what it serves. A failure is a note,
// not an error: the base URL and key just typed are not thrown away because
// the server does not implement /models.
func loginEndpointModels(cmd *cobra.Command, rep *reporter, base, key string) []catalog.Model {
	var models []catalog.Model
	if err := spin("reading served models", func() error {
		var err error
		models, err = endpointModels(cmd.Context(), base, key)
		return err
	}); err != nil {
		rep.printf("note endpoint did not list models (%v); type the model id\n", err)
		return nil
	}
	return models
}

// loginBaseURL trims a pasted request path rather than rejecting it, and says
// what it used: promptui keeps a rejected entry in the line buffer, so the
// correction is typed onto the end of the bad URL and that is what gets saved.
func loginBaseURL(rep *reporter) (string, error) {
	typed, err := loginInput(baseURLLabel, config.ValidBaseURL)
	if err != nil {
		return "", err
	}
	base, trimmed := config.NormalizeBaseURL(typed)
	if trimmed {
		rep.printf("note %s is a request path; using %s\n", typed, base)
	}
	return base, nil
}

func loginCatalogProvider(cmd *cobra.Command, rep *reporter, p catalog.Provider) (config.ProviderSelection, error) {
	base := p.BaseURL
	if base == "" {
		// catwalk carries a $VAR placeholder for providers whose SDK has a
		// built-in endpoint; standup always needs a real base URL.
		typed, err := loginBaseURL(rep)
		if err != nil {
			return config.ProviderSelection{}, err
		}
		base = typed
	}
	key, err := loginKey(p.Adapter)
	if err != nil {
		return config.ProviderSelection{}, err
	}
	model, err := loginModel(p.Models)
	if err != nil {
		return config.ProviderSelection{}, err
	}
	return config.ProviderSelection{Provider: p.Adapter, BaseURL: base, Model: model, APIKey: key}, nil
}

// loginKey requires a value only where the adapter does: an OpenAI-compatible
// endpoint on localhost needs none, and demanding one would lock those out.
func loginKey(adapter string) (string, error) {
	if adapter == "anthropic" {
		return loginSecret("API key", func(value string) error {
			if value == "" {
				return errors.New("the anthropic provider requires an API key")
			}
			return nil
		})
	}
	return loginSecret("API key (leave empty for a local endpoint)", nil)
}

func loginModel(models []catalog.Model) (string, error) {
	if len(models) == 0 {
		return loginInput("model id", nonEmptyModel)
	}
	items := make([]string, len(models))
	for i, m := range models {
		items[i] = modelItem(m)
	}
	i, err := loginSelect("model", items)
	if err != nil {
		return "", err
	}
	return models[i].ID, nil
}

// joinWords renders "A", "A and B", "A, B and C".
func joinWords(words []string) string {
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
}

func verb(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

func modelItem(m catalog.Model) string {
	if m.Name == "" || m.Name == m.ID {
		return m.ID
	}
	return fmt.Sprintf("%s (%s)", m.Name, m.ID)
}

func nonEmptyModel(value string) error {
	if value == "" {
		return errors.New("a model id is required")
	}
	return nil
}

// loginApply saves, then proves — the same real model call doctor ends on.
// A failed check keeps the settings: the common failures (an unentitled
// model, a rate limit, a flaky network) say nothing about what was typed, and
// discarding a pasted key over one would be the worst possible answer.
func loginApply(cmd *cobra.Command, d Deps, rep *reporter, sel config.ProviderSelection) error {
	shadowed := config.Shadowed(sel)
	path, err := saveProvider(sel)
	if err != nil {
		return err
	}
	// Name both homes: the provider key goes to config.yaml and the endpoint
	// to .env, and a user who cannot find what login wrote cannot fix it.
	rep.printf("ok   provider %s in %s\n", sel.Provider, filepath.Join(filepath.Dir(path), "config.yaml"))
	rep.printf("ok   endpoint and model in %s\n", path)
	if len(shadowed) > 0 {
		// One problem, one note: a line per variable repeated the same
		// sentence three times and buried the one thing to do.
		rep.printf("note %s %s set in your environment and %s over %s\n", joinWords(shadowed),
			verb(len(shadowed), "is", "are"), verb(len(shadowed), "wins", "win"), path)
		rep.printf("     run: unset %s\n", strings.Join(shadowed, " "))
	}

	cfg := d.Config
	cfg.Provider = sel.Provider
	if err := modelCheck(cmd.Context(), cfg); err != nil {
		rep.printf("fail model answers: %v\n", userFacing(err))
		rep.printf("note settings kept; fix one with `standup login` or `standup config set`, then run `standup doctor`\n")
		return errors.New("login: the endpoint did not answer")
	}
	rep.printf("ok   model answers\n")
	return rep.err
}
