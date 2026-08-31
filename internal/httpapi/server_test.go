package httpapi_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
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
	return newServerWith(t, script, nil)
}

// newServerNoInvites is a deployment where nobody configured OBA_INVITE_CODES —
// the state a careless install lands in, and the one sign-up must refuse.
func newServerNoInvites(t *testing.T) *httptest.Server {
	return newServerWith(t, llm.Script{Turns: []llm.ScriptedTurn{{Text: "ok"}}},
		func(c *config.Config) { c.InviteCodes = nil })
}

func newServerWith(t *testing.T, script llm.Script, tweak func(*config.Config)) *httptest.Server {
	return newServerTweaking(t, script, tweak, nil)
}

// newServerTweaking also allows reaching the assembled Server, for the parts
// that are wired in at startup rather than configured — the speech provider is
// the first of them.
func newServerTweaking(t *testing.T, script llm.Script, tweak func(*config.Config),
	tweakSrv func(*httpapi.Server)) *httptest.Server {
	t.Helper()
	c, err := corpus.Load("../../testdata/corpus")
	if err != nil {
		t.Fatalf("corpus: %v", err)
	}
	cfg := config.Config{
		AgentModel: "test", Effort: "high", MaxTokens: 4096,
		MaxIterations: 6, MaxToolCalls: 8, MaxWallClock: 20 * time.Second,
		MaxOutputTokens: 50000, KAnonymityFloor: 5, CorpusDir: "../../data",
		ReplyLanguage: "zh-CN",
		// Sign-up is closed unless codes are configured, so the tests configure
		// one. That default is itself under test in TestSignUpIsClosedWithoutInviteCodes.
		InviteCodes: []string{"let-me-in"},
		SignInTTL:   time.Hour,
	}
	if tweak != nil {
		tweak(&cfg)
	}
	st := store.New("", slog.New(slog.NewTextHandler(io.Discard, nil)))
	ag := &agent.Agent{
		Cfg: cfg, LLM: llm.NewScripted(script), Store: st, Corpus: c,
		Index: retrieval.NewIndex(c), Tools: tools.Default(),
	}
	srv := &httpapi.Server{Agent: ag, Store: st, Cfg: cfg, Web: os.DirFS("../../web/static"), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	// TLS, not plain http: see signedIn for why the Secure cookie flag makes
	// this the honest harness rather than a fussy one.
	if tweakSrv != nil {
		tweakSrv(srv)
	}
	ts := httptest.NewTLSServer(srv.Routes())
	t.Cleanup(ts.Close)
	return ts
}

func postAs(t *testing.T, c *http.Client, url string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		t.Fatalf("build post %s: %v", url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	return res
}

func getAs(t *testing.T, c *http.Client, url string) *http.Response {
	t.Helper()
	res, err := c.Get(url)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	return res
}

// anon is a client that trusts the test server's certificate and holds no
// sign-in. It is what a stranger with a URL has.
func anon(t *testing.T, srv *httptest.Server) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	// A FRESH client each time, borrowing only the transport that trusts the
	// test certificate. srv.Client() hands back the same *http.Client on every
	// call, so assigning a jar to it makes every "separate" identity in a test
	// the same browser — which silently turns an isolation test into a test of
	// one account against itself.
	return &http.Client{Transport: srv.Client().Transport, Jar: jar}
}

// signedIn returns a client holding a valid sign-in for a fresh account.
//
// The test server speaks TLS on purpose. The sign-in cookie is marked Secure
// unconditionally — production terminates TLS at the ingress, so deciding the
// flag from r.TLS would quietly drop it exactly where it matters — and a cookie
// jar will not return a Secure cookie over http. Testing over TLS means the
// flag is exercised as shipped rather than worked around.
func signedIn(t *testing.T, srv *httptest.Server, username string) *http.Client {
	t.Helper()
	c := anon(t, srv)
	// An address is required of new accounts — it is the only route back into
	// one whose password is forgotten. Derived from the username so every test
	// account has its own and no two collide, which is itself enforced.
	res := postAs(t, c, srv.URL+"/api/auth/signup", map[string]string{
		"username": username, "password": "a passphrase worth typing", "invite_code": "let-me-in",
		"email": store.NormaliseUsername(username) + "@example.test",
	})
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("sign up %s: %d %s", username, res.StatusCode, b)
	}
	return c
}

func TestConversationStreamsAndTracks(t *testing.T) {
	srv := newServer(t, llm.Script{Turns: []llm.ScriptedTurn{
		{ToolCalls: []struct {
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}{{Name: "opportunity_search", Input: json.RawMessage(`{"query":"养老 护理 白班","city":"成都"}`)}}},
		// Recording the step is part of the flow this test streams, not an extra:
		// individual_pathway's next_step_is_tracked verifier requires a turn that
		// found a named programme to leave a record of the step it hands over.
		// Without this the draft is sent back for a redraft, the script runs out,
		// and the stream carries no final event - which is what this test caught.
		{ToolCalls: []struct {
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}{{Name: "case_task_create", Input: json.RawMessage(`{"domain":"employment","title":"Ask the Qingyang day centre about the day-shift care post (job-002)","owner":"resident","linked_ref":"job-002","channel_phone":"028-5550-2244","channel_window":"12 Shudu Ave, Qingyang","channel_hours":"Mon-Fri 09:00-17:00"}`)}}},
		{Text: "job-002 fits. Call 028-5550-2244, or the Qingyang window at 12 Shudu Ave, Mon-Fri 09:00-17:00."},
	}})
	defer srv.Close()
	c := signedIn(t, srv, "streamer")

	res := postAs(t, c, srv.URL+"/api/sessions", map[string]string{"role": "resident", "locale": "en"})
	var ses store.Session
	if err := json.NewDecoder(res.Body).Decode(&ses); err != nil {
		t.Fatalf("session: %v", err)
	}

	res = postAs(t, c, srv.URL+"/api/sessions/"+ses.ID+"/messages",
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

	c := signedIn(t, srv, "erroring")

	res := postAs(t, c, srv.URL+"/api/sessions", map[string]string{"role": "wizard"})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", res.StatusCode)
	}
	var e struct{ Code, Message, Remedy string }
	_ = json.NewDecoder(res.Body).Decode(&e)
	if e.Code != "ROLE_INVALID" || e.Remedy == "" {
		t.Errorf("an error without a remedy leaves the caller stuck: %+v", e)
	}

	res = postAs(t, c, srv.URL+"/api/sessions/ses_nope/messages", map[string]string{"message": "hi"})
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status %d for an unknown session", res.StatusCode)
	}
}

func TestConsentAndForgetAreReachableWithoutTheAgent(t *testing.T) {
	// A person must be able to grant, withdraw and delete without having to ask
	// the agent nicely. These are rights, not features of a conversation.
	srv := newServer(t, llm.Script{Turns: []llm.ScriptedTurn{{Text: "x"}}})
	defer srv.Close()

	c := signedIn(t, srv, "consenter")

	res := postAs(t, c, srv.URL+"/api/sessions", map[string]string{"role": "resident"})
	var ses store.Session
	_ = json.NewDecoder(res.Body).Decode(&ses)

	res = postAs(t, c, srv.URL+"/api/consent", map[string]any{
		"session_id": ses.ID, "scope": "store_profile", "granted": true,
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("consent status %d", res.StatusCode)
	}
	res = postAs(t, c, srv.URL+"/api/consent", map[string]any{
		"session_id": ses.ID, "scope": "read_everything", "granted": true,
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("an invented consent scope was accepted (status %d)", res.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/sessions/"+ses.ID+"/profile", nil)
	del, err := c.Do(req)
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

	c := signedIn(t, srv, "localiser")

	res := postAs(t, c, srv.URL+"/api/sessions", map[string]string{"role": "resident"})
	var ses store.Session
	_ = json.NewDecoder(res.Body).Decode(&ses)
	if ses.Locale != "zh-CN" {
		t.Fatalf("a session with no locale did not take the deployment default: %q", ses.Locale)
	}

	res = postAs(t, c, srv.URL+"/api/sessions/"+ses.ID+"/messages",
		map[string]string{"message": "hello", "intent": "individual_pathway", "locale": "en"})
	_, _ = io.Copy(io.Discard, res.Body)

	res = getAs(t, c, srv.URL+"/api/sessions/"+ses.ID)
	var detail struct{ Session store.Session }
	_ = json.NewDecoder(res.Body).Decode(&detail)
	if detail.Session.Locale != "en" {
		t.Errorf("the locale sent with the message did not stick: %q", detail.Session.Locale)
	}

	res = postAs(t, c, srv.URL+"/api/sessions/"+ses.ID+"/messages",
		map[string]string{"message": "hello", "locale": "klingon"})
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("an unsupported answer language was accepted (status %d)", res.StatusCode)
	}
}

func TestMetaDeclaresTheLimitsUpFront(t *testing.T) {
	srv := newServer(t, llm.Script{Turns: []llm.ScriptedTurn{{Text: "x"}}})
	defer srv.Close()
	res := getAs(t, signedIn(t, srv, "reader"), srv.URL+"/api/meta")
	var m map[string]any
	_ = json.NewDecoder(res.Body).Decode(&m)
	for _, k := range []string{"cities_covered", "corpus_is_sample", "k_anonymity_floor", "max_tool_calls"} {
		if _, ok := m[k]; !ok {
			t.Errorf("meta does not declare %q, so a person discovers the limit by hitting it", k)
		}
	}
	// The harness loads the FIXTURE corpus, which is still the invented one, so
	// true is the right answer here — and it is derived from that data rather
	// than declared. A literal kept the 「演示语料」 badge over the five real
	// national schemes once the invented records left the product.
	// See docs/bugfix/2026-08-31-the-invented-corpus-left-the-product.md
	if m["corpus_is_sample"] != true {
		t.Error("the interface is not told the fixture corpus contains invented records")
	}

	// The landing page states how large the corpus is, and it used to state it
	// as a number typed into the copy: it said 21 while the answer was 26. The
	// count now has one producer, and this is the join between the producer and
	// the page. See docs/bugfix/2026-08-31-honest-limits-were-not-honest.md
	c, err := corpus.Load("../../testdata/corpus")
	if err != nil {
		t.Fatalf("corpus: %v", err)
	}
	for _, want := range []struct {
		key string
		n   int
	}{
		{"corpus_opportunities", len(c.Opportunities)},
		{"corpus_knowledge_docs", len(c.Docs)},
	} {
		got, ok := m[want.key].(float64)
		if !ok {
			t.Errorf("meta does not report %q, so the page has no source for the tally and would fall "+
				"back to a number somebody typed", want.key)
			continue
		}
		if int(got) != want.n {
			t.Errorf("meta reports %s=%d, corpus holds %d", want.key, int(got), want.n)
		}
	}
}

// The landing page states these facts, and its readers are not signed in.
//
// They were first served from /api/meta, which is behind the gate: an anonymous
// reader got a 401 and the two sentences under "honest limits" never appeared —
// silently, on the one section of the page whose job is to be accurate. They now
// come from /api/health, which is already open, and this holds that open.
// See docs/bugfix/2026-08-31-honest-limits-were-not-honest.md
func TestDeploymentFactsAreReadableWithoutSigningIn(t *testing.T) {
	srv := newServer(t, llm.Script{Turns: []llm.ScriptedTurn{{Text: "x"}}})
	defer srv.Close()

	// srv.Client() trusts the test certificate and carries no cookie jar, so
	// this is a stranger with no sign-in.
	res, err := srv.Client().Get(srv.URL + "/api/health")
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("an anonymous reader got %d from /api/health; the landing page cannot state anything", res.StatusCode)
	}
	var m map[string]any
	_ = json.NewDecoder(res.Body).Decode(&m)

	c, err := corpus.Load("../../testdata/corpus")
	if err != nil {
		t.Fatalf("corpus: %v", err)
	}
	for _, want := range []struct {
		key string
		n   int
	}{
		{"corpus_opportunities", len(c.Opportunities)},
		{"corpus_knowledge_docs", len(c.Docs)},
	} {
		got, ok := m[want.key].(float64)
		if !ok {
			t.Errorf("/api/health does not report %q; the landing page would fall back to a number "+
				"somebody typed, which is how it came to say 21 when the answer was 26", want.key)
			continue
		}
		if int(got) != want.n {
			t.Errorf("health reports %s=%d, corpus holds %d", want.key, int(got), want.n)
		}
	}
	// The landing page's privacy sentence branches on this. A deployment that
	// does not report it is one whose page cannot tell the reader whether their
	// answer text is sent to a speech vendor.
	// See docs/bugfix/2026-08-31-the-privacy-claim-was-false.md
	if _, ok := m["speech_vendor_enabled"].(bool); !ok {
		t.Error("/api/health does not report speech_vendor_enabled; the page could not say whether " +
			"pressing read-aloud sends the answer text to a third party")
	}
	if _, ok := m["live_search_enabled"].(bool); !ok {
		t.Error("/api/health does not report live_search_enabled; the page could not say whether " +
			"THIS deployment has the nationwide lookup, only that the product needs it configured")
	}
	// And nothing personal rode along with them.
	for _, forbidden := range []string{"sessions", "subject_id", "username", "accounts"} {
		if _, present := m[forbidden]; present {
			t.Errorf("/api/health carries %q; it is an unauthenticated endpoint", forbidden)
		}
	}
}

// ---------------------------------------------------------------------------
// Fences for docs/bugfix/2026-08-28-data-exposure-no-ownership-checks.md
//
// Before these, this service had no authentication of any kind. Anyone could
// list every visitor's conversation, read a stranger's transcript, profile and
// consents, continue their conversation on the deployment's model budget, delete
// their profile, or change their consents — and session ids are sequential, so
// nothing had to be guessed.

// stranger holds an account of their own. That matters: a test where the
// attacker is merely anonymous cannot tell "the gate works" apart from "the
// ownership check works", and it is the second one that survives a sign-up form.
func TestOneAccountCannotReachAnother(t *testing.T) {
	srv := newServer(t, llm.Script{Turns: []llm.ScriptedTurn{{Text: "ok"}, {Text: "ok"}}})
	defer srv.Close()

	owner := signedIn(t, srv, "owner")
	stranger := signedIn(t, srv, "stranger")

	res := postAs(t, owner, srv.URL+"/api/sessions", map[string]string{"role": "resident"})
	var ses store.Session
	if err := json.NewDecoder(res.Body).Decode(&ses); err != nil {
		t.Fatalf("session: %v", err)
	}
	// The owner has to actually say something. A conversation with no user turn
	// is hidden from everybody by SessionSummaries, which would make the
	// "does not see it in the list" check below pass whether or not the list is
	// scoped — a fence that cannot fail.
	said := postAs(t, owner, srv.URL+"/api/sessions/"+ses.ID+"/messages",
		map[string]string{"message": "私事，不该被别人看到", "intent": "individual_pathway"})
	_, _ = io.Copy(io.Discard, said.Body)
	said.Body.Close()

	// 404 rather than 403 throughout: 403 confirms the id exists, and with
	// sequential ids that is most of what an enumerator is after.
	t.Run("cannot read it", func(t *testing.T) {
		res := getAs(t, stranger, srv.URL+"/api/sessions/"+ses.ID)
		defer res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("status %d: a stranger read the transcript, profile and consents", res.StatusCode)
		}
	})
	t.Run("cannot continue it", func(t *testing.T) {
		res := postAs(t, stranger, srv.URL+"/api/sessions/"+ses.ID+"/messages",
			map[string]string{"message": "hi", "intent": "individual_pathway"})
		defer res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("status %d: a stranger spent this deployment's model budget on somebody else's conversation", res.StatusCode)
		}
	})
	t.Run("cannot delete its profile", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/sessions/"+ses.ID+"/profile", nil)
		res, err := stranger.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("status %d: a stranger deleted somebody else's profile", res.StatusCode)
		}
	})
	t.Run("cannot change its consents", func(t *testing.T) {
		res := postAs(t, stranger, srv.URL+"/api/consent", map[string]any{
			"session_id": ses.ID, "scope": "store_profile", "granted": true,
		})
		defer res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("status %d: a stranger granted a permission on somebody else's record", res.StatusCode)
		}
	})
	t.Run("does not see it in the list", func(t *testing.T) {
		res := getAs(t, stranger, srv.URL+"/api/sessions")
		defer res.Body.Close()
		var rows []map[string]any
		_ = json.NewDecoder(res.Body).Decode(&rows)
		for _, r := range rows {
			if r["id"] == ses.ID {
				t.Errorf("the conversation picker listed somebody else's conversation: %v", r["title"])
			}
		}
		// And confirm the owner CAN see it, so an always-empty list is not
		// mistaken for isolation.
		mine := getAs(t, owner, srv.URL+"/api/sessions")
		defer mine.Body.Close()
		var ours []map[string]any
		_ = json.NewDecoder(mine.Body).Decode(&ours)
		found := false
		for _, r := range ours {
			if r["id"] == ses.ID {
				found = true
			}
		}
		if !found {
			t.Error("the owner cannot see their own conversation; this test would pass on an always-empty list")
		}
	})
	t.Run("the owner still can", func(t *testing.T) {
		res := getAs(t, owner, srv.URL+"/api/sessions/"+ses.ID)
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("status %d: the ownership check locked out the owner", res.StatusCode)
		}
	})
}

// Without a sign-in there is no reading and no spending. Health stays open
// because the Kubernetes probes hit it; a gated health check is a pod that
// never becomes ready.
func TestNothingIsReachableWithoutSigningIn(t *testing.T) {
	srv := newServer(t, llm.Script{Turns: []llm.ScriptedTurn{{Text: "ok"}}})
	defer srv.Close()
	c := anon(t, srv)

	for _, path := range []string{"/api/sessions", "/api/sessions/ses_0001", "/api/meta", "/api/intents"} {
		res := getAs(t, c, srv.URL+path)
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s answered %d to a caller with no account", path, res.StatusCode)
		}
	}
	res := postAs(t, c, srv.URL+"/api/sessions", map[string]string{"role": "resident"})
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST /api/sessions answered %d to a caller with no account", res.StatusCode)
	}
	health := getAs(t, c, srv.URL+"/api/health")
	health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Errorf("health answered %d; the readiness probe would never pass", health.StatusCode)
	}
}

// The subject is the key every profile, task and consent hangs off. If the
// request body could name it, the ownership checks above would be decoration.
func TestCreateSessionIgnoresASpoofedSubject(t *testing.T) {
	srv := newServer(t, llm.Script{Turns: []llm.ScriptedTurn{{Text: "ok"}}})
	defer srv.Close()

	victim := signedIn(t, srv, "victim")
	res := getAs(t, victim, srv.URL+"/api/auth/me")
	var me struct {
		SubjectID string `json:"subject_id"`
	}
	_ = json.NewDecoder(res.Body).Decode(&me)
	res.Body.Close()
	if me.SubjectID == "" {
		t.Fatal("no subject on the signed-in account")
	}

	thief := signedIn(t, srv, "thief")
	res = postAs(t, thief, srv.URL+"/api/sessions",
		map[string]string{"role": "resident", "subject_id": me.SubjectID})
	defer res.Body.Close()
	var ses store.Session
	_ = json.NewDecoder(res.Body).Decode(&ses)
	if ses.SubjectID == me.SubjectID {
		t.Fatal("a session was opened onto somebody else's record by naming it in the request body")
	}
}

func TestSignUpNeedsAValidInviteCode(t *testing.T) {
	srv := newServer(t, llm.Script{Turns: []llm.ScriptedTurn{{Text: "ok"}}})
	defer srv.Close()
	c := anon(t, srv)

	for name, code := range map[string]string{"missing": "", "wrong": "open-sesame"} {
		t.Run(name, func(t *testing.T) {
			res := postAs(t, c, srv.URL+"/api/auth/signup", map[string]string{
				"username": "gatecrasher-" + name, "password": "a passphrase worth typing", "invite_code": code,
			})
			defer res.Body.Close()
			if res.StatusCode != http.StatusForbidden {
				t.Errorf("status %d: an account was created with a %s invite code", res.StatusCode, name)
			}
		})
	}
}

// An unconfigured deployment must refuse new accounts, not admit everybody.
// The failure this guards against is a public sign-up form attached to a paid
// model key, arrived at by forgetting a setting.
func TestSignUpIsClosedWithoutInviteCodes(t *testing.T) {
	srv := newServer(t, llm.Script{Turns: []llm.ScriptedTurn{{Text: "ok"}}})
	defer srv.Close()
	srvNoCodes := newServerNoInvites(t)
	defer srvNoCodes.Close()

	res := postAs(t, anon(t, srvNoCodes), srvNoCodes.URL+"/api/auth/signup", map[string]string{
		"username": "anybody", "password": "a passphrase worth typing", "invite_code": "anything",
	})
	defer res.Body.Close()
	var e struct{ Code string }
	_ = json.NewDecoder(res.Body).Decode(&e)
	if res.StatusCode != http.StatusForbidden || e.Code != "SIGNUP_CLOSED" {
		// Matching the REASON, not just the status: an unconfigured deployment
		// also refuses with INVITE_INVALID as a side effect of having no codes
		// to match, so a status-only assertion passes even when the
		// closed-by-default rule has been deleted.
		t.Errorf("status %d code %q: sign-up is not explicitly closed on a deployment with no invite codes",
			res.StatusCode, e.Code)
	}
}

func TestWrongPasswordIsRefusedAndSaysNothingMore(t *testing.T) {
	srv := newServer(t, llm.Script{Turns: []llm.ScriptedTurn{{Text: "ok"}}})
	defer srv.Close()
	_ = signedIn(t, srv, "realperson")
	c := anon(t, srv)

	wrong := postAs(t, c, srv.URL+"/api/auth/signin",
		map[string]string{"username": "realperson", "password": "not the passphrase"})
	defer wrong.Body.Close()
	if wrong.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d for a wrong password", wrong.StatusCode)
	}
	var withAccount, withoutAccount struct{ Code, Message string }
	_ = json.NewDecoder(wrong.Body).Decode(&withAccount)

	unknown := postAs(t, c, srv.URL+"/api/auth/signin",
		map[string]string{"username": "nobody-at-all", "password": "not the passphrase"})
	defer unknown.Body.Close()
	_ = json.NewDecoder(unknown.Body).Decode(&withoutAccount)

	// Identical answers on purpose. A different code or message for "no such
	// account" is a free way to ask whether somebody has an account on a
	// service about unemployment and benefits.
	if withAccount != withoutAccount {
		t.Errorf("the response distinguishes a real account from an unknown one:\n  known:   %+v\n  unknown: %+v",
			withAccount, withoutAccount)
	}
}

func TestSignInCookieCarriesItsProtections(t *testing.T) {
	srv := newServer(t, llm.Script{Turns: []llm.ScriptedTurn{{Text: "ok"}}})
	defer srv.Close()

	res := postAs(t, anon(t, srv), srv.URL+"/api/auth/signup", map[string]string{
		"username": "flagcheck", "password": "a passphrase worth typing", "invite_code": "let-me-in",
		"email": "flagcheck@example.test",
	})
	defer res.Body.Close()
	var found *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == "oba_signin" {
			found = c
		}
	}
	if found == nil {
		t.Fatal("signing up set no sign-in cookie")
	}
	if !found.HttpOnly {
		t.Error("the cookie is readable from JavaScript; one XSS is then one stolen sign-in")
	}
	if !found.Secure {
		t.Error("the cookie may travel over plain http")
	}
	if found.SameSite != http.SameSiteLaxMode && found.SameSite != http.SameSiteStrictMode {
		t.Error("SameSite is not set; it is this application's whole CSRF defence — see auth.go")
	}
}

func TestSignOutRevokesServerSide(t *testing.T) {
	srv := newServer(t, llm.Script{Turns: []llm.ScriptedTurn{{Text: "ok"}}})
	defer srv.Close()

	// Keep the raw cookie. Checking that the SAME BROWSER is signed out proves
	// nothing: the response clears the cookie, so the jar stops sending it and
	// the 401 arrives whether or not the server revoked anything. The failure
	// worth catching is a cookie that was copied elsewhere before sign-out and
	// still works after it.
	fresh := anon(t, srv)
	up := postAs(t, fresh, srv.URL+"/api/auth/signup", map[string]string{
		"username": "leaver", "password": "a passphrase worth typing", "invite_code": "let-me-in",
		"email": "leaver@example.test",
	})
	up.Body.Close()
	var stolen *http.Cookie
	for _, c := range up.Cookies() {
		if c.Name == "oba_signin" {
			stolen = c
		}
	}
	if stolen == nil {
		t.Fatal("no sign-in cookie to replay")
	}

	out := postAs(t, fresh, srv.URL+"/api/auth/signout", map[string]string{})
	out.Body.Close()

	replay, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/auth/me", nil)
	replay.AddCookie(&http.Cookie{Name: stolen.Name, Value: stolen.Value})
	res, err := (&http.Client{Transport: srv.Client().Transport}).Do(replay)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("status %d: the cookie still works after sign-out, so signing out only cleared one browser", res.StatusCode)
	}
}

// The front door and the app are two different documents, and both are served
// without signing in.
//
// `/` used to BE the app, so a stranger who followed a link arrived at an
// unlabelled password box. It is the landing page now and the app moved to
// `/app` — which has no file of its own name, so it is a named route rather
// than something the file server finds. Rename `app.html` and that route 500s
// while `/` keeps working, which is a failure nobody would notice from the
// front page. Both must also stay reachable signed-out: a landing page behind
// a sign-in is a landing page nobody arriving can read.
// One honest limit: dropping only the bare "GET /app" pattern does NOT turn
// this red, because Go's mux then redirects /app to the /app/ subtree pattern
// and the client follows it. What it does catch is the app shell being renamed
// (500), both patterns going away (404), and `/` drifting back to the app.
// See docs/14-interface.md, "The landing page".
func TestTheLandingPageAndTheAppAreBothServedSignedOut(t *testing.T) {
	ts := newServer(t, llm.Script{})

	for _, tc := range []struct{ path, wants, what string }{
		{"/", `class="hero"`, "the landing page"},
		{"/app", `id="gate"`, "the conversational shell"},
		{"/app/", `id="gate"`, "the conversational shell (trailing slash)"},
	} {
		res, err := ts.Client().Get(ts.URL + tc.path)
		if err != nil {
			t.Fatalf("get %s: %v", tc.path, err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("%s: status %d, want 200 — %s is not being served", tc.path, res.StatusCode, tc.what)
			continue
		}
		if !strings.Contains(string(body), tc.wants) {
			t.Errorf("%s did not serve %s (looked for %q)", tc.path, tc.what, tc.wants)
		}
	}
}
