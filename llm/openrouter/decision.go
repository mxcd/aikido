package openrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mxcd/aikido/llm"
)

// Compile-time conformance for the optional decisions capability.
var _ llm.Decider = (*Client)(nil)

// Decide asks a decision model typed questions through OpenRouter's Decisions
// API (POST /api/alpha/decisions; alpha, outside /api/v1). Decision models
// (typesafe/jev-1.14, perplexity/pplx-decider-v1.2, ...) answer only here;
// chat completions rejects them. Retry, error classification and provider
// routing (Options.Provider, WithProvider) are Complete's.
func (c *Client) Decide(ctx context.Context, req llm.DecisionRequest) (llm.DecisionResponse, error) {
	body, err := c.buildDecisionsBody(ctx, req)
	if err != nil {
		return llm.DecisionResponse{}, fmt.Errorf("openrouter: decisions: %w", err)
	}
	start := time.Now()
	raw, _, err := c.do(ctx, http.MethodPost, c.apiRoot()+"/alpha/decisions", body, "application/json", http.StatusOK)
	if err != nil {
		return llm.DecisionResponse{}, err
	}
	var out decisionsResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return llm.DecisionResponse{}, fmt.Errorf("openrouter: decisions: decode response: %w", llm.ErrServerError)
	}
	resp := llm.DecisionResponse{
		Answers: make(map[string]llm.DecisionAnswer, len(out.Answers)),
		Model:   out.Model,
		ID:      out.ID,
		Usage: &llm.Usage{
			PromptTokens:     out.Usage.InputTokens,
			CompletionTokens: out.Usage.OutputTokens,
			CostUSD:          out.Usage.Cost,
			Provider:         out.Provider,
		},
	}
	resp.Usage.SetTiming(start, time.Time{}, time.Now())
	// A handful of answer tokens over the whole call is no throughput.
	resp.Usage.TokensPerSecond = 0
	for name, a := range out.Answers {
		ans := llm.DecisionAnswer{
			Kind:          llm.DecisionKind(a.Type),
			Noul:          a.Noul,
			Choice:        a.Choice,
			Score:         a.Score,
			Probabilities: a.Probabilities,
			Confidence:    a.Confidence,
		}
		if len(a.Legend) > 0 {
			ans.Legend = make(map[string]string, len(a.Legend))
			for k, v := range a.Legend {
				ans.Legend[k] = legendText(v)
			}
		}
		if a.Box != nil {
			ans.Box = &llm.DecisionBoxArea{Top: a.Box.Top, Bottom: a.Box.Bottom, Left: a.Box.Left, Right: a.Box.Right}
		}
		resp.Answers[name] = ans
	}
	for name := range req.Questions {
		if _, ok := resp.Answers[name]; !ok {
			return resp, fmt.Errorf("openrouter: decisions: no answer for question %q: %w", name, llm.ErrServerError)
		}
	}
	return resp, nil
}

// apiRoot is the API root above /v1: the Decisions API lives at
// /api/alpha/decisions. A BaseURL without /v1 (tests) is the root itself.
func (c *Client) apiRoot() string {
	return strings.TrimSuffix(c.baseURL, "/v1")
}

// buildDecisionsBody validates the questions per kind and maps them and the
// state's images onto the wire.
func (c *Client) buildDecisionsBody(ctx context.Context, req llm.DecisionRequest) ([]byte, error) {
	if strings.TrimSpace(req.Model) == "" {
		return nil, fmt.Errorf("model is required: %w", llm.ErrInvalidRequest)
	}
	if req.State == nil {
		return nil, fmt.Errorf("state is required: %w", llm.ErrInvalidRequest)
	}
	if len(req.Questions) == 0 {
		return nil, fmt.Errorf("at least one question is required: %w", llm.ErrInvalidRequest)
	}
	dr := decisionsRequest{
		Model:     req.Model,
		Questions: make(map[string]apiDecisionQuestion, len(req.Questions)),
		Provider:  c.providerPrefs(ctx),
	}
	for name, q := range req.Questions {
		aq, err := apiQuestion(q)
		if err != nil {
			return nil, fmt.Errorf("question %q: %w", name, err)
		}
		dr.Questions[name] = aq
	}
	state, err := apiState(req.State)
	if err != nil {
		return nil, err
	}
	dr.State = state
	return json.Marshal(dr)
}

func apiQuestion(q llm.DecisionQuestion) (apiDecisionQuestion, error) {
	if strings.TrimSpace(q.Instructions) == "" {
		return apiDecisionQuestion{}, fmt.Errorf("instructions are required: %w", llm.ErrInvalidRequest)
	}
	aq := apiDecisionQuestion{Type: string(q.Kind), Instructions: q.Instructions}
	switch q.Kind {
	case llm.DecisionChoice:
		if len(q.Options) < 2 {
			return aq, fmt.Errorf("a choice needs at least two options: %w", llm.ErrInvalidRequest)
		}
		aq.Criteria = q.Options
	case llm.DecisionNoul:
		if len(q.Options) > 0 {
			_, t := q.Options["true"]
			_, f := q.Options["false"]
			if !t || !f || len(q.Options) != 2 {
				return aq, fmt.Errorf(`noul options must be exactly "true" and "false": %w`, llm.ErrInvalidRequest)
			}
			aq.Criteria = q.Options
		}
	case llm.DecisionScore:
		if len(q.Levels) == 0 {
			return aq, fmt.Errorf("a score needs levels: %w", llm.ErrInvalidRequest)
		}
		aq.Criteria = q.Levels
	case llm.DecisionBox:
	default:
		return aq, fmt.Errorf("unknown kind %q: %w", q.Kind, llm.ErrInvalidRequest)
	}
	return aq, nil
}

// apiState replaces ImagePart elements of a slice state with the wire image
// object; any other state passes through for json.Marshal.
func apiState(state any) (any, error) {
	items, ok := state.([]any)
	if !ok {
		return state, nil
	}
	out := make([]any, len(items))
	for i, it := range items {
		var part *llm.ImagePart
		switch v := it.(type) {
		case llm.ImagePart:
			part = &v
		case *llm.ImagePart:
			part = v
		default:
			out[i] = it
			continue
		}
		u, err := imageURL(*part)
		if err != nil {
			return nil, fmt.Errorf("state image %d: %w", i, err)
		}
		out[i] = apiStateImage{ImageURL: apiImageURL{URL: u}}
	}
	return out, nil
}

// legendText is a legend entry as text: the level string as sent, or the JSON
// of a structured one.
func legendText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// DecisionModels lists the models served by the Decisions API. They are not
// part of the plain GET /models list.
func (c *Client) DecisionModels(ctx context.Context) ([]Model, error) {
	var out struct {
		Data []Model `json:"data"`
	}
	if err := c.getJSON(ctx, "/models?output_modalities=decisions", &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}
