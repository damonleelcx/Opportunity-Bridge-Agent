package httpapi_test

import (
	"context"
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

func TestSpeakReturnsAudio(t *testing.T) {
	fake := &fakeTTS{audio: []byte("ID3fake")}
	srv := ttsServer(t, fake)
	res := postAs(t, signedIn(t, srv, "listener"), srv.URL+"/api/tts",
		map[string]string{"text": "成都的失业保险金"})
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
	res := postAs(t, signedIn(t, srv, "listener"), srv.URL+"/api/tts",
		map[string]string{"text": "你好"})
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
