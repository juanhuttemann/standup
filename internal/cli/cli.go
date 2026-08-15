package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"standup/internal/agent"
	"standup/internal/config"
	"standup/internal/report"
	"standup/internal/store"
)

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
				return runGenerate(cmd, ass, st, cfg)
			}
			return cmd.Help()
		},
	}
	root.Flags().StringP("add", "a", "", "task text")
	root.Flags().BoolP("list", "l", false, "list today's tasks")
	root.Flags().BoolP("generate", "g", false, "generate the standup report")

	root.AddCommand(
		&cobra.Command{
			Use:          "add",
			Args:         cobra.ArbitraryArgs,
			RunE:         func(cmd *cobra.Command, args []string) error { return runAdd(cmd, ass, args) },
			SilenceUsage: true,
		},
		&cobra.Command{
			Use:          "list",
			RunE:         func(cmd *cobra.Command, args []string) error { return runList(cmd, st) },
			SilenceUsage: true,
		},
		&cobra.Command{
			Use:          "generate",
			Aliases:      []string{"g"},
			RunE:         func(cmd *cobra.Command, args []string) error { return runGenerate(cmd, ass, st, cfg) },
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
	tasks, err := st.ListDay(st.Now())
	if err != nil {
		return err
	}
	if len(tasks) == 0 {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), "no tasks today")
		return err
	}
	if interactive() {
		return selectLoop(st)
	}
	for _, t := range tasks {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %s  %s\n", t.ID, t.Status, t.Timestamp.Format("15:04"), t.Text); err != nil {
			return err
		}
	}
	return nil
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

func taskEntries(st *store.Store) ([]taskEntry, error) {
	tasks, err := st.ListDay(st.Now())
	if err != nil {
		return nil, err
	}
	entries := make([]taskEntry, len(tasks))
	for i, t := range tasks {
		entries[i] = taskEntry{task: t, label: fmt.Sprintf("%s [%s] %s %s", t.Timestamp.Format("15:04"), t.Status, shortID(t.ID), t.Text)}
	}
	return entries, nil
}

func selectLoop(st *store.Store) error {
	actions := []string{"in-progress", "done", "delete", "back"}
	for {
		entries, err := taskEntries(st)
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
		if err == promptui.ErrInterrupt || err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		j, _, err := (&promptui.Select{Label: "action", Items: actions}).Run()
		if err == promptui.ErrInterrupt || err == io.EOF {
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

func runGenerate(cmd *cobra.Command, ass agent.Assistant, st *store.Store, cfg config.Config) error {
	tasks, err := st.List()
	if err != nil {
		return err
	}
	sec, err := report.Build(tasks, st.Now(), cfg.MeetingTime)
	if err != nil {
		return err
	}
	if len(sec.Yesterday) == 0 && len(sec.Today) == 0 {
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
	_, err = fmt.Fprintln(cmd.OutOrStdout(), out)
	return err
}
