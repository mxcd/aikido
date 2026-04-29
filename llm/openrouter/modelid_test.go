package openrouter

import "testing"

func TestNormalizeModelID(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"anthropic/claude-sonnet-4.6", "anthropic/claude-sonnet-4-6"},
		{"anthropic/claude-opus-4.7", "anthropic/claude-opus-4-7"},
		{"anthropic/claude-sonnet-4-6", "anthropic/claude-sonnet-4-6"}, // already hyphenated
		{"openai/gpt-4o", "openai/gpt-4o"},                             // no change
		{"google/gemini-2.5-pro", "google/gemini-2-5-pro"},
		{"", ""},
	}
	for _, tc := range cases {
		got := normalizeModelID(tc.in)
		if got != tc.want {
			t.Errorf("normalizeModelID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
