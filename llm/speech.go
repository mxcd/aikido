package llm

import "context"

// SpeechRequest is one text-to-speech call to a provider's speech endpoint.
// Empty optional fields are omitted from the wire body, leaving the provider
// default per-field.
type SpeechRequest struct {
	Model string
	// Input is the text to speak. ElevenLabs models take up to 10,000
	// characters and read inline audio tags such as "[laughs]".
	Input string
	// Voice is the provider's voice id, e.g. "sarah" or "george" for the
	// ElevenLabs models. Some providers reject a request without one.
	Voice string
	// Format is "mp3" or "pcm" (16-bit little-endian, mono). Empty leaves the
	// provider default, which on OpenRouter is pcm.
	Format string
	// Speed is a playback multiplier. Providers without speed control ignore
	// it or reject a non-default value. Nil leaves the provider default (1.0).
	Speed *float32
}

// SpeechResponse is the synthesized audio of one Speak call.
type SpeechResponse struct {
	Audio []byte
	// ContentType is the provider's reported type: "audio/mpeg" for mp3,
	// "audio/pcm" for pcm.
	ContentType string
	// GenerationID identifies the billed generation. OpenRouter reports no
	// usage or cost on the speech response, so there is no Usage field: the
	// price is on GET /api/v1/generation?id=<GenerationID> (field "usage", in
	// USD), which answers 404 for some seconds after the call.
	GenerationID string
}

// SpeechGenerator is implemented by clients that speak a text-to-speech
// endpoint. It is an optional capability, not part of Client: callers
// type-assert on it.
//
// Implementations map provider errors onto the same Err* sentinels Complete
// uses (ErrAuth, ErrRateLimited, ErrServerError, ErrInvalidRequest).
type SpeechGenerator interface {
	Speak(ctx context.Context, req SpeechRequest) (SpeechResponse, error)
}
