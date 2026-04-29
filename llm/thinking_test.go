package llm

import "testing"

func TestThinkingByEffort_RoundTrip(t *testing.T) {
	cfg := ThinkingByEffort(ThinkingEffortHigh)
	if cfg.Effort() != ThinkingEffortHigh {
		t.Errorf("Effort() = %q, want %q", cfg.Effort(), ThinkingEffortHigh)
	}
	if cfg.Budget() != 0 {
		t.Errorf("Budget() = %d, want 0 when only effort set", cfg.Budget())
	}
}

func TestThinkingByBudget_RoundTrip(t *testing.T) {
	cfg := ThinkingByBudget(8192)
	if cfg.Budget() != 8192 {
		t.Errorf("Budget() = %d, want 8192", cfg.Budget())
	}
	if cfg.Effort() != "" {
		t.Errorf("Effort() = %q, want empty when only budget set", cfg.Effort())
	}
}

func TestThinkingConfig_NilSafe(t *testing.T) {
	var cfg *ThinkingConfig
	if cfg.Effort() != "" {
		t.Error("nil ThinkingConfig.Effort should return empty")
	}
	if cfg.Budget() != 0 {
		t.Error("nil ThinkingConfig.Budget should return 0")
	}
}
