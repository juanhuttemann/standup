package store

import "strings"

// InferStatus derives a task's status from the task's own text. Go owns
// statuses, never the model: asked to judge, a model invented `blocked` for
// routine work, and an invented blocker reaches the team's Blockers section.
//
// Explicit impediment wording is a blocker, work described in the past tense
// is done, everything else is still to do. The rules are English-only by
// design — an entry in another language lands on todo, and every status is
// one `standup status <id>` away.
func InferStatus(text string) string {
	words := statusWords(text)
	if mentionsImpediment(words) {
		return "blocked"
	}
	if verb := firstVerb(words); verb != "" && pastTense(verb) {
		return "done"
	}
	return "todo"
}

// impediments are the phrases that make an entry a blocker, as word
// sequences: "unblocked the pipeline" is finished work, not a blocker.
var impediments = [][]string{
	{"blocked"}, {"blocker"}, {"blockers"},
	{"waiting", "on"}, {"waiting", "for"},
	{"stuck", "on"}, {"stuck", "with"},
	{"cannot", "proceed"}, {"can", "t", "proceed"},
}

// irregularPast are the past-tense verbs the -ed rule cannot see.
var irregularPast = map[string]bool{
	"wrote": true, "sent": true, "built": true, "made": true, "ran": true,
	"did": true, "took": true, "gave": true, "found": true, "began": true,
	"spent": true, "went": true, "got": true, "rewrote": true,
}

// notPast are the -ed words that are not past-tense verbs, so "need to feed
// the cache" stays a todo.
var notPast = map[string]bool{
	"need": true, "feed": true, "seed": true, "weed": true, "deed": true,
	"heed": true, "reed": true, "speed": true, "exceed": true,
	"proceed": true, "succeed": true, "embed": true, "indeed": true,
}

// statusWords lowercases the text and keeps letters and digits only, so
// punctuation, tags and markup never break phrase matching.
func statusWords(text string) []string {
	return strings.Fields(strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return ' '
		}
	}, text))
}

func mentionsImpediment(words []string) bool {
	for i := range words {
		for _, phrase := range impediments {
			if matchesAt(words, i, phrase) {
				return true
			}
		}
	}
	return false
}

func matchesAt(words []string, i int, phrase []string) bool {
	if i+len(phrase) > len(words) {
		return false
	}
	for j, want := range phrase {
		if words[i+j] != want {
			return false
		}
	}
	return true
}

// firstVerb returns the word carrying the tense: the first one, past a
// leading pronoun ("i fixed the bug", "we deployed staging").
func firstVerb(words []string) string {
	for _, w := range words {
		if w == "i" || w == "we" {
			continue
		}
		return w
	}
	return ""
}

func pastTense(word string) bool {
	if irregularPast[word] {
		return true
	}
	return !notPast[word] && len(word) >= 4 && strings.HasSuffix(word, "ed")
}
