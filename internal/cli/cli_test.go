package cli

import (
	"bytes"
	"context"
	"errors"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/mxcd/aikido/llm"
	"github.com/mxcd/aikido/llm/llmtest"
)

func stubFactory(client llm.Client) ClientFactory {
	return func() (llm.Client, error) { return client, nil }
}

func TestNewApp_VersionFlag(t *testing.T) {
	app := NewApp(func() (llm.Client, error) { return nil, nil })
	var out bytes.Buffer
	app.Writer = &out
	app.ErrWriter = &out
	if err := app.Run(context.Background(), []string{"aikido", "--version"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), version()) {
		t.Errorf("version output missing %q: %q", version(), out.String())
	}
}

func TestVersionFromBuildInfo(t *testing.T) {
	rev := []debug.BuildSetting{{Key: "vcs.revision", Value: "b25d1832e0c5f6a7b8c9d0e1f2a3b4c5d6e7f8a9"}}
	cases := []struct {
		name string
		info debug.BuildInfo
		want string
	}{
		{"go install at a tag", debug.BuildInfo{Main: debug.Module{Version: "v0.6.1"}}, "v0.6.1"},
		{"checkout pseudo-version", debug.BuildInfo{Main: debug.Module{Version: "v0.6.1-0.20261011120000-b25d1832e0c5+dirty"}}, "v0.6.1-0.20261011120000-b25d1832e0c5+dirty"},
		{"devel with revision", debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: rev}, "(devel) b25d1832e0c5"},
		{"devel dirty", debug.BuildInfo{Main: debug.Module{Version: "(devel)"}, Settings: append(rev, debug.BuildSetting{Key: "vcs.modified", Value: "true"})}, "(devel) b25d1832e0c5+dirty"},
		{"devel without vcs", debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, "(devel)"},
		{"empty", debug.BuildInfo{}, "(devel)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := versionFromBuildInfo(&tc.info); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNewApp_RoutesChat(t *testing.T) {
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: "ok"},
		{Kind: llm.EventEnd},
	}})
	app := NewApp(stubFactory(stub))
	var out bytes.Buffer
	app.Writer = &out
	app.ErrWriter = &out
	if err := app.Run(context.Background(), []string{"aikido", "chat", "--model", "m1", "hello"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "ok") {
		t.Errorf("chat output missing reply: %q", out.String())
	}
	if reqs := stub.Requests(); len(reqs) != 1 || reqs[0].Model != "m1" {
		t.Errorf("model flag not propagated: %+v", reqs)
	}
}

func TestNewApp_ChatWithoutPromptFails(t *testing.T) {
	stub := llmtest.NewStubClient()
	app := NewApp(stubFactory(stub))
	var out bytes.Buffer
	app.Writer = &out
	app.ErrWriter = &out
	err := app.Run(context.Background(), []string{"aikido", "chat"})
	if err == nil {
		t.Error("expected error for missing prompt")
	}
}

func TestNewApp_RoutesAgent(t *testing.T) {
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: "fine"},
		{Kind: llm.EventEnd},
	}})
	app := NewApp(stubFactory(stub))
	var out bytes.Buffer
	app.Writer = &out
	app.ErrWriter = &out
	if err := app.Run(context.Background(), []string{"aikido", "agent", "--max-turns", "3", "do thing"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "[end] stop") {
		t.Errorf("agent output missing end: %q", out.String())
	}
}

func TestNewApp_FactoryError(t *testing.T) {
	sentinel := errors.New("no key")
	app := NewApp(func() (llm.Client, error) { return nil, sentinel })
	var out bytes.Buffer
	app.Writer = &out
	app.ErrWriter = &out
	err := app.Run(context.Background(), []string{"aikido", "chat", "hi"})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v; want %v", err, sentinel)
	}
}
