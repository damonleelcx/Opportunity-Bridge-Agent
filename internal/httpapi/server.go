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
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/agent"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/config"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/intent"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
)

type Server struct {
	Agent *agent.Agent
	Store *store.Store
	Cfg   config.Config
	Web   fs.FS
	Log   *slog.Logger
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
	mux.Handle("GET /", http.FileServerFS(s.Web))
	return logging(s.Log, mux)
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

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"status": "ok", "backend": s.Agent.LLM.Name(),
		"agent_model": s.Cfg.AgentModel, "classifier_model": s.Cfg.ClassifierModel,
		"corpus_records": len(s.Agent.Corpus.Opportunities), "cities": s.Agent.Corpus.Cities(),
	})
}

// meta tells the interface what this deployment can actually do, so limits are
// visible up front rather than discovered by a person hitting one.
func (s *Server) meta(w http.ResponseWriter, r *http.Request) {
	var disabled []string
	for _, in := range intent.All() {
		if !s.Cfg.IntentEnabled(string(in.ID)) {
			disabled = append(disabled, string(in.ID))
		}
	}
	writeJSON(w, map[string]any{
		"agent_model": s.Cfg.AgentModel, "classifier_model": s.Cfg.ClassifierModel,
		"backend": s.Agent.LLM.Name(), "effort": s.Cfg.Effort,
		"k_anonymity_floor": s.Cfg.KAnonymityFloor,
		"max_iterations":    s.Cfg.MaxIterations, "max_tool_calls": s.Cfg.MaxToolCalls,
		"max_wallclock_sec": int(s.Cfg.MaxWallClock.Seconds()),
		"cities_covered":    s.Agent.Corpus.Cities(),
		"reply_language":    s.defaultLocale(),
		"reply_languages":   config.ReplyLanguages,
		"corpus_is_sample":  true,
		"disabled_intents":  disabled,
		"roles":             []string{string(domain.RoleResident), string(domain.RoleCaseworker), string(domain.RoleAnalyst)},
	})
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
		Role      string `json:"role"`
		SubjectID string `json:"subject_id"`
		Locale    string `json:"locale"`
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
	ses := s.Store.CreateSession(role, body.SubjectID, locale)
	writeJSON(w, ses)
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.Store.Sessions())
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ses, ok := s.Store.Session(id)
	if !ok {
		writeErr(w, http.StatusNotFound, "SESSION_NOT_FOUND",
			fmt.Sprintf("No session %q.", id), "Start a new conversation.")
		return
	}
	writeJSON(w, map[string]any{
		"session":   ses,
		"profile":   s.Store.Profile(ses.SubjectID),
		"tasks":     s.Store.TasksFor(ses.SubjectID),
		"consent":   s.Store.ConsentAll(ses.SubjectID),
		"approvals": s.Store.ApprovalsFor(ses.ID),
	})
}

// forgetProfile is the delete half of "you can see and correct what I hold".
// A product that only offers inspection has offered nothing.
func (s *Server) forgetProfile(w http.ResponseWriter, r *http.Request) {
	ses, ok := s.Store.Session(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "SESSION_NOT_FOUND", "No such session.", "Start a new conversation.")
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
	if _, ok := s.Store.Session(id); !ok {
		writeErr(w, http.StatusNotFound, "SESSION_NOT_FOUND", fmt.Sprintf("No session %q.", id),
			"Start a new conversation.")
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

	_, err := s.Agent.Run(ctx, agent.Input{
		SessionID: id, Message: body.Message,
		Intent: intent.ID(body.Intent), Sink: send,
	})
	if err != nil {
		send(agent.Event{Kind: agent.EvError, Text: err.Error()})
	}
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
	ses, ok := s.Store.Session(body.SessionID)
	if !ok {
		writeErr(w, http.StatusNotFound, "SESSION_NOT_FOUND", "No such session.", "Start a new conversation.")
		return
	}
	scope := domain.ConsentScope(body.Scope)
	switch scope {
	case domain.ConsentStoreProfile, domain.ConsentShareCaseworker,
		domain.ConsentSubmitOnBehalf, domain.ConsentAggregate:
	default:
		writeErr(w, http.StatusBadRequest, "SCOPE_INVALID",
			fmt.Sprintf("%q is not a permission this service asks for.", body.Scope),
			"Valid scopes: store_profile, share_with_caseworker, submit_on_behalf, aggregate_deidentified.")
		return
	}
	g := s.Store.SetConsent(ses.SubjectID, scope, body.Granted, body.Note)
	writeJSON(w, g)
}
