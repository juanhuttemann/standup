package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"standup/internal/agent"
	"standup/internal/config"
	"standup/internal/git"
	"standup/internal/report"
	"standup/internal/store"
)

// gitLog is swappable so CLI tests never depend on a real repository.
var gitLog = git.Log

func New(ass agent.Assistant, st *store.Store, cfg config.Config) *cobra.Command {
	root := &cobra.Command{
		Use:          "standup",
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
				return runAdd(cmd, ass, []string{text})
			case list:
				return runList(cmd, st)
			case gen:
				return runGenerate(cmd, ass, st, cfg, nil)
			}
			return cmd.Help()
		},
	}
	root.Flags().StringP("add", "a", "", "task text")
	root.Flags().BoolP("list", "l", false, "list today's tasks")
	root.Flags().BoolP("generate", "g", false, "generate the standup report")

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "list tasks (today by default)",
		RunE:  func(cmd *cobra.Command, args []string) error { return runList(cmd, st) },
	}
	listCmd.Flags().String("date", "", "show tasks from this date (YYYY-MM-DD)")
	listCmd.Flags().Int("days", 0, "show tasks from the trailing N days")
	listCmd.Flags().String("tag", "", "show only tasks containing this tag token")

	genCmd := &cobra.Command{
		Use:          "generate [days]",
		Aliases:      []string{"g"},
		Args:         cobra.MaximumNArgs(1),
		Short:        "generate the standup report (yesterday + today by default)",
		RunE:         func(cmd *cobra.Command, args []string) error { return runGenerate(cmd, ass, st, cfg, args) },
		SilenceUsage: true,
	}
	genCmd.Flags().StringP("output", "o", "", "write the report to this file")

	root.AddCommand(
		&cobra.Command{
			Use:          "add",
			Args:         cobra.ArbitraryArgs,
			RunE:         func(cmd *cobra.Command, args []string) error { return runAdd(cmd, ass, args) },
			SilenceUsage: true,
		},
		listCmd,
		genCmd,
		&cobra.Command{
			Use:          "commits [days]",
			Args:         cobra.MaximumNArgs(1),
			Short:        "turn git commits from the last working day (or N days) into tasks",
			RunE:         func(cmd *cobra.Command, args []string) error { return runCommits(cmd, ass, st, args) },
			SilenceUsage: true,
		},
		&cobra.Command{
			Use:  "done <id>",
			Args: idArg,
			RunE: func(cmd *cobra.Command, args []string) error {
				task, err := st.FindByPrefix(args[0])
				if err != nil {
					return err
				}
				_, err = st.SetStatus(task.ID, "done")
				return err
			},
			SilenceUsage: true,
		},
		&cobra.Command{
			Use:  "edit <id> [text]",
			Args: editArg,
			RunE: func(cmd *cobra.Command, args []string) error {
				task, err := st.FindByPrefix(args[0])
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
				_, err = st.UpdateText(task.ID, text)
				return err
			},
			SilenceUsage: true,
		},
		&cobra.Command{
			Use:  "rm <id>",
			Args: idArg,
			RunE: func(cmd *cobra.Command, args []string) error {
				task, err := st.FindByPrefix(args[0])
				if err != nil {
					return err
				}
				return st.Delete(task.ID)
			},
			SilenceUsage: true,
		},
	)
	return root
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

// editInEditor opens the user's editor (EDITOR, fallback vi) on a temp file
// seeded with the current text and returns the saved content.
func editInEditor(current string) (text string, err error) {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
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

func runAdd(cmd *cobra.Command, ass agent.Assistant, args []string) error {
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
	var tasks []store.Task
	var addErr error
	if err := spin("adding tasks", func() error {
		tasks, addErr = ass.AddTasks(cmd.Context(), text)
		return nil
	}); err != nil {
		return err
	}
	if addErr != nil {
		return addErr
	}
	for _, t := range tasks {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "- %s\n", t.Text); err != nil {
			return err
		}
	}
	return nil
}

func runCommits(cmd *cobra.Command, ass agent.Assistant, st *store.Store, args []string) error {
	days := 0
	if len(args) == 1 {
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 1 {
			return fmt.Errorf("usage: %s commits [days] (days >= 1)", cmd.Root().Name())
		}
		days = n
	}
	now := st.Now()
	since := report.StartOfDay(report.LastWorkingDay(now))
	if days > 0 {
		since = report.StartOfDay(now.AddDate(0, 0, -(days - 1)))
	}
	commits, err := gitLog(".", since)
	if err != nil {
		return err
	}
	if len(commits) == 0 {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "no commits found")
		return err
	}
	subjects := make([]string, len(commits))
	for i, c := range commits {
		subjects[i] = c.Subject
	}
	return runAdd(cmd, ass, []string{strings.Join(subjects, "\n\n")})
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

func runList(cmd *cobra.Command, st *store.Store) error {
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
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %s  %s\n", p.quiet(t.ID), p.status(t.Status), p.quiet(t.Timestamp.Format("15:04")), t.Text); err != nil {
			return err
		}
	}
	return nil
}

// filterTag keeps tasks whose text contains the token, case-insensitively.
func filterTag(tasks []store.Task, tag string) []store.Task {
	if tag == "" {
		return tasks
	}
	want := strings.ToLower(tag)
	var out []store.Task
	for _, t := range tasks {
		if strings.Contains(strings.ToLower(t.Text), want) {
			out = append(out, t)
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
		entries = append(entries, taskEntry{task: t, label: fmt.Sprintf("%s [%s] %s %s", p.quiet(t.Timestamp.Format("15:04")), p.status(t.Status), p.quiet(shortID(t.ID)), t.Text)})
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

func runGenerate(cmd *cobra.Command, ass agent.Assistant, st *store.Store, cfg config.Config, args []string) error {
	days := 2
	if len(args) == 1 {
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 1 {
			return fmt.Errorf("usage: %s generate [days] (days >= 1)", cmd.Root().Name())
		}
		days = n
	}
	tasks, err := st.List()
	if err != nil {
		return err
	}
	sec, err := report.Build(tasks, st.Now(), cfg.MeetingTime, days)
	if err != nil {
		return err
	}
	total := len(sec.Blockers)
	for _, d := range sec.Days {
		total += len(d.Tasks)
	}
	if total == 0 {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "nothing to report")
		return err
	}
	var out string
	var genErr error
	if err := spin("generating standup", func() error {
		out, genErr = ass.Generate(cmd.Context(), sec)
		return nil
	}); err != nil {
		return err
	}
	if genErr != nil {
		return genErr
	}
	if path := flagString(cmd, "output"); path != "" {
		return os.WriteFile(filepath.Clean(path), []byte(out+"\n"), 0o644)
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), out)
	return err
}
