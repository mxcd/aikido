// Package voice wraps one agent turn with speech in and speech out.
//
// A Pipeline takes text or recorded audio, transcribes audio with
// llm.TranscribeDetailed, runs the turn on an agent session, and, when the
// output is speech, synthesizes the reply sentence by sentence while the model
// is still writing, so playback starts before the answer is complete. All four
// combinations of text or voice in and text or voice out run through the same
// Run call and the same typed event stream.
//
// The package has no web framework dependency. Handler serves a Pipeline as
// NDJSON over plain net/http; anything that mounts an http.Handler (Gin via
// gin.WrapH, chi, the standard mux) can use it.
package voice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/mxcd/aikido/agent"
	"github.com/mxcd/aikido/llm"
)

// DefaultSpeechModel and DefaultVoice are used when Options name none.
const (
	DefaultSpeechModel = "elevenlabs/eleven-v4-turbo"
	DefaultVoice       = "sarah"
	DefaultFormat      = "mp3"
)

// DefaultSpokenStyle is a reply-style instruction for answers that are read
// aloud. Set it as Options.SpokenStyle to use it.
const DefaultSpokenStyle = `Your reply will be read aloud by a text-to-speech voice. Answer in the user's language, in one to three short, plain sentences. No markdown, lists, tables, code, emojis or URLs. Write numbers, units and abbreviations the way a person says them.`

// EndReasonNoSpeech ends a turn whose audio held no intelligible speech. The
// agent does not run.
const EndReasonNoSpeech = "no_speech"

// Output selects what a turn produces.
type Output string

const (
	// OutputText streams the reply as text only.
	OutputText Output = "text"
	// OutputSpeech streams the reply as text and speech chunks.
	OutputSpeech Output = "speech"
)

// Runner runs one agent turn. *agent.Session implements it.
type Runner interface {
	RunWithOptions(ctx context.Context, userText string, opts *agent.RunOptions) (<-chan agent.Event, error)
}

// Options configure a Pipeline. Transcriber is required for audio input,
// Speech for speech output; a Pipeline without one rejects the matching
// Input with llm.ErrInvalidRequest.
type Options struct {
	// Transcriber is the client llm.TranscribeDetailed runs on.
	Transcriber     llm.Client
	TranscribeModel string // default llm.DefaultTranscribeModel
	// Vocabulary biases and corrects every transcript.
	Vocabulary *llm.Vocabulary

	Speech      llm.SpeechGenerator
	SpeechModel string   // default DefaultSpeechModel
	Voice       string   // default DefaultVoice
	Format      string   // "mp3" (default) or "pcm"
	Speed       *float32 // nil leaves the provider default
	// SpeechUSDPerChar prices synthesized characters for Usage.SpeechCostUSD.
	// OpenRouter reports no cost on speech responses, so this is the only
	// way to a per-turn price. eleven-v4-turbo lists 0.00002 (10.2026).
	SpeechUSDPerChar float64
	// SpeechConcurrency bounds parallel synthesis calls. Default 2.
	SpeechConcurrency int

	// SpokenStyle is appended to the system prompt for turns with
	// OutputSpeech, e.g. DefaultSpokenStyle. Empty adds nothing.
	SpokenStyle string
}

// Pipeline runs voice turns. Safe for concurrent use.
type Pipeline struct {
	opts Options
}

// New builds a Pipeline.
func New(opts *Options) (*Pipeline, error) {
	if opts == nil {
		return nil, errors.New("aikido/voice: nil Options")
	}
	if opts.Transcriber == nil && opts.Speech == nil {
		return nil, errors.New("aikido/voice: Options need a Transcriber, a Speech generator, or both")
	}
	cp := *opts
	if cp.SpeechModel == "" {
		cp.SpeechModel = DefaultSpeechModel
	}
	if cp.Voice == "" {
		cp.Voice = DefaultVoice
	}
	if cp.Format == "" {
		cp.Format = DefaultFormat
	}
	if cp.SpeechConcurrency <= 0 {
		cp.SpeechConcurrency = 2
	}
	return &Pipeline{opts: cp}, nil
}

// Input is one user turn: Text, or Audio with its ContentType (a MIME type
// as MediaRecorder reports it). Audio wins when both are set.
type Input struct {
	Text        string
	Audio       []byte
	ContentType string
	// Language is a hint for transcription, a code or name ("de", "German").
	Language string
	// Output defaults to OutputText.
	Output Output
}

// EventKind identifies a voice Event.
type EventKind string

const (
	EventTranscript EventKind = "transcript"
	EventText       EventKind = "text"
	EventToolCall   EventKind = "tool_call"
	EventToolResult EventKind = "tool_result"
	EventSpeech     EventKind = "speech"
	EventError      EventKind = "error"
	EventDone       EventKind = "done"
)

// Event is one item of a turn's stream. The channel closes after EventDone.
//
//   - EventTranscript: Text is the corrected transcript of the user's audio,
//     Raw the model's version. Sent once, before anything else; Text "" is
//     followed by EventDone with EndReasonNoSpeech.
//   - EventText: a delta of the agent's reply.
//   - EventToolCall, EventToolResult: passed through from agent.Session.
//   - EventSpeech: Speech holds one synthesized sentence. Chunks arrive in
//     Seq order (0, 1, 2, ...).
//   - EventError: Err is set. With Speech set, only that chunk failed (no
//     Audio) and the turn goes on; otherwise EventDone follows.
//   - EventDone: EndReason (agent.EndReason* or EndReasonNoSpeech) and Usage.
type Event struct {
	Kind       EventKind
	Text       string
	Raw        string
	ToolCall   *llm.ToolCall
	ToolResult *agent.ToolResult
	Speech     *SpeechChunk
	Err        error
	EndReason  string
	Usage      *Usage
}

// SpeechChunk is one synthesized sentence. Text is what was spoken, after
// Speakable.
type SpeechChunk struct {
	Seq          int
	Text         string
	Audio        []byte
	ContentType  string
	GenerationID string
}

// Usage aggregates one turn over transcription, the agent's model calls and
// speech. CostUSD is the sum; SpeechCostUSD is SpeechChars times
// Options.SpeechUSDPerChar.
type Usage struct {
	Transcription *llm.Usage
	LLM           llm.Usage
	SpeechChars   int
	SpeechCostUSD float64
	CostUSD       float64
}

// Run starts one turn on r and returns its events. Invalid input (nothing to
// say, audio without a Transcriber, speech without a Speech generator, an
// unknown audio type) is rejected up front with llm.ErrInvalidRequest.
//
// The caller must drain the channel. Cancelling ctx cancels the turn; a
// server that wants the turn to finish after its client went away passes
// context.WithoutCancel, as Handler does.
func (p *Pipeline) Run(ctx context.Context, r Runner, in Input) (<-chan Event, error) {
	var format string
	switch {
	case len(in.Audio) > 0:
		if p.opts.Transcriber == nil {
			return nil, fmt.Errorf("aikido/voice: audio input needs Options.Transcriber: %w", llm.ErrInvalidRequest)
		}
		f, err := llm.AudioFormat(in.ContentType)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", err, llm.ErrInvalidRequest)
		}
		format = f
	case strings.TrimSpace(in.Text) == "":
		return nil, fmt.Errorf("aikido/voice: input has neither text nor audio: %w", llm.ErrInvalidRequest)
	}
	switch in.Output {
	case "", OutputText:
		in.Output = OutputText
	case OutputSpeech:
		if p.opts.Speech == nil {
			return nil, fmt.Errorf("aikido/voice: speech output needs Options.Speech: %w", llm.ErrInvalidRequest)
		}
	default:
		return nil, fmt.Errorf("aikido/voice: unknown output %q: %w", in.Output, llm.ErrInvalidRequest)
	}
	if r == nil {
		return nil, fmt.Errorf("aikido/voice: nil Runner: %w", llm.ErrInvalidRequest)
	}

	out := make(chan Event, 32)
	go p.run(ctx, r, in, format, out)
	return out, nil
}

func (p *Pipeline) run(ctx context.Context, r Runner, in Input, format string, out chan<- Event) {
	defer close(out)
	emit := func(ev Event) {
		select {
		case out <- ev:
		case <-ctx.Done():
		}
	}
	usage := &Usage{}
	done := func(reason string) {
		usage.CostUSD = usage.LLM.CostUSD + usage.SpeechCostUSD
		if usage.Transcription != nil {
			usage.CostUSD += usage.Transcription.CostUSD
		}
		emit(Event{Kind: EventDone, EndReason: reason, Usage: usage})
	}

	text := in.Text
	if len(in.Audio) > 0 {
		req := llm.TranscribeRequest{
			Model:      p.opts.TranscribeModel,
			Audio:      in.Audio,
			Format:     format,
			Vocabulary: p.opts.Vocabulary,
		}
		if in.Language != "" {
			req.Hint = "Expected language: " + in.Language + "."
		}
		t, err := llm.TranscribeDetailed(ctx, p.opts.Transcriber, req)
		usage.Transcription = t.Usage
		if err != nil {
			emit(Event{Kind: EventError, Err: fmt.Errorf("aikido/voice: transcribe: %w", err)})
			done(agent.EndReasonError)
			return
		}
		emit(Event{Kind: EventTranscript, Text: t.Text, Raw: t.Raw})
		if t.Text == "" {
			done(EndReasonNoSpeech)
			return
		}
		text = t.Text
	}

	var ro *agent.RunOptions
	var sp *speaker
	if in.Output == OutputSpeech {
		if p.opts.SpokenStyle != "" {
			ro = &agent.RunOptions{SystemSuffix: p.opts.SpokenStyle}
		}
		sp = newSpeaker(ctx, p, emit)
	}
	events, err := r.RunWithOptions(ctx, text, ro)
	if err != nil {
		sp.close(usage)
		emit(Event{Kind: EventError, Err: fmt.Errorf("aikido/voice: run: %w", err)})
		done(agent.EndReasonError)
		return
	}
	reason := agent.EndReasonError
	for ev := range events {
		switch ev.Kind {
		case agent.EventText:
			emit(Event{Kind: EventText, Text: ev.Text})
			sp.feed(ev.Text)
		case agent.EventToolCall:
			// Text before a tool call is a sentence of its own ("Let me check.").
			sp.flush()
			emit(Event{Kind: EventToolCall, ToolCall: ev.ToolCall})
		case agent.EventToolResult:
			emit(Event{Kind: EventToolResult, ToolResult: ev.ToolResult})
		case agent.EventUsage:
			if u := ev.Usage; u != nil {
				usage.LLM.PromptTokens += u.PromptTokens
				usage.LLM.CompletionTokens += u.CompletionTokens
				usage.LLM.CacheReadTokens += u.CacheReadTokens
				usage.LLM.CacheWriteTokens += u.CacheWriteTokens
				usage.LLM.CostUSD += u.CostUSD
			}
		case agent.EventError:
			emit(Event{Kind: EventError, Err: ev.Err})
		case agent.EventEnd:
			reason = ev.EndReason
		}
	}
	sp.close(usage)
	done(reason)
}

// speaker turns streamed text into ordered speech chunks. Synthesis runs in
// parallel up to SpeechConcurrency; one emitter goroutine forwards results in
// Seq order. A nil *speaker is a no-op, which is the text-output case.
type speaker struct {
	ctx   context.Context
	p     *Pipeline
	buf   string
	seq   int
	queue chan chan Event
	sem   chan struct{}
	done  chan struct{}

	mu    sync.Mutex
	chars int
}

func newSpeaker(ctx context.Context, p *Pipeline, emit func(Event)) *speaker {
	s := &speaker{
		ctx:   ctx,
		p:     p,
		queue: make(chan chan Event, 256),
		sem:   make(chan struct{}, p.opts.SpeechConcurrency),
		done:  make(chan struct{}),
	}
	go func() {
		defer close(s.done)
		for ch := range s.queue {
			emit(<-ch)
		}
	}()
	return s
}

func (s *speaker) feed(delta string) {
	if s == nil {
		return
	}
	s.buf += delta
	var sentences []string
	sentences, s.buf = splitSentences(s.buf)
	for _, sentence := range sentences {
		s.speak(sentence)
	}
}

func (s *speaker) flush() {
	if s == nil {
		return
	}
	s.speak(s.buf)
	s.buf = ""
}

// close speaks what is left, waits for every chunk and books the characters.
func (s *speaker) close(u *Usage) {
	if s == nil {
		return
	}
	s.flush()
	close(s.queue)
	<-s.done
	u.SpeechChars = s.chars
	u.SpeechCostUSD = float64(s.chars) * s.p.opts.SpeechUSDPerChar
}

func (s *speaker) speak(text string) {
	text = Speakable(text)
	if text == "" {
		return
	}
	chunk := SpeechChunk{Seq: s.seq, Text: text}
	s.seq++
	ch := make(chan Event, 1)
	s.queue <- ch
	go func() {
		s.sem <- struct{}{}
		defer func() { <-s.sem }()
		o := s.p.opts
		resp, err := o.Speech.Speak(s.ctx, llm.SpeechRequest{
			Model:  o.SpeechModel,
			Input:  text,
			Voice:  o.Voice,
			Format: o.Format,
			Speed:  o.Speed,
		})
		if err != nil {
			ch <- Event{Kind: EventError, Speech: &chunk, Err: fmt.Errorf("aikido/voice: speak chunk %d: %w", chunk.Seq, err)}
			return
		}
		s.mu.Lock()
		s.chars += len([]rune(text))
		s.mu.Unlock()
		chunk.Audio, chunk.ContentType, chunk.GenerationID = resp.Audio, resp.ContentType, resp.GenerationID
		ch <- Event{Kind: EventSpeech, Speech: &chunk}
	}()
}
