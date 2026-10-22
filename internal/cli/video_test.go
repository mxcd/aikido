package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mxcd/aikido/llm"
	"github.com/mxcd/aikido/llm/llmtest"
)

func TestVideoCommand_WritesClipAndThreadsFlags(t *testing.T) {
	clip := []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom")
	stub := llmtest.NewStubClient()
	stub.ScriptVideos(llmtest.VideoScript{Response: llm.VideoResponse{
		Video: clip, ContentType: "video/mp4", JobID: "job1",
		Usage: &llm.Usage{CostUSD: 0.12, Duration: 48 * time.Second},
	}})
	dir := t.TempDir()
	frame := filepath.Join(dir, "first.png")
	if err := os.WriteFile(frame, fakePNG, 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	app := NewApp(func() (llm.Client, error) { return stub, nil })
	app.Writer = &out
	err := app.Run(context.Background(), []string{"aikido", "video", "--out", dir, "-d", "4", "-r", "720p", "-a", "9:16",
		"--audio=false", "--first-frame", frame, "a", "cat", "yawns"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	reqs := stub.VideoRequests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d", len(reqs))
	}
	r := reqs[0]
	if r.Model != defaultVideoModel || r.Prompt != "a cat yawns" || r.Duration != 4 || r.Resolution != "720p" || r.AspectRatio != "9:16" {
		t.Errorf("request = %+v", r)
	}
	if r.GenerateAudio == nil || *r.GenerateAudio || r.Seed != nil {
		t.Errorf("audio/seed = %v/%v, want false/unset", r.GenerateAudio, r.Seed)
	}
	if r.FirstFrame == nil || !strings.HasPrefix(r.FirstFrame.URL, "data:image/png;base64,") || r.LastFrame != nil {
		t.Errorf("frames = %+v / %+v", r.FirstFrame, r.LastFrame)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "video-*.mp4"))
	if len(matches) != 1 {
		t.Fatalf("clips written = %v", matches)
	}
	if got, _ := os.ReadFile(matches[0]); !bytes.Equal(got, clip) {
		t.Error("clip bytes differ")
	}
	if !strings.Contains(out.String(), "cost=$0.1200") || !strings.Contains(out.String(), "job=job1") {
		t.Errorf("output = %q", out.String())
	}
}

func TestVideoCommand_AudioUnsetStaysNil(t *testing.T) {
	stub := llmtest.NewStubClient()
	stub.ScriptVideos(llmtest.VideoScript{Err: llm.ErrContentFiltered, Response: llm.VideoResponse{JobID: "job9"}})
	app := NewApp(func() (llm.Client, error) { return stub, nil })
	app.Writer = &bytes.Buffer{}
	err := app.Run(context.Background(), []string{"aikido", "video", "--out", t.TempDir(), "p"})
	if !errors.Is(err, llm.ErrContentFiltered) || !strings.Contains(err.Error(), "job9") {
		t.Fatalf("err = %v, want the filter error with the job id", err)
	}
	if r := stub.VideoRequests()[0]; r.GenerateAudio != nil || r.Duration != 0 {
		t.Errorf("unset flags were sent: %+v", r)
	}
}

func TestDurations(t *testing.T) {
	cases := []struct {
		in   []int
		want string
	}{
		{[]int{3, 4, 5, 6}, "3-6"},
		{[]int{8, 4, 6}, "4,6,8"},
		{[]int{5, 10}, "5,10"},
		{nil, "-"},
	}
	for _, c := range cases {
		if got := durations(c.in); got != c.want {
			t.Errorf("durations(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
