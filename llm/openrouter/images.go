package openrouter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/mxcd/aikido/llm"
)

// Compile-time conformance for the optional images capability.
var _ llm.ImageGenerator = (*Client)(nil)

// GenerateImage renders one prompt through OpenRouter's dedicated images
// endpoint (POST /api/v1/images).
//
// Some OpenRouter image models are served only there. openai/gpt-image-2.5-flare
// and friends never appear on chat completions and are missing from a plain
// GET /models listing. Models that accept both endpoints (the Gemini image
// family) should keep using Complete with Modalities: []string{"image", "text"},
// which is the only path that also carries a conversation.
//
// Retry and error classification are Complete's: 429 and 5xx retry per
// retryPolicy, auth and bad-request short-circuit. A 200 carrying a top-level
// error envelope that isContentFilterErrorEnvelope recognises surfaces as
// ErrContentFiltered; a non-200 is classified by status through
// classifyHTTPError, which has no content-filter case.
func (c *Client) GenerateImage(ctx context.Context, req llm.ImageRequest) (llm.ImageResponse, error) {
	body, err := buildImagesBody(req)
	if err != nil {
		return llm.ImageResponse{}, fmt.Errorf("openrouter: images: %w", err)
	}
	raw, err := c.postJSON(ctx, "/images", body)
	if err != nil {
		return llm.ImageResponse{}, err
	}
	return parseImagesResponse(raw)
}

// buildImagesBody assembles the images-endpoint JSON body. Unset optional
// fields stay off the wire. ImageSize maps onto `resolution`, never `size`:
// the endpoint's `size` field wants WxH pixels and rejects the 1K/2K/4K tiers.
func buildImagesBody(req llm.ImageRequest) ([]byte, error) {
	if strings.TrimSpace(req.Model) == "" {
		return nil, fmt.Errorf("model is required: %w", llm.ErrInvalidRequest)
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, fmt.Errorf("prompt is required: %w", llm.ErrInvalidRequest)
	}
	ir := imagesRequest{
		Model:       req.Model,
		Prompt:      req.Prompt,
		AspectRatio: req.AspectRatio,
		Resolution:  req.ImageSize,
		Quality:     req.Quality,
	}
	for i, ref := range req.References {
		if ref.URL == "" {
			return nil, fmt.Errorf("reference image %d has no URL: %w", i, llm.ErrInvalidRequest)
		}
		ir.InputReferences = append(ir.InputReferences, apiImagePart{
			Type:     "image_url",
			ImageURL: &apiImageURL{URL: ref.URL},
		})
	}
	return json.Marshal(ir)
}

// parseImagesResponse maps an images-endpoint JSON body onto llm.ImageResponse.
//
// Each datum carries either base64 bytes or a URL. Missing media_type is
// sniffed from the decoded bytes; a URL datum keeps whatever the provider
// reported. An empty data array is not an error here: the caller decides
// whether zero images is a failure.
func parseImagesResponse(body []byte) (llm.ImageResponse, error) {
	var out imagesResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return llm.ImageResponse{}, fmt.Errorf("openrouter: images: decode response: %w", llm.ErrServerError)
	}

	// A 200 may still carry a top-level error envelope, same as chat completions.
	if out.Error != nil {
		msg := out.Error.Message
		if msg == "" {
			msg = "provider error"
		}
		cause := llm.ErrServerError
		if isContentFilterErrorEnvelope(out.Error) {
			cause = llm.ErrContentFiltered
		}
		return llm.ImageResponse{}, fmt.Errorf("openrouter: images: %s: %w", msg, cause)
	}

	var resp llm.ImageResponse
	for i, d := range out.Data {
		switch {
		case d.B64JSON != "":
			data, err := base64.StdEncoding.DecodeString(d.B64JSON)
			if err != nil {
				return llm.ImageResponse{}, fmt.Errorf("openrouter: images: image %d: bad base64 payload: %w", i, llm.ErrServerError)
			}
			ct := d.MediaType
			if ct == "" {
				ct = http.DetectContentType(data)
			}
			resp.Images = append(resp.Images, llm.ImagePart{ContentType: ct, Data: data})
		case d.URL != "":
			resp.Images = append(resp.Images, llm.ImagePart{URL: d.URL, ContentType: d.MediaType})
		}
	}
	resp.Usage = toLLMUsage(out.Usage)
	return resp, nil
}
