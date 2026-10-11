package voice

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mxcd/aikido/agent"
	"github.com/mxcd/aikido/agent/history/memory"
	"github.com/mxcd/aikido/llm"
	"github.com/mxcd/aikido/llm/llmtest"
)

// fakeSpeech synthesizes "audio:<text>" and can delay or fail per input.
type fakeSpeech struct {
	mu    sync.Mutex
	reqs  []llm.SpeechRequest
	delay func(text string) time.Duration
	fail  func(text string) bool
}

func (f *fakeSpeech) Speak(ctx context.Context, req llm.SpeechRequest) (llm.SpeechResponse, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	if f.delay != nil {
		select {
		case <-time.After(f.delay(req.Input)):
		case <-ctx.Done():
			return llm.SpeechResponse{}, ctx.Err()
		}
	}
	if f.fail != nil && f.fail(req.Input) {
		return llm.SpeechResponse{}, fmt.Errorf("tts down: %w", llm.ErrServerError)
	}
	return llm.SpeechResponse{Audio: []byte("audio:" + req.Input), ContentType: "audio/mpeg", GenerationID: "gen"}, nil
}

// replyTurn streams text in the given deltas and reports a priced usage.
func replyTurn(cost float64, deltas ...string) llmtest.TurnScript {
	var evs []llm.Event
	for _, d := range deltas {
		evs = append(evs, llm.Event{Kind: llm.EventTextDelta, Text: d})
	}
	evs = append(evs,
		llm.Event{Kind: llm.EventUsage, Usage: &llm.Usage{PromptTokens: 10, CompletionTokens: 5, CostUSD: cost}},
		llm.Event{Kind: llm.EventEnd, FinishReason: "stop"})
	return llmtest.TurnScript{Events: evs}
}

func transcriptTurn(text string) llmtest.TurnScript {
	return llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: text},
		{Kind: llm.EventUsage, Usage: &llm.Usage{PromptTokens: 100, CostUSD: 0.0001}},
		{Kind: llm.EventEnd},
	}}
}

func newSession(t *testing.T, client llm.Client) (*agent.Session, *memory.History) {
	t.Helper()
	hist := memory.NewHistory()
	s, err := agent.NewSession(&agent.SessionOptions{
		ID: "voice-test", Client: client, History: hist, Locker: agent.NewLocalLocker(),
		Model: "test/model", SystemPrompt: "You are a bathroom planner.",
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, hist
}

func collect(t *testing.T, ch <-chan Event) []Event {
	t.Helper()
	var evs []Event
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return evs
			}
			evs = append(evs, ev)
		case <-timeout:
			t.Fatal("event stream did not close")
		}
	}
}

func kinds(evs []Event) string {
	var ks []string
	for _, ev := range evs {
		ks = append(ks, string(ev.Kind))
	}
	return strings.Join(ks, ",")
}

func only(evs []Event, k EventKind) []Event {
	var out []Event
	for _, ev := range evs {
		if ev.Kind == k {
			out = append(out, ev)
		}
	}
	return out
}

const answer1, answer2 = "Die Dusche passt an die Fensterwand. ", "Der Waschtisch kommt gegenüber."

func TestRun_FourModes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		audio      bool
		output     Output
		transcript bool
		speech     int
	}{
		{"text in, text out", false, OutputText, false, 0},
		{"text in, voice out", false, OutputSpeech, false, 2},
		{"voice in, text out", true, OutputText, true, 0},
		{"voice in, voice out", true, OutputSpeech, true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stt := llmtest.NewStubClient(transcriptTurn("Wo kommt die Dusche hin?"))
			chat := llmtest.NewStubClient(replyTurn(0.002, "Die Dusche passt ", "an die Fensterwand. Der Wasch", "tisch kommt gegenüber."))
			tts := &fakeSpeech{}
			p, err := New(&Options{Transcriber: stt, Speech: tts, SpokenStyle: DefaultSpokenStyle, SpeechUSDPerChar: 0.00002})
			if err != nil {
				t.Fatal(err)
			}
			session, hist := newSession(t, chat)
			in := Input{Text: "Wo kommt die Dusche hin?", Output: tc.output}
			if tc.audio {
				in = Input{Audio: []byte("webm"), ContentType: "audio/webm;codecs=opus", Language: "de", Output: tc.output}
			}
			ch, err := p.Run(context.Background(), session, in)
			if err != nil {
				t.Fatal(err)
			}
			evs := collect(t, ch)

			if got := len(only(evs, EventTranscript)) == 1; got != tc.transcript {
				t.Errorf("transcript event = %v, want %v (%s)", got, tc.transcript, kinds(evs))
			}
			if tc.transcript && evs[0].Kind != EventTranscript {
				t.Errorf("transcript must come first: %s", kinds(evs))
			}
			var text strings.Builder
			for _, ev := range only(evs, EventText) {
				text.WriteString(ev.Text)
			}
			if text.String() != answer1+answer2 {
				t.Errorf("text = %q", text.String())
			}
			speech := only(evs, EventSpeech)
			if len(speech) != tc.speech {
				t.Fatalf("speech chunks = %d, want %d (%s)", len(speech), tc.speech, kinds(evs))
			}
			if tc.speech > 0 && (speech[0].Speech.Text != strings.TrimSpace(answer1) || speech[1].Speech.Text != answer2) {
				t.Errorf("chunks = %q, %q", speech[0].Speech.Text, speech[1].Speech.Text)
			}
			last := evs[len(evs)-1]
			if last.Kind != EventDone || last.EndReason != agent.EndReasonStop {
				t.Fatalf("last = %+v", last)
			}

			system := chat.Requests()[0].Messages[0].Content
			if spoken := strings.Contains(system, "read aloud"); spoken != (tc.output == OutputSpeech) {
				t.Errorf("spoken style in system prompt = %v for %s", spoken, tc.output)
			}
			if tc.audio {
				sttPrompt := stt.Requests()[0].Messages[0].Content
				if !strings.Contains(sttPrompt, "Expected language: de.") {
					t.Errorf("language hint missing: %s", sttPrompt)
				}
			}
			msgs, _ := hist.Read(context.Background(), "voice-test")
			if len(msgs) != 2 || msgs[0].Content != "Wo kommt die Dusche hin?" {
				t.Errorf("history = %+v", msgs)
			}
		})
	}
}

func TestRun_UsageAggregatesAllStages(t *testing.T) {
	t.Parallel()
	stt := llmtest.NewStubClient(transcriptTurn("Hallo."))
	chat := llmtest.NewStubClient(replyTurn(0.002, answer1+answer2))
	p, _ := New(&Options{Transcriber: stt, Speech: &fakeSpeech{}, SpeechUSDPerChar: 0.00002})
	session, _ := newSession(t, chat)
	ch, _ := p.Run(context.Background(), session, Input{Audio: []byte("x"), ContentType: "audio/wav", Output: OutputSpeech})
	evs := collect(t, ch)
	u := evs[len(evs)-1].Usage
	chars := len([]rune(strings.TrimSpace(answer1))) + len([]rune(answer2))
	if u == nil || u.SpeechChars != chars || u.LLM.PromptTokens != 10 || u.Transcription == nil || u.Transcription.PromptTokens != 100 {
		t.Fatalf("usage = %+v", u)
	}
	want := 0.0001 + 0.002 + float64(chars)*0.00002
	if math.Abs(u.CostUSD-want) > 1e-12 || math.Abs(u.SpeechCostUSD-float64(chars)*0.00002) > 1e-12 {
		t.Errorf("cost = %v (speech %v), want %v", u.CostUSD, u.SpeechCostUSD, want)
	}
}

func TestRun_SpeechChunksKeepOrderWhenSynthesisIsNot(t *testing.T) {
	t.Parallel()
	chat := llmtest.NewStubClient(replyTurn(0, "Erster Satz ist lang. ", "Zweiter Satz ist kurz. ", "Dritter Satz ganz am Ende."))
	tts := &fakeSpeech{delay: func(text string) time.Duration {
		if strings.HasPrefix(text, "Erster") {
			return 150 * time.Millisecond
		}
		return 0
	}}
	p, _ := New(&Options{Speech: tts, SpeechConcurrency: 3})
	session, _ := newSession(t, chat)
	ch, _ := p.Run(context.Background(), session, Input{Text: "Hallo", Output: OutputSpeech})
	speech := only(collect(t, ch), EventSpeech)
	if len(speech) != 3 {
		t.Fatalf("chunks = %d", len(speech))
	}
	for i, ev := range speech {
		if ev.Speech.Seq != i || string(ev.Speech.Audio) != "audio:"+ev.Speech.Text {
			t.Errorf("chunk %d = %+v", i, ev.Speech)
		}
	}
	if !strings.HasPrefix(speech[0].Speech.Text, "Erster") {
		t.Errorf("first chunk = %q", speech[0].Speech.Text)
	}
}

func TestRun_SpeechStartsBeforeTheReplyEnds(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	// The agent blocks after the first sentence until the test has seen its audio.
	chat := &gatedClient{first: "Die Dusche passt an die Fensterwand. ", rest: "Fertig ist der Plan.", gate: release}
	p, _ := New(&Options{Speech: &fakeSpeech{}})
	session, _ := newSession(t, chat)
	ch, _ := p.Run(context.Background(), session, Input{Text: "Hallo", Output: OutputSpeech})
	var evs []Event
	for ev := range ch {
		evs = append(evs, ev)
		if ev.Kind == EventSpeech && ev.Speech.Seq == 0 {
			close(release)
		}
	}
	if n := len(only(evs, EventSpeech)); n != 2 {
		t.Fatalf("chunks = %d (%s)", n, kinds(evs))
	}
}

// gatedClient streams first, waits for gate, then streams rest.
type gatedClient struct {
	first, rest string
	gate        <-chan struct{}
}

func (g *gatedClient) Stream(ctx context.Context, _ llm.Request) (<-chan llm.Event, error) {
	ch := make(chan llm.Event)
	go func() {
		defer close(ch)
		ch <- llm.Event{Kind: llm.EventTextDelta, Text: g.first}
		select {
		case <-g.gate:
		case <-time.After(3 * time.Second):
			ch <- llm.Event{Kind: llm.EventError, Err: errors.New("speech never arrived before the reply ended")}
			return
		case <-ctx.Done():
			return
		}
		ch <- llm.Event{Kind: llm.EventTextDelta, Text: g.rest}
		ch <- llm.Event{Kind: llm.EventEnd}
	}()
	return ch, nil
}

func (g *gatedClient) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("not used")
}

func TestRun_ToolCallFlushesPendingSentence(t *testing.T) {
	t.Parallel()
	chat := llmtest.NewStubClient(
		llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventTextDelta, Text: "Ich schaue im Katalog nach"},
			{Kind: llm.EventToolCall, Tool: &llm.ToolCall{ID: "c1", Name: "search", Arguments: `{}`}},
			{Kind: llm.EventEnd},
		}},
		replyTurn(0, "Es gibt drei Duschtassen."),
	)
	p, _ := New(&Options{Speech: &fakeSpeech{}})
	session, _ := newSession(t, chat)
	ch, _ := p.Run(context.Background(), session, Input{Text: "Welche Duschtassen gibt es?", Output: OutputSpeech})
	evs := collect(t, ch)
	speech := only(evs, EventSpeech)
	if len(speech) != 2 || speech[0].Speech.Text != "Ich schaue im Katalog nach" {
		t.Fatalf("speech = %+v (%s)", speech, kinds(evs))
	}
	if len(only(evs, EventToolCall)) != 1 || len(only(evs, EventToolResult)) != 1 {
		t.Errorf("tool events not passed through: %s", kinds(evs))
	}
}

func TestRun_NoSpeechSkipsTheAgent(t *testing.T) {
	t.Parallel()
	stt := llmtest.NewStubClient(transcriptTurn("[keine Sprache]"))
	chat := llmtest.NewStubClient()
	p, _ := New(&Options{Transcriber: stt, Speech: &fakeSpeech{}})
	session, _ := newSession(t, chat)
	ch, _ := p.Run(context.Background(), session, Input{Audio: []byte("x"), ContentType: "audio/webm", Output: OutputSpeech})
	evs := collect(t, ch)
	if kinds(evs) != "transcript,done" || evs[0].Text != "" || evs[1].EndReason != EndReasonNoSpeech {
		t.Fatalf("events = %s %+v", kinds(evs), evs)
	}
	if len(chat.Requests()) != 0 {
		t.Error("agent ran on silence")
	}
}

func TestRun_VocabularyCorrectsTheTranscript(t *testing.T) {
	t.Parallel()
	stt := llmtest.NewStubClient(transcriptTurn("Passt der wasch tisch von Pfeifer und Mai?"))
	chat := llmtest.NewStubClient(replyTurn(0, "Ja."))
	p, _ := New(&Options{Transcriber: stt, Vocabulary: &llm.Vocabulary{
		Terms:        []string{"Waschtisch", "PFEIFFER & MAY"},
		Replacements: map[string]string{"pfeifer und mai": "PFEIFFER & MAY"},
	}})
	session, _ := newSession(t, chat)
	ch, _ := p.Run(context.Background(), session, Input{Audio: []byte("x"), ContentType: "audio/webm"})
	evs := collect(t, ch)
	if evs[0].Text != "Passt der Waschtisch von PFEIFFER & MAY?" || evs[0].Raw != "Passt der wasch tisch von Pfeifer und Mai?" {
		t.Errorf("transcript = %+v", evs[0])
	}
	if got := chat.Requests()[0].Messages[1].Content; got != evs[0].Text {
		t.Errorf("agent got %q, want the corrected transcript", got)
	}
}

func TestRun_FailedChunkDoesNotEndTheTurn(t *testing.T) {
	t.Parallel()
	chat := llmtest.NewStubClient(replyTurn(0, answer1+answer2))
	tts := &fakeSpeech{fail: func(text string) bool { return strings.HasPrefix(text, "Die Dusche") }}
	p, _ := New(&Options{Speech: tts})
	session, _ := newSession(t, chat)
	ch, _ := p.Run(context.Background(), session, Input{Text: "Hallo", Output: OutputSpeech})
	evs := collect(t, ch)
	errs := only(evs, EventError)
	if len(errs) != 1 || errs[0].Speech == nil || errs[0].Speech.Seq != 0 || !errors.Is(errs[0].Err, llm.ErrServerError) {
		t.Fatalf("errors = %+v", errs)
	}
	speech := only(evs, EventSpeech)
	if len(speech) != 1 || speech[0].Speech.Seq != 1 {
		t.Errorf("speech = %+v", speech)
	}
	if last := evs[len(evs)-1]; last.EndReason != agent.EndReasonStop || last.Usage.SpeechChars != len([]rune(answer2)) {
		t.Errorf("done = %+v %+v", last, last.Usage)
	}
}

func TestRun_TranscriptionErrorEndsTheTurn(t *testing.T) {
	t.Parallel()
	stt := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{{Kind: llm.EventError, Err: llm.ErrRateLimited}}})
	p, _ := New(&Options{Transcriber: stt})
	session, _ := newSession(t, llmtest.NewStubClient())
	ch, _ := p.Run(context.Background(), session, Input{Audio: []byte("x"), ContentType: "audio/webm"})
	evs := collect(t, ch)
	if kinds(evs) != "error,done" || !errors.Is(evs[0].Err, llm.ErrRateLimited) || evs[1].EndReason != agent.EndReasonError {
		t.Fatalf("events = %s %+v", kinds(evs), evs)
	}
}

func TestRun_RejectsInvalidInput(t *testing.T) {
	t.Parallel()
	session, _ := newSession(t, llmtest.NewStubClient())
	textOnly, _ := New(&Options{Speech: &fakeSpeech{}})
	sttOnly, _ := New(&Options{Transcriber: llmtest.NewStubClient()})
	for name, tc := range map[string]struct {
		p  *Pipeline
		in Input
	}{
		"empty":              {textOnly, Input{Text: "  "}},
		"audio without stt":  {textOnly, Input{Audio: []byte("x"), ContentType: "audio/webm"}},
		"speech without tts": {sttOnly, Input{Text: "x", Output: OutputSpeech}},
		"unknown output":     {textOnly, Input{Text: "x", Output: "video"}},
		"unknown audio type": {sttOnly, Input{Audio: []byte("x"), ContentType: "audio/midi"}},
	} {
		if _, err := tc.p.Run(context.Background(), session, tc.in); !errors.Is(err, llm.ErrInvalidRequest) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := New(&Options{}); err == nil {
		t.Error("New without Transcriber and Speech must fail")
	}
}

func TestSplitSentences(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   string
		want []string
		rest string
	}{
		{"Die Dusche passt gut. Der Wasch", []string{"Die Dusche passt gut."}, " Der Wasch"},
		{"Das kostet 3.", nil, "Das kostet 3."},
		{"Das kostet 3.500 Euro. Und", []string{"Das kostet 3.500 Euro."}, " Und"},
		{"Ja. Die Dusche passt gut. ", []string{"Ja. Die Dusche passt gut."}, " "},
		{"Wirklich? Ja, absolut sicher! Gut", []string{"Wirklich? Ja, absolut sicher!"}, " Gut"},
		{"- Duschtasse\n- Waschtisch\n", []string{"- Duschtasse\n", "- Waschtisch\n"}, ""},
	} {
		got, rest := splitSentences(tc.in)
		if fmt.Sprint(got) != fmt.Sprint(tc.want) || rest != tc.rest {
			t.Errorf("splitSentences(%q) = %q, %q; want %q, %q", tc.in, got, rest, tc.want, tc.rest)
		}
	}
}

func TestSpeakable(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"**Wichtig:** die _Duschtasse_ ist `flach`.": "Wichtig: die _Duschtasse_ ist flach.",
		"## Ergebnis": "Ergebnis",
		"- Die [Serie Liv](https://example.com/liv) passt.":              "Die Serie Liv passt.",
		"Mehr unter https://pfeiffer-may.de/katalog?x=1 oder www.liv.de": "Mehr unter oder",
		"| Breite | 90 cm |":        "Breite, 90 cm",
		"|---|---|":                 "",
		"```go\nfmt.Println()\n```": "",
		"> Zitat\n1. Erster Punkt":  "Zitat Erster Punkt",
		"![Grundriss](plan.png)":    "Grundriss",
		"…":                         "",
	} {
		if got := Speakable(in); got != want {
			t.Errorf("Speakable(%q) = %q, want %q", in, got, want)
		}
	}
}
