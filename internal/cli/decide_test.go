package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/mxcd/aikido/llm"
	"github.com/mxcd/aikido/llm/llmtest"
)

func TestDecideCommand_BuildsQuestionsAndPrintsAnswers(t *testing.T) {
	stub := llmtest.NewStubClient()
	stub.ScriptDecisions(llmtest.DecisionScript{Response: llm.DecisionResponse{
		Model: "typesafe/jev-1.14-20261014",
		Answers: map[string]llm.DecisionAnswer{
			"is_bug": {Kind: llm.DecisionNoul, Noul: 0.95},
			"team":   {Kind: llm.DecisionChoice, Choice: "payments", Confidence: 0.72, Probabilities: map[string]float64{"payments": 0.81, "frontend": 0.19}},
			"urgency": {Kind: llm.DecisionScore, Score: 1.99, Confidence: 1,
				Probabilities: map[string]float64{"0": 0, "1": 0.01, "2": 0.99}, Legend: map[string]string{"0": "can wait", "1": "this week", "2": "blocking"}},
		},
		Usage: &llm.Usage{PromptTokens: 418, CostUSD: 0.000017556, Provider: "TypeSafe"},
	}})
	var out bytes.Buffer
	app := NewApp(func() (llm.Client, error) { return stub, nil })
	app.Writer = &out
	err := app.Run(context.Background(), []string{"aikido", "decide",
		"--noul", "is_bug=Is this a bug report?",
		"--choice", "team=Which team, a=b style?=payments, frontend",
		"--score", "urgency=How urgent?=can wait,this week,blocking",
		"Checkout", "is", "blank."})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	r := stub.DecisionRequests()[0]
	if r.Model != defaultDecisionModel || r.State != "Checkout is blank." {
		t.Errorf("model/state = %q/%v", r.Model, r.State)
	}
	if q := r.Questions["team"]; q.Kind != llm.DecisionChoice || q.Instructions != "Which team, a=b style?" || len(q.Options) != 2 || q.Options["frontend"] != "frontend" {
		t.Errorf("choice = %+v", q)
	}
	if q := r.Questions["urgency"]; len(q.Levels) != 3 || q.Levels[2] != "blocking" {
		t.Errorf("score = %+v", q)
	}
	if r.Questions["is_bug"].Kind != llm.DecisionNoul {
		t.Errorf("noul = %+v", r.Questions["is_bug"])
	}
	s := out.String()
	for _, want := range []string{"is_bug   noul    0.95", "payments (confidence 0.72; payments 0.81, frontend 0.19)", "1.99 (confidence 1.00; blocking 0.99, this week 0.01, can wait 0.00)", "provider=TypeSafe", "model=typesafe/jev-1.14-20261014"} {
		if !strings.Contains(s, want) {
			t.Errorf("output lacks %q:\n%s", want, s)
		}
	}
}

func TestParseQuestions_Errors(t *testing.T) {
	for _, tc := range []struct{ nouls, choices []string }{
		{nil, nil},
		{[]string{"no-equals"}, nil},
		{nil, []string{"team=Which team?"}},
		{[]string{"q=a"}, []string{"q=b=x,y"}},
	} {
		if _, err := parseQuestions(tc.nouls, tc.choices, nil, nil); err == nil {
			t.Errorf("%+v: want an error", tc)
		}
	}
}
