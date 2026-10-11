package openrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"strings"

	"github.com/mxcd/aikido/llm"
)

// Compile-time conformance for the optional speech capability.
var _ llm.SpeechGenerator = (*Client)(nil)

// Speak synthesizes req.Input through OpenRouter's text-to-speech endpoint
// (POST /api/v1/audio/speech, OpenAI audio/speech compatible). The 200 body
// is the raw audio; it is read in full, there is no chunked variant.
//
// Retry and error classification are Complete's: 429 and 5xx retry per
// retryPolicy, auth and bad-request short-circuit. A 200 that carries a JSON
// error envelope instead of audio surfaces as ErrServerError.
func (c *Client) Speak(ctx context.Context, req llm.SpeechRequest) (llm.SpeechResponse, error) {
	if strings.TrimSpace(req.Model) == "" {
		return llm.SpeechResponse{}, fmt.Errorf("openrouter: speech: model is required: %w", llm.ErrInvalidRequest)
	}
	if strings.TrimSpace(req.Input) == "" {
		return llm.SpeechResponse{}, fmt.Errorf("openrouter: speech: input is required: %w", llm.ErrInvalidRequest)
	}
	body, err := json.Marshal(speechRequest{
		Model:          req.Model,
		Input:          req.Input,
		Voice:          req.Voice,
		ResponseFormat: req.Format,
		Speed:          req.Speed,
	})
	if err != nil {
		return llm.SpeechResponse{}, fmt.Errorf("openrouter: speech: %w", err)
	}
	raw, header, err := c.post(ctx, "/audio/speech", body, "audio/*, application/json")
	if err != nil {
		return llm.SpeechResponse{}, err
	}
	ct := header.Get("Content-Type")
	if mt, _, _ := mime.ParseMediaType(ct); mt == "application/json" {
		msg := "response is JSON, not audio"
		if env := decodeErrorEnvelope(raw); env != nil {
			msg = env.Error.Message
		}
		return llm.SpeechResponse{}, fmt.Errorf("openrouter: speech: %s: %w", msg, llm.ErrServerError)
	}
	if len(raw) == 0 {
		return llm.SpeechResponse{}, fmt.Errorf("openrouter: speech: empty audio: %w", llm.ErrServerError)
	}
	return llm.SpeechResponse{
		Audio:        raw,
		ContentType:  ct,
		GenerationID: header.Get("X-Generation-Id"),
	}, nil
}
