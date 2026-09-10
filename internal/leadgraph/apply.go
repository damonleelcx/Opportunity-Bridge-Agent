package leadgraph

import (
	"fmt"
	"strings"
	"time"
)

// Applying reconciliation proposals.
//
// WHY APPLY IS A SECOND TOOL RATHER THAN A FLAG ON RECONCILE
//
//	Two of the four decisions are one-way doors and must be answered by a
//	person. Splitting the tools makes that structural: the thing that judges
//	cannot write, and the thing that writes cannot judge. A single tool with an
//	autoApply flag would put the whole guarantee behind one boolean that some
//	caller eventually passes true to.
//
// WHAT HAPPENS TO A PROPOSAL NOBODY ANSWERED
//
//	It is returned in Pending and NOTHING is written for it. Not an error -
//	the other proposals in the batch are usually fine and blocking them would
//	teach callers to answer everything reflexively, which is the opposite of
//	the point.

// Choice is a person's answer to a proposal they had to decide.
type Choice string

const (
	// ChooseCreateNew - "these are two different people/units". Records the
	// candidate as its own node.
	ChooseCreateNew Choice = "create_new"
	// ChooseMergeInto - "same one". Folds the candidate into the named record.
	ChooseMergeInto Choice = "merge_into"
	// ChooseAcceptChange - "the world changed". Applies the new value and
	// records the event, keeping the old value in history.
	ChooseAcceptChange Choice = "accept_change"
	// ChooseKeepCurrent - "that report is wrong". Changes nothing, and writes
	// down that the claim was made and rejected.
	ChooseKeepCurrent Choice = "keep_current"
)

// Resolution is one answer, keyed by the proposal's index in the batch.
type Resolution struct {
	Choice      Choice `json:"choice"`
	MergeIntoID string `json:"merge_into_id,omitempty"`
}

// ApplyResult says what actually happened, per proposal index, so the caller
// can report it truthfully rather than saying "done".
type ApplyResult struct {
	Created  []string `json:"created,omitempty"`  // node ids
	Updated  []string `json:"updated,omitempty"`  // node ids
	Rejected []string `json:"rejected,omitempty"` // node ids where a claim was turned down
	Pending  []int    `json:"pending,omitempty"`  // proposals still waiting on a person
}

// Apply writes the proposals that need no judgement, plus the ones a person has
// answered. It takes no lock of its own: every write below goes through the
// same guarded entry points a caller would use directly, so nothing here can
// bypass a refusal.
func (s *Store) Apply(v View, actor Actor, ps []Proposal, res map[int]Resolution) (ApplyResult, error) {
	if !v.valid() {
		return ApplyResult{}, ErrViewRequired
	}
	var out ApplyResult
	for i, p := range ps {
		r, answered := res[i]

		switch p.Decision {
		case DecideCreate, DecideUpdate:
			n, err := s.UpsertNode(v, actor, p.Candidate)
			if err != nil {
				return out, fmt.Errorf("proposal %d: %w", i, err)
			}
			if p.Decision == DecideCreate {
				out.Created = append(out.Created, n.ID)
			} else {
				out.Updated = append(out.Updated, n.ID)
			}

		case DecideMerge:
			if !answered {
				out.Pending = append(out.Pending, i)
				continue
			}
			switch r.Choice {
			case ChooseCreateNew:
				n, err := s.UpsertNode(v, actor, p.Candidate)
				if err != nil {
					return out, fmt.Errorf("proposal %d: %w", i, err)
				}
				out.Created = append(out.Created, n.ID)
			case ChooseMergeInto:
				if r.MergeIntoID == "" {
					return out, fmt.Errorf("proposal %d: %w", i, ErrResolutionRequired)
				}
				// A person said these are the same, so the fold happens as the
				// person: it may fill blanks AND replace values, which the agent
				// may never do.
				n, err := s.foldInto(v, r.MergeIntoID, p.Candidate)
				if err != nil {
					return out, fmt.Errorf("proposal %d: %w", i, err)
				}
				out.Updated = append(out.Updated, n.ID)
			default:
				return out, fmt.Errorf("proposal %d: %w: %q does not answer a merge", i, ErrResolutionRequired, r.Choice)
			}

		case DecideConflict:
			if !answered {
				out.Pending = append(out.Pending, i)
				continue
			}
			switch r.Choice {
			case ChooseAcceptChange:
				n, err := s.UpsertNode(v, ActorUser, p.Candidate)
				if err != nil {
					return out, fmt.Errorf("proposal %d: %w", i, err)
				}
				out.Updated = append(out.Updated, n.ID)
				if p.SuggestedEvent != nil {
					ev, err := s.UpsertNode(v, ActorUser, *p.SuggestedEvent)
					if err != nil {
						return out, fmt.Errorf("proposal %d event: %w", i, err)
					}
					out.Created = append(out.Created, ev.ID)
				}
			case ChooseKeepCurrent:
				if err := s.rejectClaim(v, p.TargetID, p.Conflicts, p.Candidate.Intel); err != nil {
					return out, fmt.Errorf("proposal %d: %w", i, err)
				}
				out.Rejected = append(out.Rejected, p.TargetID)
			default:
				return out, fmt.Errorf("proposal %d: %w: %q does not answer a conflict", i, ErrResolutionRequired, r.Choice)
			}
		}
	}
	return out, nil
}

// foldInto merges a candidate's facts into an existing record because a person
// said they are the same thing. The candidate's own label is kept as evidence -
// otherwise "we decided 王总 is 王五" leaves no trace that 王总 was ever seen.
func (s *Store) foldInto(v View, keepID string, in Node) (Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.nodes[keepID]
	if !ok || cur.TeamID != v.TeamID {
		return Node{}, ErrNotFound
	}
	if cur.Kind != in.Kind {
		return Node{}, ErrNotMergeable
	}
	if len(in.Intel) == 0 {
		return Node{}, ErrIntelRequired
	}
	now := s.now()
	because := in.Intel[0]

	changes, err := mergeNodeFields(cur, in, ActorUser, because, now)
	if err != nil {
		return Node{}, err
	}
	if norm(in.Label) != norm(cur.Label) {
		changes = append(changes, FieldChange{
			At: now, Field: "also_known_as", To: in.Label, By: ActorUser, Why: because,
		})
	}
	cur.Intel = mergeIntel(cur.Intel, in.Intel)
	cur.History = append(cur.History, changes...)
	cur.UpdatedAt = now
	normaliseNode(cur)
	if err := s.putNode(cur); err != nil {
		return Node{}, err
	}
	return s.project(v, *cur), nil
}

// rejectClaim records that something was asserted and turned down, without
// changing any value and WITHOUT adding the claim's intel to the record.
//
// The second half is the part that matters: intel raises corroboration, and a
// rejected rumour that quietly strengthened the record it failed to change
// would be the exact failure this whole design is built to avoid.
func (s *Store) rejectClaim(v View, targetID string, cs []FieldConflict, why []Intel) error {
	if len(why) == 0 {
		return ErrIntelRequired
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nodes[targetID]
	if !ok || n.TeamID != v.TeamID {
		return ErrNotFound
	}
	now := s.now()
	for _, c := range cs {
		n.History = append(n.History, FieldChange{
			At: now, Field: c.Field, From: c.Current, To: c.Proposed,
			By: ActorUser, Why: why[0], Rejected: true,
		})
	}
	n.UpdatedAt = now
	return s.putNode(n)
}

// MergeNodes joins two records that both already exist, because a person said
// they are the same thing. This is the one genuinely destructive operation in
// the package: one id stops existing.
//
// It is refused to the agent. Not because the agent is worse at spotting
// duplicates, but because being wrong here is unrecoverable - two people become
// one and the evidence that they were two goes with the deleted record.
func (s *Store) MergeNodes(v View, actor Actor, keepID, dropID string, because Intel) (Node, error) {
	if !v.valid() {
		return Node{}, ErrViewRequired
	}
	if actor != ActorUser {
		return Node{}, ErrResolutionRequired
	}
	if err := because.validate(); err != nil {
		return Node{}, err
	}
	if keepID == dropID {
		return Node{}, ErrNotMergeable
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	keep, ok := s.nodes[keepID]
	if !ok || keep.TeamID != v.TeamID {
		return Node{}, ErrNotFound
	}
	drop, ok := s.nodes[dropID]
	if !ok || drop.TeamID != v.TeamID {
		return Node{}, ErrNotFound
	}
	if keep.Kind != drop.Kind {
		return Node{}, ErrNotMergeable
	}
	now := s.now()

	// Facts: fill blanks only. A merge must not silently pick a winner between
	// two stated values - if both are set and they differ, the survivor keeps
	// its own and the loser's is preserved in history.
	for _, f := range []struct {
		name string
		keep *string
		drop string
	}{
		{"org", &keep.Org, drop.Org},
		{"role_title", &keep.RoleTitle, drop.RoleTitle},
		{"duty", &keep.Duty, drop.Duty},
	} {
		if f.drop == "" || f.drop == *f.keep {
			continue
		}
		if *f.keep == "" {
			keep.History = append(keep.History, FieldChange{At: now, Field: f.name, To: f.drop, By: actor, Why: because})
			*f.keep = f.drop
			continue
		}
		keep.History = append(keep.History, FieldChange{
			At: now, Field: f.name, From: f.drop, To: *f.keep, By: actor, Why: because,
			Rejected: true, // the dropped record's value did not win; it is not lost either
		})
	}
	if len(keep.UnitPath) == 0 && len(drop.UnitPath) > 0 {
		keep.UnitPath = drop.UnitPath
	}
	keep.Intel = mergeIntel(keep.Intel, drop.Intel)
	keep.History = append(keep.History, drop.History...)
	keep.History = append(keep.History, FieldChange{
		At: now, Field: "merged_from", From: drop.Label, To: keep.Label, By: actor, Why: because,
	})
	keep.UpdatedAt = now
	normaliseNode(keep)

	s.repointEdges(v.TeamID, dropID, keepID, because, now)
	s.moveAnnotations(dropID, keepID)

	delete(s.byKey, naturalKey(*drop))
	delete(s.nodes, dropID)
	if err := s.putNode(keep); err != nil {
		return Node{}, err
	}
	// Everything the merge touched, written together: the survivor above, the
	// lines that moved, the overlays that followed them, and the record that
	// stopped existing.
	for _, e := range s.edges {
		if e.TeamID == v.TeamID && (e.From == keepID || e.To == keepID) {
			_ = s.putEdge(e)
		}
	}
	for _, seat := range s.notes {
		if a, ok := seat[keepID]; ok {
			_ = s.putAnnotation(a)
		}
	}
	_ = s.deleteRecord("lead_nodes", "id = $1", dropID)
	_ = s.deleteRecord("lead_annotations", "target_id = $1", dropID)
	return s.project(v, *keep), nil
}

// repointEdges moves every line that touched the dropped node onto the
// survivor, folding duplicates and dropping lines that would now point a node
// at itself. Callers hold the write lock.
func (s *Store) repointEdges(teamID, dropID, keepID string, because Intel, now time.Time) {
	for id, e := range s.edges {
		if e.TeamID != teamID || (e.From != dropID && e.To != dropID) {
			continue
		}
		old := edgeKey(*e)
		if e.From == dropID {
			e.From = keepID
		}
		if e.To == dropID {
			e.To = keepID
		}
		delete(s.byKey, old)

		// A relationship with oneself is not a fact, it is an artefact of the
		// merge.
		if e.From == e.To {
			delete(s.edges, id)
			s.dropAnnotations(id)
			continue
		}
		key := edgeKey(*e)
		if other, exists := s.byKey[key]; exists && other != id {
			survivor := s.edges[other]
			survivor.Intel = mergeIntel(survivor.Intel, e.Intel)
			survivor.History = append(survivor.History, e.History...)
			survivor.History = append(survivor.History, FieldChange{
				At: now, Field: "merged_from", From: id, To: other, By: ActorUser, Why: because,
			})
			survivor.UpdatedAt = now
			delete(s.edges, id)
			s.dropAnnotations(id)
			continue
		}
		e.UpdatedAt = now
		s.byKey[key] = id
	}
}

// moveAnnotations carries each seat's private overlay across a merge. A seat
// that annotated both keeps its own note on the survivor: nobody else's
// judgement may overwrite it, and this seat already stated its view.
func (s *Store) moveAnnotations(dropID, keepID string) {
	for _, seat := range s.notes {
		from, ok := seat[dropID]
		if !ok {
			continue
		}
		to, exists := seat[keepID]
		if !exists {
			from.TargetID = keepID
			seat[keepID] = from
		} else {
			if to.Strength == 0 {
				to.Strength = from.Strength
			}
			if strings.TrimSpace(to.Note) == "" {
				to.Note = from.Note
			}
		}
		delete(seat, dropID)
	}
}
