package httpapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/httpapi"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/leadgraph"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/vision"
)

// Fences over the screenshot upload. The vendor is a server speaking Qwen's wire
// format that replays a reading recorded from the live model; the input-token
// counts differ with and without the picture the way the live service's do.

const shotFixtures = "../leadgraph/testdata/screenshot/"

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(shotFixtures + name)
	if err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return b
}

// visionVendor replays reading, counting every call it receives.
func visionVendor(t *testing.T, withoutPicture, withPicture int, reading string) (*llm.QwenClient, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		b, _ := io.ReadAll(r.Body)
		tokens := withoutPicture
		if bytes.Contains(b, []byte(`"image_url"`)) {
			tokens = withPicture
		}
		content, _ := json.Marshal(reading)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%s},\"finish_reason\":\"stop\"}]}\n\n", content)
		fmt.Fprintf(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":%d,\"completion_tokens\":3756}}\n\n", tokens)
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return llm.NewQwen("qw-test-key", srv.URL), &calls
}

// screenshotServer is graphServer with a vision model behind it.
func screenshotServer(t *testing.T, withoutPicture, withPicture int) (*httptest.Server, *leadgraph.Store, *int32) {
	t.Helper()
	client, calls := visionVendor(t, withoutPicture, withPicture, string(fixture(t, "reading-qwen3.7-plus.json")))
	graph := leadgraph.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := newServerTweaking(t, llm.Script{}, nil, func(s *httpapi.Server) {
		s.Agent.Graph = graph
		s.Vision = &vision.Reader{LLM: client, Model: "qwen3.7-plus"}
	})
	return ts, graph, calls
}

func uploadPicture(t *testing.T, c *http.Client, url, name string, body []byte, confirmed bool) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if confirmed {
		_ = w.WriteField("vendor_consent", "screenshot_recognition")
	}
	f, err := w.CreateFormFile("file", name)
	if err != nil {
		t.Fatalf("form: %v", err)
	}
	_, _ = f.Write(body)
	_ = w.Close()
	req, _ := http.NewRequest(http.MethodPost, url, &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	res, err := c.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	return res
}

type apiReply struct {
	Code    string                  `json:"code"`
	Message string                  `json:"message"`
	Remedy  string                  `json:"remedy"`
	Consent map[string]any          `json:"consent"`
	Staged  bool                    `json:"staged"`
	Summary leadgraph.ImportSummary `json:"summary"`
}

func decodeReply(t *testing.T, res *http.Response) apiReply {
	t.Helper()
	defer res.Body.Close()
	var out apiReply
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// The picture holds other people's names and details. Without the confirmation
// in the request, not one call reaches the vendor - whatever the interface did.
func TestAScreenshotIsNotSentWithoutConfirmation(t *testing.T) {
	ts, graph, calls := screenshotServer(t, 600, 1871)
	c := signedIn(t, ts, "amy-recruiter")
	ses, seat := recruiterSession(t, c, ts, domain.RoleRecruiter)

	res := uploadPicture(t, c, ts.URL+"/api/sessions/"+ses+"/graph/imports", "导图.png", fixture(t, "mindmap.png"), false)
	if res.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("status %d, want 412", res.StatusCode)
	}
	got := decodeReply(t, res)
	if got.Code != "CONSENT_REQUIRED" || got.Remedy == "" {
		t.Errorf("refusal = %+v, want CONSENT_REQUIRED with a remedy", got)
	}
	// The confirmation says where the picture would go, from configuration.
	if got.Consent["vision_model"] != "qwen3.7-plus" || got.Consent["vision_endpoint_host"] == "" {
		t.Errorf("the refusal does not say where the picture would go: %v", got.Consent)
	}
	if n := atomic.LoadInt32(calls); n != 0 {
		t.Errorf("the vendor received %d calls for a picture nobody confirmed sending", n)
	}
	if _, staged := graph.ImportSummary(seat, ""); staged {
		t.Error("an unconfirmed picture was staged")
	}
}

func TestAConfirmedScreenshotIsReadAndStaged(t *testing.T) {
	ts, graph, calls := screenshotServer(t, 600, 1871)
	c := signedIn(t, ts, "amy-recruiter")
	ses, seat := recruiterSession(t, c, ts, domain.RoleRecruiter)

	res := uploadPicture(t, c, ts.URL+"/api/sessions/"+ses+"/graph/imports", "导图.png", fixture(t, "mindmap.png"), true)
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d: %s", res.StatusCode, b)
	}
	got := decodeReply(t, res)
	if !got.Staged || got.Summary.Source != leadgraph.SourceScreenshot || len(got.Summary.People) != 23 || len(got.Summary.Held) != 2 {
		t.Fatalf("summary: staged=%v source=%q people=%d held=%d, want true, screenshot, 23, 2",
			got.Staged, got.Summary.Source, len(got.Summary.People), len(got.Summary.Held))
	}
	// One call for the question alone, one with the picture. No more.
	if n := atomic.LoadInt32(calls); n != 2 {
		t.Errorf("the vendor received %d calls, want 2", n)
	}
	if n := len(graph.Nodes(seat, leadgraph.NodeFilter{})); n != 0 {
		t.Errorf("staging a screenshot wrote %d records", n)
	}

	// A correction re-plans from the staged reading: the vendor is not asked again.
	chen := 0
	for _, h := range got.Summary.Held {
		if strings.Contains(h.Text, "陈望舒") {
			chen = h.Row
		}
	}
	if _, err := graph.RestageImport(seat, got.Summary.ID, leadgraph.ImportOverrides{Include: []int{chen}}); err != nil {
		t.Fatalf("restage: %v", err)
	}
	if n := atomic.LoadInt32(calls); n != 2 {
		t.Errorf("a correction called the vendor again (%d calls): it must re-plan from the staged reading", n)
	}
}

func TestABlindModelCannotStageAScreenshot(t *testing.T) {
	ts, graph, _ := screenshotServer(t, 27, 27)
	c := signedIn(t, ts, "amy-recruiter")
	ses, seat := recruiterSession(t, c, ts, domain.RoleRecruiter)

	res := uploadPicture(t, c, ts.URL+"/api/sessions/"+ses+"/graph/imports", "导图.png", fixture(t, "mindmap.png"), true)
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", res.StatusCode)
	}
	if got := decodeReply(t, res); got.Code != "VISION_MODEL_BLIND" || !strings.Contains(got.Remedy, "OBA_VISION_MODEL") {
		t.Errorf("refusal = %+v, want VISION_MODEL_BLIND naming the setting", got)
	}
	if _, staged := graph.ImportSummary(seat, ""); staged {
		t.Error("a reading from a model that did not see the picture was staged")
	}
}

func TestScreenshotImportIsOffWithoutAVisionModel(t *testing.T) {
	ts, _, _ := graphServer(t)
	c := signedIn(t, ts, "amy-recruiter")
	ses, _ := recruiterSession(t, c, ts, domain.RoleRecruiter)

	res := uploadPicture(t, c, ts.URL+"/api/sessions/"+ses+"/graph/imports", "导图.png", fixture(t, "mindmap.png"), true)
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", res.StatusCode)
	}
	if got := decodeReply(t, res); got.Code != "SCREENSHOT_IMPORT_UNAVAILABLE" || got.Remedy == "" {
		t.Errorf("refusal = %+v", got)
	}
	// And the page can know before anybody picks a picture.
	health, err := c.Get(ts.URL + "/api/health")
	if err != nil {
		t.Fatal(err)
	}
	defer health.Body.Close()
	var facts map[string]any
	_ = json.NewDecoder(health.Body).Decode(&facts)
	if facts["screenshot_import_enabled"] != false {
		t.Errorf("/api/health screenshot_import_enabled = %v, want false", facts["screenshot_import_enabled"])
	}
}

func TestAPictureOfAnotherKindIsRefused(t *testing.T) {
	ts, _, calls := screenshotServer(t, 600, 1871)
	c := signedIn(t, ts, "amy-recruiter")
	ses, _ := recruiterSession(t, c, ts, domain.RoleRecruiter)

	gif := []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\xff\xff\xff\x00\x00\x00!\xf9\x04\x00\x00\x00\x00\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;")
	res := uploadPicture(t, c, ts.URL+"/api/sessions/"+ses+"/graph/imports", "导图.gif", gif, true)
	if res.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("status %d, want 415", res.StatusCode)
	}
	if got := decodeReply(t, res); got.Code != "SCREENSHOT_TYPE_UNSUPPORTED" {
		t.Errorf("refusal = %+v", got)
	}
	if n := atomic.LoadInt32(calls); n != 0 {
		t.Errorf("the vendor received %d calls for a picture type it is never sent", n)
	}
}
