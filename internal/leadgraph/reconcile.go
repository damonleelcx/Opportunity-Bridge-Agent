package leadgraph

import (
	"sort"
	"strings"
)

// Reconciliation: comparing what the agent just heard against what the graph
// already holds, and saying which of four things it is.
//
// WHY THIS IS A SEPARATE, READ-ONLY FUNCTION AND NOT PART OF WRITING
//
//	If the model decided "are these two 王五 the same person" while writing,
//	that judgement would be unreproducible: the same conversation twice could
//	produce a different graph, the user's records would drift for reasons
//	nobody could reconstruct, and - worst - it could not be tested. Reconcile is
//	a pure function of (graph, candidates). Given the same two, it returns the
//	same proposals, byte for byte, forever. The model's job shrinks to two
//	things it is actually good at: turning speech into candidate facts, and
//	explaining a conflict to a person.
//
// WHY IT PROPOSES RATHER THAN DECIDES
//
//	Two of the four outcomes are one-way doors. A wrong merge turns two people
//	into one and destroys the evidence that there were ever two; a wrong
//	overwrite replaces a fact the user will act on weeks later. Those go to a
//	person, with the reason written out, every time.

// Decision is what Reconcile concluded about one candidate.
type Decision string

const (
	// DecideCreate - nothing in the graph resembles this. Safe to apply.
	DecideCreate Decision = "create"
	// DecideUpdate - the exact same record exists and nothing it already holds
	// disagrees. Safe to apply: this is new detail, not a changed fact.
	DecideUpdate Decision = "update"
	// DecideMerge - something in the graph looks like this, by a named rule.
	// Needs a person.
	DecideMerge Decision = "merge"
	// DecideConflict - the same record exists and holds a different value.
	// Needs a person.
	DecideConflict Decision = "conflict"
)

// FieldConflict is one value the graph holds and the candidate contradicts.
type FieldConflict struct {
	Field    string `json:"field"`
	Current  string `json:"current"`
	Proposed string `json:"proposed"`
}

// MergeCandidate is an existing record that might be the same thing, and the
// named rules that made it a candidate.
//
// Reasons are stable keys, not sentences: the interface renders them in the
// reader's language, and a test can assert on them without matching prose.
type MergeCandidate struct {
	NodeID  string   `json:"node_id"`
	Label   string   `json:"label"`
	Reasons []string `json:"reasons"`
}

// Proposal is one candidate, judged.
type Proposal struct {
	Decision  Decision         `json:"decision"`
	Candidate Node             `json:"candidate"`
	TargetID  string           `json:"target_id,omitempty"`
	Conflicts []FieldConflict  `json:"conflicts,omitempty"`
	Merges    []MergeCandidate `json:"merges,omitempty"`
	// Question is the i18n key of what to ask when a person has to choose.
	Question string `json:"question,omitempty"`
	// SuggestedEvent is the graph's default reading of a conflict: THE WORLD
	// CHANGED, not "the user misremembered". It is a proposal, not a write.
	//
	// Why that default: this product's entire value is tracking change. Reading
	// a change as a correction deletes the most valuable class of data it has.
	SuggestedEvent *Node `json:"suggested_event,omitempty"`
}

// Question keys. Kept here so the interface and the tests read the same list.
const (
	QuestionMergeOrNew       = "question.merge_or_new"
	QuestionChangedOrMistake = "question.changed_or_mistaken"
)

// Merge rule keys, in the order they are evaluated. Each one names a reason a
// human can agree or disagree with; there is deliberately no score, because a
// number would be a judgement nobody can argue with.
const (
	RuleSameOrgSameUnitSharedSurname = "same_org_same_unit_shared_surname"
	RuleSameOrgSameRoleSharedSurname = "same_org_same_role_shared_surname"
	RuleSameOrgLabelContained        = "same_org_label_contained"
	// RuleSameOrgSameLabelOtherPath - the same name in the same company, sitting
	// somewhere else in the chart.
	//
	// This one exists because of a silent failure, not a hypothetical. The user
	// records 「c业务组」 under 技术中心; a job advert produces 「c业务组」 directly
	// under the company. Different unit paths mean different natural keys, so
	// the store correctly refused to treat them as one record - and reconcile
	// used to SKIP same-label candidates entirely, on the assumption that an
	// identical label always meant an exact key match. It does not. The result
	// was a second copy of the group appearing with nobody noticing, which is
	// what put the same group on the lead board twice.
	RuleSameOrgSameLabelOtherPath = "same_org_same_label_other_path"
)

// Reconcile compares candidates against the graph. It writes nothing.
//
// Proposals come back in the order the candidates were given, and everything
// inside each proposal is sorted, so that the same input produces byte-identical
// output. Fenced by TestReconcileIsDeterministic.
func (s *Store) Reconcile(v View, candidates []Node) []Proposal {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Proposal, 0, len(candidates))
	for _, c := range candidates {
		c.TeamID = v.TeamID
		c.Note = "" // a private note is never part of identity
		p := Proposal{Decision: DecideCreate, Candidate: c}

		if id, ok := s.byKey[naturalKey(c)]; ok {
			cur := s.nodes[id]
			p.TargetID = id
			p.Conflicts = conflictsBetween(*cur, c)
			if len(p.Conflicts) > 0 {
				p.Decision = DecideConflict
				p.Question = QuestionChangedOrMistake
				p.SuggestedEvent = suggestedEvent(*cur, c, p.Conflicts)
			} else {
				p.Decision = DecideUpdate
			}
			out = append(out, p)
			continue
		}

		if m := s.mergeCandidates(v, c); len(m) > 0 {
			p.Decision = DecideMerge
			p.Merges = m
			p.Question = QuestionMergeOrNew
		}
		out = append(out, p)
	}
	return out
}

// conflictsBetween lists the fields the graph holds and the candidate
// contradicts. A blank candidate field is never a conflict: "not mentioned" is
// not "cleared".
func conflictsBetween(cur, in Node) []FieldConflict {
	pairs := []struct{ field, a, b string }{
		{"org", cur.Org, in.Org},
		{"role_title", cur.RoleTitle, in.RoleTitle},
		{"duty", cur.Duty, in.Duty},
		{"unit_path", strings.Join(cur.UnitPath, ">"), strings.Join(in.UnitPath, ">")},
	}
	var out []FieldConflict
	for _, p := range pairs {
		b := strings.TrimSpace(p.b)
		if b == "" || p.a == "" || p.a == b {
			continue
		}
		out = append(out, FieldConflict{Field: p.field, Current: p.a, Proposed: b})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Field < out[j].Field })
	return out
}

// suggestedEvent turns a conflict into the event it probably is. Nothing is
// written; this is what the interface offers the user as the first option.
func suggestedEvent(cur, in Node, cs []FieldConflict) *Node {
	if len(cs) == 0 {
		return nil
	}
	c := cs[0]
	label := cur.Label + " " + c.Field + " " + c.Current + " → " + c.Proposed
	ev := Node{
		TeamID:   cur.TeamID,
		Kind:     KindEvent,
		Label:    label,
		Org:      cur.Org,
		UnitPath: cur.UnitPath,
		Intel:    in.Intel,
	}
	return &ev
}

// mergeCandidates finds existing records that might be the same thing.
//
// Every rule needs TWO signals. A shared surname alone matches a third of the
// people in any Chinese company; a shared surname inside the same group of the
// same company is a question worth asking. That is the whole design: never one
// weak signal, always a pair, and always say which pair.
func (s *Store) mergeCandidates(v View, c Node) []MergeCandidate {
	if c.Kind != KindPerson && c.Kind != KindUnit && c.Kind != KindOrg {
		return nil
	}
	var out []MergeCandidate
	for _, n := range s.nodesLocked(v, NodeFilter{Kind: c.Kind}) {
		if n.ID == "" {
			continue
		}
		// An identical label is NOT automatically an exact key match: the key
		// also carries the unit path. Reaching here at all means no exact match
		// existed, so a same-label record is a candidate, not a duplicate of
		// something already handled.
		reasons := matchReasons(n, c)
		if len(reasons) == 0 {
			continue
		}
		sort.Strings(reasons)
		out = append(out, MergeCandidate{NodeID: n.ID, Label: n.Label, Reasons: reasons})
	}
	// nodesLocked already returns id order, so this is stable; sorting again
	// keeps that true if the read ever changes.
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

func matchReasons(existing, c Node) []string {
	var reasons []string
	sameOrg := existing.Org != "" && norm(existing.Org) == norm(c.Org)

	// Same name, same company, different place in the chart. Strong for every
	// kind: two groups in one company rarely share a name, and when they do,
	// that is precisely a question for a person rather than a silent second
	// record.
	if sameOrg && norm(existing.Label) == norm(c.Label) &&
		norm(strings.Join(existing.UnitPath, ">")) != norm(strings.Join(c.UnitPath, ">")) {
		return []string{RuleSameOrgSameLabelOtherPath}
	}

	if c.Kind == KindPerson {
		if !sameOrg || surname(existing.Label) == "" || surname(existing.Label) != surname(c.Label) {
			return nil
		}
		// One address form has to be a shortening of the other: 王总 -> 王 is a
		// prefix of 王五. 王五 and 王六 share a surname and are two people.
		a, b := nameCore(existing.Label), nameCore(c.Label)
		if !strings.HasPrefix(a, b) && !strings.HasPrefix(b, a) {
			return nil
		}
		if norm(strings.Join(existing.UnitPath, ">")) == norm(strings.Join(c.UnitPath, ">")) &&
			len(c.UnitPath) > 0 {
			reasons = append(reasons, RuleSameOrgSameUnitSharedSurname)
		}
		if existing.RoleTitle != "" && norm(existing.RoleTitle) == norm(c.RoleTitle) {
			reasons = append(reasons, RuleSameOrgSameRoleSharedSurname)
		}
		return reasons
	}

	// Units and orgs get shortened rather than nicknamed: "c业务组" and "c组".
	a, b := norm(existing.Label), norm(c.Label)
	if a == "" || b == "" || a == b {
		return nil
	}
	if c.Kind == KindUnit && !sameOrg {
		return nil
	}
	if strings.Contains(a, b) || strings.Contains(b, a) {
		reasons = append(reasons, RuleSameOrgLabelContained)
	}
	return reasons
}
