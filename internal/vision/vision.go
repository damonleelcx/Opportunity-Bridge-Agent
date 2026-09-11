// Package vision asks a model about a picture, and refuses to report what a
// model that could not see the picture said about it.
//
// # WHY EVERY READING IS CHECKED FOR HAVING BEEN SEEN
//
// A model that cannot read images does not say so. On the token-plan host,
// probed on 2026-09-11 with an image whose colours nobody would guess (orange,
// black, cyan, magenta):
//
//	qwen3.8-flash   with image 99 input tokens, without 46   "orange, black, cyan, magenta"
//	glm-5.2         with image 27 input tokens, without 27   "Red, Blue, Green, Yellow"
//	deepseek-v4-pro with image 27 input tokens, without 27   "Red, White, Black, Blue"
//
// The last two answered HTTP 200, dropped the image, and invented a plausible
// answer. Pointed at a screenshot of somebody's contacts, that is a list of
// people who do not exist, delivered with the confidence of a real reading.
//
// A table of proven models (llm.QwenVisionModels, enforced at startup) keeps
// the wrong id out of configuration. It cannot catch a provider that quietly
// stops accepting images for an id that used to, so every reading also carries
// its own evidence: the same question is asked once WITHOUT the image, and the
// reading is used only if the image added input tokens.
package vision

import (
	"context"
	"errors"
	"fmt"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
)

// MinImageTokens is how many input tokens the image must add over the same
// question without it before a reading is believed.
//
// Why 16: a model that dropped the image adds exactly 0 (27 against 27 on both
// blind models above). The smallest addition measured from a model that saw was
// 53, for a 320x80 swatch; a real screenshot is thousands of times the area. The
// floor sits far from both, so neither rounding nor a small picture can make a
// blind model look sighted or a sighted one look blind.
const MinImageTokens = 16

var (
	// ErrModelBlind means the model answered without having counted the image.
	ErrModelBlind = errors.New("VISION_MODEL_BLIND")
	// ErrTruncated means the reading was cut off by the output ceiling.
	ErrTruncated = errors.New("VISION_READING_TRUNCATED")
	// ErrEmptyReading means the model returned no text at all.
	ErrEmptyReading = errors.New("VISION_READING_EMPTY")
	// ErrUnsupportedImage means the bytes are not a picture this path accepts.
	ErrUnsupportedImage = errors.New("VISION_IMAGE_UNSUPPORTED")
)

// MediaTypes are the pictures a reading accepts. A table rather than a check on
// the "image/" prefix: an SVG or a TIFF is an image too, and neither is
// something the vendor documents or this build has read.
var MediaTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/webp": true,
}

// Question is what to ask about the picture.
type Question struct {
	Text string
	// JSON asks for a JSON object back. The text must then contain the word
	// "json", which Qwen requires for that mode.
	JSON      bool
	MaxTokens int64
}

// Result is one reading and the evidence that it came from the picture.
type Result struct {
	Text string
	// ImageTokens is how many input tokens the picture added over the same
	// question asked without it.
	ImageTokens int64
	// Usage is the usage of the call that carried the picture.
	Usage llm.Usage
}

// Reader asks questions about pictures through one model.
type Reader struct {
	LLM   llm.Client
	Model string
}

// Read asks q about the picture. It makes two calls: the question alone, which
// costs one output token and exists only to count the question's own input
// tokens, then the question with the picture. Both carry identical settings, so
// the only thing that can differ between their input counts is the picture.
func (r Reader) Read(ctx context.Context, mediaType string, img []byte, q Question) (Result, error) {
	if !MediaTypes[mediaType] {
		return Result{}, fmt.Errorf("%w: %q is not a picture this import reads. Upload a PNG, JPEG or WebP screenshot",
			ErrUnsupportedImage, mediaType)
	}
	if len(img) == 0 {
		return Result{}, fmt.Errorf("%w: the picture is empty", ErrUnsupportedImage)
	}

	baseline, err := r.LLM.Stream(ctx, llm.Request{
		Model: r.Model, MaxTokens: 1, JSON: q.JSON,
		Messages: []llm.Message{llm.UserText(q.Text)},
	}, nil)
	if err != nil {
		return Result{}, err
	}
	seen, err := r.LLM.Stream(ctx, llm.Request{
		Model: r.Model, MaxTokens: q.MaxTokens, JSON: q.JSON,
		Messages: []llm.Message{{Role: llm.RoleUser, Blocks: []llm.Block{
			// The picture first, then the question about it: the order the
			// vendor documents and the order the 2026-09-11 spike measured.
			llm.Image(mediaType, img), llm.Text(q.Text),
		}}},
	}, nil)
	if err != nil {
		return Result{}, err
	}

	added := seen.Usage.InputTokens - baseline.Usage.InputTokens
	if added < MinImageTokens {
		return Result{}, fmt.Errorf("%w: %s counted %d input tokens for the picture (%d with it, %d for the same "+
			"question without it), so it answered without seeing it. A model that cannot see answers anyway and "+
			"invents what the picture showed, so nothing it said is used. Set OBA_VISION_MODEL to a model in "+
			"llm.QwenVisionModels and restart",
			ErrModelBlind, r.Model, added, seen.Usage.InputTokens, baseline.Usage.InputTokens)
	}
	if seen.StopReason == "max_tokens" {
		return Result{}, fmt.Errorf("%w: the picture holds more than one reading can return (%d output tokens). "+
			"Crop the screenshot into two or three parts and upload them one at a time",
			ErrTruncated, seen.Usage.OutputTokens)
	}
	text := seen.TextContent()
	if text == "" {
		return Result{}, fmt.Errorf("%w: %s returned no text for the picture; upload it again, and if this "+
			"repeats the vendor is degraded", ErrEmptyReading, r.Model)
	}
	return Result{Text: text, ImageTokens: added, Usage: seen.Usage}, nil
}
