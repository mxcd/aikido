// Package codex implements llm.Client against the Codex backend of a ChatGPT
// subscription (https://chatgpt.com/backend-api/codex/responses), the way the
// Codex CLI uses it. It bills the subscription instead of an API key.
//
// The backend speaks the OpenAI Responses API with a few rules: it only
// streams, never stores, and rejects max_output_tokens. Supported: system
// messages (as instructions), user and assistant text, image input and
// structured output (llm.Request.ResponseFormat). Tools, tool results, audio
// input and Thinking are not supported and fail with llm.ErrInvalidRequest;
// MaxTokens, Temperature, StopSequences, Modalities and ImageConfig are ignored.
//
// Log in with StartDeviceLogin + PollDeviceLogin (or ParseAuthJSON), wrap the
// login in a TokenSource that persists every refresh, and pass it in Options.
package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/mxcd/aikido/llm"
	"github.com/mxcd/aikido/retry"
)

const (
	// DefaultBaseURL is the Codex backend root.
	DefaultBaseURL = "https://chatgpt.com/backend-api/codex"
	// clientVersion gates the models list; older versions hide newer models.
	clientVersion = "0.155.0"
)

// Options configure the Client.
type Options struct {
	// Tokens is required.
	Tokens *TokenSource
	// Originator is required: OpenAI asks third-party clients to identify
	// themselves (e.g. "casa-mapa").
	Originator string
	// UserAgent defaults to Originator + "/aikido".
	UserAgent string
	// BaseURL overrides DefaultBaseURL (tests).
	BaseURL string
	// HTTPClient overrides a client without timeout; pass a ctx deadline.
	HTTPClient *http.Client
}

// Client is a Codex-backend implementation of llm.Client.
type Client struct {
	o Options
}

var _ llm.Client = (*Client)(nil)

// NewClient builds a Client.
func NewClient(o *Options) (*Client, error) {
	if o == nil || o.Tokens == nil || o.Originator == "" {
		return nil, errors.New("codex: Tokens and Originator are required")
	}
	c := &Client{o: *o}
	if c.o.UserAgent == "" {
		c.o.UserAgent = o.Originator + "/aikido"
	}
	if c.o.BaseURL == "" {
		c.o.BaseURL = DefaultBaseURL
	}
	c.o.BaseURL = strings.TrimRight(c.o.BaseURL, "/")
	if c.o.HTTPClient == nil {
		c.o.HTTPClient = &http.Client{}
	}
	return c, nil
}

// Stream sends one request and returns its events; EventEnd comes last.
// 429 and 5xx at stream start are retried, mid-stream errors are not.
func (c *Client) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	body, err := buildBody(req)
	if err != nil {
		return nil, err
	}
	var resp *http.Response
	err = retry.Do(ctx, retryPolicy(), func(int) error {
		r, err := c.do(ctx, http.MethodPost, "/responses", body, "text/event-stream") //nolint:bodyclose // closed by the stream goroutine
		if err != nil {
			return err
		}
		resp = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make(chan llm.Event, 16)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		processStream(ctx, resp.Body, out)
	}()
	return out, nil
}

// Complete drains Stream: the backend has no non-streaming mode.
func (c *Client) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	events, err := c.Stream(ctx, req)
	if err != nil {
		return llm.Response{}, err
	}
	var (
		res llm.Response
		sb  strings.Builder
	)
	for ev := range events {
		switch ev.Kind {
		case llm.EventTextDelta:
			sb.WriteString(ev.Text)
		case llm.EventUsage:
			res.Usage = ev.Usage
		case llm.EventError:
			err = ev.Err
		case llm.EventEnd:
			res.FinishReason = ev.FinishReason
		}
	}
	res.Text = sb.String()
	if err == nil {
		err = ctx.Err()
	}
	return res, err
}

// Models lists the model ids the subscription offers, in the backend's order,
// hidden ones left out.
func (c *Client) Models(ctx context.Context) ([]string, error) {
	resp, err := c.do(ctx, http.MethodGet, "/models?client_version="+clientVersion, nil, "application/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var list struct {
		Models []struct {
			Slug       string `json:"slug"`
			Visibility string `json:"visibility"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("codex: models: %w", err)
	}
	var ids []string
	for _, m := range list.Models {
		if m.Slug != "" && m.Visibility != "hide" && m.Visibility != "hidden" {
			ids = append(ids, m.Slug)
		}
	}
	return ids, nil
}

// do sends one authenticated request and returns a 200 response, or an error
// wrapping an llm sentinel (closing the body).
func (c *Client) do(ctx context.Context, method, path string, body []byte, accept string) (*http.Response, error) {
	tok, err := c.o.Tokens.Token(ctx)
	if err != nil {
		return nil, err
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.o.BaseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	req.Header.Set("ChatGPT-Account-ID", tok.AccountID)
	req.Header.Set("originator", c.o.Originator)
	req.Header.Set("User-Agent", c.o.UserAgent)
	req.Header.Set("Accept", accept)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.o.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codex: http error: %v: %w", err, llm.ErrServerError)
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		return nil, fmt.Errorf("codex: status %d: %s: %w", resp.StatusCode, strings.TrimSpace(string(raw)), statusSentinel(resp.StatusCode))
	}
	return resp, nil
}

func statusSentinel(status int) error {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusPaymentRequired:
		return llm.ErrAuth
	case status == http.StatusTooManyRequests:
		return llm.ErrRateLimited
	case status >= 500 || status == http.StatusRequestTimeout:
		return llm.ErrServerError
	default:
		return llm.ErrInvalidRequest
	}
}

func retryPolicy() retry.Policy {
	p := retry.DefaultPolicy()
	p.ShouldRetry = func(err error) bool {
		return errors.Is(err, llm.ErrRateLimited) || errors.Is(err, llm.ErrServerError)
	}
	return p
}

// --- request ---

type contentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

type inputItem struct {
	Type    string        `json:"type"` // "message"
	Role    string        `json:"role"`
	Content []contentPart `json:"content"`
}

type textFormat struct {
	Type   string          `json:"type"` // "json_schema"
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

type request struct {
	Model        string      `json:"model"`
	Instructions string      `json:"instructions,omitempty"`
	Input        []inputItem `json:"input"`
	Store        bool        `json:"store"`
	Stream       bool        `json:"stream"`
	Text         *struct {
		Format textFormat `json:"format"`
	} `json:"text,omitempty"`
}

func buildBody(req llm.Request) ([]byte, error) {
	if len(req.Tools) > 0 || req.Thinking != nil {
		return nil, fmt.Errorf("codex: tools and thinking are not supported: %w", llm.ErrInvalidRequest)
	}
	r := request{Model: req.Model, Stream: true, Input: []inputItem{}}
	var instructions []string
	for _, m := range req.Messages {
		switch m.Role {
		case llm.RoleSystem:
			instructions = append(instructions, m.Content)
		case llm.RoleUser, llm.RoleAssistant:
			if len(m.ToolCalls) > 0 {
				return nil, fmt.Errorf("codex: tool calls are not supported: %w", llm.ErrInvalidRequest)
			}
			if len(m.Audio) > 0 {
				return nil, fmt.Errorf("codex: audio input is not supported: %w", llm.ErrInvalidRequest)
			}
			textType := "input_text"
			if m.Role == llm.RoleAssistant {
				textType = "output_text"
			}
			item := inputItem{Type: "message", Role: string(m.Role)}
			if m.Content != "" {
				item.Content = append(item.Content, contentPart{Type: textType, Text: m.Content})
			}
			for _, img := range m.Images {
				u := img.URL
				if u == "" && len(img.Data) > 0 {
					mime := img.ContentType
					if mime == "" {
						mime = http.DetectContentType(img.Data)
					}
					u = "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(img.Data)
				}
				item.Content = append(item.Content, contentPart{Type: "input_image", ImageURL: u})
			}
			r.Input = append(r.Input, item)
		default:
			return nil, fmt.Errorf("codex: %s messages are not supported: %w", m.Role, llm.ErrInvalidRequest)
		}
	}
	r.Instructions = strings.Join(instructions, "\n\n")
	if f := req.ResponseFormat; f != nil {
		r.Text = &struct {
			Format textFormat `json:"format"`
		}{textFormat{Type: "json_schema", Name: f.Name, Strict: f.Strict, Schema: f.Schema}}
	}
	return json.Marshal(r)
}

// --- response stream ---

type streamEvent struct {
	Type     string `json:"type"`
	Delta    string `json:"delta"`
	Text     string `json:"text"`
	Message  string `json:"message"`
	Code     string `json:"code"`
	Response *struct {
		Usage *struct {
			InputTokens        int `json:"input_tokens"`
			OutputTokens       int `json:"output_tokens"`
			InputTokensDetails *struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
	} `json:"response"`
}

// processStream turns the Responses SSE into llm events and always ends with
// exactly one EventEnd. Text arrives as output_text deltas; the .done event
// only counts when no delta came (response.completed carries no output).
func processStream(ctx context.Context, body io.Reader, out chan<- llm.Event) {
	finish := ""
	defer func() { emit(ctx, out, llm.Event{Kind: llm.EventEnd, FinishReason: finish}) }()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	sawDelta := false
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data:")
		if !ok {
			continue // "event:" lines repeat the type that data carries anyway
		}
		var ev streamEvent
		if json.Unmarshal([]byte(strings.TrimSpace(data)), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "response.output_text.delta":
			sawDelta = true
			if !emit(ctx, out, llm.Event{Kind: llm.EventTextDelta, Text: ev.Delta}) {
				return
			}
		case "response.output_text.done":
			if !sawDelta && ev.Text != "" {
				emit(ctx, out, llm.Event{Kind: llm.EventTextDelta, Text: ev.Text})
			}
		case "response.completed", "response.incomplete":
			finish = "stop"
			if r := ev.Response; r != nil {
				if r.IncompleteDetails != nil && r.IncompleteDetails.Reason != "" {
					finish = r.IncompleteDetails.Reason
				}
				if u := r.Usage; u != nil {
					usage := &llm.Usage{PromptTokens: u.InputTokens, CompletionTokens: u.OutputTokens}
					if u.InputTokensDetails != nil {
						usage.CacheReadTokens = u.InputTokensDetails.CachedTokens
					}
					emit(ctx, out, llm.Event{Kind: llm.EventUsage, Usage: usage})
				}
			}
			return
		case "response.failed", "error":
			msg, code := ev.Message, ev.Code
			if r := ev.Response; r != nil && r.Error != nil {
				msg, code = r.Error.Message, r.Error.Code
			}
			sentinel := llm.ErrServerError
			if strings.Contains(code, "content") || strings.Contains(code, "safety") {
				sentinel, finish = llm.ErrContentFiltered, "content_filter"
			} else {
				finish = "error"
			}
			emit(ctx, out, llm.Event{Kind: llm.EventError, Err: fmt.Errorf("codex: %s %s: %w", code, msg, sentinel)})
			return
		}
	}
	// completed, failed and error all return above; reaching here means a cut stream
	if ctx.Err() == nil {
		finish = "error"
		emit(ctx, out, llm.Event{Kind: llm.EventError, Err: fmt.Errorf("codex: stream ended before completion: %w", llm.ErrServerError)})
	}
}

func emit(ctx context.Context, out chan<- llm.Event, ev llm.Event) bool {
	select {
	case out <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}
