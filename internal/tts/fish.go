package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Fish renders speech through Fish Audio, in one named voice.
//
// ── Why this vendor and this shape ───────────────────────────────────────────
//
// The voice is chosen by `reference_id`, a model id from fish.audio. That is the
// whole reason this provider exists rather than a generic one: the timbre is not
// a parameter the product picks from a list, it is a specific published voice,
// and swapping vendors means finding a different voice rather than changing a
// setting. Naming it in configuration keeps that swap to one variable.
//
// The `model` header selects the synthesis backbone and is separate from the
// voice. `s2.1-pro-free` costs nothing; `s2.1-pro` bills at $0.015 per 1,000
// UTF-8 bytes, which for Chinese is 3 bytes a character — about $0.018 for a
// typical answer. The default is the free one, on purpose: a deployment that
// forgets to think about this must not start spending money.
//
// ‼️ WHAT THE FREE MODEL COSTS INSTEAD. Fish's published terms for
// `s2.1-pro-free` say requests may be used to improve model quality. The text
// sent here is the ANSWER, which describes a named city, an employment
// situation and sometimes a benefit the person is trying to claim. That is a
// privacy trade, not a free lunch, and it is why read-aloud through this
// provider is off unless somebody sets a key on purpose. See
// docs/17-read-aloud.md before switching it on for real users.
type Fish struct {
	Endpoint string
	APIKey   string
	// VoiceID is Fish's `reference_id` — which voice, not which model.
	VoiceID string
	// Model is the synthesis backbone: s2.1-pro-free | s2.1-pro | s2-pro | s1.
	Model  string
	Client *http.Client
	Log    *slog.Logger
}

const (
	DefaultFishEndpoint = "https://api.fish.audio/v1/tts"
	// DefaultFishModel is the free backbone. See the type comment for why the
	// default is the one that does not bill.
	DefaultFishModel = "s2.1-pro-free"

	// MaxChars caps one utterance.
	//
	// The browser caps too, but this side cannot trust that: the endpoint is
	// reachable by anything holding a sign-in cookie, and an uncapped one is a
	// vendor bill (or a fair-use ban) with a public URL in front of it.
	MaxChars = 3000
)

// paidBackbones are the Fish synthesis backbones whose terms do NOT permit using
// requests to improve the vendor's models.
//
// It is an allowlist rather than a denylist, and that direction is the point.
// The one thing this table decides is a sentence on the landing page telling a
// person whether the answer they are about to have read aloud - their city,
// their unemployment, the benefit they are claiming - may be trained on. A
// backbone this table has not heard of is therefore treated as one that trains.
// Over-warning costs a reader some caution they did not need; under-warning
// costs them something they cannot take back.
//
// `s1` is deliberately absent: its terms were not checked, so it warns.
// See docs/bugfix/2026-08-31-the-privacy-claim-was-false.md
var paidBackbones = map[string]bool{
	"s2.1-pro": true,
	"s2-pro":   true,
}

// TrainsOnRequests reports whether this deployment's backbone may be trained on.
// An empty model resolves the same way NewFish resolves it, so the answer cannot
// disagree with what is actually being called.
func TrainsOnRequests(model string) bool { return !paidBackbones[resolveModel(model)] }

func resolveModel(model string) string {
	if model == "" {
		return DefaultFishModel
	}
	return model
}

func NewFish(endpoint, apiKey, voiceID, model string, log *slog.Logger) *Fish {
	if endpoint == "" {
		endpoint = DefaultFishEndpoint
	}
	model = resolveModel(model)
	return &Fish{
		Endpoint: endpoint, APIKey: apiKey, VoiceID: voiceID, Model: model,
		// A generous timeout: synthesis is roughly a fifth of real time, so a
		// long answer legitimately takes ten seconds or more. Cutting it short
		// would read as "the vendor is broken" when it is merely working.
		Client: &http.Client{Timeout: 90 * time.Second},
		Log:    log,
	}
}

func (f *Fish) Name() string { return "fish" }

// fishRequest is only the fields this product sets. The API has many more, and
// leaving them out means taking the vendor's defaults, which is what we want:
// each one added here is a number somebody has to maintain a reason for.
type fishRequest struct {
	Text        string `json:"text"`
	ReferenceID string `json:"reference_id,omitempty"`
	Format      string `json:"format"`
	MP3Bitrate  int    `json:"mp3_bitrate"`
	// Latency trades quality for time-to-first-byte. "balanced" because this is
	// somebody waiting to hear an answer they can already see.
	Latency string `json:"latency"`
	// Normalize expands numbers, dates and units into spoken words. Load-bearing
	// here: answers are full of "2000 元", "12333" and "09:00-16:30", and read
	// digit by digit those are useless to the person the feature exists for.
	Normalize bool `json:"normalize"`
}

func (f *Fish) Speak(ctx context.Context, text string) (Speech, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Speech{}, fmt.Errorf("TTS_EMPTY_TEXT: nothing to speak")
	}
	if r := []rune(text); len(r) > MaxChars {
		// Truncated rather than refused: half an answer read aloud is still
		// useful, and a hard failure would silence a long answer entirely. It is
		// logged because a silent truncation is the failure this codebase keeps
		// writing fences against.
		if f.Log != nil {
			f.Log.Warn("read-aloud text truncated before synthesis",
				"code", "TTS_TEXT_TRUNCATED", "chars", len(r), "cap", MaxChars)
		}
		text = string(r[:MaxChars])
	}
	body, err := json.Marshal(fishRequest{
		Text: text, ReferenceID: f.VoiceID, Format: "mp3",
		MP3Bitrate: 128, Latency: "balanced", Normalize: true,
	})
	if err != nil {
		return Speech{}, fmt.Errorf("TTS_REQUEST_INVALID: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.Endpoint, bytes.NewReader(body))
	if err != nil {
		return Speech{}, fmt.Errorf("TTS_REQUEST_INVALID: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+f.APIKey)
	req.Header.Set("Content-Type", "application/json")
	// The backbone travels as a HEADER, not in the body. Putting it in the body
	// is accepted and ignored, which would silently bill the paid model.
	req.Header.Set("model", f.Model)

	resp, err := f.Client.Do(req)
	if err != nil {
		return Speech{}, fmt.Errorf("TTS_UNREACHABLE: %s could not be reached: %w", f.Name(), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// The body carries the vendor's own reason. Passing it through is the
		// difference between "read-aloud is broken" and "the key expired".
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return Speech{}, fmt.Errorf("TTS_REFUSED: %s answered %d: %s",
			f.Name(), resp.StatusCode, strings.TrimSpace(string(detail)))
	}
	audio, err := io.ReadAll(resp.Body)
	if err != nil {
		return Speech{}, fmt.Errorf("TTS_TRUNCATED: reading audio from %s: %w", f.Name(), err)
	}
	if len(audio) == 0 {
		// 200 with an empty body. Called out rather than returned as valid
		// silence, because silence is exactly what a broken read-aloud sounds
		// like and it would never be reported.
		return Speech{}, fmt.Errorf("TTS_EMPTY_AUDIO: %s answered 200 with no audio", f.Name())
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "audio/mpeg"
	}
	return Speech{Audio: audio, ContentType: ct}, nil
}
