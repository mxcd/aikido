package voice

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// minSentenceRunes keeps very short fragments ("Ja.", "z. B.") together with
// the next sentence: one synthesis call per word sounds choppy and costs a
// round trip each.
// ponytail: no abbreviation list, "z. B." past 12 runes still splits; add
// one if the prosody break is audible.
const minSentenceRunes = 12

// splitSentences cuts complete sentences off the front of buf and returns
// them with the unfinished rest. A sentence ends at . ! ? or an ellipsis
// followed by whitespace, or at a line break. A terminator at the very end of
// buf is not a boundary yet: the next delta may be "5 Euro" after "3.".
func splitSentences(buf string) (sentences []string, rest string) {
	start := 0
	for i, r := range buf {
		end := -1
		switch r {
		case '\n':
			end = i + 1
		case '.', '!', '?', '…':
			next := i + utf8.RuneLen(r)
			if next < len(buf) {
				nr, _ := utf8.DecodeRuneInString(buf[next:])
				if unicode.IsSpace(nr) {
					end = next
				}
			}
		}
		if end < 0 {
			continue
		}
		s := buf[start:end]
		if utf8.RuneCountInString(strings.TrimSpace(s)) < minSentenceRunes && r != '\n' {
			continue
		}
		if strings.TrimSpace(s) != "" {
			sentences = append(sentences, s)
		}
		start = end
	}
	return sentences, buf[start:]
}

var (
	reCodeFence  = regexp.MustCompile("(?s)```.*?(```|$)")
	reImage      = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	reLink       = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	reURL        = regexp.MustCompile(`(?i)\b(?:https?://|www\.)\S+`)
	reLinePrefix = regexp.MustCompile(`(?m)^\s*(?:#{1,6}\s+|>\s?|[-*+]\s+|\d+[.)]\s+)`)
	reTableRule  = regexp.MustCompile(`(?m)^\s*\|?\s*:?-{3,}.*$`)
	reEmphasis   = regexp.MustCompile(`\*\*|__|~~|\*|` + "`")
	reSpaces     = regexp.MustCompile(`\s+`)
)

// Speakable turns a markdown reply into plain text for text-to-speech: code
// blocks, URLs and table rules are dropped, links and images keep their
// text, headings, quotes, list markers and emphasis lose their markup, table
// cells are joined with commas, and whitespace collapses to single spaces.
func Speakable(s string) string {
	s = reCodeFence.ReplaceAllString(s, " ")
	s = reImage.ReplaceAllString(s, "$1")
	s = reLink.ReplaceAllString(s, "$1")
	s = reURL.ReplaceAllString(s, "")
	s = reTableRule.ReplaceAllString(s, "")
	s = reLinePrefix.ReplaceAllString(s, "")
	s = reEmphasis.ReplaceAllString(s, "")
	if strings.Contains(s, "|") {
		s = strings.Trim(strings.ReplaceAll(s, "|", ", "), ", ")
	}
	s = reSpaces.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, " ,", ",")
	s = strings.ReplaceAll(s, " .", ".")
	if !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
		return ""
	}
	return s
}
