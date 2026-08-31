// Package store separates the three kinds of memory an agent needs (step 10 of
// the build flow), because collapsing them is the usual cause of an agent that
// either forgets what it just did or remembers something it was never told.
//
//	Short-term task state  - the slots and findings of the current objective.
//	                         Cleared when the objective changes.
//	Conversation history   - the turns. Trimmed by budget, never mutated.
//	Long-term memory       - profile, case tasks, consent, demand signals.
//	                         Consent-scoped and survives the session.
//
// Persistence has two backends and exactly one is in use at a time.
//
//	postgres  the durable home in a real deployment (OpenPostgres). Chosen
//	          because the previous one — a JSON file on a node-local volume with
//	          no backup — had exactly one copy of every conversation.
//	JSON file the local-development and test backend (New). Unchanged.
//
// Reads never touch either: the snapshot is held in memory and loaded once at
// start, which is why the read path is identical under both.
//
// A failed WRITE logs and continues under both, so the main path never breaks
// because a disk or a database did; the turn is still answered, and what is
// lost is the memory of it. A failed LOAD differs by backend, and deliberately:
// an unreadable file starts empty with a warning, while an unreadable database
// refuses to start, because Save() makes the database match the snapshot and an
// empty snapshot would sweep away the records it failed to read.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
)

// TaskState is the short-term working memory of one objective.
type TaskState struct {
	Objective string            `json:"objective,omitempty"`
	Slots     map[string]string `json:"slots,omitempty"`
	Findings  []Finding         `json:"findings,omitempty"`
	Step      int               `json:"step"`
}

// Finding is something a tool established this turn, kept so a later turn can
// cite it without paying to retrieve it again.
type Finding struct {
	Tool      string `json:"tool"`
	Summary   string `json:"summary"`
	SourceRef string `json:"source_ref,omitempty"`
}

type Turn struct {
	Role   string    `json:"role"` // "user" | "assistant"
	Text   string    `json:"text"`
	Intent string    `json:"intent,omitempty"`
	At     time.Time `json:"at"`
	RunID  string    `json:"run_id,omitempty"`
}

// PendingApproval is a high-risk tool call frozen mid-loop, waiting for a human
// to say yes (step 18). It carries the full arguments so the UI can show
// exactly what will happen, not a paraphrase.
type PendingApproval struct {
	ID        string          `json:"id"`
	SessionID string          `json:"session_id"`
	Tool      string          `json:"tool"`
	Args      json.RawMessage `json:"args"`
	Summary   string          `json:"summary"`
	Impact    string          `json:"impact"`
	CreatedAt time.Time       `json:"created_at"`
	Decided   bool            `json:"decided"`
	Approved  bool            `json:"approved"`
	DecidedAt time.Time       `json:"decided_at,omitempty"`
	Reason    string          `json:"reason,omitempty"`
}

// Session is one conversation.
type Session struct {
	ID          string              `json:"id"`
	Role        domain.Role         `json:"role"`
	SubjectID   string              `json:"subject_id"` // whose record this session acts on
	Locale      string              `json:"locale"`
	AccessNeeds []domain.AccessNeed `json:"access_needs,omitempty"`
	Intent      string              `json:"intent,omitempty"` // last resolved intent
	Task        TaskState           `json:"task"`
	History     []Turn              `json:"history,omitempty"`
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
}

type snapshot struct {
	// Accounts and SignIns are what make every other map below somebody's
	// rather than everybody's. Before they existed, GET /api/sessions/{id}
	// answered for any id, and ids are sequential.
	// See docs/bugfix/2026-08-28-data-exposure-no-ownership-checks.md
	Accounts map[string]*Account `json:"accounts,omitempty"`
	SignIns  map[string]*SignIn  `json:"sign_ins,omitempty"`
	// EmailTokens are the outstanding verify/reset links, keyed by the token's
	// hash for the same reason sign-ins are. See internal/store/emailtokens.go.
	EmailTokens map[string]*EmailToken `json:"email_tokens,omitempty"`
	// LegacyAdopted records that the one-off adoption of pre-account data has
	// run. It is a marker, not a feature: without it the adoption would keep
	// sweeping up subjects on every restart.
	LegacyAdopted bool `json:"legacy_adopted,omitempty"`

	Sessions  map[string]*Session                                    `json:"sessions"`
	Profiles  map[string]*domain.Profile                             `json:"profiles"`
	Tasks     map[string]*domain.CaseTask                            `json:"case_tasks"`
	Consent   map[string]map[domain.ConsentScope]domain.ConsentGrant `json:"consent"`
	Signals   []domain.DemandSignal                                  `json:"demand_signals"`
	Approvals map[string]*PendingApproval                            `json:"approvals"`
	Seq       int                                                    `json:"seq"`
}

type Store struct {
	mu   sync.RWMutex
	s    snapshot
	path string
	// db, when set, is the durable home instead of path. Exactly one of the two
	// is ever in use: a store that wrote to both would have two answers to
	// "what happened", and the wrong one would win on the next restart.
	db  *pgBackend
	log *slog.Logger
	// loadErr carries a failure out of load(), which has no return value
	// because the file backend has nothing to report. See load().
	loadErr error
}

func New(path string, log *slog.Logger) *Store {
	return newStore(path, nil, log)
}

// OpenPostgres returns a store whose durable home is postgres.
//
// It returns an error rather than falling back to a file, and that is the point:
// a deployment configured for a database it cannot reach must not come up
// quietly writing somewhere else. The operator believes their data is in
// postgres; a silent fallback makes that belief false and nothing says so.
func OpenPostgres(ctx context.Context, dsn string, log *slog.Logger) (*Store, error) {
	if log == nil {
		log = slog.Default()
	}
	db, err := openPG(ctx, dsn, log)
	if err != nil {
		return nil, err
	}
	st := newStore("", db, log)
	if st.loadErr != nil {
		db.Close()
		return nil, fmt.Errorf("STATE_LOAD_FAILED: refusing to start on a database that "+
			"cannot be read, because the first write would sweep it: %w", st.loadErr)
	}
	return st, nil
}

// newSnapshot is an empty store. Every map is non-nil, because a nil map here
// panics on the first write rather than on the first read, which puts the
// failure a long way from its cause.
func newSnapshot() snapshot {
	return snapshot{
		Sessions:  map[string]*Session{},
		Profiles:  map[string]*domain.Profile{},
		Tasks:     map[string]*domain.CaseTask{},
		Consent:   map[string]map[domain.ConsentScope]domain.ConsentGrant{},
		Approvals: map[string]*PendingApproval{},
		Accounts:  map[string]*Account{},
		SignIns:   map[string]*SignIn{},
		// Not optional: a nil map here panics on the first verification link a
		// fresh deployment ever issues, which is the first sign-up.
		EmailTokens: map[string]*EmailToken{},
	}
}

func newStore(path string, db *pgBackend, log *slog.Logger) *Store {
	if log == nil {
		log = slog.Default()
	}
	st := &Store{
		path: path,
		db:   db,
		log:  log,
		s:    newSnapshot(),
	}
	st.load()
	return st
}

// Close releases the database connections, if there are any.
func (s *Store) Close() {
	if s.db != nil {
		s.db.Close()
	}
}

// load is deliberately forgiving. A corrupt or absent state file must not stop
// the service from answering the next person's question.
func (s *Store) load() {
	if s.db != nil {
		// Postgres is NOT forgiving here, unlike the file below. An empty start
		// against a database that actually holds records would present every
		// signed-up person with a service that has forgotten them, and the
		// first write would then sweep their rows away — Save() makes the
		// database match the snapshot, and the snapshot would be empty.
		// Refusing to come up is the recoverable failure; this is why load
		// reports an error at all.
		s.loadErr = s.db.Load(context.Background(), &s.s)
		if s.loadErr == nil {
			s.log.Info("state loaded from postgres", "code", "STATE_LOADED",
				"sessions", len(s.s.Sessions), "accounts", len(s.s.Accounts),
				"profiles", len(s.s.Profiles), "tasks", len(s.s.Tasks))
		}
		return
	}
	if s.path == "" {
		return
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		if !os.IsNotExist(err) {
			s.log.Warn("state file unreadable, starting empty",
				"code", "STATE_READ_FAILED", "path", s.path, "error", err)
		}
		return
	}
	var snap snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		s.log.Warn("state file unparseable, starting empty",
			"code", "STATE_PARSE_FAILED", "path", s.path, "error", err)
		return
	}
	if snap.Sessions == nil {
		snap.Sessions = map[string]*Session{}
	}
	if snap.Profiles == nil {
		snap.Profiles = map[string]*domain.Profile{}
	}
	if snap.Tasks == nil {
		snap.Tasks = map[string]*domain.CaseTask{}
	}
	if snap.Consent == nil {
		snap.Consent = map[string]map[domain.ConsentScope]domain.ConsentGrant{}
	}
	if snap.Approvals == nil {
		snap.Approvals = map[string]*PendingApproval{}
	}
	if snap.Accounts == nil {
		snap.Accounts = map[string]*Account{}
	}
	if snap.EmailTokens == nil {
		snap.EmailTokens = map[string]*EmailToken{}
	}
	if snap.SignIns == nil {
		snap.SignIns = map[string]*SignIn{}
	}
	s.s = snap
}

// persist writes a snapshot. Callers hold the write lock.
func (s *Store) persist() {
	if s.db != nil {
		// ERROR, not WARN. With the file backend a failed write meant the next
		// restart forgot the last turn. It means the same thing here, but the
		// database is now the only copy, so this line is the only warning
		// anybody gets that a person's record did not survive.
		if err := s.db.Save(context.Background(), &s.s); err != nil {
			s.log.Error("state not written to postgres; this turn will not survive a restart",
				"code", "STATE_WRITE_FAILED", "error", err)
		}
		return
	}
	if s.path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		s.log.Warn("state directory not creatable", "code", "STATE_WRITE_FAILED", "error", err)
		return
	}
	b, err := json.MarshalIndent(s.s, "", "  ")
	if err != nil {
		s.log.Warn("state not serialisable", "code", "STATE_WRITE_FAILED", "error", err)
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		s.log.Warn("state not writable", "code", "STATE_WRITE_FAILED", "path", tmp, "error", err)
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		s.log.Warn("state rename failed", "code", "STATE_WRITE_FAILED", "path", s.path, "error", err)
	}
}

func (s *Store) nextID(prefix string) string {
	s.s.Seq++
	return fmt.Sprintf("%s_%04d", prefix, s.s.Seq)
}

// ---- sessions ----

func (s *Store) CreateSession(role domain.Role, subjectID, locale string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextID("ses")
	if subjectID == "" {
		subjectID = s.nextID("sub")
	}
	now := time.Now().UTC()
	ses := &Session{
		ID: id, Role: role, SubjectID: subjectID, Locale: locale,
		Task:      TaskState{Slots: map[string]string{}},
		CreatedAt: now, UpdatedAt: now,
	}
	s.s.Sessions[id] = ses
	s.persist()
	return cloneSession(ses)
}

func (s *Store) Session(id string) (*Session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ses, ok := s.s.Sessions[id]
	if !ok {
		return nil, false
	}
	return cloneSession(ses), true
}

// SessionSummary is the conversation list's view of a session: enough to label
// one and pick it, and no transcript.
//
// Why not just return []*Session: the client refetches the list after every
// turn, and a Session carries its whole history, so listing shipped every
// message of every conversation on every turn - a payload that grows without
// bound, and a disclosure a picker never needed.
type SessionSummary struct {
	ID        string      `json:"id"`
	Role      domain.Role `json:"role"`
	Locale    string      `json:"locale,omitempty"`
	Intent    string      `json:"intent,omitempty"`
	Title     string      `json:"title"` // first thing the person said; "" if they said nothing legible
	Turns     int         `json:"turns"` // user turns, so the client need not carry history to count them
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
}

// sessionTitleRunes caps the title at a length that is generous for a tooltip
// and still bounded. The row itself ellipsises in CSS; this only stops one
// pasted essay from dominating the list payload.
const sessionTitleRunes = 80

// SessionSummaries lists the conversations somebody actually started, most
// recently active first.
//
// Two rules here rather than in the client, so every client gets the same
// answer. Both fix what the sidebar was showing - see
// docs/bugfix/2026-08-28-session-list.md:
//
//  1. A session nobody has spoken in is not a conversation. The web client
//     creates a session on page load and on every role change, so each reload
//     minted an empty shell that then sat in the list for ever labelled with its
//     raw internal id (ses_0018). Filtering here means those shells are invisible
//     no matter which client asks.
//  2. Order by last activity, not by creation. Sorting on CreatedAt buried a
//     conversation you had just carried on underneath newer, idler ones.
//
// The shells are still stored; this hides them, it does not delete them. If that
// growth needs collecting, that is a separate decision about deleting data.
// SessionSummaries lists every conversation, for callers that legitimately have
// no owner to scope by. It is NOT what the HTTP layer calls: a picker that lists
// everybody's conversations to everybody is how a stranger's transcript ended up
// one sequential id away from anyone who asked.
// See docs/bugfix/2026-08-28-data-exposure-no-ownership-checks.md
func (s *Store) SessionSummaries() []SessionSummary {
	return s.summaries(nil)
}

// SessionSummariesFor lists only the conversations this account owns. The
// ownership question is asked here, in the store, rather than by filtering
// afterwards in a handler, because a handler that forgets to filter looks
// exactly like one that did.
func (s *Store) SessionSummariesFor(a *Account) []SessionSummary {
	if a == nil {
		return nil
	}
	return s.summaries(a)
}

func (s *Store) summaries(owner *Account) []SessionSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]SessionSummary, 0, len(s.s.Sessions))
	for _, ses := range s.s.Sessions {
		if owner != nil && !owner.Owns(ses.SubjectID) {
			continue
		}
		title, turns := "", 0
		for _, h := range ses.History {
			if h.Role != "user" {
				continue
			}
			turns++
			if title == "" {
				title = clipRunes(collapseSpace(h.Text), sessionTitleRunes)
			}
		}
		if turns == 0 {
			continue
		}
		out = append(out, SessionSummary{
			ID: ses.ID, Role: ses.Role, Locale: ses.Locale, Intent: ses.Intent,
			Title: title, Turns: turns,
			CreatedAt: ses.CreatedAt, UpdatedAt: ses.UpdatedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out
}

// collapseSpace flattens a multi-line message into one label line. A pasted
// message with newlines in it must not become a tall, ragged row.
func collapseSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

// clipRunes cuts by rune, not by byte. Cutting Chinese by byte splits a
// character and produces a replacement glyph in the middle of a title.
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "\u2026"
}

// MutateSession applies fn under the write lock and persists. Returning an
// error leaves the session untouched.
func (s *Store) MutateSession(id string, fn func(*Session) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ses, ok := s.s.Sessions[id]
	if !ok {
		return fmt.Errorf("SESSION_NOT_FOUND: no session %q; start a new conversation", id)
	}
	work := cloneSession(ses)
	if err := fn(work); err != nil {
		return err
	}
	work.UpdatedAt = time.Now().UTC()
	s.s.Sessions[id] = work
	s.persist()
	return nil
}

func cloneSession(in *Session) *Session {
	out := *in
	out.History = append([]Turn(nil), in.History...)
	out.AccessNeeds = append([]domain.AccessNeed(nil), in.AccessNeeds...)
	out.Task.Findings = append([]Finding(nil), in.Task.Findings...)
	out.Task.Slots = map[string]string{}
	for k, v := range in.Task.Slots {
		out.Task.Slots[k] = v
	}
	return &out
}

// ---- profiles (long-term memory) ----

func (s *Store) Profile(subjectID string) domain.Profile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p, ok := s.s.Profiles[subjectID]; ok {
		return *p
	}
	return domain.Profile{SubjectID: subjectID}
}

func (s *Store) SaveProfile(p domain.Profile) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p.UpdatedAt = time.Now().UTC()
	cp := p
	s.s.Profiles[p.SubjectID] = &cp
	s.persist()
}

// ForgetProfile is the delete half of "the user can inspect and correct it".
func (s *Store) ForgetProfile(subjectID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.s.Profiles, subjectID)
	s.persist()
}

// ---- consent ----

func (s *Store) Consent(subjectID string, scope domain.ConsentScope) domain.ConsentGrant {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if m, ok := s.s.Consent[subjectID]; ok {
		if g, ok := m[scope]; ok {
			return g
		}
	}
	return domain.ConsentGrant{Scope: scope, Granted: false}
}

func (s *Store) ConsentAll(subjectID string) []domain.ConsentGrant {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.ConsentGrant
	for _, g := range s.s.Consent[subjectID] {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Scope < out[j].Scope })
	return out
}

func (s *Store) SetConsent(subjectID string, scope domain.ConsentScope, granted bool, note string) domain.ConsentGrant {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.s.Consent[subjectID] == nil {
		s.s.Consent[subjectID] = map[domain.ConsentScope]domain.ConsentGrant{}
	}
	g := domain.ConsentGrant{Scope: scope, Granted: granted, GrantedAt: time.Now().UTC(), Note: note}
	s.s.Consent[subjectID][scope] = g
	s.persist()
	return g
}

// ---- case tasks ----

func (s *Store) CreateTask(t domain.CaseTask) domain.CaseTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	t.ID = s.nextID("task")
	t.CreatedAt, t.UpdatedAt = now, now
	if t.Status == "" {
		t.Status = domain.TaskOpen
	}
	t.History = append(t.History, domain.TaskEvent{At: now, Status: t.Status, Note: "created"})
	cp := t
	s.s.Tasks[t.ID] = &cp
	s.persist()
	return t
}

func (s *Store) UpdateTask(id string, fn func(*domain.CaseTask) error) (domain.CaseTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.s.Tasks[id]
	if !ok {
		return domain.CaseTask{}, fmt.Errorf("TASK_NOT_FOUND: no case task %q; list tasks first to get a valid id", id)
	}
	work := *t
	work.History = append([]domain.TaskEvent(nil), t.History...)
	before := work.Status
	if err := fn(&work); err != nil {
		return domain.CaseTask{}, err
	}
	work.UpdatedAt = time.Now().UTC()
	if work.Status != before {
		work.History = append(work.History, domain.TaskEvent{At: work.UpdatedAt, Status: work.Status})
	}
	s.s.Tasks[id] = &work
	s.persist()
	return work, nil
}

func (s *Store) TasksFor(subjectID string) []domain.CaseTask {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.CaseTask
	for _, t := range s.s.Tasks {
		if t.SubjectID == subjectID {
			out = append(out, *t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// ---- demand signals (de-identified) ----

// RecordSignal appends one de-identified observation. The caller is responsible
// for having checked aggregation consent; the signal itself carries no way back
// to a person, which is why this method takes no subject id.
func (s *Store) RecordSignal(sig domain.DemandSignal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sig.At.IsZero() {
		sig.At = time.Now().UTC()
	}
	s.s.Signals = append(s.s.Signals, sig)
	s.persist()
}

func (s *Store) Signals() []domain.DemandSignal {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]domain.DemandSignal(nil), s.s.Signals...)
}

// ConsentCoverage reports how many known subjects granted aggregation consent.
// Insight answers must quote this: a gap computed over a twelfth of the
// population is a hypothesis, not a finding.
func (s *Store) ConsentCoverage() (granted, total int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := map[string]bool{}
	for id := range s.s.Profiles {
		seen[id] = true
	}
	for id := range s.s.Consent {
		seen[id] = true
	}
	for id := range seen {
		total++
		if g, ok := s.s.Consent[id][domain.ConsentAggregate]; ok && g.Granted {
			granted++
		}
	}
	return granted, total
}

// ---- approvals ----

func (s *Store) CreateApproval(a PendingApproval) PendingApproval {
	s.mu.Lock()
	defer s.mu.Unlock()
	a.ID = s.nextID("appr")
	a.CreatedAt = time.Now().UTC()
	cp := a
	s.s.Approvals[a.ID] = &cp
	s.persist()
	return a
}

func (s *Store) Approval(id string) (PendingApproval, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.s.Approvals[id]
	if !ok {
		return PendingApproval{}, false
	}
	return *a, true
}

func (s *Store) DecideApproval(id string, approved bool, reason string) (PendingApproval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.s.Approvals[id]
	if !ok {
		return PendingApproval{}, fmt.Errorf("APPROVAL_NOT_FOUND: no pending approval %q", id)
	}
	if a.Decided {
		return *a, fmt.Errorf("APPROVAL_ALREADY_DECIDED: approval %q was already %s", id, decidedWord(a.Approved))
	}
	a.Decided, a.Approved, a.Reason = true, approved, reason
	a.DecidedAt = time.Now().UTC()
	s.persist()
	return *a, nil
}

// ApprovalsFor returns every approval raised in a session, decided or not.
func (s *Store) ApprovalsFor(sessionID string) []PendingApproval {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []PendingApproval
	for _, a := range s.s.Approvals {
		if a.SessionID == sessionID {
			out = append(out, *a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (s *Store) PendingApprovals(sessionID string) []PendingApproval {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []PendingApproval
	for _, a := range s.s.Approvals {
		if a.SessionID == sessionID && !a.Decided {
			out = append(out, *a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func decidedWord(approved bool) string {
	if approved {
		return "approved"
	}
	return "declined"
}
