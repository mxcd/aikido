package openrouter

import "strings"

// normalizeModelID converts the dot-using human form to the hyphenated form
// OpenRouter's catalog uses. See ADR-025 and the OpenRouter model-ID gotcha
// note: a mismatch silently falls back to a different model or returns 4xx
// without a helpful error, so aikido normalizes at request-build time.
func normalizeModelID(id string) string {
	return strings.ReplaceAll(id, ".", "-")
}
