package openrouter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mxcd/aikido/llm"
)

func TestWithProvider_OverridesClientPrefs(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"provider":"Azure","choices":[{"message":{"content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":40}}`))
	}))
	defer srv.Close()
	c, err := NewClient(&Options{APIKey: "k", BaseURL: srv.URL, ProviderOrder: []string{"anthropic"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithProvider(context.Background(), &ProviderPrefs{
		Only: []string{"azure"}, Sort: "throughput", MaxPrice: &MaxPrice{Completion: 6},
	})
	resp, err := c.Complete(ctx, llm.Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	prov, _ := captured["provider"].(map[string]any)
	if prov["sort"] != "throughput" || prov["order"] != nil || prov["max_price"].(map[string]any)["completion"] != 6.0 {
		t.Errorf("provider block = %v; want the context prefs only", prov)
	}
	u := resp.Usage
	if u == nil || u.Provider != "Azure" || u.Duration <= 0 || u.Latency != u.Duration {
		t.Errorf("usage = %+v", u)
	}
}

func TestStream_UsageCarriesProviderAndTiming(t *testing.T) {
	chunks := []string{
		`{"provider":"Anthropic","choices":[{"delta":{"role":"assistant","content":""}}]}`,
		`{"provider":"Anthropic","choices":[{"delta":{"content":"Hello"}}]}`,
		`{"provider":"Anthropic","choices":[{"delta":{"content":" world"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":30}}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		for i, c := range chunks {
			if i > 0 {
				time.Sleep(150 * time.Millisecond)
			}
			_, _ = io.WriteString(w, "data: "+c+"\n\n")
			f.Flush()
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	c, _ := NewClient(&Options{APIKey: "k", BaseURL: srv.URL})
	ch, err := c.Stream(context.Background(), llm.Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	var u *llm.Usage
	for _, ev := range drain(t, ch, 5*time.Second) {
		if ev.Kind == llm.EventUsage {
			u = ev.Usage
		}
	}
	if u == nil {
		t.Fatal("no usage event")
	}
	// The role-only opening chunk is not output: latency covers the first sleep.
	if u.Provider != "Anthropic" || u.Latency < 140*time.Millisecond || u.Duration < u.Latency+140*time.Millisecond {
		t.Errorf("usage = %+v", u)
	}
	// 150 ms of generation is under the 250 ms rate window: no rate.
	if u.TokensPerSecond != 0 {
		t.Errorf("tokens/s = %v; want 0 under the rate window", u.TokensPerSecond)
	}
}

func TestModelEndpoints(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"data":{"id":"anthropic/claude-haiku-4.5","endpoints":[{"provider_name":"Azure","tag":"azure/global",
			"pricing":{"prompt":"0.000001","completion":"0.000005","discount":0},"max_prompt_tokens":null,"uptime_last_30m":100,
			"latency_last_30m":{"p50":414,"p75":499,"p90":705.8,"p99":2744.8},"throughput_last_30m":{"p50":84,"p75":78,"p90":59.8,"p99":28.88}},
			{"provider_name":"Idle","latency_last_30m":null,"throughput_last_30m":null}]}}`))
	}))
	defer srv.Close()
	c, _ := NewClient(&Options{APIKey: "k", BaseURL: srv.URL})
	me, err := c.ModelEndpoints(context.Background(), "anthropic/claude-haiku-4.5")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/models/anthropic/claude-haiku-4.5/endpoints" {
		t.Errorf("path = %s", path)
	}
	e := me.Endpoints[0]
	if e.Tag != "azure/global" || e.Pricing.Completion.PerMillion() != 5 || e.LatencyLast30m.P50 != 414 || e.ThroughputLast30m.P50 != 84 || *e.UptimeLast30m != 100 {
		t.Errorf("endpoint = %+v", e)
	}
	if me.Endpoints[1].LatencyLast30m != nil {
		t.Error("null latency should stay nil")
	}
	if _, err := c.ModelEndpoints(context.Background(), "noslash"); err == nil {
		t.Error("id without author should error")
	}
}
