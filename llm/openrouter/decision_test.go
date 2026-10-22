package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mxcd/aikido/llm"
)

// liveDecisionsAnswer is a real answer of typesafe/jev-1.14 (22.10.2026) plus
// a box answer from the same day.
const liveDecisionsAnswer = `{"model":"typesafe/jev-1.14-20261014","answers":{
"is_bug":{"type":"noul","noul":0.95},
"team":{"type":"choice","choice":"payments","probabilities":{"payments":0.81,"frontend":0.19,"account":0},"confidence":0.72},
"urgency":{"type":"score","score":1.99,"legend":{"0":"can wait","1":"this week","2":"blocking revenue"},"probabilities":{"0":0,"1":0,"2":1},"confidence":1},
"red":{"type":"box","box":{"top":0.05,"bottom":0.96,"left":0.04,"right":0.97},"confidence":0.02}},
"usage":{"input_tokens":418,"output_tokens":70,"cost":0.000017556},"id":"gen-dec-1","provider":"TypeSafe"}`

func TestDecide_RoundTrip(t *testing.T) {
	var path string
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &got); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte(liveDecisionsAnswer))
	}))
	defer srv.Close()
	// BaseURL with /v1 like the default: the Decisions API sits above it.
	c, _ := NewClient(&Options{APIKey: "k", BaseURL: srv.URL + "/v1", ProviderOrder: []string{"typesafe"}})

	resp, err := c.Decide(context.Background(), llm.DecisionRequest{
		Model: "typesafe/jev-1.14",
		State: []any{"Checkout is blank after Pay.", llm.ImagePart{Data: fakePNGBytes, ContentType: "image/png"}},
		Questions: map[string]llm.DecisionQuestion{
			"is_bug":  llm.NoulQuestion("Is it a defect?"),
			"team":    llm.ChoiceQuestion("Which team?", map[string]string{"payments": "billing", "frontend": "UI", "account": "login"}),
			"urgency": llm.ScoreQuestion("How urgent?", "can wait", "this week", "blocking revenue"),
			"red":     llm.BoxQuestion("The red rectangle"),
		},
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if path != "/alpha/decisions" {
		t.Errorf("path = %s, want /alpha/decisions", path)
	}

	// Wire: criteria per kind, image in the state, provider routing.
	qs := got["questions"].(map[string]any)
	if _, has := qs["is_bug"].(map[string]any)["criteria"]; has {
		t.Error("noul without options must send no criteria")
	}
	if lv := qs["urgency"].(map[string]any)["criteria"].([]any); len(lv) != 3 || lv[2] != "blocking revenue" {
		t.Errorf("score criteria = %v", lv)
	}
	if qs["team"].(map[string]any)["criteria"].(map[string]any)["payments"] != "billing" || qs["red"].(map[string]any)["type"] != "box" {
		t.Errorf("questions = %v", qs)
	}
	state := got["state"].([]any)
	if img := state[1].(map[string]any)["image_url"].(map[string]any)["url"].(string); !strings.HasPrefix(img, "data:image/png;base64,") {
		t.Errorf("state image = %v", state[1])
	}
	if got["provider"].(map[string]any)["order"].([]any)[0] != "typesafe" {
		t.Errorf("provider = %v", got["provider"])
	}

	// Answers.
	a := resp.Answers
	if a["is_bug"].Kind != llm.DecisionNoul || a["is_bug"].Noul != 0.95 {
		t.Errorf("noul = %+v", a["is_bug"])
	}
	if a["team"].Choice != "payments" || a["team"].Probabilities["frontend"] != 0.19 || a["team"].Confidence != 0.72 {
		t.Errorf("choice = %+v", a["team"])
	}
	if a["urgency"].Score != 1.99 || a["urgency"].Legend["2"] != "blocking revenue" {
		t.Errorf("score = %+v", a["urgency"])
	}
	if b := a["red"].Box; b == nil || b.Top != 0.05 || b.Right != 0.97 {
		t.Errorf("box = %+v", a["red"])
	}
	if resp.Model != "typesafe/jev-1.14-20261014" || resp.ID != "gen-dec-1" {
		t.Errorf("model/id = %s/%s", resp.Model, resp.ID)
	}
	if u := resp.Usage; u.PromptTokens != 418 || u.CompletionTokens != 70 || u.CostUSD != 0.000017556 || u.Provider != "TypeSafe" || u.Duration <= 0 || u.TokensPerSecond != 0 {
		t.Errorf("usage = %+v", u)
	}
}

var fakePNGBytes = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

func TestDecide_Validation(t *testing.T) {
	c, _ := NewClient(&Options{APIKey: "k", BaseURL: "http://127.0.0.1:1"})
	q := map[string]llm.DecisionQuestion{"q": llm.NoulQuestion("ok?")}
	for name, req := range map[string]llm.DecisionRequest{
		"no model":          {State: "s", Questions: q},
		"no state":          {Model: "m", Questions: q},
		"no questions":      {Model: "m", State: "s"},
		"one option":        {Model: "m", State: "s", Questions: map[string]llm.DecisionQuestion{"q": llm.ChoiceQuestion("x", map[string]string{"a": "a"})}},
		"no levels":         {Model: "m", State: "s", Questions: map[string]llm.DecisionQuestion{"q": llm.ScoreQuestion("x")}},
		"half noul options": {Model: "m", State: "s", Questions: map[string]llm.DecisionQuestion{"q": {Kind: llm.DecisionNoul, Instructions: "x", Options: map[string]string{"true": "y"}}}},
		"unknown kind":      {Model: "m", State: "s", Questions: map[string]llm.DecisionQuestion{"q": {Kind: "maybe", Instructions: "x"}}},
		"no instructions":   {Model: "m", State: "s", Questions: map[string]llm.DecisionQuestion{"q": llm.BoxQuestion("")}},
	} {
		if _, err := c.Decide(context.Background(), req); !errors.Is(err, llm.ErrInvalidRequest) {
			t.Errorf("%s: err = %v, want ErrInvalidRequest", name, err)
		}
	}
}

func TestDecide_ErrorsAndMissingAnswer(t *testing.T) {
	status, body := 422, `{"error":{"message":"HTTP 422: criteria should have at least 1 item","code":422}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c, _ := NewClient(&Options{APIKey: "k", BaseURL: srv.URL, MaxAttempts: 1})
	req := llm.DecisionRequest{Model: "m", State: "s", Questions: map[string]llm.DecisionQuestion{"q": llm.NoulQuestion("ok?")}}

	if _, err := c.Decide(context.Background(), req); !errors.Is(err, llm.ErrInvalidRequest) {
		t.Errorf("422: err = %v", err)
	}
	status, body = 200, `{"model":"m","answers":{},"usage":{"input_tokens":1,"output_tokens":0}}`
	if _, err := c.Decide(context.Background(), req); !errors.Is(err, llm.ErrServerError) || !strings.Contains(err.Error(), `"q"`) {
		t.Errorf("missing answer: err = %v", err)
	}
}
