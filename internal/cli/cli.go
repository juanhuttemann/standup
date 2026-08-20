package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	defaults "standup/config"
	"standup/internal/agent"
	"standup/internal/config"
	"standup/internal/git"
	"standup/internal/obsidian"
	"standup/internal/report"
	"standup/internal/store"
	"standup/internal/sync"
	standupupdate "standup/internal/update"
)

// gitLog is swappable so CLI tests never depend on a real repository.
var gitLog = git.Log

// gitSubmodules is swappable so CLI tests never depend on a real repository.
var gitSubmodules = git.Submodules

// gitLogAll is swappable so CLI tests never depend on a real repository.
var gitLogAll = git.LogAll

// syncRun is swappable so CLI tests never depend on a running server.
var syncRun = sync.Run

// Deps carries everything a command needs; it is built lazily so help,
// version, and init never touch config, store, or provider settings. The
// assistant itself is lazy on top: read-only commands (list, done, rm,
// status, edit, commits) never require provider credentials.
type Deps struct {
	// Assistant builds the model-backed assistant on first use (online add,
	// generate, speak, and prompt; nil for commands that never call a model).
	Assistant func() (agent.Assistant, error)
	// Raw is the deterministic assistant used by `add --raw`.
	Raw    agent.Assistant
	Store  *store.Store
	Config config.Config
}

type progressPlanner interface {
	PlanWithProgress(context.Context, string, []store.Task, time.Time, func(string)) ([]store.BatchOperation, error)
}

func New(load func() (Deps, error)) *cobra.Command {
	root := &cobra.Command{
		Use:          "standup",
		Short:        "AI-assisted standup CLI",
		Args:         rootArgs,
		SilenceUsage: true,
		RunE:         func(cmd *cobra.Command, args []string) error { return runRoot(cmd, args, load) },
	}
	root.Flags().StringP("add", "a", "", "task text")
	root.Flags().StringP("prompt", "p", "", "apply task changes from a natural-language prompt (use - for stdin; weak models may need a longer model_call_timeout)")
	root.Flags().Bool("verbose", false, "show specialist tool calls for -p")
	root.Flags().BoolP("yes", "y", false, "apply a -p plan that deletes tasks without confirmation")
	root.Flags().BoolP("list", "l", false, "list today's tasks")
	root.Flags().BoolP("generate", "g", false, "generate the standup report")

	addCmd := &cobra.Command{
		Use:   "add",
		Short: "add a task (model-cleaned; --raw stores verbatim)",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return lazy(cmd, load, func(c *cobra.Command, d Deps) error { return runAdd(c, d, args) })
		},
		SilenceUsage: true,
	}
	addCmd.Flags().Bool("raw", false, "store text verbatim, no model")

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "list tasks (today by default)",
		RunE:  func(cmd *cobra.Command, args []string) error { return lazy(cmd, load, runList) },
	}
	listCmd.Flags().String("date", "", "show tasks from this date (YYYY-MM-DD)")
	listCmd.Flags().Int("days", 0, "show tasks from the trailing N days")
	listCmd.Flags().String("tag", "", "show only tasks containing this tag token")

	genCmd := &cobra.Command{
		Use:     "generate [days]",
		Aliases: []string{"g"},
		Args:    cobra.MaximumNArgs(1),
		Short:   "generate the standup report (yesterday + today by default)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return lazy(cmd, load, func(c *cobra.Command, d Deps) error { return runGenerate(c, d, args) })
		},
		SilenceUsage: true,
	}
	genCmd.Flags().StringP("output", "o", "", "write the report to this file")
	genCmd.Flags().String("from", "", "explicit window start date (YYYY-MM-DD, with --to)")
	genCmd.Flags().String("to", "", "explicit window end date (YYYY-MM-DD, with --from)")
	genCmd.Flags().Bool("clip", false, "copy the report to the clipboard")
	genCmd.Flags().String("webhook", "", "POST the report to this webhook URL (Slack-compatible JSON)")
	genCmd.Flags().String("mail", "", "send the report to this email address (needs smtp_* config)")
	genCmd.Flags().Bool("team", false, "group the report by recorded commit author (see commits --all-authors)")
	genCmd.Flags().Bool("obsidian", false, "publish into the configured Obsidian vault")

	speakCmd := &cobra.Command{
		Use:   "speak [days]",
		Args:  cobra.MaximumNArgs(1),
		Short: "speak the standup report (prints the script; -o synthesizes audio)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return lazy(cmd, load, func(c *cobra.Command, d Deps) error { return runSpeak(c, d, args) })
		},
		SilenceUsage: true,
	}
	speakCmd.Flags().StringP("output", "o", "", "synthesize the script into this audio file (wav)")
	speakCmd.Flags().String("from", "", "explicit window start date (YYYY-MM-DD, with --to)")
	speakCmd.Flags().String("to", "", "explicit window end date (YYYY-MM-DD, with --from)")
	speakCmd.Flags().Bool("team", false, "group the report by recorded commit author (see commits --all-authors)")

	commitsCmd := &cobra.Command{
		Use:   "commits [days] [paths...]",
		Args:  cobra.ArbitraryArgs,
		Short: "turn git commits from the last working day (or N days) into tasks",
		RunE: func(cmd *cobra.Command, args []string) error {
			return lazy(cmd, load, func(c *cobra.Command, d Deps) error { return runCommits(c, d, args) })
		},
		SilenceUsage: true,
	}
	commitsCmd.Flags().Bool("branch", false, "record the branch name with each imported commit")
	commitsCmd.Flags().Bool("all-authors", false, "import every author's commits (team standup), recording the author")

	doctorCmd := &cobra.Command{
		Use:   "doctor",
		Short: "check the setup: data file, git identity, endpoint",
		RunE: func(cmd *cobra.Command, args []string) error {
			return lazy(cmd, load, func(c *cobra.Command, d Deps) error { return runDoctor(c, d) })
		},
		SilenceUsage: true,
	}

	doneCmd := &cobra.Command{
		Use:   "done <id>",
		Short: "mark a task done",
		Args:  idArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			return lazy(cmd, load, func(c *cobra.Command, d Deps) error { return runDone(c, d, args) })
		},
		SilenceUsage: true,
	}

	editCmd := &cobra.Command{
		Use:   "edit <id> [text]",
		Short: "edit a task's text (no argument opens $EDITOR)",
		Args:  editArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			return lazy(cmd, load, func(c *cobra.Command, d Deps) error { return runEdit(c, d, args) })
		},
		SilenceUsage: true,
	}

	rmCmd := &cobra.Command{
		Use:   "rm <id>",
		Short: "delete a task",
		Args:  idArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			return lazy(cmd, load, func(c *cobra.Command, d Deps) error { return runRm(c, d, args) })
		},
		SilenceUsage: true,
	}
	rmCmd.Flags().BoolP("force", "f", false, "delete without further confirmation")

	statusCmd := &cobra.Command{
		Use:   "status <id> <status>",
		Short: "set a task's status (todo, in-progress, blocked, done)",
		Args:  statusArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			return lazy(cmd, load, func(c *cobra.Command, d Deps) error { return runStatus(c, d, args) })
		},
		SilenceUsage: true,
	}

	initCmd := &cobra.Command{
		Use:   "init",
		Short: "write default config files to the active config dir",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := config.Init()
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "config dir: %s (existing files kept)\n", dir)
			return err
		},
	}

	configCmd := newConfigCmd()

	updateCmd := &cobra.Command{
		Use:   "update",
		Short: "update to the latest release",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpdate(cmd)
		},
		SilenceUsage: true,
	}
	updateCmd.Flags().Bool("check", false, "check for an update without installing it")

	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "print the standup version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s version %s\n", cmd.Root().Name(), cmd.Root().Version)
			return err
		},
	}

	skillCmd := &cobra.Command{
		Use:   "skill install",
		Short: "install the standup agent skill into the current repo (--global: your home dir)",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 || args[0] != "install" {
				return fmt.Errorf("usage: %s skill install [--global]", cmd.Root().Name())
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSkillInstall(cmd, flagBool(cmd, "global"))
		},
		SilenceUsage: true,
	}
	skillCmd.Flags().BoolP("global", "g", false, "install to ~/.agents and ~/.claude instead of the repo")

	syncCmd := &cobra.Command{
		Use:   "sync",
		Short: "sync tasks with the configured PocketBase server",
		RunE: func(cmd *cobra.Command, args []string) error {
			return lazy(cmd, load, runSync)
		},
		SilenceUsage: true,
	}

	root.AddCommand(addCmd, listCmd, genCmd, speakCmd, commitsCmd, doneCmd, editCmd, rmCmd, statusCmd, initCmd, configCmd, doctorCmd, skillCmd, updateCmd, versionCmd, syncCmd)
	return root
}

func rootArgs(cmd *cobra.Command, args []string) error {
	if cmd.Flags().Changed("prompt") || flagBool(cmd, "generate") {
		return nil
	}
	return cobra.NoArgs(cmd, args)
}

func runRoot(cmd *cobra.Command, args []string, load func() (Deps, error)) error {
	prompt := cmd.Flags().Changed("prompt")
	actions := 0
	for _, enabled := range []bool{prompt, cmd.Flags().Changed("add"), flagBool(cmd, "list"), flagBool(cmd, "generate")} {
		if enabled {
			actions++
		}
	}
	if actions > 1 {
		return errors.New("only one of --prompt, --add, --list, or --generate may be used")
	}
	if flagBool(cmd, "verbose") && !prompt {
		return errors.New("--verbose requires --prompt")
	}
	if prompt && len(args) > 0 {
		return errors.New("--prompt does not accept positional arguments")
	}
	for _, action := range []struct {
		enabled bool
		run     func(*cobra.Command, Deps) error
	}{
		{cmd.Flags().Changed("prompt"), func(c *cobra.Command, d Deps) error { return runPrompt(c, d, flagString(c, "prompt")) }},
		{cmd.Flags().Changed("add"), func(c *cobra.Command, d Deps) error { return runAdd(c, d, []string{flagString(c, "add")}) }},
		{flagBool(cmd, "list"), runList},
		{flagBool(cmd, "generate"), func(c *cobra.Command, d Deps) error { return runGenerate(c, d, args) }},
	} {
		if !action.enabled {
			continue
		}
		d, err := load()
		if err != nil {
			return userFacing(err)
		}
		return userFacing(action.run(cmd, d))
	}
	return cmd.Help()
}

func newConfigCmd() *cobra.Command {
	configCmd := &cobra.Command{Use: "config", Short: "set or edit configuration"}
	configSetCmd := &cobra.Command{
		Use:   "set KEY VALUE",
		Short: "set a configuration value",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			file, err := config.Set(args[0], args[1])
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "set %s=%s in %s\n", args[0], args[1], file)
			return err
		},
		SilenceUsage: true,
	}
	configEditCmd := &cobra.Command{
		Use:          "edit",
		Short:        "open config.yaml in $EDITOR",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			file, err := config.EnsureConfig()
			if err != nil {
				return err
			}
			before, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "opening %s in %s\n", file, editorName()); err != nil {
				return err
			}
			if err := runEditor(file); err != nil {
				return err
			}
			if err := config.ValidateFile(file); err != nil {
				if restoreErr := os.WriteFile(file, before, 0o644); restoreErr != nil {
					return errors.Join(err, fmt.Errorf("restore config: %w", restoreErr))
				}
				return fmt.Errorf("invalid config; original restored: %w", err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "config: %s\n", file)
			return err
		},
	}
	configCmd.AddCommand(configSetCmd, configEditCmd)
	return configCmd
}

var selfUpdate = standupupdate.Run

// runUpdate updates the running binary without loading application or model
// dependencies. --check preserves a read-only path for automation.
func runUpdate(cmd *cobra.Command) error {
	if strings.EqualFold(strings.TrimSpace(cmd.Root().Version), "dev") {
		return errors.New("update: cannot check updates for a development build; install a released binary first")
	}
	result, err := selfUpdate(cmd.Context(), cmd.Root().Version, flagBool(cmd, "check"))
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	switch {
	case result.Updated:
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "updated %s -> %s\n", result.Current, result.Latest)
	case result.State == standupupdate.UpgradeAvailable:
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "update available: %s (current: %s)\n", result.Latest, result.Current)
	case result.State == standupupdate.NewerInstalled:
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "installed version %s is newer than latest release %s\n", result.Current, result.Latest)
	default:
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "up to date (%s)\n", result.Latest)
	}
	return err
}

// runSkillInstall writes the embedded agent skill as real files (symlinks
// break on Windows checkouts): into the current repo by default, or the
// home roots with --global. Two roots cover the skills-compatible harnesses
// (.agents for most, .claude for Claude Code — both read at repo and home
// level); proprietary mechanisms (Windsurf rules, Hermes) are out of scope —
// a file drop cannot reach them. Like init it never loads deps: the skill
// is embedded content, nothing to configure.
func runSkillInstall(cmd *cobra.Command, global bool) error {
	base := ""
	if global {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("skill install --global: %w", err)
		}
		base = home
	}
	roots := []struct {
		dir string
		who string
	}{
		{filepath.Join(base, ".agents", "skills", "standup"), "Codex, Cursor, OpenCode, Amp, Copilot, Gemini CLI, Goose"},
		{filepath.Join(base, ".claude", "skills", "standup"), "Claude Code"},
	}
	// Preflight every target before changing either one. Windows checkouts can
	// materialize the repository symlinks as small files containing their target;
	// those known placeholders are safe to replace, arbitrary files are not.
	for _, r := range roots {
		if err := validateSkillTarget(r.dir); err != nil {
			return err
		}
	}
	for _, r := range roots {
		if err := removeSkillPlaceholder(r.dir); err != nil {
			return err
		}
		if err := os.MkdirAll(r.dir, 0o755); err != nil {
			return err
		}
		file := filepath.Join(r.dir, "SKILL.md")
		edited, err := skillWasEdited(file)
		if err != nil {
			return err
		}
		if err := os.WriteFile(file, []byte(defaults.SkillMD), 0o644); err != nil {
			return err
		}
		// Silently replacing someone's edits is the one thing a file drop
		// must not do quietly.
		if edited {
			if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "warning: replaced an edited %s\n", file); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "skill: %s (%s)\n", file, r.who); err != nil {
			return err
		}
	}
	return nil
}

// skillWasEdited reports whether an existing SKILL.md differs from the
// embedded copy that is about to replace it.
func skillWasEdited(file string) (bool, error) {
	b, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return string(b) != defaults.SkillMD, nil
}

func validateSkillTarget(dir string) error {
	info, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		return nil
	}
	b, err := os.ReadFile(dir)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(b)) != "../../config/skill" {
		return fmt.Errorf("skill target %s exists and is not a directory", dir)
	}
	return nil
}

func removeSkillPlaceholder(dir string) error {
	info, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) || err == nil && info.IsDir() {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.Remove(dir); err != nil {
		return fmt.Errorf("replace skill placeholder %s: %w", dir, err)
	}
	return nil
}

// lazy loads deps, then runs the command body.
func lazy(cmd *cobra.Command, load func() (Deps, error), run func(*cobra.Command, Deps) error) error {
	d, err := load()
	if err != nil {
		return userFacing(err)
	}
	return userFacing(run(cmd, d))
}

// internalPrefixes are the package names that leaked into user-facing errors.
// The messages themselves are good — specific, actionable, they name valid
// values — but `store:` and `agent:` are concepts the user does not have.
var internalPrefixes = []string{"store: ", "agent: ", "report: ", "config: ", "git: "}

func userFacing(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	for _, prefix := range internalPrefixes {
		message = strings.ReplaceAll(message, prefix, "")
	}
	if message == err.Error() {
		return err
	}
	return errors.New(message)
}

func runDone(cmd *cobra.Command, d Deps, args []string) error {
	task, err := d.Store.FindByPrefix(args[0])
	if err != nil {
		return err
	}
	task, err = d.Store.SetStatus(task.ID, "done")
	if err != nil {
		return err
	}
	return echoTask(cmd, task)
}

func runEdit(cmd *cobra.Command, d Deps, args []string) error {
	task, err := d.Store.FindByPrefix(args[0])
	if err != nil {
		return err
	}
	text := strings.Join(args[1:], " ")
	if len(args) == 1 {
		if !editorInteractive() {
			return fmt.Errorf("edit: cannot open an editor in a non-interactive session; provide replacement text after the task id")
		}
		text, err = editInEditor(task.Text)
		if err != nil {
			return err
		}
	}
	task, err = d.Store.UpdateText(task.ID, text)
	if err != nil {
		return err
	}
	return echoTask(cmd, task)
}

func runRm(cmd *cobra.Command, d Deps, args []string) error {
	task, err := d.Store.FindByPrefix(args[0])
	if err != nil {
		return err
	}
	if !flagBool(cmd, "force") {
		return fmt.Errorf("refusing to remove %s (%s) without --force", shortID(task.ID), flat(task.Text))
	}
	if err := d.Store.Delete(task.ID); err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "- removed: %s\n", flat(task.Text))
	return err
}

// runSync merges the local store with the configured PocketBase server.
// Credentials never come from config: the sync package reads them from the
// environment.
func runSync(cmd *cobra.Command, d Deps) error {
	if d.Config.SyncURL == "" {
		return fmt.Errorf("sync is not configured: set sync.url in config.yaml (or PB_URL)")
	}
	var res sync.Result
	if err := spin("syncing", func() error {
		var err error
		res, err = syncRun(d.Store, sync.Server{
			URL:        d.Config.SyncURL,
			Collection: d.Config.SyncCollection,
			Email:      d.Config.SyncEmail,
			Password:   d.Config.SyncPassword,
		})
		return err
	}); err != nil {
		return err
	}
	line := fmt.Sprintf("- synced: %d pushed, %d pulled", len(res.Push), res.Pulled)
	if res.Resolved > 0 {
		line += fmt.Sprintf(", %s resolved", plural(res.Resolved, "duplicate"))
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), line)
	return err
}

// plural renders "1 duplicate" / "2 duplicates".
func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func runStatus(cmd *cobra.Command, d Deps, args []string) error {
	task, err := d.Store.FindByPrefix(args[0])
	if err != nil {
		return err
	}
	task, err = d.Store.SetStatus(task.ID, args[1])
	if err != nil {
		return err
	}
	return echoTask(cmd, task)
}

// fallbackEditor is the OS default when $EDITOR is unset: Windows ships
// notepad, everything else has vi.
func fallbackEditor() string {
	if runtime.GOOS == "windows" {
		return "notepad"
	}
	return "vi"
}

func editorName() string {
	if editor := os.Getenv("EDITOR"); editor != "" {
		return editor
	}
	return fallbackEditor()
}

// flat collapses a task text to one row: multi-line entries (commit bodies)
// must not break the column layout.
func flat(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// echoTask prints the mutated row so silent mutations never happen. The text
// is bounded like a list row: importing 30 commits printed 30 walls of text.
func echoTask(cmd *cobra.Command, t store.Task) error {
	p := newPainter(cmd.OutOrStdout())
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "- [%s] %s\n", p.status(t.Status), truncate(flat(t.Text), rowTextBudget))
	return err
}

// colorReport paints the [status] tokens of a rendered report; verbatim when
// colors are off (piped output, NO_COLOR).
func colorReport(s string, p painter) string {
	if !p.on {
		return s
	}
	r := strings.NewReplacer(
		"[todo]", "["+p.status("todo")+"]",
		"[in-progress]", "["+p.status("in-progress")+"]",
		"[blocked]", "["+p.status("blocked")+"]",
		"[done]", "["+p.status("done")+"]",
	)
	return r.Replace(s)
}

func idArg(cmd *cobra.Command, args []string) error {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return fmt.Errorf("usage: %s %s <id> (find ids with: %s list)", cmd.Root().Name(), cmd.Name(), cmd.Root().Name())
	}
	return nil
}

func editArg(cmd *cobra.Command, args []string) error {
	if len(args) < 1 || strings.TrimSpace(args[0]) == "" {
		return fmt.Errorf("usage: %s edit <id> [text] (find ids with: %s list)", cmd.Root().Name(), cmd.Root().Name())
	}
	return nil
}

func statusArg(cmd *cobra.Command, args []string) error {
	if len(args) != 2 || strings.TrimSpace(args[0]) == "" || strings.TrimSpace(args[1]) == "" {
		return fmt.Errorf("usage: %s status <id> <status> (todo, in-progress, blocked, done; find ids with: %s list)", cmd.Root().Name(), cmd.Root().Name())
	}
	return nil
}

// editInEditor opens the user's editor ($EDITOR, fallback vi — notepad on
// Windows) on a temp file seeded with the current text and returns the saved
// content.
func editInEditor(current string) (text string, err error) {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = fallbackEditor()
	}
	f, err := os.CreateTemp("", "standup-*.md")
	if err != nil {
		return "", err
	}
	defer func() {
		if rmErr := os.Remove(f.Name()); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			err = errors.Join(err, rmErr)
		}
	}()
	if _, err := f.WriteString(current); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := runEditorWith(editor, f.Name()); err != nil {
		return "", err
	}
	b, err := os.ReadFile(f.Name())
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func runEditor(file string) error {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = fallbackEditor()
	}
	return runEditorWith(editor, file)
}

func runEditorWith(editor, file string) error {
	parts := strings.Fields(editor)
	c := exec.Command(parts[0], append(parts[1:], file)...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("editor %q: %w", editor, err)
	}
	return nil
}

// flagString reads a string flag that may not exist on this command.
func flagString(cmd *cobra.Command, name string) string {
	if f := cmd.Flags().Lookup(name); f != nil {
		return f.Value.String()
	}
	return ""
}

// flagInt reads an int flag that may not exist on this command.
func flagInt(cmd *cobra.Command, name string) int {
	if f := cmd.Flags().Lookup(name); f != nil {
		n, err := strconv.Atoi(f.Value.String())
		if err == nil {
			return n
		}
	}
	return 0
}

// flagBool reads a bool flag that may not exist on this command.
func flagBool(cmd *cobra.Command, name string) bool {
	if f := cmd.Flags().Lookup(name); f != nil {
		return f.Value.String() == "true"
	}
	return false
}

// spin runs fn with an active-TTY spinner and always stops it.
func spin(msg string, fn func() error) error {
	sp := newSpinner(os.Stderr, term.IsTerminal(int(os.Stderr.Fd())), msg)
	if err := sp.Start(); err != nil {
		return err
	}
	err := fn()
	if serr := sp.Stop(); serr != nil {
		return fmt.Errorf("spinner: %w", serr)
	}
	return err
}

func runAdd(cmd *cobra.Command, d Deps, args []string) error {
	text, err := addText(cmd, args)
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("usage: %s add \"task text\" (or pipe text to %s add)", cmd.Root().Name(), cmd.Root().Name())
	}
	existing, err := d.Store.List()
	if err != nil {
		return err
	}
	a := d.Raw
	if !flagBool(cmd, "raw") {
		assist, err := d.Assistant()
		if err != nil {
			return err
		}
		a = assist
	}
	var tasks []store.Task
	var addErr error
	if err := spin("adding tasks", func() error {
		tasks, addErr = a.AddTasks(cmd.Context(), text)
		return nil
	}); err != nil {
		return err
	}
	if addErr != nil {
		return addErr
	}
	seen := make(map[string]struct{}, len(existing)+len(tasks))
	for _, task := range existing {
		seen[task.Text] = struct{}{}
	}
	for _, t := range tasks {
		if _, duplicate := seen[t.Text]; duplicate {
			if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "warning: exact duplicate task: %s\n", flat(t.Text)); err != nil {
				return err
			}
		}
		seen[t.Text] = struct{}{}
		if err := echoTask(cmd, t); err != nil {
			return err
		}
	}
	return nil
}

func addText(cmd *cobra.Command, args []string) (string, error) {
	text := strings.Join(args, " ")
	if len(args) == 1 && args[0] == "-" {
		b, err := io.ReadAll(cmd.InOrStdin())
		return strings.ReplaceAll(string(b), "\r\n", "\n"), err
	}
	if strings.TrimSpace(text) != "" || interactive() {
		return text, nil
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	// Windows pipes carry CRLF; normalize so \r never reaches the store.
	return strings.ReplaceAll(string(b), "\r\n", "\n"), nil
}

func runPrompt(cmd *cobra.Command, d Deps, prompt string) error {
	if prompt == "-" {
		input, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return err
		}
		prompt = strings.TrimSpace(strings.ReplaceAll(string(input), "\r\n", "\n"))
	}
	if prompt == "" {
		return fmt.Errorf("usage: %s -p \"prompt\" (or pipe a prompt to %s -p -)", cmd.Root().Name(), cmd.Root().Name())
	}
	now, err := nowIn(d)
	if err != nil {
		return err
	}
	tasks, err := d.Store.List()
	if err != nil {
		return err
	}
	assist, err := d.Assistant()
	if err != nil {
		return err
	}
	operations, err := planOperations(cmd, d, assist, prompt, tasks, now)
	if err != nil {
		return err
	}
	operations = dropNoOps(operations, tasks)
	if err := confirmDeletes(cmd, operations, tasks); err != nil {
		return err
	}
	changes, err := d.Store.ApplyBatch(operations)
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "no changes")
		return err
	}
	for _, change := range changes {
		if err := echoChange(cmd, change); err != nil {
			return err
		}
	}
	return nil
}

// planOperations asks the assistant for a plan under the command's wall-clock
// budget.
func planOperations(cmd *cobra.Command, d Deps, assist agent.Assistant, prompt string, tasks []store.Task, now time.Time) ([]store.BatchOperation, error) {
	budget := promptBudget(d.Config.ModelCallTimeout)
	ctx, cancel := context.WithTimeout(cmd.Context(), budget)
	defer cancel()
	var operations []store.BatchOperation
	var planErr error
	if flagBool(cmd, "verbose") {
		operations, planErr = planWithProgress(ctx, cmd, assist, prompt, tasks, now)
	} else if err := spin("planning changes", func() error {
		operations, planErr = assist.Plan(ctx, prompt, tasks, now)
		return nil
	}); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("gave up after %s: the coordinator was still working (model_call_timeout bounds one call, %d of them bound the command); raise model_call_timeout or ask for one change at a time", budget, promptCalls)
	}
	return operations, planErr
}

// promptCalls is how many model calls one -p run is allowed to spend: the
// coordinator plus its three specialists, with a call to spare.
const promptCalls = 5

// promptBudget bounds a whole -p run. model_call_timeout bounds one call and
// the coordinator makes several, so without this a weak model could keep the
// command running indefinitely with nothing on screen.
func promptBudget(perCall time.Duration) time.Duration {
	if perCall <= 0 {
		perCall = time.Minute
	}
	return promptCalls * perCall
}

// dropNoOps removes operations that would change nothing. The verbose plan
// reported `status <id> blocked -> blocked` as a change, and applying it
// would restamp the record for no reason.
func dropNoOps(operations []store.BatchOperation, tasks []store.Task) []store.BatchOperation {
	current := make(map[string]store.Task, len(tasks))
	for _, t := range tasks {
		current[t.ID] = t
	}
	kept := make([]store.BatchOperation, 0, len(operations))
	for _, op := range operations {
		t, known := current[op.ID]
		switch {
		case known && op.Kind == store.OperationStatus && t.Status == op.Status:
		case known && op.Kind == store.OperationEdit && t.Text == op.Text:
		default:
			kept = append(kept, op)
		}
	}
	return kept
}

// confirmDeletes previews a plan that removes tasks and asks before applying
// it. The prompt path is the one where a model decides the blast radius — a
// single "delete all of my tasks" wiped the whole store — so it is the path
// that most needs a preview, while `rm` already refuses a single task without
// --force.
func confirmDeletes(cmd *cobra.Command, operations []store.BatchOperation, tasks []store.Task) error {
	byID := make(map[string]store.Task, len(tasks))
	for _, t := range tasks {
		byID[t.ID] = t
	}
	var deletes []string
	for _, op := range operations {
		if op.Kind != store.OperationDelete {
			continue
		}
		if t, ok := byID[op.ID]; ok {
			deletes = append(deletes, fmt.Sprintf("%s [%s] %s", shortID(t.ID), t.Status, truncate(flat(t.Text), rowTextBudget)))
			continue
		}
		deletes = append(deletes, shortID(op.ID))
	}
	if len(deletes) == 0 || flagBool(cmd, "yes") {
		return nil
	}
	for _, line := range deletes {
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "will delete %s\n", line); err != nil {
			return err
		}
	}
	if !interactive() {
		return fmt.Errorf("refusing to delete %s without confirmation; re-run with --yes", plural(len(deletes), "task"))
	}
	return confirmPrompt(fmt.Sprintf("delete %s", plural(len(deletes), "task")))
}

// confirmPrompt asks a yes/no question on the terminal, defaulting to no.
var confirmPrompt = func(question string) error {
	_, err := (&promptui.Prompt{Label: question, IsConfirm: true}).Run()
	if err != nil {
		return fmt.Errorf("cancelled: %s", question)
	}
	return nil
}

func planWithProgress(ctx context.Context, cmd *cobra.Command, assist agent.Assistant, prompt string, tasks []store.Task, now time.Time) ([]store.BatchOperation, error) {
	planner, ok := assist.(progressPlanner)
	if !ok {
		return assist.Plan(ctx, prompt, tasks, now)
	}
	var writeErr error
	operations, err := planner.PlanWithProgress(ctx, prompt, tasks, now, func(message string) {
		if writeErr == nil {
			_, writeErr = fmt.Fprintln(cmd.ErrOrStderr(), message)
		}
	})
	if writeErr != nil {
		return nil, writeErr
	}
	return operations, err
}

func echoChange(cmd *cobra.Command, change store.Change) error {
	// Rows, like every other listing: a task holding a whole commit message
	// must not print a wall of text per change.
	row := func(t *store.Task) string { return truncate(flat(t.Text), rowTextBudget) }
	switch change.Kind {
	case store.OperationCreate:
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "created %s [%s] %s\n", shortID(change.After.ID), change.After.Status, row(change.After))
		return err
	case store.OperationEdit:
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "edited %s %s -> %s\n", shortID(change.After.ID), row(change.Before), row(change.After))
		return err
	case store.OperationStatus:
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "status %s %s -> %s: %s\n", shortID(change.After.ID), change.Before.Status, change.After.Status, row(change.After))
		return err
	case store.OperationDelete:
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "deleted %s [%s] %s\n", shortID(change.Before.ID), change.Before.Status, row(change.Before))
		return err
	default:
		return fmt.Errorf("unknown change kind %q", change.Kind)
	}
}

// nowIn returns the store clock in the configured timezone (empty = local).
// Report windows, the meeting cutoff and the day split all derive from the
// result's Location, so this one conversion covers every command.
func nowIn(d Deps) (time.Time, error) {
	if d.Config.Timezone == "" {
		return d.Store.Now(), nil
	}
	loc, err := time.LoadLocation(d.Config.Timezone)
	if err != nil {
		return time.Time{}, fmt.Errorf("config timezone %q: %w", d.Config.Timezone, err)
	}
	return d.Store.Now().In(loc), nil
}

func runCommits(cmd *cobra.Command, d Deps, args []string) error {
	days, paths, err := commitsArgs(cmd, args)
	if err != nil {
		return err
	}
	now, err := nowIn(d)
	if err != nil {
		return err
	}
	since := report.StartOfDay(report.LastWorkingDay(now))
	if days > 0 {
		since = report.StartOfDay(now.AddDate(0, 0, -(days - 1)))
	}
	commits, err := collectCommits(d.Config, paths, since, flagBool(cmd, "all-authors"))
	if err != nil {
		return err
	}
	if len(commits) == 0 {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "no commits found since %s — check that `git config user.email` matches your commit identity\n", since.Format("2006-01-02"))
		return err
	}
	return importCommits(cmd, d.Store, commits)
}

// commitsArgs splits [days] [paths...]; days is optional, paths default to
// the current directory.
func commitsArgs(cmd *cobra.Command, args []string) (int, []string, error) {
	rest := args
	days := 0
	if len(rest) > 0 {
		if n, err := strconv.Atoi(rest[0]); err == nil {
			if n < 1 {
				return 0, nil, fmt.Errorf("usage: %s commits [days] [paths...] (days >= 1)", cmd.Root().Name())
			}
			days = n
			rest = rest[1:]
		}
	}
	if len(rest) == 0 {
		return days, []string{"."}, nil
	}
	for i, p := range rest {
		if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
			// The first positional is documented as [days], so a typo there
			// ("1o") must not send the user looking for a directory.
			if i == 0 && days == 0 {
				return 0, nil, fmt.Errorf("usage: %s commits [days] [paths...] (%q is neither a day count nor a directory)", cmd.Root().Name(), p)
			}
			return 0, nil, fmt.Errorf("usage: %s commits [days] [paths...] (no such directory: %q)", cmd.Root().Name(), p)
		}
	}
	return days, rest, nil
}

// collectCommits gathers commits from every repo (and its submodules),
// filtered by the repos.include/exclude globs, deduped by hash and ordered
// oldest first across repos. allAuthors collects the whole team's commits
// instead of just the configured git user's.
func collectCommits(cfg config.Config, paths []string, since time.Time, allAuthors bool) ([]git.Commit, error) {
	log := gitLog
	if allAuthors {
		log = gitLogAll
	}
	var repos []string
	for _, p := range paths {
		subs, err := gitSubmodules(p)
		if err != nil {
			return nil, err
		}
		repos = append(repos, p)
		for _, s := range subs {
			repos = append(repos, filepath.Join(p, s))
		}
	}
	repos, err := filterRepos(repos, cfg)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var commits []git.Commit
	for _, repo := range repos {
		cs, err := log(repo, since)
		if err != nil {
			return nil, err
		}
		for _, c := range cs {
			if c.Hash != "" && seen[c.Hash] {
				continue
			}
			seen[c.Hash] = true
			commits = append(commits, c)
		}
	}
	sort.SliceStable(commits, func(i, j int) bool { return commits[i].When.Before(commits[j].When) })
	return commits, nil
}

// filterRepos applies the repos.include/exclude globs (path.Match against
// each path as passed); an empty include list keeps everything not excluded.
func filterRepos(paths []string, cfg config.Config) ([]string, error) {
	match := func(globs []string, p string) (bool, error) {
		for _, g := range globs {
			ok, err := path.Match(g, p)
			if err != nil {
				return false, fmt.Errorf("repos glob %q: %w", g, err)
			}
			if ok {
				return true, nil
			}
		}
		return false, nil
	}
	var out []string
	for _, p := range paths {
		inc, err := match(cfg.ReposInclude, p)
		if err != nil {
			return nil, err
		}
		exc, err := match(cfg.ReposExclude, p)
		if err != nil {
			return nil, err
		}
		if (len(cfg.ReposInclude) == 0 || inc) && !exc {
			out = append(out, p)
		}
	}
	return out, nil
}

// importCommits stores commits as done tasks stamped with the commit time;
// commits already imported (same text, same day) are skipped — including
// tombstoned ones, so a deleted import never resurrects on re-run.
func importCommits(cmd *cobra.Command, st *store.Store, commits []git.Commit) error {
	existing, err := st.Snapshot()
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, t := range existing {
		known[t.Text+"|"+t.Timestamp.Format("2006-01-02")] = true
	}
	skipped := 0
	for _, c := range commits {
		if known[c.Body+"|"+c.When.Format("2006-01-02")] {
			skipped++
			continue
		}
		t, err := st.AddAt(c.Body, "done", c.When)
		if err != nil {
			// One bad commit must not abort the import: skip it, say so,
			// keep going.
			if _, werr := fmt.Fprintf(cmd.OutOrStdout(), "- skipped %s: %v\n", shortHash(c.Hash), err); werr != nil {
				return werr
			}
			continue
		}
		if flagBool(cmd, "branch") && c.Branch != "" {
			if t, err = st.SetBranch(t.ID, c.Branch); err != nil {
				return err
			}
		}
		// The display name is what a team report shows as a heading; the
		// email only ever served the identity filter.
		if author := commitAuthor(c); flagBool(cmd, "all-authors") && author != "" {
			if t, err = st.SetAuthor(t.ID, author); err != nil {
				return err
			}
		}
		if err := echoTask(cmd, t); err != nil {
			return err
		}
	}
	if skipped > 0 {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "- skipped %d already imported\n", skipped)
		return err
	}
	return nil
}

// commitAuthor is the name a team report attributes the commit to.
func commitAuthor(c git.Commit) string {
	if c.Name != "" {
		return c.Name
	}
	return c.Author
}

var spinnerFrames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

type spinner struct {
	w       io.Writer
	active  bool
	msg     string
	done    chan struct{}
	stopped chan struct{}
}

func newSpinner(w io.Writer, active bool, msg string) *spinner {
	return &spinner{w: w, active: active, msg: msg}
}

func (s *spinner) render(i int) error {
	_, err := fmt.Fprintf(s.w, "\r%c %s", spinnerFrames[i%len(spinnerFrames)], s.msg)
	return err
}

func (s *spinner) Start() error {
	if !s.active {
		return nil
	}
	s.done = make(chan struct{})
	s.stopped = make(chan struct{})
	if err := s.render(0); err != nil {
		return err
	}
	go func() {
		defer close(s.stopped)
		for i := 1; ; i++ {
			select {
			case <-s.done:
				return
			case <-time.After(100 * time.Millisecond):
				if err := s.render(i); err != nil {
					return
				}
			}
		}
	}()
	return nil
}

func (s *spinner) Stop() error {
	if !s.active || s.done == nil {
		return nil
	}
	close(s.done)
	<-s.stopped
	_, err := fmt.Fprintf(s.w, "\r%s\r", strings.Repeat(" ", len([]rune(s.msg))+2))
	return err
}

func interactive() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

var editorInteractive = interactive

func runList(cmd *cobra.Command, d Deps) error {
	st := d.Store
	now, err := nowIn(d)
	if err != nil {
		return err
	}
	tasks, err := listTasks(cmd, st, now)
	if err != nil {
		return err
	}
	tag := flagString(cmd, "tag")
	tasks = filterTag(tasks, tag)
	if len(tasks) == 0 {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "no tasks")
		return err
	}
	if flagString(cmd, "date") == "" && !cmd.Flags().Changed("days") && interactive() {
		return selectLoop(st, tag, now, newPainter(cmd.OutOrStdout()))
	}
	p := newPainter(cmd.OutOrStdout())
	for _, t := range tasks {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %s  %s\n", p.quiet(shortID(t.ID)), p.status(t.Status), p.quiet(t.Timestamp.Format("15:04")), rowText(t)); err != nil {
			return err
		}
	}
	return nil
}

// rowTextBudget bounds a list row's text. `commits` stores a commit's whole
// message, and a single 1700-character task destroyed the column layout of
// the entire listing. The store keeps the full text; reports show the subject
// line.
const rowTextBudget = 100

// rowText renders a task row's text, attributing the branch when recorded.
func rowText(t store.Task) string {
	text := truncate(flat(t.Text), rowTextBudget)
	if t.Branch == "" {
		return text
	}
	return text + " [" + t.Branch + "]"
}

func truncate(s string, budget int) string {
	runes := []rune(s)
	if len(runes) <= budget {
		return s
	}
	return strings.TrimRight(string(runes[:budget]), " ") + "…"
}

// listTasks resolves the list window: one --date day, trailing --days, or
// today (in the configured timezone — now carries its location).
func listTasks(cmd *cobra.Command, st *store.Store, now time.Time) ([]store.Task, error) {
	date, days := flagString(cmd, "date"), flagInt(cmd, "days")
	daysGiven := cmd.Flags().Changed("days")
	if date != "" && daysGiven {
		return nil, fmt.Errorf("usage: %s list: --date and --days are mutually exclusive", cmd.Root().Name())
	}
	if date != "" {
		day, err := time.ParseInLocation("2006-01-02", date, now.Location())
		if err != nil {
			return nil, fmt.Errorf("usage: %s list --date YYYY-MM-DD (got %q)", cmd.Root().Name(), date)
		}
		return st.ListDay(day)
	}
	if daysGiven {
		// 0 used to be silently reinterpreted as "today" while -5 was
		// rejected, though both break the stated rule.
		if days < 1 {
			return nil, fmt.Errorf("usage: %s list --days N (N >= 1)", cmd.Root().Name())
		}
		return st.ListRange(report.StartOfDay(now.AddDate(0, 0, -(days-1))), now)
	}
	return st.ListDay(now)
}

// filterTag keeps tasks carrying the literal #tag token (case-insensitive),
// with or without the leading # in the query; plain words never match.
func filterTag(tasks []store.Task, tag string) []store.Task {
	if tag == "" {
		return tasks
	}
	want := strings.TrimPrefix(strings.ToLower(tag), "#")
	var out []store.Task
	for _, t := range tasks {
		for _, f := range strings.Fields(t.Text) {
			f = strings.TrimRight(strings.TrimPrefix(f, "#"), ".,;:!?")
			if f != "" && strings.EqualFold(f, want) {
				out = append(out, t)
				break
			}
		}
	}
	return out
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// shortHash abbreviates a commit hash like git does (7 chars).
func shortHash(h string) string {
	if len(h) > 7 {
		return h[:7]
	}
	return h
}

type taskEntry struct {
	task  store.Task
	label string
}

func taskEntries(st *store.Store, tag string, now time.Time, p painter) ([]taskEntry, error) {
	tasks, err := st.ListDay(now)
	if err != nil {
		return nil, err
	}
	entries := make([]taskEntry, 0, len(tasks))
	for _, t := range filterTag(tasks, tag) {
		entries = append(entries, taskEntry{task: t, label: fmt.Sprintf("%s [%s] %s %s", p.quiet(t.Timestamp.Format("15:04")), p.status(t.Status), p.quiet(shortID(t.ID)), rowText(t))})
	}
	return entries, nil
}

// aborted reports whether a promptui error means the user quit (Ctrl-C,
// Ctrl-D/EOF). promptui surfaces the ^D control rune itself as an error.
func aborted(err error) bool {
	if err == nil {
		return false
	}
	return err == promptui.ErrInterrupt || err == io.EOF || err.Error() == "^D"
}

func selectLoop(st *store.Store, tag string, now time.Time, p painter) error {
	// todo is in the list because a mis-click on blocked (one row above
	// delete) otherwise has to be undone from the command line.
	actions := []string{"todo", "in-progress", "done", "blocked", "delete", "back"}
	for {
		entries, err := taskEntries(st, tag, now, p)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			return nil
		}
		labels := make([]string, len(entries))
		for i, e := range entries {
			labels[i] = e.label
		}
		i, _, err := (&promptui.Select{Label: "task", Items: labels}).Run()
		if aborted(err) {
			return nil
		}
		if err != nil {
			return err
		}
		j, _, err := (&promptui.Select{Label: "action", Items: actions}).Run()
		if aborted(err) {
			return nil
		}
		if err != nil {
			return err
		}
		switch actions[j] {
		case "back":
			return nil
		case "delete":
			if err := confirmPrompt("delete " + flat(entries[i].task.Text)); err != nil {
				continue
			}
			if err := st.Delete(entries[i].task.ID); err != nil {
				return err
			}
		default:
			if _, err := st.SetStatus(entries[i].task.ID, actions[j]); err != nil {
				return err
			}
		}
	}
}

func runGenerate(cmd *cobra.Command, d Deps, args []string) error {
	if flagBool(cmd, "obsidian") && d.Config.ObsidianVault == "" {
		return errors.New("obsidian.vault is not configured (run: standup config set obsidian.vault /path/to/vault)")
	}
	// Checking the delivery settings costs nothing; discovering them after a
	// minute of model calls costs the whole report.
	if flagString(cmd, "mail") != "" && d.Config.SMTPHost == "" {
		return errors.New("mail: smtp_host is not configured (set smtp_* in config.yaml)")
	}
	now, err := nowIn(d)
	if err != nil {
		return err
	}
	dates, err := generateDates(cmd, args, now)
	if err != nil {
		return err
	}
	tasks, err := d.Store.List()
	if err != nil {
		return err
	}
	out, err := renderReport(cmd, d, tasks, now, dates)
	if err != nil {
		return err
	}
	if out == "" {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "nothing to report")
		return err
	}
	if err := deliverReport(cmd, d.Config, out); err != nil {
		return err
	}
	return publishReport(cmd, d, out, now)
}

// publishReport routes the rendered report to its destinations: the vault,
// a file, or the terminal. Every write echoes the path it wrote.
func publishReport(cmd *cobra.Command, d Deps, out string, now time.Time) error {
	published := false
	if flagBool(cmd, "obsidian") {
		note := strings.ReplaceAll(d.Config.ObsidianNote, "{date}", now.Format("2006-01-02"))
		path, err := obsidian.Publish(d.Config.ObsidianVault, note, out)
		if err != nil {
			return fmt.Errorf("obsidian: %w", err)
		}
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", path); err != nil {
			return err
		}
		published = true
	}
	if path := flagString(cmd, "output"); path != "" {
		if err := os.WriteFile(filepath.Clean(path), []byte(out+"\n"), 0o644); err != nil {
			return err
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", path)
		return err
	}
	if published {
		return nil
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), colorReport(out, newPainter(cmd.OutOrStdout())))
	return err
}

// renderReport builds the section(s) and renders them through the
// assistant: one section normally; with --team one section per recorded
// author (each under a `## author` heading, unattributed tasks first with
// none), so one person can run the standup for the whole team. The store
// stays personal — grouping happens here, at report time. "" means no
// tasks in the window.
func renderReport(cmd *cobra.Command, d Deps, tasks []store.Task, now time.Time, dates []time.Time) (string, error) {
	team := flagBool(cmd, "team")
	groups := map[string][]store.Task{}
	var order []string
	for _, t := range tasks {
		a := ""
		if team {
			a = t.Author
		}
		if _, ok := groups[a]; !ok {
			order = append(order, a)
		}
		groups[a] = append(groups[a], t)
	}
	assist, err := d.Assistant()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, a := range order {
		sec, err := report.Build(groups[a], now, d.Config.MeetingTime, dates)
		if err != nil {
			return "", err
		}
		total := len(sec.Blockers)
		for _, day := range sec.Days {
			total += len(day.Tasks)
		}
		if total == 0 {
			continue
		}
		var out agent.Generated
		var genErr error
		if err := spin("generating standup", func() error {
			out, genErr = assist.Generate(cmd.Context(), sec)
			return nil
		}); err != nil {
			return "", err
		}
		if genErr != nil {
			return "", genErr
		}
		// The fallback fires precisely when the input is large and messy —
		// exactly when verbatim task text is least usable — so say so
		// instead of shipping a raw commit dump as if the model wrote it.
		if out.Fallback != "" {
			if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "note: %s; using the task texts verbatim\n", out.Fallback); err != nil {
				return "", err
			}
		}
		if team {
			// Every block gets a heading — a reader could not tell whose the
			// first one was — and the day headings nest under it.
			b.WriteString("## " + authorHeading(a) + "\n")
			b.WriteString(strings.TrimRight(demoteHeadings(out.Text), "\n") + "\n")
			continue
		}
		b.WriteString(strings.TrimRight(out.Text, "\n") + "\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// gitName is swappable so CLI tests never depend on a real repository.
var gitName = git.Name

// authorHeading names a team-report block. Tasks with no recorded author are
// the person running the report: their commits predate --all-authors, or they
// typed them in.
func authorHeading(author string) string {
	if author != "" {
		return author
	}
	if name, err := gitName("."); err == nil && name != "" {
		return name
	}
	if email, err := gitIdentity("."); err == nil && email != "" {
		return email
	}
	return "unattributed"
}

// demoteHeadings pushes a rendered report one level down so its days nest
// under the author heading instead of sitting beside it.
func demoteHeadings(out string) string {
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "## ") {
			lines[i] = "#" + line
		}
	}
	return strings.Join(lines, "\n")
}

// deliverReport fans the rendered report out to the requested sinks:
// webhook POST, email, clipboard.
func deliverReport(cmd *cobra.Command, cfg config.Config, out string) error {
	var errs []error
	// A failure is reported once, by returning it: printing it here too put
	// the same fact on both streams.
	deliver := func(name string, fn func() error) {
		if err := fn(); err != nil {
			errs = append(errs, err)
			return
		}
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "%s: delivered\n", name); err != nil {
			errs = append(errs, err)
		}
	}
	if u := flagString(cmd, "webhook"); u != "" {
		deliver("webhook", func() error { return postWebhook(u, out) })
	}
	if addr := flagString(cmd, "mail"); addr != "" {
		deliver("mail", func() error { return mailReport(cfg, addr, out) })
	}
	if flagBool(cmd, "clip") {
		deliver("clipboard", func() error { return copyToClipboard(out) })
	}
	return errors.Join(errs...)
}

// runSpeak renders the report and rewrites it as a spoken brief. The script
// is always printed — the preview; -o additionally synthesizes it into an
// audio file. Nothing is stored: speak is a read-only command.
func runSpeak(cmd *cobra.Command, d Deps, args []string) error {
	now, err := nowIn(d)
	if err != nil {
		return err
	}
	dates, err := generateDates(cmd, args, now)
	if err != nil {
		return err
	}
	tasks, err := d.Store.List()
	if err != nil {
		return err
	}
	out, err := renderReport(cmd, d, tasks, now, dates)
	if err != nil {
		return err
	}
	if out == "" {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "nothing to report")
		return err
	}
	assist, err := d.Assistant()
	if err != nil {
		return err
	}
	var script string
	var scriptErr error
	if err := spin("writing the brief", func() error {
		script, scriptErr = assist.Script(cmd.Context(), out)
		return nil
	}); err != nil {
		return err
	}
	if scriptErr != nil {
		return scriptErr
	}
	if _, err := fmt.Fprintln(cmd.OutOrStdout(), script); err != nil {
		return err
	}
	path := flagString(cmd, "output")
	if path == "" {
		return nil
	}
	var audio []byte
	var synthErr error
	if err := spin("synthesizing speech", func() error {
		audio, synthErr = assist.Synthesize(cmd.Context(), script)
		return nil
	}); err != nil {
		return err
	}
	if synthErr != nil {
		return fmt.Errorf("audio synthesis failed (the script was printed above): %w", synthErr)
	}
	if err := os.WriteFile(filepath.Clean(path), audio, 0o644); err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", path)
	return err
}

// generateDates resolves the report window: explicit --from/--to dates, the
// weekend-aware default (last working day + today), or trailing N days.
func generateDates(cmd *cobra.Command, args []string, now time.Time) ([]time.Time, error) {
	from, to := flagString(cmd, "from"), flagString(cmd, "to")
	if from != "" || to != "" {
		if from == "" {
			from = to
		}
		if to == "" {
			to = from
		}
		if len(args) > 0 {
			return nil, fmt.Errorf("usage: %s generate: [days] and --from/--to are mutually exclusive", cmd.Root().Name())
		}
		f, err := time.ParseInLocation("2006-01-02", from, now.Location())
		if err != nil {
			return nil, fmt.Errorf("usage: %s generate --from YYYY-MM-DD (got %q)", cmd.Root().Name(), from)
		}
		t, err := time.ParseInLocation("2006-01-02", to, now.Location())
		if err != nil {
			return nil, fmt.Errorf("usage: %s generate --to YYYY-MM-DD (got %q)", cmd.Root().Name(), to)
		}
		if t.Before(f) {
			return nil, fmt.Errorf("usage: %s generate: --to must not be before --from", cmd.Root().Name())
		}
		var dates []time.Time
		for d := report.StartOfDay(f); !d.After(report.StartOfDay(t)); d = d.AddDate(0, 0, 1) {
			dates = append(dates, d)
		}
		return dates, nil
	}
	days := 2
	if len(args) == 1 {
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 1 {
			return nil, fmt.Errorf("usage: %s generate [days] (days >= 1)", cmd.Root().Name())
		}
		days = n
	}
	if days == 2 {
		return report.DefaultWindow(now), nil
	}
	return report.Trailing(now, days), nil
}

// copyToClipboard is swappable so tests never need a real clipboard.
var copyToClipboard = copyClipboard

// postWebhook is swappable so tests never need a real webhook target.
var postWebhook = postReport

// smtpSend is swappable so tests never need a real SMTP server.
var smtpSend = smtp.SendMail

// mailReport sends the report as a plain-text email via SMTP (SendMail does
// STARTTLS when the server offers it; auth only when smtp_user is set).
func mailReport(cfg config.Config, to, text string) error {
	if cfg.SMTPHost == "" {
		return fmt.Errorf("mail: smtp_host is not configured (set smtp_* in config.yaml)")
	}
	from := cfg.MailFrom
	if from == "" {
		from = cfg.SMTPUser
	}
	if strings.ContainsAny(to, "\r\n") || strings.ContainsAny(from, "\r\n") {
		return errors.New("mail: address contains a newline")
	}
	var auth smtp.Auth
	if cfg.SMTPUser != "" {
		auth = smtp.PlainAuth("", cfg.SMTPUser, cfg.SMTPPassword, cfg.SMTPHost)
	}
	msg := "From: " + from + "\r\nTo: " + to +
		"\r\nSubject: standup " + time.Now().Format("2006-01-02") +
		"\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" +
		strings.ReplaceAll(text, "\n", "\r\n")
	addr := net.JoinHostPort(cfg.SMTPHost, strconv.Itoa(cfg.SMTPPort))
	if err := smtpSend(addr, auth, from, []string{to}, []byte(msg)); err != nil {
		return fmt.Errorf("mail: %w", err)
	}
	return nil
}

// postReport POSTs the report as a Slack-compatible JSON payload
// ({"text": ...}) — a sensible generic body for any JSON webhook too.
func postReport(url, text string) error {
	body, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webhook: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			err = errors.Join(err, cerr)
		}
	}()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook: %s", resp.Status)
	}
	return nil
}

func copyClipboard(text string) error {
	name, args, err := clipboardCommand(runtime.GOOS, exec.LookPath)
	if err != nil {
		return err
	}
	return writeClipboard(name, args, text)
}

// clipboardCommand picks the platform's clipboard writer.
func clipboardCommand(goos string, lookPath func(string) (string, error)) (string, []string, error) {
	switch goos {
	case "windows":
		return "clip", nil, nil
	case "darwin":
		return "pbcopy", nil, nil
	}
	for _, c := range []string{"wl-copy", "xclip", "xsel"} {
		if _, err := lookPath(c); err == nil {
			if c == "xclip" {
				return c, []string{"-selection", "clipboard"}, nil
			}
			return c, nil, nil
		}
	}
	return "", nil, errors.New("no clipboard command found (install xclip or wl-clipboard)")
}

// writeClipboard pipes the text to the clipboard command without giving it
// output pipes. On Wayland `wl-copy` owns the selection from a forked child
// that inherits them, so collecting its output waits for that child to die:
// `generate --clip` copied the report and then hung forever, printing nothing.
func writeClipboard(name string, args []string, text string) error {
	c := exec.Command(name, args...)
	c.Stdin = strings.NewReader(text)
	if err := c.Run(); err != nil {
		return fmt.Errorf("clipboard %s: %w", name, err)
	}
	return nil
}

func runDoctor(cmd *cobra.Command, d Deps) error {
	out := cmd.OutOrStdout()
	healthy := true
	var werr error
	report := func(format string, a ...any) {
		if _, err := fmt.Fprintf(out, format, a...); err != nil && werr == nil {
			werr = err
		}
	}
	check := func(name string, err error) {
		if err != nil {
			healthy = false
			report("fail %s: %v\n", name, err)
			return
		}
		report("ok   %s\n", name)
	}
	check("data file writable ("+d.Config.DataFile+")", checkWritable(d.Config.DataFile))
	if email, err := gitIdentity("."); err != nil {
		check("git identity", err)
	} else {
		report("ok   git identity (%s)\n", email)
	}
	if d.Config.Offline {
		report("ok   offline mode — endpoint checks skipped\n")
	} else {
		doctorProvider(cmd, d.Config, report, check)
	}
	if werr != nil {
		return werr
	}
	return errOr(healthy)
}

// modelCheck is swappable so CLI tests never need a model endpoint.
var modelCheck = agent.Check

// doctorProvider checks the provider setup end to end. Present variables and
// a host that answers prove nothing about a dead key or a model that does not
// exist, so the last step is a real model call.
func doctorProvider(cmd *cobra.Command, cfg config.Config, report func(string, ...any), check func(string, error)) {
	required, err := config.ProviderEnv(cfg.Provider)
	if err != nil {
		check("provider", err)
		return
	}
	missing := false
	for _, key := range required {
		if os.Getenv(key) == "" {
			check("env "+key, errors.New("not set (required for add/generate, or set offline: true)"))
			missing = true
			continue
		}
		report("ok   env %s\n", key)
	}
	reportOptionalOpenAIKey(cfg.Provider, report)
	if missing {
		return
	}
	if err := reachable(os.Getenv(required[0]), required[0]); err != nil {
		check("endpoint reachable", err)
		return
	}
	report("ok   endpoint reachable\n")
	check("model answers", modelCheck(cmd.Context(), cfg))
}

func reportOptionalOpenAIKey(provider string, report func(string, ...any)) {
	if (provider == "" || provider == "openai") && os.Getenv("OPENAI_API_KEY") == "" {
		report("note env OPENAI_API_KEY not set (optional for OpenAI-compatible endpoints)\n")
	}
}

func errOr(healthy bool) error {
	if !healthy {
		return errors.New("doctor: problems found")
	}
	return nil
}

// gitIdentity is swappable so CLI tests never depend on a real repo.
var gitIdentity = git.Identity

// checkWritable probes the data file without creating it: a read-only
// diagnostic left an empty tasks.jsonl behind on machines that had never run
// standup. An existing file is opened for append; otherwise the parent
// directory is probed with a temporary file that is removed again.
func checkWritable(path string) error {
	// The data dir is created lazily by the first add; doctor (the natural
	// first command after install) must not fail on a fresh install.
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		return f.Close()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	probe, err := os.CreateTemp(dir, ".standup-doctor-*")
	if err != nil {
		return err
	}
	if err := probe.Close(); err != nil {
		return errors.Join(err, os.Remove(probe.Name()))
	}
	return os.Remove(probe.Name())
}

// reachable reports whether the endpoint answers at all (any HTTP status).
var reachable = func(base, envKey string) error {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(base)
	if err != nil {
		return fmt.Errorf("%w (check %s and network)", err, envKey)
	}
	return resp.Body.Close()
}
