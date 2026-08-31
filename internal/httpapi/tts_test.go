package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/httpapi"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/tts"
)

type fakeTTS struct {
	audio []byte
	err   error
	calls int
	got   string
}

func (f *fakeTTS) Name() string { return "fake" }
func (f *fakeTTS) Speak(_ context.Context, text string) (tts.Speech, error) {
	f.calls++
	f.got = text
	if f.err != nil {
		return tts.Speech{}, f.err
	}
	return tts.Speech{Audio: f.audio, ContentType: "audio/mpeg"}, nil
}

func ttsServer(t *testing.T, p tts.Provider) *httptest.Server {
	t.Helper()
	return newServerTweaking(t, llm.Script{Turns: []llm.ScriptedTurn{{Text: "ok"}}}, nil,
		func(s *httpapi.Server) { s.TTS = p })
}

// The endpoint spends a vendor's budget on every call, so it must be behind the
// same sign-in as everything else. Routes() wraps the whole mux in the gate,
// which is what makes this true by default — this is the fence that says so.
func TestSpeakRequiresASignIn(t *testing.T) {
	fake := &fakeTTS{audio: []byte("audio")}
	srv := ttsServer(t, fake)
	res := postAs(t, anon(t, srv), srv.URL+"/api/tts", map[string]string{"text": "你好"})
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401: an unauthenticated caller can spend the speech budget", res.StatusCode)
	}
	if fake.calls != 0 {
		t.Errorf("the vendor was called %d times for an anonymous request", fake.calls)
	}
}

// readAloudSession opens a session and settles the read-aloud permission on it.
//
// Every test below has to say which way it went, because "the vendor was called"
// and "the vendor was called with permission" are different claims and only the
// second one is allowed. See docs/bugfix/2026-08-31-read-aloud-needs-consent.md
func readAloudSession(t *testing.T, srv *httptest.Server, c *http.Client, granted bool) string {
	t.Helper()
	res := postAs(t, c, srv.URL+"/api/sessions", map[string]string{"role": "resident", "locale": "en"})
	defer res.Body.Close()
	var ses struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(res.Body).Decode(&ses); err != nil || ses.ID == "" {
		t.Fatalf("could not open a session: %v", err)
	}
	if granted {
		r := postAs(t, c, srv.URL+"/api/consent", map[string]any{
			"session_id": ses.ID, "scope": "read_aloud_via_vendor", "granted": true,
		})
		defer r.Body.Close()
		if r.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(r.Body)
			t.Fatalf("granting read_aloud_via_vendor answered %d: %s — the scope is not accepted by the API",
				r.StatusCode, b)
		}
	}
	return ses.ID
}

// Read-aloud sends the answer text out of this process. It must not do that
// until the person has said it may.
//
// The disclosure on the landing page came first and was not enough: this service
// asks permission merely to STORE the person's city and situation, and was
// posting the same sentences to an outside vendor with no question asked.
// See docs/bugfix/2026-08-31-read-aloud-needs-consent.md
func TestSpeakRefusesUntilThePersonHasAgreed(t *testing.T) {
	fake := &fakeTTS{audio: []byte("ID3fake")}
	srv := ttsServer(t, fake)
	c := signedIn(t, srv, "listener")
	sid := readAloudSession(t, srv, c, false)

	res := postAs(t, c, srv.URL+"/api/tts", map[string]any{"text": "成都的失业保险金", "session_id": sid})
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", res.StatusCode)
	}
	if fake.calls != 0 {
		t.Errorf("the vendor was called %d times without permission; the text has already left", fake.calls)
	}
	var body struct {
		Code    string `json:"code"`
		Consent struct {
			Scope     string `json:"scope"`
			Plain     string `json:"plain"`
			Retention string `json:"retention"`
		} `json:"consent"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("refusal was not JSON: %v", err)
	}
	if body.Code != "CONSENT_REQUIRED" {
		t.Errorf("code = %q; the interface keys the permission card off this", body.Code)
	}
	if body.Consent.Scope != "read_aloud_via_vendor" || body.Consent.Plain == "" {
		t.Errorf("the refusal carries no question to put to the person: %+v", body.Consent)
	}
	// The stakes differ by backbone, and the person is entitled to the one that
	// applies to the deployment they are actually using.
	if !strings.Contains(body.Consent.Retention, "improve its own models") {
		t.Errorf("the free backbone's training clause is not in what the person is asked to agree to: %q",
			body.Consent.Retention)
	}
}

// Refusing must not break read-aloud — the browser reads it instead — and it
// must not be a one-off question either: granting it later has to work.
func TestSpeakProceedsOnceGranted(t *testing.T) {
	fake := &fakeTTS{audio: []byte("ID3fake")}
	srv := ttsServer(t, fake)
	c := signedIn(t, srv, "listener")
	sid := readAloudSession(t, srv, c, true)

	res := postAs(t, c, srv.URL+"/api/tts", map[string]any{"text": "成都的失业保险金", "session_id": sid})
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status = %d after granting: %s", res.StatusCode, b)
	}
	if fake.got != "成都的失业保险金" {
		t.Errorf("vendor received %q", fake.got)
	}
}

// A withdrawal has to stop it. "You can withdraw this at any time" is a promise
// that is worth nothing if the next press still sends the text.
func TestWithdrawingReadAloudConsentStopsTheVendor(t *testing.T) {
	fake := &fakeTTS{audio: []byte("ID3fake")}
	srv := ttsServer(t, fake)
	c := signedIn(t, srv, "listener")
	sid := readAloudSession(t, srv, c, true)

	r := postAs(t, c, srv.URL+"/api/consent", map[string]any{
		"session_id": sid, "scope": "read_aloud_via_vendor", "granted": false,
	})
	r.Body.Close()

	before := fake.calls
	res := postAs(t, c, srv.URL+"/api/tts", map[string]any{"text": "你好", "session_id": sid})
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d after withdrawal, want 403", res.StatusCode)
	}
	if fake.calls != before {
		t.Errorf("the vendor was called after the permission was withdrawn")
	}
}

func TestSpeakReturnsAudio(t *testing.T) {
	fake := &fakeTTS{audio: []byte("ID3fake")}
	srv := ttsServer(t, fake)
	c := signedIn(t, srv, "listener")
	sid := readAloudSession(t, srv, c, true)
	res := postAs(t, c, srv.URL+"/api/tts",
		map[string]any{"text": "成都的失业保险金", "session_id": sid})
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status = %d: %s", res.StatusCode, b)
	}
	if ct := res.Header.Get("Content-Type"); ct != "audio/mpeg" {
		t.Errorf("Content-Type = %q", ct)
	}
	// The audio renders one person's answer, naming their city and situation.
	// It must not be storable in a shared cache.
	if cc := res.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	if fake.got != "成都的失业保险金" {
		t.Errorf("vendor received %q", fake.got)
	}
}

// A deployment with no speech vendor must answer 503, not 404 and not 500.
// The browser keys its "stop asking" behaviour off exactly this status, so an
// unkeyed deployment makes one call rather than one per answer forever.
func TestSpeakSaysDisabledRatherThanMissing(t *testing.T) {
	srv := ttsServer(t, nil)
	res := postAs(t, signedIn(t, srv, "listener"), srv.URL+"/api/tts",
		map[string]string{"text": "你好"})
	defer res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 so the browser stops asking", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "TTS_DISABLED") {
		t.Errorf("body = %s, want the TTS_DISABLED code", body)
	}
}

// A vendor failure is a 502 and nothing else changes. The answer is already on
// the reader's screen; this is a side channel degrading, not a turn breaking.
func TestSpeakReportsVendorFailureWithoutBreakingTheTurn(t *testing.T) {
	fake := &fakeTTS{err: errors.New("TTS_REFUSED: fish answered 402: insufficient credit")}
	srv := ttsServer(t, fake)
	c := signedIn(t, srv, "listener")
	sid := readAloudSession(t, srv, c, true)
	res := postAs(t, c, srv.URL+"/api/tts", map[string]any{"text": "你好", "session_id": sid})
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	// The vendor's own reason has to reach whoever is reading the response, or
	// "read-aloud is broken" and "the credit ran out" look identical.
	if !strings.Contains(string(body), "insufficient credit") {
		t.Errorf("body = %s, want the vendor's reason", body)
	}
}

func TestSpeakRefusesEmptyText(t *testing.T) {
	fake := &fakeTTS{audio: []byte("audio")}
	srv := ttsServer(t, fake)
	res := postAs(t, signedIn(t, srv, "listener"), srv.URL+"/api/tts",
		map[string]string{"text": "   "})
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", res.StatusCode)
	}
	if fake.calls != 0 {
		t.Errorf("the vendor was billed for empty text (%d calls)", fake.calls)
	}
}
