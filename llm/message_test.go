package llm

import (
	"testing"
	"time"
)

func TestUsageSetTiming(t *testing.T) {
	t0 := time.Unix(0, 0)
	u := Usage{CompletionTokens: 100}
	u.SetTiming(t0, t0.Add(500*time.Millisecond), t0.Add(2500*time.Millisecond))
	if u.Latency != 500*time.Millisecond || u.Duration != 2500*time.Millisecond || u.TokensPerSecond != 50 {
		t.Errorf("stream timing = %+v", u)
	}
	u.SetTiming(t0, time.Time{}, t0.Add(2*time.Second))
	if u.Latency != 2*time.Second || u.TokensPerSecond != 50 {
		t.Errorf("complete timing = %+v", u)
	}
	u.SetTiming(t0, t0.Add(time.Second), t0.Add(time.Second+100*time.Millisecond))
	if u.TokensPerSecond != 0 {
		t.Errorf("burst rate = %v; want 0", u.TokensPerSecond)
	}
}
