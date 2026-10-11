package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/mxcd/aikido/agent"
	"github.com/mxcd/aikido/llm"
)

// HandlerOptions configure a Handler.
type HandlerOptions struct {
	Pipeline *Pipeline
	// Session resolves the agent session for a request (auth, tenant,
	// conversation id). An error answers 400 with its message; check auth
	// in a middleware in front for a 401. Extra request fields are there
	// to read: a JSON body can be decoded again from r.Body (the handler
	// restores it), multipart fields come from r.FormValue, query
	// parameters from r.URL.
	Session func(r *http.Request) (Runner, error)
	// ToolResult, when set, picks what of a tool result reaches the client:
	// a non-nil return is sent as the "content" of the tool_result line.
	// Nil (the default, and the return for results to keep private) sends
	// no content.
	ToolResult func(res agent.ToolResult) any
	// MaxAudioBytes caps the upload. Default 8 MiB; larger answers 413.
	MaxAudioBytes int64
	// Timeout caps the turn, which runs detached from the request context so
	// a client that goes away does not lose it. Default 5 minutes.
	Timeout time.Duration
}

// Handler serves one voice turn per POST and streams its events as NDJSON
// (application/x-ndjson, one JSON object per line, flushed per event).
//
// The request is one of:
//   - application/json: {"text": "...", "output": "text"|"speech", "language": "de"}
//   - multipart/form-data: an "audio" file part (its Content-Type is the
//     audio type) or a "text" field, plus "output" and "language" fields
//   - a raw audio/* body, with ?output= and ?language=
//
// Each line has a "type" (the EventKind) and the fields of that event:
//
//	{"type":"transcript","text":"...","raw":"..."}
//	{"type":"text","text":"..."}
//	{"type":"tool_call","tool":{"id":"...","name":"...","arguments":"..."}}
//	{"type":"tool_result","tool":{"id":"...","name":"...","ok":true,"error":"","content":...}}
//	{"type":"speech","seq":0,"text":"...","content_type":"audio/mpeg","audio":"<base64>"}
//	{"type":"error","error":"...","seq":2}
//	{"type":"done","end_reason":"stop","usage":{"cost_usd":0.0031,...}}
//
// The done line is always the last one, also for a turn that hit Timeout
// (end_reason "timeout"), as long as the client is still connected.
//
// Input errors answer 4xx with a JSON {"error": "..."} body before any line.
type Handler struct {
	opts HandlerOptions
}

// NewHandler builds a Handler.
func NewHandler(opts *HandlerOptions) (*Handler, error) {
	if opts == nil || opts.Pipeline == nil || opts.Session == nil {
		return nil, errors.New("aikido/voice: HandlerOptions need Pipeline and Session")
	}
	cp := *opts
	if cp.MaxAudioBytes <= 0 {
		cp.MaxAudioBytes = 8 << 20
	}
	if cp.Timeout <= 0 {
		cp.Timeout = 5 * time.Minute
	}
	return &Handler{opts: cp}, nil
}

type wireTool struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Arguments string `json:"arguments,omitempty"`
	OK        *bool  `json:"ok,omitempty"`
	Error     string `json:"error,omitempty"`
	Content   any    `json:"content,omitempty"`
}

type wireUsage struct {
	CostUSD              float64 `json:"cost_usd"`
	TranscriptionCostUSD float64 `json:"transcription_cost_usd"`
	LLMCostUSD           float64 `json:"llm_cost_usd"`
	SpeechCostUSD        float64 `json:"speech_cost_usd"`
	SpeechChars          int     `json:"speech_chars"`
	PromptTokens         int     `json:"prompt_tokens"`
	CompletionTokens     int     `json:"completion_tokens"`
}

type wireEvent struct {
	Type        EventKind  `json:"type"`
	Text        string     `json:"text,omitempty"`
	Raw         string     `json:"raw,omitempty"`
	Tool        *wireTool  `json:"tool,omitempty"`
	Seq         *int       `json:"seq,omitempty"`
	ContentType string     `json:"content_type,omitempty"`
	Audio       []byte     `json:"audio,omitempty"`
	Error       string     `json:"error,omitempty"`
	EndReason   string     `json:"end_reason,omitempty"`
	Usage       *wireUsage `json:"usage,omitempty"`
}

func (h *Handler) toWire(ev Event) wireEvent {
	w := wireEvent{Type: ev.Kind, Text: ev.Text, Raw: ev.Raw, EndReason: ev.EndReason}
	if ev.Err != nil {
		w.Error = ev.Err.Error()
	}
	if c := ev.ToolCall; c != nil {
		w.Tool = &wireTool{ID: c.ID, Name: c.Name, Arguments: c.Arguments}
	}
	if r := ev.ToolResult; r != nil {
		ok := r.OK
		w.Tool = &wireTool{ID: r.CallID, Name: r.Name, OK: &ok, Error: r.Error}
		if h.opts.ToolResult != nil {
			w.Tool.Content = h.opts.ToolResult(*r)
		}
	}
	if s := ev.Speech; s != nil {
		seq := s.Seq
		w.Seq, w.Text, w.ContentType, w.Audio = &seq, s.Text, s.ContentType, s.Audio
	}
	if u := ev.Usage; u != nil {
		wu := &wireUsage{
			CostUSD:          u.CostUSD,
			LLMCostUSD:       u.LLM.CostUSD,
			SpeechCostUSD:    u.SpeechCostUSD,
			SpeechChars:      u.SpeechChars,
			PromptTokens:     u.LLM.PromptTokens,
			CompletionTokens: u.LLM.CompletionTokens,
		}
		if t := u.Transcription; t != nil {
			wu.TranscriptionCostUSD = t.CostUSD
			wu.PromptTokens += t.PromptTokens
			wu.CompletionTokens += t.CompletionTokens
		}
		w.Usage = wu
	}
	return w
}

func httpError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		httpError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	in, status, err := h.readInput(w, r)
	if err != nil {
		httpError(w, status, err.Error())
		return
	}
	runner, err := h.opts.Session(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Detached from the request: a closed tab must not cancel the turn
	// half-way, or the agent's History never gets the reply.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), h.opts.Timeout)
	defer cancel()
	events, err := h.opts.Pipeline.Run(ctx, runner, in)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, llm.ErrInvalidRequest) {
			status = http.StatusBadRequest
		}
		httpError(w, status, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	enc := json.NewEncoder(w)
	gone := false
	for ev := range events {
		if gone {
			continue // keep draining so the turn completes
		}
		if enc.Encode(h.toWire(ev)) != nil || rc.Flush() != nil {
			gone = true
		}
	}
}

// readInput parses the three request shapes into an Input.
func (h *Handler) readInput(w http.ResponseWriter, r *http.Request) (Input, int, error) {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	body := http.MaxBytesReader(w, r.Body, h.opts.MaxAudioBytes)
	tooLarge := func(err error) (Input, int, error) {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return Input{}, http.StatusRequestEntityTooLarge, fmt.Errorf("body exceeds %d bytes", h.opts.MaxAudioBytes)
		}
		return Input{}, http.StatusBadRequest, err
	}
	var in Input
	switch {
	case mt == "application/json":
		var req struct {
			Text     string `json:"text"`
			Output   Output `json:"output"`
			Language string `json:"language"`
		}
		raw, err := io.ReadAll(body)
		if err != nil {
			return tooLarge(err)
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			return Input{}, http.StatusBadRequest, fmt.Errorf("invalid JSON: %w", err)
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		in = Input{Text: req.Text, Output: req.Output, Language: req.Language}
	case mt == "multipart/form-data":
		r.Body = body
		if err := r.ParseMultipartForm(h.opts.MaxAudioBytes); err != nil {
			return tooLarge(err)
		}
		in = Input{
			Text:     r.FormValue("text"),
			Output:   Output(r.FormValue("output")),
			Language: r.FormValue("language"),
		}
		if f, fh, err := r.FormFile("audio"); err == nil {
			defer f.Close()
			data, err := io.ReadAll(f)
			if err != nil {
				return tooLarge(err)
			}
			in.Audio, in.ContentType = data, fh.Header.Get("Content-Type")
		}
	case strings.HasPrefix(mt, "audio/"):
		data, err := io.ReadAll(body)
		if err != nil {
			return tooLarge(err)
		}
		q := r.URL.Query()
		in = Input{Audio: data, ContentType: r.Header.Get("Content-Type"), Output: Output(q.Get("output")), Language: q.Get("language")}
	default:
		return Input{}, http.StatusUnsupportedMediaType, fmt.Errorf("unsupported content type %q", mt)
	}
	if len(in.Audio) > 0 {
		if _, err := llm.AudioFormat(in.ContentType); err != nil {
			return Input{}, http.StatusUnsupportedMediaType, err
		}
	}
	return in, 0, nil
}
