package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mxcd/aikido/llm"
)

// fakeMP4 is the smallest byte string http.DetectContentType calls video/mp4.
var fakeMP4 = []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom")

// fakeVideoAPI serves the three video endpoints. Polls answer statuses in
// order, the last one repeating; a completed poll lists a URL unless noURL.
type fakeVideoAPI struct {
	statuses []string
	failWith any
	noURL    bool
	polls    atomic.Int32
	submit   map[string]any
}

func (f *fakeVideoAPI) client(t *testing.T) *Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /videos", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&f.submit); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"id":"job1","polling_url":"x","status":"pending"}`))
	})
	mux.HandleFunc("GET /videos/job1", func(w http.ResponseWriter, _ *http.Request) {
		n := int(f.polls.Add(1)) - 1
		st := f.statuses[min(n, len(f.statuses)-1)]
		out := map[string]any{"id": "job1", "status": st}
		if st == "completed" {
			out["usage"] = map[string]any{"cost": 0.42}
			if !f.noURL {
				out["unsigned_urls"] = []string{"https://openrouter.ai/api/v1/videos/job1/content?index=0"}
			}
		}
		if st == "failed" {
			out["error"] = f.failWith
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("GET /videos/job1/content", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("index") != "0" {
			t.Errorf("index = %q", r.URL.Query().Get("index"))
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(fakeMP4)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c, err := NewClient(&Options{APIKey: "k", BaseURL: srv.URL, VideoPollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestGenerateVideo_SubmitsPollsAndDownloads(t *testing.T) {
	f := &fakeVideoAPI{statuses: []string{"pending", "in_progress", "completed"}}
	off, seed := false, 7
	resp, err := f.client(t).GenerateVideo(context.Background(), llm.VideoRequest{
		Model: "kwaivgi/kling-v3.0-pro", Prompt: "the cup steams", AspectRatio: "9:16", Resolution: "720p",
		Duration: 5, GenerateAudio: &off, Seed: &seed,
		FirstFrame: &llm.ImagePart{Data: []byte("jpg"), ContentType: "image/jpeg"},
		LastFrame:  &llm.ImagePart{URL: "https://example.com/last.png"},
	})
	if err != nil {
		t.Fatalf("GenerateVideo: %v", err)
	}
	if string(resp.Video) != string(fakeMP4) || resp.ContentType != "video/mp4" || resp.JobID != "job1" {
		t.Fatalf("resp = %q %q %q", resp.Video, resp.ContentType, resp.JobID)
	}
	if resp.Usage == nil || resp.Usage.CostUSD != 0.42 || resp.Usage.Duration <= 0 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	if n := f.polls.Load(); n != 3 {
		t.Fatalf("polled %d times, want 3", n)
	}
	s := f.submit
	if s["aspect_ratio"] != "9:16" || s["resolution"] != "720p" || s["duration"] != 5.0 || s["generate_audio"] != false || s["seed"] != 7.0 {
		t.Fatalf("submit body = %+v", s)
	}
	frames, _ := s["frame_images"].([]any)
	if len(frames) != 2 {
		t.Fatalf("frames = %+v", frames)
	}
	first := frames[0].(map[string]any)
	if first["frame_type"] != "first_frame" || !strings.HasPrefix(first["image_url"].(map[string]any)["url"].(string), "data:image/jpeg;base64,") {
		t.Fatalf("first frame = %+v", first)
	}
	if last := frames[1].(map[string]any); last["frame_type"] != "last_frame" || last["image_url"].(map[string]any)["url"] != "https://example.com/last.png" {
		t.Fatalf("last frame = %+v", last)
	}
}

func TestBuildVideoBody_OptionalFieldsStayOffTheWire(t *testing.T) {
	body, err := buildVideoBody(llm.VideoRequest{Model: "m", Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"model":"m","prompt":"p"}` {
		t.Fatalf("body = %s", body)
	}
	for _, req := range []llm.VideoRequest{
		{Model: "m"},
		{Prompt: "p"},
		{Model: "m", Prompt: "p", Size: "1280x720", Resolution: "720p"},
		{Model: "m", Prompt: "p", FirstFrame: &llm.ImagePart{}},
	} {
		if _, err := buildVideoBody(req); !errors.Is(err, llm.ErrInvalidRequest) {
			t.Errorf("%+v: err = %v, want ErrInvalidRequest", req, err)
		}
	}
}

func TestGenerateVideo_Refusals(t *testing.T) {
	cases := []struct {
		name string
		f    *fakeVideoAPI
		want error
	}{
		{"moderation object", &fakeVideoAPI{statuses: []string{"pending", "failed"}, failWith: map[string]any{"message": "content flagged by moderation"}}, llm.ErrContentFiltered},
		{"plain failure string", &fakeVideoAPI{statuses: []string{"failed"}, failWith: "gpu fell over"}, llm.ErrServerError},
		{"completed without output", &fakeVideoAPI{statuses: []string{"completed"}, noURL: true}, llm.ErrContentFiltered},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := tc.f.client(t).GenerateVideo(context.Background(), llm.VideoRequest{Model: "m", Prompt: "p"})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if resp.JobID != "job1" {
				t.Errorf("JobID = %q, want it kept on failure", resp.JobID)
			}
		})
	}
}

func TestGenerateVideo_SubmitErrors(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   error
	}{
		{400, `{"error":{"message":"prompt violates our content policy"}}`, llm.ErrContentFiltered},
		{400, `{"error":{"message":"Duration 5s is not supported"}}`, llm.ErrInvalidRequest},
		{402, `{"error":{"message":"insufficient credits"}}`, llm.ErrAuth},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		c, _ := NewClient(&Options{APIKey: "k", BaseURL: srv.URL, MaxAttempts: 1})
		_, err := c.GenerateVideo(context.Background(), llm.VideoRequest{Model: "m", Prompt: "p"})
		srv.Close()
		if !errors.Is(err, tc.want) {
			t.Errorf("%d %s: err = %v, want %v", tc.status, tc.body, err, tc.want)
		}
	}
}

func TestGenerateVideo_ContextEndsPolling(t *testing.T) {
	f := &fakeVideoAPI{statuses: []string{"pending"}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := f.client(t).GenerateVideo(ctx, llm.VideoRequest{Model: "m", Prompt: "p"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestVideoModels_Decodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/videos/models" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"google/veo-3.1-lite","supported_durations":[4,6,8],"supported_resolutions":["720p"],"supported_frame_images":["first_frame"],"generate_audio":true,"seed":null,"pricing_skus":{"duration_seconds_720p":"0.03"}}]}`))
	}))
	defer srv.Close()
	c, _ := NewClient(&Options{APIKey: "k", BaseURL: srv.URL})
	models, err := c.VideoModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m := models[0]
	if m.ID != "google/veo-3.1-lite" || len(m.SupportedDurations) != 3 || m.GenerateAudio == nil || !*m.GenerateAudio || m.Seed != nil || m.PricingSKUs["duration_seconds_720p"] != "0.03" {
		t.Fatalf("model = %+v", m)
	}
}
