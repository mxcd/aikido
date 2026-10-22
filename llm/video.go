package llm

import "context"

// VideoRequest is one clip from a provider's video-generation endpoint.
// Every optional field stays off the wire when empty: video providers reject
// fields they do not know (Grok refuses generate_audio, Veo refuses a 5 s
// duration) instead of ignoring them, so the caller sends only what the model
// lists as supported.
type VideoRequest struct {
	Model  string
	Prompt string

	// AspectRatio is "W:H" from the model's supported list. Models that take
	// the ratio from the first frame list none; leave it empty then.
	AspectRatio string
	// Resolution is a tier such as "720p" or "1080p", not pixels.
	Resolution string
	// Size is an explicit "WIDTHxHEIGHT", for models that list sizes instead
	// of resolution tiers. Set Size OR AspectRatio+Resolution.
	Size string
	// Duration in seconds. The model's list of supported lengths is closed.
	Duration int
	// GenerateAudio is sent only when non-nil: models without an audio
	// option refuse the field. Models with audio default to true.
	GenerateAudio *bool
	// Seed is sent only when non-nil; not every provider honours it.
	Seed *int

	// FirstFrame and LastFrame anchor the clip (image-to-video). Most models
	// take a first frame; only some take a last one and interpolate (Kling,
	// Seedance). Only URL is read when set, else Data is sent as a data URI.
	FirstFrame *ImagePart
	LastFrame  *ImagePart
	// References guide identity and style without anchoring a frame. The
	// provider ignores them when frames are set.
	References []ImagePart
}

// VideoResponse is the finished clip.
type VideoResponse struct {
	Video []byte
	// ContentType is sniffed from the bytes ("video/mp4"); providers do not
	// report a reliable one.
	ContentType string
	// JobID identifies the provider job, e.g. to re-download the clip.
	JobID string
	// Usage carries CostUSD (0 when the provider reported none) and the
	// wall-clock Latency/Duration of the whole job; it has no tokens.
	Usage *Usage
}

// VideoGenerator is implemented by clients that speak a video-generation
// endpoint. It is an optional capability, not part of Client: callers
// type-assert on it.
//
// Generation is a job: the implementation submits, polls until the job
// settles and downloads the clip, so a call blocks for minutes and the caller
// bounds it with ctx. Implementations map provider errors onto the same Err*
// sentinels Complete uses; a refused prompt or frame is ErrContentFiltered.
type VideoGenerator interface {
	GenerateVideo(ctx context.Context, req VideoRequest) (VideoResponse, error)
}
