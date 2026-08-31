package tts_test

// These run against a stand-in HTTP server rather than against Fish, because
// the point is the wire contract this product depends on — which header carries
// the backbone, which body field carries the voice — and that has to hold
// whether or not a key is available to the person running the suite.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/tts"
)

func fishServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

// The backbone travels in a HEADER and the voice in the BODY. Swapping them is
// accepted by the API and silently ignored, which would mean either the wrong
// voice or — for the model — billing against the paid backbone while the
// configuration says the free one. Neither failure is visible at runtime, so it
// is asserted here.
func TestFishSendsTheVoiceInTheBodyAndTheModelInTheHeader(t *testing.T) {
	var gotModel, gotAuth string
	var body map[string]any
	srv := fishServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotModel = r.Header.Get("model")
		gotAuth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write([]byte("ID3fake-audio"))
	})

	f := tts.NewFish(srv.URL, "key-123", "voice-abc", "", nil)
	got, err := f.Speak(context.Background(), "成都的失业保险金")
	if err != nil {
		t.Fatalf("Speak: %v", err)
	}
	if gotAuth != "Bearer key-123" {
		t.Errorf("Authorization = %q, want bearer key", gotAuth)
	}
	if gotModel != tts.DefaultFishModel {
		t.Errorf("model header = %q, want the free default %q", gotModel, tts.DefaultFishModel)
	}
	if body["reference_id"] != "voice-abc" {
		t.Errorf("reference_id = %v, want the configured voice", body["reference_id"])
	}
	if body["normalize"] != true {
		t.Error("normalize is not on: 12333 and 09:00-16:30 get read out digit by digit")
	}
	if string(got.Audio) != "ID3fake-audio" || got.ContentType != "audio/mpeg" {
		t.Errorf("got %q %q", got.Audio, got.ContentType)
	}
}

// The default must be the model that does not bill. A deployment that sets a key
// and thinks no further must not start spending.
func TestFishDefaultsToTheFreeBackbone(t *testing.T) {
	if tts.DefaultFishModel != "s2.1-pro-free" {
		t.Errorf("default model is %q; the default must be the free backbone", tts.DefaultFishModel)
	}
	f := tts.NewFish("", "k", "v", "", nil)
	if f.Model != tts.DefaultFishModel {
		t.Errorf("empty model resolved to %q, want %q", f.Model, tts.DefaultFishModel)
	}
	if f.Endpoint != tts.DefaultFishEndpoint {
		t.Errorf("empty endpoint resolved to %q", f.Endpoint)
	}
	// An explicit choice still wins; this is a default, not a lock.
	if p := tts.NewFish("", "k", "v", "s2.1-pro", nil); p.Model != "s2.1-pro" {
		t.Errorf("explicit model was overridden with %q", p.Model)
	}
}

// A 200 with an empty body is the failure that would otherwise reach the person
// as silence, which is indistinguishable from read-aloud they never switched on.
func TestFishTreatsEmptyAudioAsAFailure(t *testing.T) {
	srv := fishServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(http.StatusOK) // no body
	})
	_, err := tts.NewFish(srv.URL, "k", "v", "", nil).Speak(context.Background(), "hello")
	if err == nil {
		t.Fatal("empty audio was accepted; the reader would hear nothing and never be told")
	}
	if !strings.Contains(err.Error(), "TTS_EMPTY_AUDIO") {
		t.Errorf("error = %v, want TTS_EMPTY_AUDIO", err)
	}
}

// The vendor's own reason has to survive to the log. "Read-aloud is broken" and
// "the key expired" need different actions from whoever is on call.
func TestFishPassesTheVendorsReasonThrough(t *testing.T) {
	srv := fishServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		io.WriteString(w, `{"status":402,"message":"insufficient credit"}`)
	})
	_, err := tts.NewFish(srv.URL, "k", "v", "", nil).Speak(context.Background(), "hello")
	if err == nil {
		t.Fatal("a 402 was treated as success")
	}
	if !strings.Contains(err.Error(), "insufficient credit") || !strings.Contains(err.Error(), "402") {
		t.Errorf("error = %v, want the vendor's status and message", err)
	}
}

// Over-long text is truncated, not refused: half an answer read aloud beats
// silence. What must not happen is it being refused, or truncated without a word.
func TestFishTruncatesRatherThanRefusingLongText(t *testing.T) {
	var sent string
	srv := fishServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		sent, _ = body["text"].(string)
		w.Write([]byte("audio"))
	})
	long := strings.Repeat("补", tts.MaxChars+500)
	if _, err := tts.NewFish(srv.URL, "k", "v", "", nil).Speak(context.Background(), long); err != nil {
		t.Fatalf("long text was refused: %v", err)
	}
	if n := len([]rune(sent)); n != tts.MaxChars {
		t.Errorf("sent %d chars, want the cap of %d", n, tts.MaxChars)
	}
}

func TestFishRefusesEmptyText(t *testing.T) {
	srv := fishServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("the vendor was called for empty text; that is a billed request for nothing")
	})
	if _, err := tts.NewFish(srv.URL, "k", "v", "", nil).Speak(context.Background(), "   "); err == nil {
		t.Fatal("empty text was accepted")
	}
}

// An unrecognised backbone must be reported as one that trains.
//
// This table decides a sentence telling a person whether the answer they are
// about to have read aloud may be trained on. Over-warning costs a reader some
// caution they did not need; under-warning costs them something they cannot take
// back. See docs/bugfix/2026-08-31-the-privacy-claim-was-false.md
func TestUnknownBackboneIsAssumedToTrain(t *testing.T) {
	for _, c := range []struct {
		model string
		train bool
		why   string
	}{
		{"s2.1-pro-free", true, "the free backbone's terms permit it"},
		{"", true, "empty resolves to the free default, exactly as NewFish resolves it"},
		{"s2.1-pro", false, "the paid backbone's terms do not"},
		{"s2-pro", false, "likewise"},
		{"s1", true, "terms not checked, so it warns"},
		{"s3-whatever-ships-next", true, "a backbone nobody has checked must not be assumed safe"},
		{"S2.1-PRO", true, "matching is exact; a near-miss must not silently read as paid"},
	} {
		if got := tts.TrainsOnRequests(c.model); got != c.train {
			t.Errorf("TrainsOnRequests(%q) = %v, want %v — %s", c.model, got, c.train, c.why)
		}
	}
}
