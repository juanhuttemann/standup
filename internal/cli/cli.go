package cli

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
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
	"standup/internal/report"
	"standup/internal/store"
)

// gitLog is swappable so CLI tests never depend on a real repository.
var gitLog = git.Log

// Deps carries everything a command needs; it is built lazily so help,
// version, and init never touch config, store, or provider settings. The
// assistant itself is lazy on top: read-only commands (list, done, rm,
// status, edit, commits) never require provider credentials.
type Deps struct {
	// Assistant builds the model-backed assistant on first use (add and
	// generate online; nil for commands that never call a model).
	Assistant func() (agent.Assistant, error)
	// Raw is the deterministic assistant used by `add --raw`.
	Raw    agent.Assistant
	Store  *store.Store
	Config config.Config
}

func New(load func() (Deps, error)) *cobra.Command {
	root := &cobra.Command{
		Use:          "standup",
		Short:        "AI-assisted standup CLI",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			text, err := cmd.Flags().GetString("add")
			if err != nil {
				return err
			}
			list, err := cmd.Flags().GetBool("list")
			if err != nil {
				return err
			}
			gen, err := cmd.Flags().GetBool("generate")
			if err != nil {
				return err
			}
			switch {
			case cmd.Flags().Changed("add"):
				d, err := load()
				if err != nil {
					return err
				}
				return runAdd(cmd, d, []string{text})
			case list:
				d, err := load()
				if err != nil {
					return err
				}
				return runList(cmd, d)
			case gen:
				d, err := load()
				if err != nil {
					return err
				}
				return runGenerate(cmd, d, args)
			}
			return cmd.Help()
		},
	}
	root.Flags().StringP("add", "a", "", "task text")
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

	commitsCmd := &cobra.Command{
		Use:   "commits [days] [paths...]",
		Args:  cobra.ArbitraryArgs,
		Short: "turn git commits from the last working day (or N days) into tasks",
		RunE: func(cmd *cobra.Command, args []string) error {
			return lazy(cmd, load, func(c *cobra.Command, d Deps) error { return runCommits(c, d, args) })
		},
		SilenceUsage: true,
	}

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
		Short: "write default config files to the user config dir",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := config.Init()
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "config dir: %s (existing files kept)\n", dir)
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

	root.AddCommand(addCmd, listCmd, genCmd, commitsCmd, doneCmd, editCmd, rmCmd, statusCmd, initCmd, doctorCmd, skillCmd)
	return root
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
	for _, r := range roots {
		if err := os.MkdirAll(r.dir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(r.dir, "SKILL.md"), []byte(defaults.SkillMD), 0o644); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "skill: %s (%s)\n", filepath.Join(r.dir, "SKILL.md"), r.who); err != nil {
			return err
		}
	}
	return nil
}

// lazy loads deps, then runs the command body.
func lazy(cmd *cobra.Command, load func() (Deps, error), run func(*cobra.Command, Deps) error) error {
	d, err := load()
	if err != nil {
		return err
	}
	return run(cmd, d)
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
	if err := d.Store.Delete(task.ID); err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "- removed: %s\n", task.Text)
	return err
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

// flat collapses a task text to one row: multi-line entries (commit bodies)
// must not break the column layout.
func flat(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// echoTask prints the mutated row so silent mutations never happen.
func echoTask(cmd *cobra.Command, t store.Task) error {
	p := newPainter(cmd.OutOrStdout())
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "- [%s] %s\n", p.status(t.Status), flat(t.Text))
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
	parts := strings.Fields(editor)
	c := exec.Command(parts[0], append(parts[1:], f.Name())...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("editor %q: %w", editor, err)
	}
	b, err := os.ReadFile(f.Name())
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
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
	text := strings.Join(args, " ")
	if strings.TrimSpace(text) == "" && !interactive() {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		text = string(b)
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("usage: %s add \"task text\" (or pipe text to %s add)", cmd.Root().Name(), cmd.Root().Name())
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
	for _, t := range tasks {
		if err := echoTask(cmd, t); err != nil {
			return err
		}
	}
	return nil
}

func runCommits(cmd *cobra.Command, d Deps, args []string) error {
	days, paths, err := commitsArgs(cmd, args)
	if err != nil {
		return err
	}
	now := d.Store.Now()
	since := report.StartOfDay(report.LastWorkingDay(now))
	if days > 0 {
		since = report.StartOfDay(now.AddDate(0, 0, -(days - 1)))
	}
	commits, err := collectCommits(paths, since)
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
	for _, p := range rest {
		if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
			return 0, nil, fmt.Errorf("usage: %s commits [days] [paths...] (no such directory: %q)", cmd.Root().Name(), p)
		}
	}
	return days, rest, nil
}

// collectCommits gathers commits from every repo, deduped by hash and
// ordered oldest first across repos.
func collectCommits(paths []string, since time.Time) ([]git.Commit, error) {
	seen := map[string]bool{}
	var commits []git.Commit
	for _, p := range paths {
		cs, err := gitLog(p, since)
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

// importCommits stores commits as done tasks stamped with the commit time;
// commits already imported (same text, same day) are skipped.
func importCommits(cmd *cobra.Command, st *store.Store, commits []git.Commit) error {
	existing, err := st.List()
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
			return err
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

func runList(cmd *cobra.Command, d Deps) error {
	st := d.Store
	now := st.Now()
	date := flagString(cmd, "date")
	days := flagInt(cmd, "days")
	tag := flagString(cmd, "tag")
	if date != "" && days != 0 {
		return fmt.Errorf("usage: %s list: --date and --days are mutually exclusive", cmd.Root().Name())
	}

	var tasks []store.Task
	var err error
	switch {
	case date != "":
		var day time.Time
		day, err = time.ParseInLocation("2006-01-02", date, now.Location())
		if err != nil {
			return fmt.Errorf("usage: %s list --date YYYY-MM-DD (got %q)", cmd.Root().Name(), date)
		}
		tasks, err = st.ListDay(day)
		if err != nil {
			return err
		}
	case days != 0:
		if days < 0 {
			return fmt.Errorf("usage: %s list --days N (N >= 1)", cmd.Root().Name())
		}
		tasks, err = st.ListRange(report.StartOfDay(now.AddDate(0, 0, -(days-1))), now)
		if err != nil {
			return err
		}
	default:
		tasks, err = st.ListDay(now)
		if err != nil {
			return err
		}
		tasks = filterTag(tasks, tag)
		if len(tasks) == 0 {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "no tasks")
			return err
		}
		if interactive() {
			return selectLoop(st, tag, newPainter(cmd.OutOrStdout()))
		}
	}
	tasks = filterTag(tasks, tag)
	if len(tasks) == 0 {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "no tasks")
		return err
	}
	p := newPainter(cmd.OutOrStdout())
	for _, t := range tasks {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %s  %s\n", p.quiet(shortID(t.ID)), p.status(t.Status), p.quiet(t.Timestamp.Format("15:04")), flat(t.Text)); err != nil {
			return err
		}
	}
	return nil
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

type taskEntry struct {
	task  store.Task
	label string
}

func taskEntries(st *store.Store, tag string, p painter) ([]taskEntry, error) {
	tasks, err := st.ListDay(st.Now())
	if err != nil {
		return nil, err
	}
	entries := make([]taskEntry, 0, len(tasks))
	for _, t := range filterTag(tasks, tag) {
		entries = append(entries, taskEntry{task: t, label: fmt.Sprintf("%s [%s] %s %s", p.quiet(t.Timestamp.Format("15:04")), p.status(t.Status), p.quiet(shortID(t.ID)), flat(t.Text))})
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

func selectLoop(st *store.Store, tag string, p painter) error {
	actions := []string{"in-progress", "done", "blocked", "delete", "back"}
	for {
		entries, err := taskEntries(st, tag, p)
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
	dates, err := generateDates(cmd, args, d.Store.Now())
	if err != nil {
		return err
	}
	tasks, err := d.Store.List()
	if err != nil {
		return err
	}
	sec, err := report.Build(tasks, d.Store.Now(), d.Config.MeetingTime, dates)
	if err != nil {
		return err
	}
	total := len(sec.Blockers)
	for _, day := range sec.Days {
		total += len(day.Tasks)
	}
	if total == 0 {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "nothing to report")
		return err
	}
	assist, err := d.Assistant()
	if err != nil {
		return err
	}
	var out string
	var genErr error
	if err := spin("generating standup", func() error {
		out, genErr = assist.Generate(cmd.Context(), sec)
		return nil
	}); err != nil {
		return err
	}
	if genErr != nil {
		return genErr
	}
	if flagBool(cmd, "clip") {
		if err := copyToClipboard(out); err != nil {
			return err
		}
	}
	if path := flagString(cmd, "output"); path != "" {
		return os.WriteFile(filepath.Clean(path), []byte(out+"\n"), 0o644)
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), colorReport(out, newPainter(cmd.OutOrStdout())))
	return err
}

// generateDates resolves the report window: explicit --from/--to dates, the
// weekend-aware default (last working day + today), or trailing N days.
func generateDates(cmd *cobra.Command, args []string, now time.Time) ([]time.Time, error) {
	from, to := flagString(cmd, "from"), flagString(cmd, "to")
	if from != "" || to != "" {
		if from == "" || to == "" {
			return nil, fmt.Errorf("usage: %s generate --from and --to are both required (YYYY-MM-DD)", cmd.Root().Name())
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

func copyClipboard(text string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "windows":
		name = "clip"
	case "darwin":
		name = "pbcopy"
	default:
		for _, c := range []string{"wl-copy", "xclip", "xsel"} {
			if _, err := exec.LookPath(c); err == nil {
				name = c
				break
			}
		}
		if name == "" {
			return errors.New("no clipboard command found (install xclip or wl-clipboard)")
		}
		if name == "xclip" {
			args = []string{"-selection", "clipboard"}
		}
	}
	c := exec.Command(name, args...)
	c.Stdin = strings.NewReader(text)
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("clipboard %s: %w: %s", name, err, strings.TrimSpace(string(out)))
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
	check("data file writable", checkWritable(d.Config.DataFile))
	if email, err := gitIdentity("."); err != nil {
		check("git identity", err)
	} else {
		report("ok   git identity (%s)\n", email)
	}
	if d.Config.Offline {
		report("ok   offline mode — endpoint checks skipped\n")
		if werr != nil {
			return werr
		}
		return errOr(healthy)
	}
	for _, key := range []string{"OPENAI_BASE_URL", "OPENAI_MODEL"} {
		if os.Getenv(key) == "" {
			check("env "+key, fmt.Errorf("not set (required for add/generate, or set offline: true)"))
		} else {
			report("ok   env %s\n", key)
		}
	}
	if base := os.Getenv("OPENAI_BASE_URL"); base != "" {
		check("endpoint reachable", reachable(base))
	}
	if werr != nil {
		return werr
	}
	return errOr(healthy)
}

func errOr(healthy bool) error {
	if !healthy {
		return errors.New("doctor: problems found")
	}
	return nil
}

// gitIdentity is swappable so CLI tests never depend on a real repo.
var gitIdentity = git.Identity

func checkWritable(path string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}

// reachable reports whether the endpoint answers at all (any HTTP status).
var reachable = func(base string) error {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(base)
	if err != nil {
		return fmt.Errorf("%w (check OPENAI_BASE_URL and network)", err)
	}
	return resp.Body.Close()
}
