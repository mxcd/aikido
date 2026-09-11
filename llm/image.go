package llm

import "context"

// ImageRequest is one call to a provider's dedicated images endpoint.
//
// Unlike Request, an ImageRequest has no message list: the images endpoints
// take a single prompt and no system role. Empty fields are omitted from the
// wire body, leaving the provider default per-field.
type ImageRequest struct {
	Model  string
	Prompt string

	// AspectRatio takes the same values as ImageConfig.AspectRatio. OpenAI
	// images-API models accept 1:1, 3:2, 2:3, 4:3, 3:4, 16:9, 9:16, 21:9 and
	// "auto"; 4:5 is rejected with a 400.
	AspectRatio string

	// ImageSize is the resolution tier: "1K", "2K" or "4K", same values as
	// ImageConfig.ImageSize. Sent as the endpoint's `resolution` field.
	ImageSize string

	// Size is an explicit "WIDTHxHEIGHT" in pixels, sent as the endpoint's
	// `size` field. It is the way to get a frame the ratio list does not
	// offer: OpenAI rejects aspect_ratio 4:5 but renders size 1024x1280, and
	// renders 3:4 as 1024x1536 while 1024x1365 comes back exact. OpenAI wants
	// both sides divisible by 16 and the longest edge at most 3840. Set Size
	// OR AspectRatio+ImageSize, not both: the provider refuses the mix.
	Size string

	// Quality is "auto", "low", "medium", "high", "xhigh" or "max". Empty
	// leaves the provider default (auto). On OpenAI models "max" costs about
	// 16x auto.
	Quality string

	// References are input images the model edits or takes as guidance. Only
	// URL is read: it must be a data: URI or an https URL. Data is ignored.
	References []ImagePart
}

// ImageResponse is the fully-assembled result of an images-endpoint call.
// Images carries inline bytes when the provider returned base64, or a URL
// when it returned a link. Usage is nil when the provider reported none.
type ImageResponse struct {
	Images []ImagePart
	Usage  *Usage
}

// ImageGenerator is implemented by clients that speak a dedicated images
// endpoint in addition to chat completions. It is an optional capability, not
// part of Client: callers type-assert and fall back to Complete with
// Modalities: []string{"image", "text"} when the assertion fails.
//
// Implementations map provider errors onto the same Err* sentinels Complete
// uses (ErrAuth, ErrRateLimited, ErrServerError, ErrInvalidRequest,
// ErrContentFiltered).
type ImageGenerator interface {
	GenerateImage(ctx context.Context, req ImageRequest) (ImageResponse, error)
}
