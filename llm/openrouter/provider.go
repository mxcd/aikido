package openrouter

import "context"

// ProviderPrefs is OpenRouter's provider-routing block
// (https://openrouter.ai/docs/features/provider-routing), sent as `provider`
// on chat completions. Zero fields stay off the wire and leave OpenRouter's
// default (load-balanced across providers, fallbacks on).
type ProviderPrefs struct {
	// Order lists provider slugs to try in order ("anthropic", "azure", ...).
	Order []string `json:"order,omitempty"`
	// AllowFallbacks false pins the request to Order/Only: no other provider
	// is tried when they fail. Nil keeps OpenRouter's default (true).
	AllowFallbacks *bool `json:"allow_fallbacks,omitempty"`
	// Only restricts routing to these provider slugs.
	Only []string `json:"only,omitempty"`
	// Ignore excludes these provider slugs.
	Ignore []string `json:"ignore,omitempty"`
	// Sort ranks the candidates: "price", "throughput" or "latency".
	Sort string `json:"sort,omitempty"`
	// Quantizations restricts to endpoints with these quantizations
	// ("fp8", "bf16", ...).
	Quantizations []string `json:"quantizations,omitempty"`
	// RequireParameters routes only to providers that support every
	// parameter in the request (tools, response_format, ...).
	RequireParameters bool `json:"require_parameters,omitempty"`
	// DataCollection "deny" excludes providers that may store or train on
	// prompts.
	DataCollection string `json:"data_collection,omitempty"`
	// MaxPrice caps the price in USD per million tokens.
	MaxPrice *MaxPrice `json:"max_price,omitempty"`
}

// MaxPrice is the per-million-token price cap of ProviderPrefs.
type MaxPrice struct {
	Prompt     float64 `json:"prompt,omitempty"`
	Completion float64 `json:"completion,omitempty"`
}

type providerKey struct{}

// WithProvider returns a context whose chat calls (Stream, Complete and
// everything built on them: agent runs, voice turns, Transcribe) use p instead
// of the client's Options.Provider. The prefs replace the client's, they are
// not merged. Nil p falls back to the client's prefs.
func WithProvider(ctx context.Context, p *ProviderPrefs) context.Context {
	return context.WithValue(ctx, providerKey{}, p)
}

// providerPrefs resolves the routing block for one call: the context
// override, else the client default.
func (c *Client) providerPrefs(ctx context.Context) *ProviderPrefs {
	if p, _ := ctx.Value(providerKey{}).(*ProviderPrefs); p != nil {
		return p
	}
	return c.provider
}

// prefsFromOrder is the ProviderOrder shorthand: one entry pins the provider
// (no fallbacks), several form a fallback chain.
func prefsFromOrder(order []string) *ProviderPrefs {
	if len(order) == 0 {
		return nil
	}
	fallbacks := len(order) > 1
	return &ProviderPrefs{Order: append([]string(nil), order...), AllowFallbacks: &fallbacks}
}
