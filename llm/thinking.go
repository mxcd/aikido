package llm

type ThinkingEffort string

const (
	ThinkingEffortLow    ThinkingEffort = "low"
	ThinkingEffortMedium ThinkingEffort = "medium"
	ThinkingEffortHigh   ThinkingEffort = "high"
)

// ThinkingConfig configures provider-side thinking. Use ThinkingByEffort or
// ThinkingByBudget — the unexported fields prevent setting both.
type ThinkingConfig struct {
	effort ThinkingEffort
	budget int
}

func ThinkingByEffort(e ThinkingEffort) *ThinkingConfig {
	return &ThinkingConfig{effort: e}
}

func ThinkingByBudget(n int) *ThinkingConfig {
	return &ThinkingConfig{budget: n}
}

// Effort returns the configured coarse effort, empty string if budget-based.
func (t *ThinkingConfig) Effort() ThinkingEffort {
	if t == nil {
		return ""
	}
	return t.effort
}

// Budget returns the configured token budget, 0 if effort-based.
func (t *ThinkingConfig) Budget() int {
	if t == nil {
		return 0
	}
	return t.budget
}
