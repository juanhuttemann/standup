package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestInferStatus(t *testing.T) {
	tests := []struct{ text, want string }{
		{"fixed the login redirect bug on the #auth service", "done"},
		{"Reviewed the release PR for the payments team", "done"},
		{"finished the docs", "done"},
		{"deployed to staging", "done"},
		{"wrote unit tests for the parser", "done"},
		{"triaged the flaky CI job", "done"},
		{"i fixed the parser", "done"},
		{"we shipped the release", "done"},
		{"unblocked the deploy pipeline", "done"},
		{"update the readme", "todo"},
		{"add API docs for today", "todo"},
		{"review the release PR", "todo"},
		{"need to feed the cache", "todo"},
		{"", "todo"},
		{"Actualicé el README", "todo"},
		{"waiting on the infra team", "blocked"},
		{"blocked on review", "blocked"},
		{"stuck on the flaky test", "blocked"},
		{"fixed the parser but blocked on CI", "blocked"},
		{"deploy is a blocker until infra answers", "blocked"},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			assert.Equal(t, tt.want, InferStatus(tt.text))
		})
	}
}

// TestInferStatusPastTenseBeyondTheLeadingVerb covers the wordings that a
// leading-verb-only rule reads as unstarted work: passive voice, an adverb
// or filler before the verb, "got X done", and "done with X". Reporting
// finished work as todo ships wrong information to the team.
func TestInferStatusPastTenseBeyondTheLeadingVerb(t *testing.T) {
	tests := []struct{ text, want string }{
		{"the parser is fixed", "done"},
		{"the migration was merged this morning", "done"},
		{"the flaky tests are fixed", "done"},
		{"finally got the parser fixed", "done"},
		{"just fixed the parser", "done"},
		{"done with the parser", "done"},
		{"finished with the billing migration", "done"},
		{"ugh spent like 4 hrs on the caching thing, anyway fixed it i think", "done"},
		{"today i finally shipped the rewrite", "done"},
		// Intent still wins when it comes first: the sentence opens with a
		// plan, so the past-tense word later in it describes context.
		{"need to fix the deprecated api", "todo"},
		{"going to rewrite the parser i broke", "todo"},
		{"will deploy the fixed build tomorrow", "todo"},
		{"tomorrow: finish the docs", "todo"},
		// A past participle inside a noun phrase is not finished work.
		{"remove the deprecated api", "todo"},
		{"review the merged branch", "todo"},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			assert.Equal(t, tt.want, InferStatus(tt.text))
		})
	}
}

// TestInferStatusInProgress covers work described as underway: `in-progress`
// is a validated status the heuristic never produced, so every "working on
// X" note landed on todo.
func TestInferStatusInProgress(t *testing.T) {
	tests := []struct{ text, want string }{
		{"working on the payment retries", "in-progress"},
		{"still working on the parser", "in-progress"},
		{"in progress: the billing migration", "in-progress"},
		{"wip refactor of the checkout flow", "in-progress"},
		{"halfway through the API docs", "in-progress"},
		// Intent and completion still win when they come first.
		{"need to keep working on the parser", "todo"},
		{"finished working on the parser", "done"},
		// A blocker outranks progress: the team needs to hear it.
		{"working on the parser but blocked on review", "blocked"},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			assert.Equal(t, tt.want, InferStatus(tt.text))
		})
	}
}
