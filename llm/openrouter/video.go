package openrouter

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mxcd/aikido/llm"
)

// Compile-time conformance for the optional video capability.
var _ llm.VideoGenerator = (*Client)(nil)

// GenerateVideo renders one clip through OpenRouter's video endpoints: POST
// /videos enqueues a job, GET /videos/{id} is polled every
// Options.VideoPollInterval until it settles, GET /videos/{id}/content serves
// the file under the same bearer token. It blocks for minutes; ctx bounds it.
//
// Each request retries like Complete's (429, 5xx). A refusal surfaces as
// ErrContentFiltered: a failed job or a 400 whose message reads like
// moderation, and a completed job without any output, which is how some
// providers (Veo) report a silent safety rejection.
func (c *Client) GenerateVideo(ctx context.Context, req llm.VideoRequest) (llm.VideoResponse, error) {
	body, err := buildVideoBody(req)
	if err != nil {
		return llm.VideoResponse{}, fmt.Errorf("openrouter: video: %w", err)
	}
	start := time.Now()
	raw, _, err := c.do(ctx, http.MethodPost, c.baseURL+"/videos", body, "application/json", http.StatusAccepted)
	if err != nil {
		if errors.Is(err, llm.ErrInvalidRequest) && refusal(err.Error()) {
			return llm.VideoResponse{}, fmt.Errorf("%w: %w", err, llm.ErrContentFiltered)
		}
		return llm.VideoResponse{}, err
	}
	var job videoJob
	if err := json.Unmarshal(raw, &job); err != nil || job.ID == "" {
		return llm.VideoResponse{}, fmt.Errorf("openrouter: video: submit answered without a job id: %w", llm.ErrServerError)
	}
	id := url.PathEscape(job.ID)

	job, err = c.awaitVideo(ctx, id, job)
	if err != nil {
		return llm.VideoResponse{JobID: job.ID}, err
	}
	resp := llm.VideoResponse{JobID: job.ID, Usage: &llm.Usage{}}
	if job.Usage != nil {
		resp.Usage.CostUSD = job.Usage.Cost
	}
	resp.Video, _, err = c.do(ctx, http.MethodGet, c.baseURL+"/videos/"+id+"/content?index=0", nil, "video/*", http.StatusOK)
	if err != nil {
		return resp, err
	}
	if len(resp.Video) == 0 {
		return resp, fmt.Errorf("openrouter: video: job %s: empty clip: %w", job.ID, llm.ErrServerError)
	}
	resp.ContentType = http.DetectContentType(resp.Video)
	resp.Usage.SetTiming(start, time.Time{}, time.Now())
	return resp, nil
}

// awaitVideo polls the job until it completes or fails. job is the submit
// answer, returned as is when it already settled.
func (c *Client) awaitVideo(ctx context.Context, id string, job videoJob) (videoJob, error) {
	t := time.NewTicker(c.videoPoll)
	defer t.Stop()
	for {
		switch job.Status {
		case "completed":
			if len(job.UnsignedURLs) == 0 && len(job.URLs) == 0 {
				return job, fmt.Errorf("openrouter: video: job %s completed without a clip (silent moderation refusal?): %w", job.ID, llm.ErrContentFiltered)
			}
			return job, nil
		case "failed", "cancelled", "expired":
			msg := videoErrorMessage(job.Error)
			if msg == "" {
				msg = "job " + job.Status
			}
			cause := llm.ErrServerError
			if refusal(msg) {
				cause = llm.ErrContentFiltered
			}
			return job, fmt.Errorf("openrouter: video: job %s: %s: %w", job.ID, msg, cause)
		}
		select {
		case <-ctx.Done():
			return job, ctx.Err()
		case <-t.C:
		}
		raw, _, err := c.do(ctx, http.MethodGet, c.baseURL+"/videos/"+id, nil, "application/json", http.StatusOK)
		if err != nil {
			return job, err
		}
		if err := json.Unmarshal(raw, &job); err != nil {
			return job, fmt.Errorf("openrouter: video: decode job: %w", llm.ErrServerError)
		}
	}
}

// buildVideoBody assembles the POST /videos body. Unset optional fields stay
// off the wire.
func buildVideoBody(req llm.VideoRequest) ([]byte, error) {
	if strings.TrimSpace(req.Model) == "" {
		return nil, fmt.Errorf("model is required: %w", llm.ErrInvalidRequest)
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, fmt.Errorf("prompt is required: %w", llm.ErrInvalidRequest)
	}
	if req.Size != "" && (req.AspectRatio != "" || req.Resolution != "") {
		return nil, fmt.Errorf("size and aspect ratio/resolution are mutually exclusive: %w", llm.ErrInvalidRequest)
	}
	vr := videoRequest{
		Model:         req.Model,
		Prompt:        req.Prompt,
		AspectRatio:   req.AspectRatio,
		Resolution:    req.Resolution,
		Size:          req.Size,
		Duration:      req.Duration,
		GenerateAudio: req.GenerateAudio,
		Seed:          req.Seed,
	}
	for _, f := range []struct {
		part *llm.ImagePart
		kind string
	}{{req.FirstFrame, "first_frame"}, {req.LastFrame, "last_frame"}} {
		if f.part == nil {
			continue
		}
		u, err := imageURL(*f.part)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.kind, err)
		}
		vr.FrameImages = append(vr.FrameImages, apiFrameImage{Type: "image_url", ImageURL: &apiImageURL{URL: u}, FrameType: f.kind})
	}
	for i, ref := range req.References {
		u, err := imageURL(ref)
		if err != nil {
			return nil, fmt.Errorf("reference image %d: %w", i, err)
		}
		vr.InputReferences = append(vr.InputReferences, apiImagePart{Type: "image_url", ImageURL: &apiImageURL{URL: u}})
	}
	return json.Marshal(vr)
}

// imageURL is the part's URL, or its bytes as a data URI.
func imageURL(p llm.ImagePart) (string, error) {
	if p.URL != "" {
		return p.URL, nil
	}
	if len(p.Data) == 0 {
		return "", fmt.Errorf("image has neither URL nor data: %w", llm.ErrInvalidRequest)
	}
	ct := p.ContentType
	if ct == "" {
		ct = http.DetectContentType(p.Data)
	}
	return "data:" + ct + ";base64," + base64.StdEncoding.EncodeToString(p.Data), nil
}

// videoErrorMessage flattens the job's error field: a string or an object
// with a message.
func videoErrorMessage(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var e apiError
	if json.Unmarshal(raw, &e) == nil && e.Message != "" {
		return e.Message
	}
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	return string(raw)
}

// refusal recognises a moderation refusal in a video provider's message. No
// provider sends a dedicated code, and each words it differently.
func refusal(msg string) bool {
	m := strings.ToLower(msg)
	for _, w := range []string{"moderation", "content policy", "safety", "flagged", "sensitive", "prohibited", "nsfw"} {
		if strings.Contains(m, w) {
			return true
		}
	}
	return false
}

// VideoModel is one entry of GET /videos/models: the closed lists of values a
// model accepts. Send only listed values; the endpoint rejects the rest.
type VideoModel struct {
	ID                    string   `json:"id"`
	Name                  string   `json:"name"`
	Description           string   `json:"description"`
	Created               int64    `json:"created"`
	SupportedResolutions  []string `json:"supported_resolutions"`
	SupportedAspectRatios []string `json:"supported_aspect_ratios"`
	SupportedSizes        []string `json:"supported_sizes"`
	SupportedDurations    []int    `json:"supported_durations"`
	// SupportedFrameImages lists "first_frame" and/or "last_frame".
	SupportedFrameImages []string `json:"supported_frame_images"`
	// GenerateAudio is nil when the model has no audio switch: send no
	// generate_audio then.
	GenerateAudio *bool `json:"generate_audio"`
	Seed          *bool `json:"seed"`
	// PricingSKUs maps a SKU ("duration_seconds_720p",
	// "cents_per_video_output_second_1080p", ...) to its price as sent; the
	// unit is in the key and differs per provider.
	PricingSKUs map[string]string `json:"pricing_skus"`
}

// VideoModels lists the video-generation models with their supported values.
// They are not part of GET /models.
func (c *Client) VideoModels(ctx context.Context) ([]VideoModel, error) {
	var out struct {
		Data []VideoModel `json:"data"`
	}
	if err := c.getJSON(ctx, "/videos/models", &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}
