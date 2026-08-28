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
// Persistence is a snapshot to one JSON file. It is an enhancement, never a
// dependency: a failed load starts empty with a warning, and a failed write logs
// and continues, so the main path never breaks because a disk did.
package store

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
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
	log  *slog.Logger
}

func New(path string, log *slog.Logger) *Store {
	if log == nil {
		log = slog.Default()
	}
	st := &Store{
		path: path,
		log:  log,
		s: snapshot{
			Sessions:  map[string]*Session{},
			Profiles:  map[string]*domain.Profile{},
			Tasks:     map[string]*domain.CaseTask{},
			Consent:   map[string]map[domain.ConsentScope]domain.ConsentGrant{},
			Approvals: map[string]*PendingApproval{},
		},
	}
	st.load()
	return st
}

// load is deliberately forgiving. A corrupt or absent state file must not stop
// the service from answering the next person's question.
func (s *Store) load() {
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
	s.s = snap
}

// persist writes a snapshot. Callers hold the write lock.
func (s *Store) persist() {
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

func (s *Store) Sessions() []*Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Session, 0, len(s.s.Sessions))
	for _, ses := range s.s.Sessions {
		out = append(out, cloneSession(ses))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
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
