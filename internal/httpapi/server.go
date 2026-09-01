// Package httpapi is the transport. It owns no logic of its own: routing,
// guardrails and budgets all live behind agent.Run, so an answer given over HTTP
// and an answer given by the eval runner go through exactly the same path.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/agent"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/config"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/intent"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/mailer"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/tts"
)

type Server struct {
	Agent *agent.Agent
	Store *store.Store
	Cfg   config.Config
	Web   fs.FS
	Log   *slog.Logger
	// Mail sends the confirm-your-address and set-a-new-password messages. NIL
	// MEANS OFF, like TTS below: a deployment with no relay still signs people
	// up and in, it just cannot offer a password reset, and it says so rather
	// than showing a form that quietly does nothing.
	// See docs/bugfix/2026-08-31-email-verification-and-reset.md
	Mail mailer.Sender

	// TTS renders answers as speech. NIL MEANS OFF, and off is the default:
	// with no provider the browser reads answers in its own built-in voice,
	// which is what it did before this existed. See tts.go.
	TTS tts.Provider

	// Failed sign-in attempts, per username. See auth.go for why this is in
	// memory rather than in the store.
	limiterOnce sync.Once
	lim         *attemptLimiter
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/meta", s.meta)
	mux.HandleFunc("GET /api/intents", s.intents)
	mux.HandleFunc("POST /api/sessions", s.createSession)
	mux.HandleFunc("GET /api/sessions", s.listSessions)
	mux.HandleFunc("GET /api/sessions/{id}", s.getSession)
	mux.HandleFunc("POST /api/sessions/{id}/messages", s.postMessage)
	mux.HandleFunc("DELETE /api/sessions/{id}/profile", s.forgetProfile)
	mux.HandleFunc("POST /api/approvals/{id}", s.decideApproval)
	mux.HandleFunc("POST /api/consent", s.setConsent)
	mux.HandleFunc("POST /api/tts", s.speak)
	mux.HandleFunc("POST /api/auth/signup", s.signUp)
	mux.HandleFunc("POST /api/auth/signin", s.signIn)
	mux.HandleFunc("POST /api/auth/signout", s.signOut)
	mux.HandleFunc("GET /api/auth/me", s.me)
	// Confirming an address and getting back in after forgetting a password.
	// `verify` and the two reset routes are OPEN (see isOpenPath): somebody who
	// cannot sign in is exactly who needs them.
	mux.HandleFunc("GET /api/auth/verify", s.verifyEmail)
	mux.HandleFunc("POST /api/auth/verify", s.requestVerification)
	mux.HandleFunc("POST /api/auth/email", s.setEmail)
	mux.HandleFunc("POST /api/auth/reset", s.requestReset)
	mux.HandleFunc("POST /api/auth/reset/confirm", s.confirmReset)
	// `/` is the landing page (web/static/index.html); the conversational shell is
	// at `/app`. Both are served by the file server, but `/app` has no extension
	// and therefore no file of its own name, so it is named here. Bound twice
	// because Go's mux treats "/app" and "/app/" as different patterns and a
	// trailing slash a person typed should not be a 404.
	mux.HandleFunc("GET /app", s.appShell)
	mux.HandleFunc("GET /app/", s.appShell)
	mux.Handle("GET /", http.FileServerFS(s.Web))
	// The gate wraps every route rather than being applied per handler: a route
	// added later is protected by default, and forgetting to opt in cannot
	// silently publish somebody's transcript. See auth.go.
	return logging(s.Log, s.gate(mux))
}

// appShell serves the conversational interface at /app.
//
// It reads the file rather than delegating to the file server so the URL can be
// /app rather than /app.html: the app is the thing people bookmark and paste to
// each other, and ".html" in that link is an implementation detail that becomes
// permanent the moment somebody saves it.
func (s *Server) appShell(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(s.Web, "app.html")
	if err != nil {
		s.Log.Error("app shell missing", "error", err)
		http.Error(w, "interface unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

func logging(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(rw, r)
		if rw.status >= 400 || strings.HasPrefix(r.URL.Path, "/api/") {
			level := slog.LevelInfo
			if rw.status >= 500 {
				level = slog.LevelError
			} else if rw.status >= 400 {
				level = slog.LevelWarn
			}
			log.Log(r.Context(), level, "http request",
				"method", r.Method, "path", r.URL.Path, "status", rw.status,
				"duration_ms", time.Since(start).Milliseconds())
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(c int) { w.status = c; w.ResponseWriter.WriteHeader(c) }
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// apiError is the one error shape the interface has to handle: a code it can
// branch on, a message a person can read, and a remedy they can act on.
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Remedy  string `json:"remedy,omitempty"`
}

func writeErr(w http.ResponseWriter, status int, code, msg, remedy string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiError{Code: code, Message: msg, Remedy: remedy})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// deploymentFacts are the things the landing page states out loud about this
// instance: how much corpus it holds, and whether the nationwide lookup is
// connected. One producer, used by both /api/health and /api/meta, so the front
// page and the conversation cannot disagree about them.
//
// They are readable from /api/health because the landing page is read by people
// who are NOT signed in, and the gate's list of open paths is short on purpose.
// /api/health is already public, already the "what is this deployment" endpoint
// and already reported corpus_records; widening the gate to /api/meta for a
// front-page sentence would trade a security boundary for a decoration.
// See docs/bugfix/2026-08-31-honest-limits-were-not-honest.md
func (s *Server) deploymentFacts() map[string]any {
	return map[string]any{
		// Why live_search_enabled is surfaced at all: with no search key the only
		// live provider is the directory, which returns the official portal for a
		// region and never a named employer or course. So a person outside the
		// cities in the corpus gets the national framework and a website -
		// correct, but it looks like "there is nothing for you" rather than "this
		// instance cannot look". The startup log says LIVE_SEARCH_DISABLED; nobody
		// reading the page can see a log.
		// See docs/bugfix/2026-08-28-subject-identity-and-tracked-steps.md
		"corpus_opportunities":  len(s.Agent.Corpus.Opportunities),
		"corpus_knowledge_docs": len(s.Agent.Corpus.Docs),
		"live_search_enabled":   s.Cfg.SearchAPIKey != "",
		// Named per vendor rather than as one boolean: they cover different
		// people, so "which one is on" changes what an empty result means.
		"external_talent_pdl":    s.Cfg.PDLAPIKey != "",
		"external_talent_apollo": s.Cfg.ApolloAPIKey != "",
		// Why the landing page needs this: its read-aloud card used to promise
		// "no audio leaves your device". With a speech vendor configured, the
		// ANSWER TEXT is sent to that vendor to be rendered - a person's city,
		// their unemployment, the benefit they are claiming - and on the free
		// backbone the vendor's terms allow using those requests to improve its
		// models. docs/17-read-aloud.md said so all along; the page said the
		// opposite. A privacy claim is the worst kind to hardcode, because it is
		// true on the machine of whoever wrote it and false in production.
		// See docs/bugfix/2026-08-31-the-privacy-claim-was-false.md
		"speech_vendor_enabled": s.TTS != nil,
		// Whether this deployment can put a message in somebody's inbox. The
		// sign-in page offers "forgot your password" only when it can actually
		// work — a form that silently does nothing is worse than an absent one,
		// because the person waits for a mail that was never sent.
		// See docs/bugfix/2026-08-31-email-verification-and-reset.md
		"mail_enabled": s.Mail != nil,
		// Whether that vendor's terms let it train on what is sent. Derived, not
		// written down: the copy on the landing page says one thing on the free
		// backbone and another on a paid one, and a deployment that switches
		// backbones must not have to remember to edit a sentence. Unknown
		// backbones report true — see tts.paidBackbones for why that direction.
		"speech_vendor_trains_on_text": s.TTS != nil && tts.TrainsOnRequests(s.Cfg.TTSModel),
	}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	_, deploymentSpent := s.Store.SpentToday("")
	out := map[string]any{
		"status": "ok", "backend": s.Agent.LLM.Name(),
		"agent_model": s.Cfg.AgentModel, "classifier_model": s.Cfg.ClassifierModel,
		"corpus_records": len(s.Agent.Corpus.Opportunities), "cities": s.Agent.Corpus.Cities(),
		// The day's model spending against the ceiling that stops it. Reported
		// here and NOT in deploymentFacts because these are operating numbers,
		// not something the landing page states about the product — and
		// deploymentFacts also feeds /api/meta.
		//
		// This endpoint is public (the probes need it), so these two numbers are
		// public. What they reveal is roughly how busy the service is, which is
		// the price of being able to tune the ceiling from real use instead of
		// from a guess — and knowing the service is near its limit tells an
		// abuser nothing they would not learn from their next request.
		"spend_today_tokens":   deploymentSpent,
		"spend_ceiling_tokens": s.Cfg.DeploymentDailyTokens,
	}
	for k, v := range s.deploymentFacts() {
		out[k] = v
	}
	writeJSON(w, out)
}

// meta tells the interface what this deployment can actually do, so limits are
// visible up front rather than discovered by a person hitting one.

// ownedSession resolves a session id and refuses it unless the signed-in
// account owns the subject behind it.
//
// It answers 404, never 403. 403 would confirm that the id exists, which for
// sequential ids (ses_0001, ses_0002, …) is most of what an enumerator wants.
// "No such session, as far as you are concerned" is both true and quiet.
//
// Every handler that takes a session id goes through here. That is the point:
// the check being in one place is what makes it possible to say it is applied
// everywhere. See docs/bugfix/2026-08-28-data-exposure-no-ownership-checks.md
func (s *Server) ownedSession(w http.ResponseWriter, r *http.Request, id string) (*store.Session, bool) {
	ses, ok := s.Store.Session(id)
	if ok {
		if acct := accountFor(r); acct != nil && acct.Owns(ses.SubjectID) {
			return ses, true
		}
	}
	writeErr(w, http.StatusNotFound, "SESSION_NOT_FOUND",
		fmt.Sprintf("No session %q.", id), "Start a new conversation.")
	return nil, false
}

func (s *Server) meta(w http.ResponseWriter, r *http.Request) {
	var disabled []string
	for _, in := range intent.All() {
		if !s.Cfg.IntentEnabled(string(in.ID)) {
			disabled = append(disabled, string(in.ID))
		}
	}
	out := map[string]any{
		"agent_model": s.Cfg.AgentModel, "classifier_model": s.Cfg.ClassifierModel,
		"backend": s.Agent.LLM.Name(), "effort": s.Cfg.Effort,
		"k_anonymity_floor": s.Cfg.KAnonymityFloor,
		"max_iterations":    s.Cfg.MaxIterations, "max_tool_calls": s.Cfg.MaxToolCalls,
		"max_wallclock_sec": int(s.Cfg.MaxWallClock.Seconds()),
		"cities_covered":    s.Agent.Corpus.Cities(),
		"reply_language":    s.defaultLocale(),
		"reply_languages":   config.ReplyLanguages,
		// Derived from the corpus, not declared here. See corpus.IsSample: a
		// literal true kept the 「演示语料」 badge over real national schemes once
		// the invented records left.
		"corpus_is_sample": s.Agent.Corpus.IsSample(),
		"disabled_intents": disabled,
		// The interface renders one revoke control per scope from this list. It
		// used to keep its own copy, so a scope added on the server was one a
		// person could be asked for and then could not withdraw.
		// See docs/bugfix/2026-08-31-read-aloud-needs-consent.md
		"consent_scopes": consentScopeNames(),
		"roles":          roleStrings(),
	}
	for k, v := range s.deploymentFacts() {
		out[k] = v
	}
	writeJSON(w, out)
}

func (s *Server) intents(w http.ResponseWriter, r *http.Request) {
	role := domain.Role(r.URL.Query().Get("role"))
	list := intent.All()
	if role != "" {
		if !role.Valid() {
			writeErr(w, http.StatusBadRequest, "ROLE_INVALID",
				fmt.Sprintf("%q is not a role.", role), "Use resident, caseworker or analyst.")
			return
		}
		list = intent.ForRole(role)
	}
	type card struct {
		intent.Intent
		Enabled bool `json:"enabled"`
	}
	out := make([]card, 0, len(list))
	for _, in := range list {
		out = append(out, card{Intent: in, Enabled: s.Cfg.IntentEnabled(string(in.ID))})
	}
	writeJSON(w, out)
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Role string `json:"role"`
		// No subject_id. It used to be accepted here and is deliberately gone:
		// a field that is silently ignored reads, to the next person, like a
		// field that works.
		Locale string `json:"locale"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, http.ErrBodyReadAfterClose) {
		writeErr(w, http.StatusBadRequest, "BODY_INVALID", "The request body was not valid JSON.", "Send {\"role\":\"resident\"}.")
		return
	}
	role := domain.Role(body.Role)
	if role == "" {
		role = domain.RoleResident
	}
	if !role.Valid() {
		writeErr(w, http.StatusBadRequest, "ROLE_INVALID",
			fmt.Sprintf("%q is not a role.", body.Role), "Use resident, caseworker or analyst.")
		return
	}
	locale := s.defaultLocale()
	if body.Locale != "" {
		if !validLocale(body.Locale) {
			writeErr(w, http.StatusBadRequest, "LOCALE_INVALID",
				fmt.Sprintf("%q is not an answer language this service offers.", body.Locale),
				"Use zh-CN, en, or match.")
			return
		}
		locale = body.Locale
	}
	// The subject comes from the signed-in account, never from the request.
	// Honouring body.SubjectID would let anyone open a session onto anybody
	// else's record and read it back through the panels — the same exposure the
	// ownership checks close, wearing a cookie.
	acct := accountFor(r)
	if acct == nil {
		writeErr(w, http.StatusUnauthorized, "SIGNIN_REQUIRED",
			"You need to be signed in to start a conversation.",
			"Sign in, or create an account — it takes a username, a password and an email address.")
		return
	}
	ses := s.Store.CreateSession(role, acct.SubjectID, locale)
	writeJSON(w, ses)
}

// listSessions backs the conversation picker. It returns summaries, not whole
// sessions: the picker refetches after every turn and never needed transcripts.
func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	// Scoped to the caller. This endpoint used to answer with every visitor's
	// conversation title, to anybody who asked.
	writeJSON(w, s.Store.SessionSummariesFor(accountFor(r)))
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ses, ok := s.ownedSession(w, r, id)
	if !ok {
		return
	}
	writeJSON(w, map[string]any{
		// Consent is applied again on the way out, so a reopened conversation
		// shows the pool as it is rather than as it was. See freshenCards.
		"session":   freshenCards(s.Store, ses),
		"profile":   s.Store.Profile(ses.SubjectID),
		"tasks":     s.Store.TasksFor(ses.SubjectID),
		"consent":   s.Store.ConsentAll(ses.SubjectID),
		"approvals": s.Store.ApprovalsFor(ses.ID),
	})
}

// forgetProfile is the delete half of "you can see and correct what I hold".
// A product that only offers inspection has offered nothing.
func (s *Server) forgetProfile(w http.ResponseWriter, r *http.Request) {
	ses, ok := s.ownedSession(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	s.Store.ForgetProfile(ses.SubjectID)
	writeJSON(w, map[string]any{"deleted": true, "subject_id": ses.SubjectID})
}

// postMessage streams the turn as server-sent events. Streaming is not a nicety
// here: a turn that retrieves, reads criteria and drafts a document takes long
// enough that a spinner with no detail reads as a hang.
func (s *Server) postMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Message string `json:"message"`
		Intent  string `json:"intent"`
		// Locale, when present, sets the answer language for this turn and
		// every turn after it. Carried on the message rather than through a
		// separate endpoint: it is a property of the conversation, and adding a
		// second round trip to change a language is the kind of friction this
		// product exists to remove.
		Locale string `json:"locale"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "BODY_INVALID", "The request body was not valid JSON.",
			"Send {\"message\":\"...\"}.")
		return
	}
	if strings.TrimSpace(body.Message) == "" {
		writeErr(w, http.StatusBadRequest, "MESSAGE_EMPTY", "The message was empty.", "Type what you need help with.")
		return
	}
	if _, ok := s.ownedSession(w, r, id); !ok {
		return
	}
	// The spend gate stands here, BEFORE the stream opens, so a refusal is an
	// ordinary JSON error the interface already knows how to render inline
	// rather than an error event inside a half-started answer.
	if !s.spendAllowed(w, r) {
		return
	}
	if body.Locale != "" {
		if !validLocale(body.Locale) {
			writeErr(w, http.StatusBadRequest, "LOCALE_INVALID",
				fmt.Sprintf("%q is not an answer language this service offers.", body.Locale),
				"Use zh-CN, en, or match.")
			return
		}
		if err := s.Store.MutateSession(id, func(ses *store.Session) error {
			ses.Locale = body.Locale
			return nil
		}); err != nil {
			writeErr(w, http.StatusNotFound, "SESSION_NOT_FOUND", err.Error(), "Start a new conversation.")
			return
		}
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "STREAMING_UNSUPPORTED",
			"This server cannot stream responses.", "Report this; it is a deployment problem, not something you did.")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(ev agent.Event) {
		b, err := json.Marshal(ev)
		if err != nil {
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.Cfg.MaxWallClock+30*time.Second)
	defer cancel()

	res, err := s.Agent.Run(ctx, agent.Input{
		SessionID: id, Message: body.Message,
		Intent: intent.ID(body.Intent), Sink: send,
	})
	if err != nil {
		send(agent.Event{Kind: agent.EvError, Text: err.Error()})
	}
	// Charged AFTER the turn, from what it actually cost, and charged even when
	// it ended in an error: a turn that failed on its last iteration still spent
	// everything before it, and not charging for failures is an invitation to
	// spend the budget on turns engineered to fail.
	s.recordSpend(r, res.Usage)
	fmt.Fprint(w, "data: {\"kind\":\"close\"}\n\n")
	flusher.Flush()
}

// decideApproval is the human-in-the-loop gate (step 18). The decision is
// recorded against the exact arguments that were shown; the agent will only act
// on a match.
// defaultLocale is the deployment's answer language, with a fallback for a
// hand-constructed Config. A zero value should not turn session creation into a
// 400 - config.Validate is where a genuinely wrong value is caught, at startup.
func (s *Server) defaultLocale() string {
	if validLocale(s.Cfg.ReplyLanguage) {
		return s.Cfg.ReplyLanguage
	}
	return "zh-CN"
}

// validLocale keeps the accepted set in one place; config.ReplyLanguages is the
// same list the deployment default is validated against.
func validLocale(l string) bool {
	for _, v := range config.ReplyLanguages {
		if v == l {
			return true
		}
	}
	return false
}

func (s *Server) decideApproval(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Approved bool   `json:"approved"`
		Reason   string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "BODY_INVALID", "The request body was not valid JSON.",
			"Send {\"approved\":true}.")
		return
	}
	// An approval authorises an irreversible act inside one conversation, so it
	// is only decidable by whoever owns that conversation.
	pending, found := s.Store.Approval(r.PathValue("id"))
	if !found {
		writeErr(w, http.StatusNotFound, "APPROVAL_NOT_FOUND",
			"No such request.", "Reload the conversation to see the current state.")
		return
	}
	if _, ok := s.ownedSession(w, r, pending.SessionID); !ok {
		return
	}
	ap, err := s.Store.DecideApproval(r.PathValue("id"), body.Approved, body.Reason)
	if err != nil {
		writeErr(w, http.StatusConflict, "APPROVAL_FAILED", err.Error(),
			"Reload the conversation to see the current state of this request.")
		return
	}
	writeJSON(w, ap)
}

func (s *Server) setConsent(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SessionID string `json:"session_id"`
		Scope     string `json:"scope"`
		Granted   bool   `json:"granted"`
		Note      string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "BODY_INVALID", "The request body was not valid JSON.", "")
		return
	}
	ses, ok := s.ownedSession(w, r, body.SessionID)
	if !ok {
		return
	}
	// Validated against domain.ConsentScopes(), not against a list repeated here.
	// A scope this switch had not been taught about answered 400 while existing
	// everywhere else — the person would see the permission, press grant, and
	// nothing would happen. See docs/bugfix/2026-08-31-read-aloud-needs-consent.md
	scope := domain.ConsentScope(body.Scope)
	if !domain.IsConsentScope(body.Scope) {
		writeErr(w, http.StatusBadRequest, "SCOPE_INVALID",
			fmt.Sprintf("%q is not a permission this service asks for.", body.Scope),
			"Valid scopes: "+strings.Join(consentScopeNames(), ", ")+".")
		return
	}
	g := s.Store.SetConsent(ses.SubjectID, scope, body.Granted, body.Note)
	writeJSON(w, g)
}

func consentScopeNames() []string {
	out := make([]string, 0, len(domain.ConsentScopes()))
	for _, s := range domain.ConsentScopes() {
		out = append(out, string(s))
	}
	return out
}

// roleStrings renders the role vocabulary for /api/meta.
//
// It reads domain.Roles() rather than listing them, because the hand-written
// list here had already fallen out of step once: a role that exists in the
// domain but not in this payload cannot be selected in the interface, and the
// intent behind it becomes dead code that still passes every test it has.
func roleStrings() []string {
	rs := domain.Roles()
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, string(r))
	}
	return out
}
