package llm

import (
	"context"
	"fmt"
	"mime"
	"strings"
)

// DefaultTranscribeModel is the model Transcribe uses when the request names
// none. On OpenRouter it is the fastest model that transcribes German,
// English and Italian verbatim (about 1 s per clip); gemini-2.5-flash-lite
// translates Italian into German instead.
const DefaultTranscribeModel = "google/gemini-3.1-flash-lite"

// noSpeech is the marker the transcription prompt asks for on a clip without
// speech. Asking for an empty reply instead makes models invent a plausible
// sentence.
const noSpeech = "[no speech]"

const transcribePrompt = `Transcribe the speech in this recording verbatim, in the language it is spoken, with punctuation. Do not translate, summarize, correct, invent or add anything.

The recording may end mid-word; output only the words that are fully intelligible. If the recording contains no intelligible speech (only silence, noise, breathing or clicks), answer exactly ` + noSpeech + `.

Output only the transcript, without quotes, comments or labels.`

// AudioFormat maps an audio MIME type, as a browser MediaRecorder reports it
// ("audio/webm;codecs=opus", "audio/mp4"), onto the AudioPart.Format value.
// Parameters are ignored. Unknown types return an error.
func AudioFormat(contentType string) (string, error) {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", fmt.Errorf("aikido/llm: invalid audio content type %q: %w", contentType, err)
	}
	switch mt {
	case "audio/webm":
		return "webm", nil
	case "audio/mp4", "audio/m4a", "audio/x-m4a", "audio/aac":
		return "m4a", nil
	case "audio/mpeg", "audio/mp3":
		return "mp3", nil
	case "audio/wav", "audio/x-wav", "audio/wave":
		return "wav", nil
	case "audio/ogg":
		return "ogg", nil
	case "audio/flac", "audio/x-flac":
		return "flac", nil
	case "audio/aiff", "audio/x-aiff":
		return "aiff", nil
	}
	return "", fmt.Errorf("aikido/llm: unsupported audio type %q", mt)
}

// TranscribeRequest is one speech-to-text call made through Transcribe.
type TranscribeRequest struct {
	// Model defaults to DefaultTranscribeModel.
	Model string
	// Audio is the encoded clip; Format is its AudioPart.Format.
	Audio  []byte
	Format string
	// Hint is appended to the built-in instruction: the likely language,
	// domain vocabulary, how to write numbers. Keep example sentences and
	// amounts out of it, the model repeats them for a silent clip.
	Hint string
	// Vocabulary, when set, lists its Terms in the prompt and corrects the
	// transcript with Vocabulary.Correct.
	Vocabulary *Vocabulary
}

// Transcription is the result of TranscribeDetailed. Raw is the model's
// transcript, Text the same after Vocabulary.Correct (equal without a
// vocabulary). Both are "" for a clip without speech. Usage is nil when the
// provider reported none.
type Transcription struct {
	Text  string
	Raw   string
	Usage *Usage
}

// Transcribe returns the speech in req.Audio as text, using a chat-completions
// call on c.Complete, so it works with any Client whose provider accepts
// audio input. Temperature is 0. A clip without intelligible speech returns
// "", not an error. The text is the corrected one when req.Vocabulary is set;
// TranscribeDetailed also returns the raw transcript.
//
// Usage is returned alongside the text, also on error when the provider
// reported one.
func Transcribe(ctx context.Context, c Client, req TranscribeRequest) (string, *Usage, error) {
	t, err := TranscribeDetailed(ctx, c, req)
	return t.Text, t.Usage, err
}

// TranscribeDetailed is Transcribe returning the raw and the corrected
// transcript.
func TranscribeDetailed(ctx context.Context, c Client, req TranscribeRequest) (Transcription, error) {
	if len(req.Audio) == 0 || req.Format == "" {
		return Transcription{}, fmt.Errorf("aikido/llm: transcribe: audio and format are required: %w", ErrInvalidRequest)
	}
	model := req.Model
	if model == "" {
		model = DefaultTranscribeModel
	}
	prompt := transcribePrompt
	if line := req.Vocabulary.promptLine(); line != "" {
		prompt += "\n\n" + line
	}
	if req.Hint != "" {
		prompt += "\n\n" + req.Hint
	}
	resp, err := c.Complete(ctx, Request{
		Model: model,
		Messages: []Message{{
			Role:    RoleUser,
			Content: prompt,
			Audio:   []AudioPart{{Data: req.Audio, Format: req.Format}},
		}},
		Temperature: Float32(0),
	})
	if err != nil {
		return Transcription{Usage: resp.Usage}, err
	}
	raw := strings.TrimSpace(resp.Text)
	if isNoSpeech(raw) {
		return Transcription{Usage: resp.Usage}, nil
	}
	return Transcription{Text: req.Vocabulary.Correct(raw), Raw: raw, Usage: resp.Usage}, nil
}

// isNoSpeech reports whether a reply is the no-speech marker. Models with a
// non-English hint translate it ("[keine Sprache]", "[nessun parlato]"), so
// any reply that is only a short bracketed phrase counts, as does the marker
// anywhere in the reply.
func isNoSpeech(text string) bool {
	if strings.Contains(strings.ToLower(text), noSpeech) {
		return true
	}
	inner, ok := strings.CutPrefix(text, "[")
	if !ok {
		return false
	}
	inner, ok = strings.CutSuffix(strings.TrimRight(inner, "."), "]")
	return ok && !strings.ContainsAny(inner, "[]") && len(strings.Fields(inner)) <= 4
}
