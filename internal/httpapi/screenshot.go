package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/leadgraph"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/vision"
)

// ---- importing a screenshot of a mind map ----
//
// WHY THE PICTURE IS SENT ONLY ON A CONFIRMATION CARRIED BY THE UPLOAD ITSELF
//
//	A screenshot of somebody's contacts holds other people's names, titles,
//	ages and salaries, and reading it means sending it to a model vendor. This
//	service asks permission merely to STORE a person's own facts, so sending a
//	picture of strangers on disclosure alone was never going to be enough.
//	The owner decided (2026-09-11): confirm on every upload. The interface asks
//	before it posts, but the interface protects nobody - this route is reachable
//	by anything holding a sign-in - so the confirmation travels in the request
//	and is checked here, before a byte of the picture leaves this process.
//	Same stance as read-aloud; see tts.go.
//
// WHAT IS LOGGED
//
//	That a picture was confirmed, read and staged, and how long it took: never
//	the picture, never the reading. The reading is people's names.

const (
	// screenshotConsentField and screenshotConsentValue are the confirmation an
	// upload carries. A value, not a boolean, so that an unrelated field set to
	// "true" by some other form can never stand in for it.
	screenshotConsentField = "vendor_consent"
	screenshotConsentValue = "screenshot_recognition"

	// screenshotReadTimeout bounds one reading. qwen3.7-plus took about 65s on a
	// 31-topic map; this allows a larger picture and a slow vendor, and past it
	// the person is told, rather than left looking at a spinner.
	screenshotReadTimeout = 150 * time.Second
)

// pictureType reports whether an upload is a picture, and which kind, from its
// bytes. The file name is not trusted: a screenshot saved as .csv is a picture,
// and a CSV named .png is not.
func pictureType(raw []byte) (string, bool) {
	mt := http.DetectContentType(raw)
	return mt, strings.HasPrefix(mt, "image/")
}

// readingRefusals says what each way a reading can fail means for the person
// holding the screenshot. A table rather than a chain of branches, so a new
// failure is a row and cannot fall through to a generic 500.
var readingRefusals = []struct {
	err    error
	status int
	remedy string
}{
	{vision.ErrUnsupportedImage, http.StatusUnsupportedMediaType,
		"Upload the screenshot as PNG, JPEG or WebP."},
	{vision.ErrModelBlind, http.StatusBadGateway,
		"This is a deployment setting, not something about your screenshot: the configured model did not see " +
			"the picture. Ask the operator to set OBA_VISION_MODEL to a model in llm.QwenVisionModels."},
	{vision.ErrTruncated, http.StatusUnprocessableEntity,
		"Crop the screenshot into two or three parts and upload them one at a time."},
	{vision.ErrEmptyReading, http.StatusBadGateway,
		"Upload it again. If it keeps happening, the vision service is degraded; import a CSV meanwhile."},
	{leadgraph.ErrTranscriptInvalid, http.StatusUnprocessableEntity,
		"Upload it again. If it keeps failing, crop it into smaller parts."},
}

// stageScreenshot reads a screenshot with the vision model and stages what it
// read for review. Like a spreadsheet upload, it writes nothing to the graph.
func (s *Server) stageScreenshot(w http.ResponseWriter, r *http.Request, sessionID string, v leadgraph.View, fileName, mediaType string, raw []byte) {
	if !vision.MediaTypes[mediaType] {
		writeErr(w, http.StatusUnsupportedMediaType, "SCREENSHOT_TYPE_UNSUPPORTED",
			fmt.Sprintf("This is a %s picture, which the screenshot import does not read.", mediaType),
			"Upload the screenshot as PNG, JPEG or WebP.")
		return
	}
	if s.Vision == nil {
		writeErr(w, http.StatusServiceUnavailable, "SCREENSHOT_IMPORT_UNAVAILABLE",
			"This deployment has no model that can read a picture, so a screenshot cannot be imported.",
			"Import the list as a CSV instead, or ask the operator to run with OBA_BACKEND=qwen, where "+
				"OBA_VISION_MODEL chooses the model that reads screenshots.")
		return
	}
	if r.FormValue(screenshotConsentField) != screenshotConsentValue {
		s.Log.InfoContext(r.Context(), "screenshot refused: sending it was not confirmed", "code", "CONSENT_REQUIRED")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPreconditionFailed)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": "CONSENT_REQUIRED",
			"message": "Reading a screenshot means sending it, with every name and detail in it, to the model " +
				"vendor named below. Nothing was sent.",
			"remedy":  "Confirm that the screenshot may be sent, or import the list as a CSV instead.",
			"consent": s.screenshotFacts(),
		})
		return
	}

	s.Log.InfoContext(r.Context(), "screenshot confirmed for reading", "code", "SCREENSHOT_CONSENT_GIVEN",
		"media_type", mediaType, "bytes", len(raw), "model", s.Vision.Model)
	ctx, cancel := context.WithTimeout(r.Context(), screenshotReadTimeout)
	defer cancel()
	started := time.Now()
	res, err := s.Vision.Read(ctx, mediaType, raw, vision.Question{
		Text: leadgraph.TranscriptPrompt, JSON: true, MaxTokens: leadgraph.TranscriptMaxTokens,
	})
	var sess leadgraph.ImportSession
	if err == nil {
		sess, err = s.Agent.Graph.StageScreenshot(v, fileName, raw, res.Text)
	}
	if err != nil {
		s.refuseReading(w, r, ctx, err, time.Since(started))
		return
	}
	s.Log.InfoContext(r.Context(), "screenshot read and staged", "code", "SCREENSHOT_STAGED",
		"people", len(sess.Plan.Proposals), "held", len(sess.Plan.Held), "links", len(sess.Plan.Links),
		"image_tokens", res.ImageTokens, "output_tokens", res.Usage.OutputTokens,
		"seconds", time.Since(started).Round(time.Second).Seconds())
	sum, _ := s.Agent.Graph.ImportSummary(v, sess.ID)
	// Only on success, as for a spreadsheet: a refused reading leaves nothing.
	// See docs/bugfix/2026-09-11-upload-only-conversation-was-hidden.md
	s.recordUpload(r.Context(), sessionID, fileName, sum)
	writeJSON(w, map[string]any{"staged": true, "summary": sum})
}

func (s *Server) refuseReading(w http.ResponseWriter, r *http.Request, ctx context.Context, err error, took time.Duration) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		s.Log.WarnContext(r.Context(), "screenshot reading timed out", "code", "VISION_TIMEOUT",
			"seconds", took.Round(time.Second).Seconds())
		writeErr(w, http.StatusGatewayTimeout, "VISION_TIMEOUT",
			fmt.Sprintf("Reading the screenshot took longer than %s, so it was stopped. Nothing was staged.", screenshotReadTimeout),
			"Crop the screenshot into smaller parts and upload them one at a time.")
		return
	}
	for _, ref := range readingRefusals {
		if errors.Is(err, ref.err) {
			s.Log.WarnContext(r.Context(), "screenshot could not be read", "code", ref.err.Error(), "err", err.Error())
			writeErr(w, ref.status, ref.err.Error(), err.Error()+" Nothing was staged.", ref.remedy)
			return
		}
	}
	s.Log.WarnContext(r.Context(), "screenshot reading failed", "code", "VISION_UNAVAILABLE", "err", err.Error())
	writeErr(w, http.StatusBadGateway, "VISION_UNAVAILABLE", err.Error()+" Nothing was staged.",
		"Upload it again in a minute. If it keeps failing, import the list as a CSV meanwhile.")
}

// screenshotFacts is what the confirmation shows about where a screenshot goes.
//
// Derived from configuration and served, never written into the page. A
// privacy statement in page copy is true on the machine of whoever wrote it and
// false wherever the deployment differs; that has already happened here once.
// See docs/bugfix/2026-08-31-the-privacy-claim-was-false.md
func (s *Server) screenshotFacts() map[string]any {
	if s.Vision == nil {
		return map[string]any{"screenshot_import_enabled": false}
	}
	base := s.Cfg.QwenBaseURL
	if base == "" {
		base = llm.DefaultQwenBaseURL
	}
	host := base
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		host = u.Host
	}
	return map[string]any{
		"screenshot_import_enabled": true,
		"vision_vendor":             s.Vision.LLM.Name(),
		"vision_model":              s.Vision.Model,
		"vision_endpoint_host":      host,
	}
}
