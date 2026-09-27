package openrouter

import (
	"encoding/json"
	"testing"

	"github.com/mxcd/aikido/llm"
)

// Stream and Complete share buildBody, so one check covers both paths.
func TestBuildBody_ResponseFormat(t *testing.T) {
	t.Parallel()
	c, err := NewClient(&Options{APIKey: "sk-test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, stream := range []bool{true, false} {
		raw, err := c.buildBody(llm.Request{
			Model:    "m",
			Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
			ResponseFormat: &llm.JSONSchema{
				Name: "meal", Strict: true,
				Schema: json.RawMessage(`{"type":"object","properties":{"kcal":{"type":"number"}},"required":["kcal"],"additionalProperties":false}`),
			},
		}, stream)
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			ResponseFormat struct {
				Type       string `json:"type"`
				JSONSchema struct {
					Name   string         `json:"name"`
					Strict bool           `json:"strict"`
					Schema map[string]any `json:"schema"`
				} `json:"json_schema"`
			} `json:"response_format"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		rf := body.ResponseFormat
		if rf.Type != "json_schema" || rf.JSONSchema.Name != "meal" || !rf.JSONSchema.Strict || rf.JSONSchema.Schema["type"] != "object" {
			t.Errorf("stream=%v: response_format = %+v", stream, rf)
		}
	}

	raw, _ := c.buildBody(llm.Request{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}}}, true)
	var plain map[string]any
	_ = json.Unmarshal(raw, &plain)
	if _, ok := plain["response_format"]; ok {
		t.Error("response_format sent without a ResponseFormat")
	}
}
