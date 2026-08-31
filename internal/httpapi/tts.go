package httpapi

// Read-aloud, rendered by a speech vendor instead of by the browser.
//
// ── Why this is an endpoint on our own server ────────────────────────────────
//
// The browser cannot call the vendor directly, because that means shipping the
// API key to the browser, and a key in a public page is a key anybody can spend.
// So the request comes here, and the key stays on this side.
//
// Two consequences, both deliberate:
//
//   - It sits behind the same sign-in as everything else. Routes() wraps every
//     handler in the gate, so this is protected by default rather than by
//     somebody remembering. An endpoint that spends a vendor's budget must never
//     be reachable by a stranger with the URL.
//   - It is NOT on the answer's critical path. The answer has already streamed
//     to the reader by the time the browser asks for audio. If this fails, the
//     browser falls back to its own built-in voice; nothing about the turn
//     changes. That is why a vendor outage here is a 502 and not an incident.

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/tools"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/tts"
)

func (s *Server) speak(w http.ResponseWriter, r *http.Request) {
	if s.TTS == nil {
		// 503 rather than 404: the route exists, the feature is switched off.
		// The browser reads this as "stop asking" and uses its own voice for the
		// rest of the session, so an unkeyed deployment makes exactly one of
		// these calls rather than one per answer.
		writeErr(w, http.StatusServiceUnavailable, "TTS_DISABLED",
			"Read-aloud through a speech vendor is not configured on this deployment.",
			"The browser's own voice is used instead. To enable it, set OBA_TTS_API_KEY "+
				"and OBA_TTS_VOICE_ID and restart.")
		return
	}
	var body struct {
		Text string `json:"text"`
		// SessionID is required because the permission belongs to a person, not
		// to a request. Without it this endpoint has no subject to check, which
		// is how it came to send answers to a vendor with nobody having agreed.
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "BODY_INVALID", "The request body was not valid JSON.",
			"Send {\"text\":\"...\"}.")
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeErr(w, http.StatusBadRequest, "TTS_EMPTY_TEXT", "There was no text to read.",
			"Send the answer text to be spoken.")
		return
	}

	// ---- consent, before a byte of the answer leaves this process.
	//
	// This service asks permission merely to STORE the person's city and
	// situation. The same sentences were being posted to an outside speech
	// vendor the moment somebody pressed read-aloud, with no question asked -
	// disclosure on the landing page, but never consent. The gate is here rather
	// than in the interface because the interface is not what protects anybody:
	// this endpoint is reachable by anything holding a sign-in.
	//
	// Refusing costs the person nothing. The client falls back to the browser's
	// own voice, which is what an unkeyed deployment does anyway.
	// See docs/bugfix/2026-08-31-read-aloud-needs-consent.md
	ses, ok := s.ownedSession(w, r, body.SessionID)
	if !ok {
		return
	}
	if g := s.Store.Consent(ses.SubjectID, domain.ConsentReadAloudVendor); !g.Granted {
		prompt := tools.ConsentPromptFor(domain.ConsentReadAloudVendor)
		// What the vendor may then do with it depends on the backbone, so it is
		// derived rather than written into the prompt. See tts.TrainsOnRequests.
		if tts.TrainsOnRequests(s.Cfg.TTSModel) {
			prompt.Retention += " This deployment's speech backbone also permits the vendor to use what it " +
				"receives to improve its own models."
		} else {
			prompt.Retention += " This deployment's speech backbone does not permit the vendor to train on it."
		}
		s.Log.Info("read-aloud refused: permission not granted",
			"code", "CONSENT_REQUIRED", "scope", string(domain.ConsentReadAloudVendor))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": "CONSENT_REQUIRED",
			"message": "Reading this out through the speech service means sending the text of the answer to it, " +
				"and that needs your permission first.",
			"remedy":  "Grant \"read_aloud_via_vendor\", or say no and have it read in your own device's voice.",
			"consent": prompt,
		})
		return
	}

	speech, err := s.TTS.Speak(r.Context(), text)
	if err != nil {
		// Logged at WARN, not ERROR: the reader still has the answer on screen
		// and is about to hear it in the browser's voice. It is a degraded side
		// channel, not a broken turn — but it is logged, because a read-aloud
		// that has quietly stopped working sounds exactly like one nobody used.
		s.Log.Warn("speech synthesis failed; the browser will fall back to its own voice",
			"code", "TTS_FAILED", "provider", s.TTS.Name(), "error", err)
		writeErr(w, http.StatusBadGateway, "TTS_FAILED",
			"The speech service could not render this answer: "+err.Error(),
			"The answer is unchanged and on screen; it will be read in the browser's own voice instead.")
		return
	}

	w.Header().Set("Content-Type", speech.ContentType)
	// no-store because the audio is a rendering of one person's answer, which
	// names their city and their situation. It must not sit in a shared cache.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := w.Write(speech.Audio); err != nil {
		s.Log.Warn("audio was rendered but not delivered",
			"code", "TTS_NOT_DELIVERED", "bytes", len(speech.Audio), "error", err)
	}
}
