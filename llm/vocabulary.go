package llm

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxVocabularyPromptTerms and MaxVocabularyPromptBytes cap how much of
// Vocabulary.Terms goes into the transcription prompt. Terms past the cap are
// still used for post-correction.
const (
	MaxVocabularyPromptTerms = 200
	MaxVocabularyPromptBytes = 3000
)

// Vocabulary is a dictionary of well-known terms that corrects voice input in
// two steps: Terms bias the transcription prompt ("spell exactly like this"),
// then Correct rewrites the transcript deterministically.
//
// Correct works on word tokens (letters and digits; punctuation between them
// is kept outside a match and dropped inside it), case-insensitive and whole
// word. At each position it tries, longest first:
//
//  1. Replacements: a heard variant ("pfeifer und mai") becomes its canonical
//     term ("PFEIFFER & MAY").
//  2. Terms matched exactly once spaces, punctuation and case are ignored:
//     "Liv 60 g" becomes "LIV60G", "wasch tisch" becomes "Waschtisch".
//  3. With Fuzzy, terms of at least 6 letters matched with an edit distance of
//     1 (2 from 9 letters on), same first letter, and every heard word at
//     least 4 letters long: "Duschtase" becomes "Duschtasse". Short common
//     words are never touched.
type Vocabulary struct {
	Terms        []string
	Replacements map[string]string
	Fuzzy        bool
}

// promptLine is the instruction block appended to the transcription prompt,
// or "" without terms.
func (v *Vocabulary) promptLine() string {
	if v == nil {
		return ""
	}
	var b strings.Builder
	n := 0
	for _, t := range v.Terms {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if n == MaxVocabularyPromptTerms || b.Len()+len(t)+2 > MaxVocabularyPromptBytes {
			break
		}
		if n > 0 {
			b.WriteString(", ")
		}
		b.WriteString(t)
		n++
	}
	if n == 0 {
		return ""
	}
	return "Expected terms; when you hear one, spell it exactly like this: " + b.String() + "."
}

type token struct {
	start, end int    // byte span in the text
	norm       string // lowercase
}

func tokenize(s string) []token {
	var toks []token
	start := -1
	for i, r := range s {
		word := unicode.IsLetter(r) || unicode.IsDigit(r)
		switch {
		case word && start < 0:
			start = i
		case !word && start >= 0:
			toks = append(toks, token{start, i, strings.ToLower(s[start:i])})
			start = -1
		}
	}
	if start >= 0 {
		toks = append(toks, token{start, len(s), strings.ToLower(s[start:])})
	}
	return toks
}

func joinNorm(toks []token) string {
	var b strings.Builder
	for _, t := range toks {
		b.WriteString(t.norm)
	}
	return b.String()
}

type rule struct {
	words []string // replacement variant, tokenized
	key   string   // term, normalized with no separators
	canon string
	fuzzy bool
}

// Correct returns text with the vocabulary applied. A nil Vocabulary returns
// text unchanged.
func (v *Vocabulary) Correct(text string) string {
	if v == nil || text == "" {
		return text
	}
	var reps, terms []rule
	for heard, canon := range v.Replacements {
		var words []string
		for _, t := range tokenize(heard) {
			words = append(words, t.norm)
		}
		if len(words) > 0 && canon != "" {
			reps = append(reps, rule{words: words, canon: canon})
		}
	}
	// Longest variant first, then a stable order so a map never decides.
	sort.Slice(reps, func(i, j int) bool {
		if len(reps[i].words) != len(reps[j].words) {
			return len(reps[i].words) > len(reps[j].words)
		}
		return strings.Join(reps[i].words, " ") < strings.Join(reps[j].words, " ")
	})
	for _, t := range v.Terms {
		key := joinNorm(tokenize(t))
		if key == "" {
			continue
		}
		terms = append(terms, rule{key: key, canon: strings.TrimSpace(t), fuzzy: v.Fuzzy && utf8.RuneCountInString(key) >= 6})
	}
	sort.SliceStable(terms, func(i, j int) bool { return len(terms[i].key) > len(terms[j].key) })

	toks := tokenize(text)
	var b strings.Builder
	last := 0
	for i := 0; i < len(toks); {
		n, canon := matchAt(toks[i:], reps, terms)
		if n == 0 {
			i++
			continue
		}
		b.WriteString(text[last:toks[i].start])
		b.WriteString(canon)
		last = toks[i+n-1].end
		i += n
	}
	b.WriteString(text[last:])
	return b.String()
}

// maxTermWindow bounds how many heard words one term may span ("L I V 6 0 G").
const maxTermWindow = 6

func matchAt(toks []token, reps, terms []rule) (int, string) {
	for _, r := range reps {
		if len(r.words) > len(toks) {
			continue
		}
		ok := true
		for k, w := range r.words {
			if toks[k].norm != w {
				ok = false
				break
			}
		}
		if ok {
			return len(r.words), r.canon
		}
	}
	// Exact match over the widest window first, so "liv 60 g" wins over "liv".
	for n := min(maxTermWindow, len(toks)); n >= 1; n-- {
		heard := joinNorm(toks[:n])
		for _, t := range terms {
			if heard == t.key {
				return n, t.canon
			}
		}
	}
	for n := min(3, len(toks)); n >= 1; n-- {
		short := false
		for _, t := range toks[:n] {
			if utf8.RuneCountInString(t.norm) < 4 {
				short = true
			}
		}
		if short {
			continue
		}
		heard := []rune(joinNorm(toks[:n]))
		for _, t := range terms {
			if !t.fuzzy {
				continue
			}
			key := []rune(t.key)
			maxDist := 1
			if len(key) >= 9 {
				maxDist = 2
			}
			if heard[0] != key[0] || abs(len(heard)-len(key)) > maxDist {
				continue
			}
			if levenshtein(heard, key) <= maxDist {
				return n, t.canon
			}
		}
	}
	return 0, ""
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func levenshtein(a, b []rune) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
