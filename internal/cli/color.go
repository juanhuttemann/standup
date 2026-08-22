package cli

import (
	"io"
	"os"
	"runtime"

	"golang.org/x/term"
)

// Terminal palette: dark-terminal tuned, hues in the GitHub-Primer brightness
// band. Status colors map 1:1 to the store's validated statuses; Quiet Gray
// keeps ids and timestamps recessive.
const (
	ansiTodo       = "\x1b[38;2;255;171;112m" // Morning Amber  #FFAB70
	ansiInProgress = "\x1b[38;2;88;166;255m"  // Focus Blue    #58A6FF
	ansiBlocked    = "\x1b[38;2;248;81;73m"   // Blocker Red   #F85149
	ansiDone       = "\x1b[38;2;63;185;80m"   // Shipped Green #3FB950
	ansiQuiet      = "\x1b[38;2;139;148;158m" // Quiet Gray    #8B949E
	ansiReset      = "\x1b[0m"
)

// painter wraps strings in ANSI truecolor escapes; with on=false every
// method passes text through unchanged.
// ponytail: truecolor only — add a 256-color downgrade if a legacy terminal
// ever shows garbage.
type painter struct{ on bool }

// newPainter enables color only for terminal writers without NO_COLOR set
// (https://no-color.org).
func newPainter(w io.Writer) painter {
	f, ok := w.(*os.File)
	terminal := ok && term.IsTerminal(int(f.Fd()))
	return painter{on: truecolorSupported(runtime.GOOS, terminal, os.Getenv("NO_COLOR") != "")}
}

// truecolorSupported excludes Windows: promptui's readline ANSI writer
// panics while parsing 24-bit color sequences used in interactive lists.
func truecolorSupported(goos string, terminal, noColor bool) bool {
	return goos != "windows" && terminal && !noColor
}

func (p painter) wrap(code, s string) string {
	if !p.on || s == "" {
		return s
	}
	return code + s + ansiReset
}

// statusColor is the palette entry for a validated status, empty for
// anything else.
func statusColor(s string) string {
	switch s {
	case "todo":
		return ansiTodo
	case "in-progress":
		return ansiInProgress
	case "blocked":
		return ansiBlocked
	case "done":
		return ansiDone
	}
	return ""
}

func (p painter) status(s string) string {
	code := statusColor(s)
	if code == "" {
		return s
	}
	return p.wrap(code, s)
}

func (p painter) quiet(s string) string { return p.wrap(ansiQuiet, s) }
