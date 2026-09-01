package httpapi_test

// Fences over the two spending ceilings.
//
// What they are guarding, and why it needs guarding: sign-up needs no invite
// code, so anybody with the address can hold an account, and every turn spends
// tokens against a paid key. agent.Budget bounds one turn and nothing bounded
// the number of turns. If these go red, the model bill is unbounded again.
// See docs/bugfix/2026-09-01-per-account-and-deployment-spend-caps.md
//
// Every test here drives the REAL endpoint a person's browser posts to, with the
// scripted model backend, rather than calling the gate directly — a cap that
// works when called in isolation and is not wired into the request path is
// exactly the failure these exist to catch.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/config"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/httpapi"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
)

// spendReply is long enough that the scripted backend reports a non-zero token
// count for it: it charges len(text)/4 output tokens, so a two-character "ok"
// costs nothing and would make every assertion below vacuously true.
var spendReply = strings.Repeat("a sentence that costs something to produce. ", 12)

// spendServer builds a server with known ceilings and hands back the store, so a
// test can read the counter that was written rather than trusting a 200.
func spendServer(t *testing.T, accountCap, deploymentCap int64) (*httptest.Server, *store.Store) {
	t.Helper()
	var st *store.Store
	srv := newServerTweaking(t,
		llm.Script{Turns: []llm.ScriptedTurn{
			{Text: spendReply}, {Text: spendReply}, {Text: spendReply}, {Text: spendReply},
		}},
		func(c *config.Config) {
			c.AccountDailyTokens = accountCap
			c.DeploymentDailyTokens = deploymentCap
		},
		func(s *httpapi.Server) { st = s.Store },
	)
	return srv, st
}

// say runs one turn through the endpoint the interface posts to and returns the
// status. The body is drained because the handler streams.
func say(t *testing.T, c *http.Client, srv *httptest.Server, sessionID, msg string) (int, errBody) {
	t.Helper()
	res := postAs(t, c, srv.URL+"/api/sessions/"+sessionID+"/messages",
		map[string]string{"message": msg, "intent": "individual_pathway"})
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	var e errBody
	if res.StatusCode != http.StatusOK {
		_ = json.Unmarshal(b, &e)
	}
	return res.StatusCode, e
}

type errBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Remedy  string `json:"remedy"`
}

func newSession(t *testing.T, c *http.Client, srv *httptest.Server) store.Session {
	t.Helper()
	res := postAs(t, c, srv.URL+"/api/sessions", map[string]string{"role": "resident", "locale": "en"})
	defer res.Body.Close()
	var ses store.Session
	if err := json.NewDecoder(res.Body).Decode(&ses); err != nil {
		t.Fatalf("session: %v", err)
	}
	return ses
}

// A turn's cost is recorded against the account that ran it, and against nobody
// else. Without this the caps would count something, pass their own tests, and
// bill the wrong person — or everybody.
func TestSpendIsRecordedAgainstTheAccountThatSpentIt(t *testing.T) {
	srv, st := spendServer(t, 0, 0) // ceilings off: this is about the counting
	spender := signedIn(t, srv, "spender")
	// Exists, signed in, and never says anything: the account that must not be
	// charged for somebody else's turn.
	_ = signedIn(t, srv, "bystander")

	if code, _ := say(t, spender, srv, newSession(t, spender, srv).ID, "hello"); code != http.StatusOK {
		t.Fatalf("turn answered %d, want 200", code)
	}

	spent, deployment := st.SpentToday("spender")
	if spent <= 0 {
		t.Errorf("the turn cost %d tokens against the account: nothing was recorded, so no cap can ever bite", spent)
	}
	if deployment != spent {
		t.Errorf("deployment total %d does not match the only account that spent (%d)", deployment, spent)
	}
	if other, _ := st.SpentToday("bystander"); other != 0 {
		t.Errorf("an account that ran no turn was charged %d tokens", other)
	}
}

// An account over its allowance is refused the NEXT turn, by code and not merely
// by status. 429 is also what a rate limiter answers, so a status-only assertion
// would still pass if this refusal were coming from somewhere else entirely.
func TestAccountSpendCapRefusesTheNextTurn(t *testing.T) {
	srv, st := spendServer(t, 1, 0) // a one-token allowance: the first turn exhausts it
	c := signedIn(t, srv, "talker")
	ses := newSession(t, c, srv)

	// The first turn is ALLOWED even though it will blow past the cap. The gate
	// asks "may this start", not "will this fit" — see spendAllowed.
	if code, e := say(t, c, srv, ses.ID, "first"); code != http.StatusOK {
		t.Fatalf("the first turn was refused with %d %s; a fresh account must be able to speak", code, e.Code)
	}
	if spent, _ := st.SpentToday("talker"); spent <= 1 {
		t.Fatalf("the first turn recorded %d tokens; the rest of this test proves nothing", spent)
	}

	code, e := say(t, c, srv, ses.ID, "second")
	if code != http.StatusTooManyRequests || e.Code != "SPEND_CAP_REACHED" {
		t.Fatalf("second turn answered %d %q, want 429 SPEND_CAP_REACHED", code, e.Code)
	}
	// The refusal has to tell somebody what to do next. This one's whole job is
	// to say the account is fine and when it comes back.
	if !strings.Contains(e.Remedy, "UTC") {
		t.Errorf("the remedy does not say when the allowance returns, so it reads as a permanent ban: %q", e.Remedy)
	}
}

// The deployment ceiling refuses EVERY account, including one that has spent
// nothing. That is the whole point of the second counter: a per-account cap
// alone multiplies by however many accounts somebody registers.
func TestDeploymentCeilingRefusesEveryAccount(t *testing.T) {
	srv, st := spendServer(t, 0, 1) // no per-account cap; a one-token service ceiling
	first := signedIn(t, srv, "first-through")
	if code, e := say(t, first, srv, newSession(t, first, srv).ID, "hello"); code != http.StatusOK {
		t.Fatalf("the first turn was refused with %d %s", code, e.Code)
	}
	if _, deployment := st.SpentToday("first-through"); deployment <= 1 {
		t.Fatalf("the deployment total is %d; the ceiling was never crossed", deployment)
	}

	// A different account, which has spent nothing at all.
	second := signedIn(t, srv, "innocent")
	code, e := say(t, second, srv, newSession(t, second, srv).ID, "hello")
	if code != http.StatusServiceUnavailable || e.Code != "SERVICE_BUDGET_REACHED" {
		t.Fatalf("an account that has spent nothing answered %d %q, want 503 SERVICE_BUDGET_REACHED",
			code, e.Code)
	}
	if spent, _ := st.SpentToday("innocent"); spent != 0 {
		t.Fatalf("the refused account was charged %d tokens for a turn that never ran", spent)
	}
	// It must not read as the person's fault, or they will go and make another
	// account — which is precisely the behaviour this ceiling exists to stop.
	if !strings.Contains(strings.ToLower(e.Remedy), "not about your account") {
		t.Errorf("the remedy blames the person for a service-wide stop: %q", e.Remedy)
	}
}

// Zero means "no ceiling", not "a ceiling of zero". Getting this backwards would
// take the service offline for everybody the moment somebody left the setting
// out — the same trap the invite-code default fell into.
func TestSpendCapsDisabledAtZero(t *testing.T) {
	srv, st := spendServer(t, 0, 0)
	c := signedIn(t, srv, "unlimited")
	ses := newSession(t, c, srv)

	for i, msg := range []string{"one", "two", "three"} {
		if code, e := say(t, c, srv, ses.ID, msg); code != http.StatusOK {
			t.Fatalf("turn %d answered %d %q with both ceilings disabled", i+1, code, e.Code)
		}
	}
	// Still counted, though: the numbers are what /api/health reports, and an
	// operator sizing a ceiling needs them before they switch one on.
	if spent, _ := st.SpentToday("unlimited"); spent <= 0 {
		t.Errorf("nothing was recorded with the ceilings off, so there is no usage to size a ceiling from")
	}
}

// /api/health carries the day's total and the ceiling. Without it the ceiling
// can only be set by guessing, and a service stopped by its own budget looks
// identical from outside to one that is merely quiet.
func TestHealthReportsTheDaysSpending(t *testing.T) {
	srv, _ := spendServer(t, 0, 4242)
	c := signedIn(t, srv, "watched")
	if code, _ := say(t, c, srv, newSession(t, c, srv).ID, "hello"); code != http.StatusOK {
		t.Fatal("the turn was refused")
	}

	res := getAs(t, anon(t, srv), srv.URL+"/api/health")
	defer res.Body.Close()
	var out struct {
		Spent   int64 `json:"spend_today_tokens"`
		Ceiling int64 `json:"spend_ceiling_tokens"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("health: %v", err)
	}
	if out.Spent <= 0 {
		t.Errorf("health reports %d spent after a real turn; the gauge is dead", out.Spent)
	}
	if out.Ceiling != 4242 {
		t.Errorf("health reports a ceiling of %d, want the configured 4242", out.Ceiling)
	}
}
