package httpapi_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/agent"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/config"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/corpus"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/httpapi"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/retrieval"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/tools"
)

func newServer(t *testing.T, script llm.Script) *httptest.Server {
	t.Helper()
	c, err := corpus.Load("../../data")
	if err != nil {
		t.Fatalf("corpus: %v", err)
	}
	cfg := config.Config{
		AgentModel: "test", Effort: "high", MaxTokens: 4096,
		MaxIterations: 6, MaxToolCalls: 8, MaxWallClock: 20 * time.Second,
		MaxOutputTokens: 50000, KAnonymityFloor: 5, CorpusDir: "../../data",
		ReplyLanguage: "zh-CN",
	}
	st := store.New("", slog.New(slog.NewTextHandler(io.Discard, nil)))
	ag := &agent.Agent{
		Cfg: cfg, LLM: llm.NewScripted(script), Store: st, Corpus: c,
		Index: retrieval.NewIndex(c), Tools: tools.Default(),
	}
	srv := &httpapi.Server{Agent: ag, Store: st, Cfg: cfg, Web: os.DirFS("../../web/static"), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return httptest.NewServer(srv.Routes())
}

func post(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	res, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	return res
}

func TestConversationStreamsAndTracks(t *testing.T) {
	srv := newServer(t, llm.Script{Turns: []llm.ScriptedTurn{
		{ToolCalls: []struct {
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}{{Name: "opportunity_search", Input: json.RawMessage(`{"query":"养老 护理 白班","city":"成都"}`)}}},
		{Text: "job-002 fits. Call 028-5550-2244, or the Qingyang window at 12 Shudu Ave, Mon-Fri 09:00-17:00."},
	}})
	defer srv.Close()

	res := post(t, srv.URL+"/api/sessions", map[string]string{"role": "resident", "locale": "en"})
	var ses store.Session
	if err := json.NewDecoder(res.Body).Decode(&ses); err != nil {
		t.Fatalf("session: %v", err)
	}

	res = post(t, srv.URL+"/api/sessions/"+ses.ID+"/messages",
		map[string]string{"message": "成都的养老护理岗", "intent": "individual_pathway"})
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content type %q, want an event stream", ct)
	}
	kinds := map[string]bool{}
	var final agent.Result
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev agent.Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			continue
		}
		kinds[string(ev.Kind)] = true
		if ev.Kind == agent.EvFinal && ev.Final != nil {
			final = *ev.Final
		}
	}
	for _, want := range []string{"routed", "tool_start", "tool_result", "text", "final", "trace"} {
		if !kinds[want] {
			t.Errorf("the stream never carried a %q event; the interface cannot show what it does not receive", want)
		}
	}
	if !strings.Contains(final.Answer, "job-002") {
		t.Errorf("final answer: %q", final.Answer)
	}
}

func TestErrorsCarryACodeAndARemedy(t *testing.T) {
	srv := newServer(t, llm.Script{Turns: []llm.ScriptedTurn{{Text: "x"}}})
	defer srv.Close()

	res := post(t, srv.URL+"/api/sessions", map[string]string{"role": "wizard"})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", res.StatusCode)
	}
	var e struct{ Code, Message, Remedy string }
	_ = json.NewDecoder(res.Body).Decode(&e)
	if e.Code != "ROLE_INVALID" || e.Remedy == "" {
		t.Errorf("an error without a remedy leaves the caller stuck: %+v", e)
	}

	res = post(t, srv.URL+"/api/sessions/ses_nope/messages", map[string]string{"message": "hi"})
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status %d for an unknown session", res.StatusCode)
	}
}

func TestConsentAndForgetAreReachableWithoutTheAgent(t *testing.T) {
	// A person must be able to grant, withdraw and delete without having to ask
	// the agent nicely. These are rights, not features of a conversation.
	srv := newServer(t, llm.Script{Turns: []llm.ScriptedTurn{{Text: "x"}}})
	defer srv.Close()

	res := post(t, srv.URL+"/api/sessions", map[string]string{"role": "resident"})
	var ses store.Session
	_ = json.NewDecoder(res.Body).Decode(&ses)

	res = post(t, srv.URL+"/api/consent", map[string]any{
		"session_id": ses.ID, "scope": "store_profile", "granted": true,
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("consent status %d", res.StatusCode)
	}
	res = post(t, srv.URL+"/api/consent", map[string]any{
		"session_id": ses.ID, "scope": "read_everything", "granted": true,
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("an invented consent scope was accepted (status %d)", res.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/sessions/"+ses.ID+"/profile", nil)
	del, err := http.DefaultClient.Do(req)
	if err != nil || del.StatusCode != http.StatusOK {
		t.Errorf("profile deletion failed: %v", err)
	}
}

// The answer language is part of the conversation, not a property of the app
// shell: choosing a language in the header has to change the next answer, not
// the next conversation.
func TestLocaleTravelsWithTheMessage(t *testing.T) {
	srv := newServer(t, llm.Script{Turns: []llm.ScriptedTurn{{Text: "ok"}, {Text: "ok"}}})
	defer srv.Close()

	res := post(t, srv.URL+"/api/sessions", map[string]string{"role": "resident"})
	var ses store.Session
	_ = json.NewDecoder(res.Body).Decode(&ses)
	if ses.Locale != "zh-CN" {
		t.Fatalf("a session with no locale did not take the deployment default: %q", ses.Locale)
	}

	res = post(t, srv.URL+"/api/sessions/"+ses.ID+"/messages",
		map[string]string{"message": "hello", "intent": "individual_pathway", "locale": "en"})
	_, _ = io.Copy(io.Discard, res.Body)

	res, err := http.Get(srv.URL + "/api/sessions/" + ses.ID)
	if err != nil {
		t.Fatal(err)
	}
	var detail struct{ Session store.Session }
	_ = json.NewDecoder(res.Body).Decode(&detail)
	if detail.Session.Locale != "en" {
		t.Errorf("the locale sent with the message did not stick: %q", detail.Session.Locale)
	}

	res = post(t, srv.URL+"/api/sessions/"+ses.ID+"/messages",
		map[string]string{"message": "hello", "locale": "klingon"})
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("an unsupported answer language was accepted (status %d)", res.StatusCode)
	}
}

func TestMetaDeclaresTheLimitsUpFront(t *testing.T) {
	srv := newServer(t, llm.Script{Turns: []llm.ScriptedTurn{{Text: "x"}}})
	defer srv.Close()
	res, err := http.Get(srv.URL + "/api/meta")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.NewDecoder(res.Body).Decode(&m)
	for _, k := range []string{"cities_covered", "corpus_is_sample", "k_anonymity_floor", "max_tool_calls"} {
		if _, ok := m[k]; !ok {
			t.Errorf("meta does not declare %q, so a person discovers the limit by hitting it", k)
		}
	}
	if m["corpus_is_sample"] != true {
		t.Error("the interface must be told the corpus is sample data")
	}
}
