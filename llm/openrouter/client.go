// Package openrouter implements llm.Client against OpenRouter
// (https://openrouter.ai). Streaming-first; tool-call assembly; 429/5xx
// retry at stream-start. The SSE parser is inlined here per ADR-022.
package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/mxcd/aikido/retry"
	"github.com/mxcd/aikido/llm"
)

// DefaultBaseURL is OpenRouter's API root.
const DefaultBaseURL = "https://openrouter.ai/api/v1"

// Options configure the OpenRouter Client.
type Options struct {
	// APIKey is required.
	APIKey string

	// BaseURL overrides DefaultBaseURL. Useful for tests via httptest.Server.
	BaseURL string

	// HTTPClient overrides the default. Default: &http.Client{Timeout: 0}.
	// Streams may be long-lived; callers wanting a wall-clock cap pass a
	// context with deadline rather than configuring a client-level timeout.
	HTTPClient *http.Client

	// HTTPReferer is the optional OpenRouter ranking-attribution header.
	HTTPReferer string

	// XTitle is the optional OpenRouter ranking-attribution header.
	XTitle string

	// ProviderOrder is the optional `provider.order` routing preference.
	// A single entry locks to one provider (allow_fallbacks=false); multiple
	// entries form a fallback chain (allow_fallbacks=true). Matches the
	// production pattern in asolabs/hub.
	ProviderOrder []string
}

// Client is an OpenRouter implementation of llm.Client.
type Client struct {
	apiKey      string
	baseURL     string
	httpClient  *http.Client
	httpReferer string
	xTitle      string
	provOrder   []string
}

// Compile-time conformance.
var _ llm.Client = (*Client)(nil)

// NewClient builds a Client. Returns an error if APIKey is empty.
func NewClient(opts *Options) (*Client, error) {
	if opts == nil || opts.APIKey == "" {
		return nil, errors.New("openrouter: APIKey is required")
	}
	c := &Client{
		apiKey:      opts.APIKey,
		baseURL:     opts.BaseURL,
		httpClient:  opts.HTTPClient,
		httpReferer: opts.HTTPReferer,
		xTitle:      opts.XTitle,
		provOrder:   append([]string(nil), opts.ProviderOrder...),
	}
	if c.baseURL == "" {
		c.baseURL = DefaultBaseURL
	}
	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: 0}
	}
	return c, nil
}

// Stream sends one chat-completions request and returns a channel of events.
// EventEnd is always the last event; the channel closes after EventEnd.
//
// Retry happens only at stream-start: if the HTTP response is 429 or 5xx
// before any SSE bytes have been read, aikido closes the body and retries
// per the policy returned by retryPolicy(). Mid-stream errors do NOT retry —
// they propagate as EventError.
func (c *Client) Stream(ctx context.Context, req llm.Request) (<-chan llm.Event, error) {
	body, err := c.buildBody(req)
	if err != nil {
		return nil, fmt.Errorf("openrouter: build request: %w", err)
	}

	var resp *http.Response
	startErr := retry.Do(ctx, retryPolicy(), func(attempt int) error {
		// Each attempt builds a fresh *http.Request; net/http does not allow
		// reusing one across calls.
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("openrouter: build http request: %w", err)
		}
		c.setHeaders(httpReq)

		r, err := c.httpClient.Do(httpReq)
		if err != nil {
			// Network-layer errors are transient — treat as 5xx-class.
			return fmt.Errorf("openrouter: http error: %w", llm.ErrServerError)
		}
		if r.StatusCode != http.StatusOK {
			// classifyHTTPError closes the body.
			cls := classifyHTTPError(r)
			return cls
		}
		resp = r
		return nil
	})
	if startErr != nil {
		return nil, startErr
	}

	out := make(chan llm.Event, 16)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		processStream(ctx, resp.Body, out)
	}()
	return out, nil
}

// setHeaders writes the OpenRouter-required headers.
func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if c.httpReferer != "" {
		req.Header.Set("HTTP-Referer", c.httpReferer)
	}
	if c.xTitle != "" {
		req.Header.Set("X-Title", c.xTitle)
	}
}

// buildBody assembles the chat-completions JSON body from llm.Request.
func (c *Client) buildBody(req llm.Request) ([]byte, error) {
	msgs, err := buildAPIMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	cr := chatRequest{
		Model:       req.Model,
		Messages:    msgs,
		Tools:       buildAPITools(req.Tools),
		Stream:      true,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Stop:        req.StopSequences,
	}
	if eff := effortFromConfig(req.Thinking); eff != "" {
		cr.Reasoning = &apiReasoning{Effort: eff}
	}
	if len(c.provOrder) > 0 {
		ap := &apiProvider{Order: append([]string(nil), c.provOrder...)}
		// asolabs/hub pattern: a single-entry order means "lock to this
		// provider" (no fallbacks); multi-entry means "try them in order
		// with fallbacks enabled."
		fallbacks := len(c.provOrder) > 1
		ap.AllowFallbacks = &fallbacks
		cr.Provider = ap
	}
	return json.Marshal(cr)
}

// jsonUnmarshalLenient is a permissive json.Unmarshal that ignores trailing
// data and recovers from minor encoding issues.
func jsonUnmarshalLenient(b []byte, v any) error {
	if len(b) == 0 {
		return io.ErrUnexpectedEOF
	}
	return json.Unmarshal(b, v)
}
