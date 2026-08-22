package store

import "strings"

// InferStatus derives a task's status from the task's own text. Go owns
// statuses, never the model: asked to judge, a model invented `blocked` for
// routine work, and an invented blocker reaches the team's Blockers section.
//
// Explicit impediment wording is a blocker. Otherwise the first status signal
// in the sentence decides: a stated intention is still to do, completion
// wording is done, and work described as underway is in progress. Reading
// only the leading verb reported finished work as not started — "the parser
// is fixed" and "finally got the parser fixed" both landed on todo — and the
// team has no reason to re-check a status the tool assigned confidently.
//
// The rules are English-only by design — an entry in another language lands
// on todo, and every status is one `standup status <id>` away.
func InferStatus(text string) string {
	words := statusWords(text)
	if mentionsImpediment(words) {
		return "blocked"
	}
	if status := firstSignal(words); status != "" {
		return status
	}
	return "todo"
}

// firstSignal returns the status named by the earliest signal in the
// sentence, empty when there is none. Position decides between competing
// readings: "need to fix the deprecated api" opens with a plan, while "fixed
// the bug, will deploy tomorrow" opens with finished work.
func firstSignal(words []string) string {
	head := headIndex(words)
	for i := range words {
		switch {
		case matchesAny(words, i, intents):
			return "todo"
		case completedAt(words, i, head):
			return "done"
		case matchesAny(words, i, progress):
			return "in-progress"
		}
	}
	return ""
}

// completedAt reports whether position i carries completion wording: a
// standalone completion word, an auxiliary followed by a past participle
// ("is fixed", "got the parser fixed"), or the sentence's own leading verb in
// the past tense.
func completedAt(words []string, i, head int) bool {
	if completions[words[i]] {
		return true
	}
	if auxiliaries[words[i]] && participleAfter(words, i) {
		return true
	}
	return i == head && pastTense(words[i])
}

// participleAfter looks for a past participle in the three words following an
// auxiliary, the span that covers "is fixed" through "got the parser fixed".
func participleAfter(words []string, i int) bool {
	for j := i + 1; j <= i+3 && j < len(words); j++ {
		if completions[words[j]] || pastTense(words[j]) {
			return true
		}
	}
	return false
}

// headIndex is the position of the sentence's leading verb: the first word
// that is not a pronoun, an adverb or a filler. A past participle only counts
// as finished work there, so "remove the deprecated api" stays a todo.
func headIndex(words []string) int {
	for i, w := range words {
		if !skippable[w] {
			return i
		}
	}
	return -1
}

// impediments are the phrases that make an entry a blocker, as word
// sequences: "unblocked the pipeline" is finished work, not a blocker.
var impediments = [][]string{
	{"blocked"}, {"blocker"}, {"blockers"},
	{"waiting", "on"}, {"waiting", "for"},
	{"stuck", "on"}, {"stuck", "with"},
	{"cannot", "proceed"}, {"can", "t", "proceed"},
}

// intents are the phrases that state work not started yet.
var intents = [][]string{
	{"need", "to"}, {"needs", "to"}, {"have", "to"}, {"has", "to"},
	{"want", "to"}, {"wants", "to"}, {"going", "to"}, {"plan", "to"},
	{"plans", "to"}, {"planning", "to"}, {"will"}, {"shall"}, {"should"},
	{"must"}, {"todo"}, {"to", "do"}, {"tomorrow"}, {"next", "up"},
}

// progress are the phrases that describe work underway.
var progress = [][]string{
	{"working", "on"}, {"in", "progress"}, {"wip"}, {"halfway"},
	{"midway"}, {"partway"},
}

// completions are the words that name finished work on their own. "complete"
// and "merged" are absent on purpose: "complete the docs" is an instruction
// and "the merged branch" is a noun phrase.
var completions = map[string]bool{"done": true, "finished": true}

// auxiliaries carry the tense of a passive or periphrastic completion, so
// "the parser is fixed" and "got the parser fixed" read as done.
var auxiliaries = map[string]bool{
	"is": true, "was": true, "are": true, "were": true, "be": true,
	"been": true, "being": true, "am": true, "got": true, "gotten": true,
	"have": true, "has": true, "had": true,
}

// skippable words may precede the sentence's leading verb without being it.
// "the" is deliberately absent: it would promote the participle in "the
// deprecated api" to the head position.
var skippable = map[string]bool{
	"i": true, "we": true, "today": true, "yesterday": true, "this": true,
	"morning": true, "afternoon": true, "finally": true, "just": true,
	"also": true, "then": true, "anyway": true, "ugh": true, "ok": true,
	"okay": true, "so": true, "well": true, "actually": true, "still": true,
	"basically": true, "quickly": true, "already": true, "now": true,
	"again": true, "eventually": true,
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
		if matchesAny(words, i, impediments) {
			return true
		}
	}
	return false
}

// matchesAny reports whether any of the phrases starts at position i.
func matchesAny(words []string, i int, phrases [][]string) bool {
	for _, phrase := range phrases {
		if matchesAt(words, i, phrase) {
			return true
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

func pastTense(word string) bool {
	if irregularPast[word] {
		return true
	}
	return !notPast[word] && len(word) >= 4 && strings.HasSuffix(word, "ed")
}
