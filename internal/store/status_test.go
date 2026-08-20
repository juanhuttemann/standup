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
