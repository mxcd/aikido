package llm_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mxcd/aikido/llm"
	"github.com/mxcd/aikido/llm/llmtest"
)

var bathroomVocabulary = &llm.Vocabulary{
	Terms: []string{"Liv", "PFEIFFER & MAY", "LIV60G", "Waschtisch", "Duschtasse", "Unterschrank"},
	Replacements: map[string]string{
		"pfeifer und mai":  "PFEIFFER & MAY",
		"pfeiffer und may": "PFEIFFER & MAY",
		"liv sechzig g":    "LIV60G",
		"lieb":             "Liv",
		"lieb sechzig":     "LIV60",
	},
	Fuzzy: true,
}

func TestVocabulary_Correct(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ in, want string }{
		// Replacements, case-insensitive, whole word, longest first.
		{"Ich will was von Pfeifer und Mai.", "Ich will was von PFEIFFER & MAY."},
		{"Den liv sechzig G bitte.", "Den LIV60G bitte."},
		{"Die Serie Lieb sechzig gefällt mir.", "Die Serie LIV60 gefällt mir."},
		{"Lieb ist schön, aber lieber nicht.", "Liv ist schön, aber lieber nicht."},
		// Exact term with separators and case ignored.
		{"Nimm den Liv 60 g.", "Nimm den LIV60G."},
		{"Ein wasch tisch aus Keramik", "Ein Waschtisch aus Keramik"},
		{"Ein Waschtisch und ein Unterschrank", "Ein Waschtisch und ein Unterschrank"},
		// Fuzzy near misses.
		{"Eine flache Duschtase, bitte.", "Eine flache Duschtasse, bitte."},
		{"Der Unterschrang ist zu tief.", "Der Unterschrank ist zu tief."},
		{"Der Waschtich hängt schief.", "Der Waschtisch hängt schief."},
		// Untouched: short common words, unrelated words, other first letter.
		{"Das ist mir lieber so.", "Das ist mir lieber so."},
		{"Ich will eine Dusche.", "Ich will eine Dusche."},
		{"Ein Raschtisch?", "Ein Raschtisch?"},
		{"Die Tasse ist voll.", "Die Tasse ist voll."},
		{"", ""},
	} {
		if got := bathroomVocabulary.Correct(tc.in); got != tc.want {
			t.Errorf("Correct(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	var none *llm.Vocabulary
	if got := none.Correct("wasch tisch"); got != "wasch tisch" {
		t.Errorf("nil vocabulary changed text: %q", got)
	}
	exact := &llm.Vocabulary{Terms: []string{"Duschtasse"}}
	if got := exact.Correct("Duschtase"); got != "Duschtase" {
		t.Errorf("fuzzy off still corrected: %q", got)
	}
}

func TestTranscribeDetailed_Vocabulary(t *testing.T) {
	t.Parallel()
	stub := llmtest.NewStubClient(textTurn("Ich hätte gern einen wasch tisch von Pfeifer und Mai."))
	got, err := llm.TranscribeDetailed(context.Background(), stub, llm.TranscribeRequest{
		Audio:      []byte("RIFF"),
		Format:     "wav",
		Hint:       "The speaker speaks German.",
		Vocabulary: bathroomVocabulary,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Raw != "Ich hätte gern einen wasch tisch von Pfeifer und Mai." || got.Text != "Ich hätte gern einen Waschtisch von PFEIFFER & MAY." {
		t.Errorf("got %+v", got)
	}
	prompt := stub.Requests()[0].Messages[0].Content
	if !strings.Contains(prompt, "spell it exactly like this: Liv, PFEIFFER & MAY, LIV60G, Waschtisch") {
		t.Errorf("prompt lacks the terms:\n%s", prompt)
	}
	if strings.Index(prompt, "Expected terms") > strings.Index(prompt, "The speaker speaks German.") {
		t.Errorf("hint must come last:\n%s", prompt)
	}
}

func TestTranscribe_VocabularyPromptCap(t *testing.T) {
	t.Parallel()
	terms := make([]string, 500)
	for i := range terms {
		terms[i] = "Begriff" + strings.Repeat("x", i%7)
	}
	stub := llmtest.NewStubClient(textTurn("ok"))
	if _, _, err := llm.Transcribe(context.Background(), stub, llm.TranscribeRequest{Audio: []byte("x"), Format: "wav", Vocabulary: &llm.Vocabulary{Terms: terms}}); err != nil {
		t.Fatal(err)
	}
	prompt := stub.Requests()[0].Messages[0].Content
	line := prompt[strings.Index(prompt, "like this: "):]
	if n := strings.Count(line, ","); n+1 > llm.MaxVocabularyPromptTerms || len(line) > llm.MaxVocabularyPromptBytes+100 {
		t.Errorf("prompt line not capped: %d terms, %d bytes", n+1, len(line))
	}
}

func TestTranscribe_TranslatedNoSpeechMarker(t *testing.T) {
	t.Parallel()
	for _, reply := range []string{"[keine Sprache]", "[Keine Sprache erkannt].", "[nessun parlato]", "[No speech]"} {
		stub := llmtest.NewStubClient(textTurn(reply))
		got, err := llm.TranscribeDetailed(context.Background(), stub, llm.TranscribeRequest{Audio: []byte("x"), Format: "wav"})
		if err != nil || got.Text != "" || got.Raw != "" {
			t.Errorf("%q: got %+v, %v", reply, got, err)
		}
	}
	stub := llmtest.NewStubClient(textTurn("[Musik] Hallo zusammen, wie geht es euch heute?"))
	if text, _, _ := llm.Transcribe(context.Background(), stub, llm.TranscribeRequest{Audio: []byte("x"), Format: "wav"}); text == "" {
		t.Error("speech with a leading tag was dropped")
	}
}
