package leadgraph

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// One conversational turn's worth of graph maintenance.
//
// THE CONSTRAINT THIS FILE EXISTS TO ENFORCE
//
//	The user is in the middle of doing something else. They mentioned a fact in
//	passing. An assistant that answers with three confirmation prompts gets
//	routed around by the second day - the user stops telling it things, and the
//	graph starves. Starvation, not missing features, is how this category of
//	product dies.
//
//	So a turn may produce AT MOST: one line of receipt, and one question. Not
//	one per fact - one, total. Everything else waits in a queue that survives
//	the turn, because a question that was not worth interrupting for is still
//	worth asking eventually.
//
// AND THE ONE IT MUST NOT BREAK WHILE DOING IT
//
//	Being quiet is not the same as being permissive. Nothing that needed a
//	person's judgement is written just because there was no room to ask about
//	it this turn. It is parked, and it stays parked.

// TurnInput is what the model extracted from one turn, plus the user's answer
// to whatever was asked last turn.
type TurnInput struct {
	// TurnRef identifies the conversation turn. It ends up in every piece of
	// intel written here, which is what makes "where did this come from"
	// answerable months later.
	TurnRef string
	Nodes   []Node
	// Answer resolves one queued question. Applied BEFORE this turn's new facts:
	// the user answered about the graph as it was when asked.
	Answer *Answer
}

// Answer is a person's reply to a question this pipeline asked earlier.
type Answer struct {
	PendingID  string
	Resolution Resolution
}

// TurnResult is what the caller says out loud, and what it holds back.
type TurnResult struct {
	Applied []string `json:"applied,omitempty"`
	// Receipt is nil when nothing was recorded. A receipt on every turn is
	// noise, and noise is how a one-line receipt becomes something users learn
	// to skip past.
	Receipt *Receipt `json:"receipt,omitempty"`
	// Question is at most one, and nil when nothing needs asking.
	Question  *Question `json:"question,omitempty"`
	QueuedIDs []string  `json:"queued_ids,omitempty"`
	// Answered reports what the user's reply actually did, so the caller can say
	// it rather than assuming it worked.
	Answered *ApplyResult `json:"answered,omitempty"`
}

// Receipt is the one line. It carries counts and a few named items rather than
// a sentence, because the sentence belongs to the interface and its language.
type Receipt struct {
	Created int           `json:"created"`
	Updated int           `json:"updated"`
	Queued  int           `json:"queued"`
	Items   []ReceiptItem `json:"items,omitempty"`
}

// ReceiptItemCap is how many things a receipt may name.
//
// Three, because the receipt has to fit on one line under an answer about
// something else. A receipt that lists everything is not a receipt, it is a
// report, and it has stopped being unobtrusive.
const ReceiptItemCap = 3

type ReceiptItem struct {
	NodeID string   `json:"node_id"`
	Label  string   `json:"label"`
	Kind   NodeKind `json:"kind"`
	// Corroboration travels with the item so the line can say "未证实" without
	// the interface having to go and look it up - and so it cannot forget to.
	Corroboration Corroboration `json:"corroboration"`
	Unconfirmed   []string      `json:"unconfirmed"`
}

// Question is the single thing worth interrupting for this turn.
type Question struct {
	PendingID string   `json:"pending_id"`
	Key       string   `json:"key"`
	Proposal  Proposal `json:"proposal"`
}

// PendingItem is a decision parked until somebody has room to make it.
type PendingItem struct {
	ID       string    `json:"id"`
	TeamID   string    `json:"team_id"`
	SeatID   string    `json:"seat_id"`
	Proposal Proposal  `json:"proposal"`
	At       time.Time `json:"at"`
	// Asked is when this was last put to the user. Something already asked is
	// not asked again in the next turn: repeating a question the user chose not
	// to answer is nagging, and it trains them to ignore the channel.
	Asked *time.Time `json:"asked,omitempty"`
}

// RecordTurn is the whole of path one: apply the answer, reconcile what was
// just heard, write what needs no judgement, park what does, and hand back one
// line and at most one question.
func (s *Store) RecordTurn(v View, actor Actor, in TurnInput) (TurnResult, error) {
	if !v.valid() {
		return TurnResult{}, ErrViewRequired
	}
	var out TurnResult

	if in.Answer != nil {
		res, err := s.Answer(v, ActorUser, in.Answer.PendingID, in.Answer.Resolution)
		if err != nil {
			return out, fmt.Errorf("answering %s: %w", in.Answer.PendingID, err)
		}
		out.Answered = &res
		out.Applied = append(out.Applied, res.Created...)
		out.Applied = append(out.Applied, res.Updated...)
	}

	candidates := stampTurn(in.Nodes, in.TurnRef)
	proposals := s.Reconcile(v, candidates)

	applied, err := s.Apply(v, actor, proposals, nil)
	if err != nil {
		return out, err
	}
	out.Applied = append(out.Applied, applied.Created...)
	out.Applied = append(out.Applied, applied.Updated...)

	for _, i := range applied.Pending {
		id, queued := s.queuePending(v, proposals[i])
		if queued {
			out.QueuedIDs = append(out.QueuedIDs, id)
		}
	}

	out.Receipt = s.receipt(v, applied, len(out.QueuedIDs))
	out.Question = s.nextQuestion(v)
	return out, nil
}

// stampTurn puts the turn reference on every piece of intel that carries none,
// so provenance cannot be lost by an extractor that forgot to set it.
func stampTurn(ns []Node, turnRef string) []Node {
	if turnRef == "" {
		return ns
	}
	out := make([]Node, 0, len(ns))
	for _, n := range ns {
		in := make([]Intel, len(n.Intel))
		copy(in, n.Intel)
		for i := range in {
			if in[i].Kind == IntelUserSaid && in[i].TurnRef == "" {
				in[i].TurnRef = turnRef
			}
		}
		n.Intel = in
		out = append(out, n)
	}
	return out
}

// receipt builds the line, or returns nil when there is nothing to say.
func (s *Store) receipt(v View, r ApplyResult, queued int) *Receipt {
	if len(r.Created) == 0 && len(r.Updated) == 0 && queued == 0 {
		return nil
	}
	rec := &Receipt{Created: len(r.Created), Updated: len(r.Updated), Queued: queued}
	ids := append(append([]string{}, r.Created...), r.Updated...)
	for _, id := range ids {
		if len(rec.Items) == ReceiptItemCap {
			break
		}
		n, ok := s.Node(v, id)
		if !ok {
			continue
		}
		rec.Items = append(rec.Items, ReceiptItem{
			NodeID: n.ID, Label: n.Label, Kind: n.Kind,
			Corroboration: n.Corroboration(), Unconfirmed: n.Unconfirmed,
		})
	}
	return rec
}

// ---- the pending queue ----

// pendingKey is what makes mentioning the same thing twice ask once. Without
// it, a user who repeats themselves - which is normal - accumulates duplicate
// questions and concludes the assistant is not listening.
func pendingKey(p Proposal) string {
	parts := []string{string(p.Decision), p.TargetID, naturalKey(p.Candidate)}
	for _, c := range p.Conflicts {
		parts = append(parts, c.Field+"="+c.Proposed)
	}
	return strings.Join(parts, "|")
}

// queuePending parks a decision for its seat. Reports false when an identical
// one is already waiting.
func (s *Store) queuePending(v View, p Proposal) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := v.SeatID + "|pending|" + pendingKey(p)
	if id, ok := s.byKey[key]; ok {
		return id, false
	}
	item := &PendingItem{
		ID: s.nextID("pd"), TeamID: v.TeamID, SeatID: v.SeatID,
		Proposal: p, At: s.now(),
	}
	if s.pending == nil {
		s.pending = map[string]*PendingItem{}
	}
	s.pending[item.ID] = item
	s.byKey[key] = item.ID
	return item.ID, true
}

// Pending lists what this seat still has to decide, oldest first.
//
// Per SEAT, not per team: the question is about a conversation this person had,
// and a teammate answering a question they never heard is worse than nobody
// answering it.
func (s *Store) Pending(v View) []PendingItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []PendingItem
	for _, p := range s.pending {
		if p.TeamID == v.TeamID && p.SeatID == v.SeatID {
			out = append(out, *p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.Before(out[j].At)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// nextQuestion picks the one thing worth interrupting for.
//
// CONFLICTS BEFORE MERGES, then oldest first.
//
//	A conflict means the graph currently holds something the user's own words
//	contradict. Leave it, and the next answer cites a fact they just corrected -
//	which is how an assistant loses trust fastest. An unresolved merge only
//	costs a duplicate record, which is visible, harmless and fixable later.
//
// Something already asked is not asked again: a question the user chose not to
// answer is not more urgent the second time.
func (s *Store) nextQuestion(v View) *Question {
	items := s.Pending(v)
	var pick *PendingItem
	for _, want := range []Decision{DecideConflict, DecideMerge} {
		for i := range items {
			if items[i].Asked != nil || items[i].Proposal.Decision != want {
				continue
			}
			pick = &items[i]
			break
		}
		if pick != nil {
			break
		}
	}
	if pick == nil {
		return nil
	}
	s.mu.Lock()
	if held, ok := s.pending[pick.ID]; ok {
		at := s.now()
		held.Asked = &at
	}
	s.mu.Unlock()
	return &Question{PendingID: pick.ID, Key: pick.Proposal.Question, Proposal: pick.Proposal}
}

// Answer applies a person's decision to one queued item and removes it.
//
// Only the seat that was asked may answer, and only a person may: an agent
// "answering" its own question would close the loop it exists to open.
func (s *Store) Answer(v View, actor Actor, pendingID string, r Resolution) (ApplyResult, error) {
	if !v.valid() {
		return ApplyResult{}, ErrViewRequired
	}
	if actor != ActorUser {
		return ApplyResult{}, ErrResolutionRequired
	}
	s.mu.RLock()
	item, ok := s.pending[pendingID]
	var p Proposal
	if ok && item.TeamID == v.TeamID && item.SeatID == v.SeatID {
		p = item.Proposal
	} else {
		ok = false
	}
	s.mu.RUnlock()
	if !ok {
		return ApplyResult{}, ErrNotFound
	}

	res, err := s.Apply(v, ActorUser, []Proposal{p}, map[int]Resolution{0: r})
	if err != nil {
		return res, err
	}
	// Note the ordering: the item is removed only AFTER Apply returned without
	// error. A reply that does not fit the question (accepting a "change" when
	// the question was a merge) errors above and leaves the item queued - a
	// swallowed question never comes back, and the user believes they answered.
	s.mu.Lock()
	delete(s.pending, pendingID)
	delete(s.byKey, v.SeatID+"|pending|"+pendingKey(p))
	s.mu.Unlock()
	return res, nil
}

// DropPending removes a queued decision without applying it - the user said
// "never mind". It is a separate call from Answer so that "I do not want to
// decide" cannot be mistaken in the record for "I decided".
func (s *Store) DropPending(v View, pendingID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.pending[pendingID]
	if !ok || item.TeamID != v.TeamID || item.SeatID != v.SeatID {
		return false
	}
	delete(s.pending, pendingID)
	delete(s.byKey, v.SeatID+"|pending|"+pendingKey(item.Proposal))
	return true
}
