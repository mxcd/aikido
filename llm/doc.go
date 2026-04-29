// Package llm defines the provider-agnostic types and the Client interface
// every LLM provider satisfies.
//
// The shapes here are deliberately the lowest common denominator across
// providers. Provider-specific knobs live on each provider's Options struct.
//
// See [docs/v1/API.md] for the rationale behind every type.
package llm
