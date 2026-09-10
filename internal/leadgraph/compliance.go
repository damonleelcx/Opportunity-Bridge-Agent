package leadgraph

import (
	"sort"
	"strings"
	"time"
)

// The parts a regulator, a customer's legal team, or a person who found
// themselves in this database would ask about.
//
// WHY THIS SHIPS WITH THE FETCHING AND NOT AFTER IT
//
//	Everything else in this product can be added later. This cannot: the moment
//	path two starts pulling public sources, records about people who never met
//	this service begin accumulating, and the ability to answer "what do you hold
//	about me, and delete it" has to already exist. Building it afterwards means
//	the first request arrives before the mechanism does.

// Audit actions. A closed list, because the point of an audit trail is that
// somebody can enumerate what is in it.
const (
	AuditExport        = "export"
	AuditSubjectAccess = "subject_access"
	AuditSubjectDelete = "subject_delete"
	AuditFetchRefused  = "fetch_refused"
)

// AuditEntry is one thing worth being able to account for later.
type AuditEntry struct {
	ID     string            `json:"id"`
	TeamID string            `json:"team_id"`
	SeatID string            `json:"seat_id"`
	Action string            `json:"action"`
	At     time.Time         `json:"at"`
	Detail map[string]string `json:"detail,omitempty"`
}

// audit appends an entry. Callers must NOT hold the lock.
func (s *Store) audit(v View, action string, detail map[string]string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if at.IsZero() {
		at = s.now()
	}
	s.auditLog = append(s.auditLog, AuditEntry{
		ID: s.nextID("au"), TeamID: v.TeamID, SeatID: v.SeatID,
		Action: action, At: at, Detail: detail,
	})
}

// AuditTrail returns a team's entries, oldest first.
func (s *Store) AuditTrail(v View) []AuditEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []AuditEntry
	for _, e := range s.auditLog {
		if e.TeamID == v.TeamID {
			out = append(out, e)
		}
	}
	return out
}

// ---- provenance ----

// ProvenanceReport answers "where did this come from" for one record, in the
// form somebody can check: the evidence itself, the documents behind it, and
// every change the record has been through.
type ProvenanceReport struct {
	NodeID        string        `json:"node_id"`
	Label         string        `json:"label"`
	Intel         []Intel       `json:"intel"`
	Observations  []Observation `json:"observations"`
	History       []FieldChange `json:"history"`
	Corroboration Corroboration `json:"corroboration"`
}

// Provenance assembles the answer for one node.
func (s *Store) Provenance(v View, nodeID string) (ProvenanceReport, bool) {
	n, ok := s.Node(v, nodeID)
	if !ok {
		return ProvenanceReport{}, false
	}
	rep := ProvenanceReport{
		NodeID: n.ID, Label: n.Label, Intel: n.Intel, History: n.History,
		Corroboration: n.Corroboration(), Observations: []Observation{},
	}
	urls := map[string]bool{}
	for _, i := range n.Intel {
		if i.SourceURL != "" {
			urls[i.SourceURL] = true
		}
	}
	for _, o := range s.Observations(v, "") {
		if urls[o.URL] || contains(o.Produced, nodeID) {
			rep.Observations = append(rep.Observations, o)
		}
	}
	return rep, true
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// ---- subject rights ----

// SubjectMatch identifies a person the way a request would arrive: by name and
// company, because that is what the person themselves would say.
type SubjectMatch struct {
	Org   string
	Label string
}

func (m SubjectMatch) matches(n Node) bool {
	if n.Kind != KindPerson {
		return false
	}
	if norm(n.Label) != norm(m.Label) {
		return false
	}
	return m.Org == "" || norm(n.Org) == norm(m.Org)
}

// SubjectRecord is everything one team holds about one person.
type SubjectRecord struct {
	TeamID       string        `json:"team_id"`
	Node         Node          `json:"node"`
	Edges        []Edge        `json:"edges"`
	Touchpoints  []Touchpoint  `json:"touchpoints"`
	Observations []Observation `json:"observations"`
	// PrivateNotes are the per-seat impressions. They are INCLUDED: the right
	// of access is the subject's, and a note about somebody is information about
	// them however privately it was written.
	//
	// This is a real tension - a consultant's private opinion becoming
	// disclosable changes what they are willing to write down - and it is
	// resolved in the subject's favour here because that is what the law does.
	// Flagged in the PRD as a decision worth taking to counsel.
	PrivateNotes []Annotation `json:"private_notes"`
}

// SubjectRecords is the ONE read in this package that crosses the team
// boundary.
//
//	A data subject's rights are about them, not about our tenancy model. Somebody
//	asking "what do you hold about me" cannot be expected to name the teams that
//	might hold it, and answering only for one would be answering falsely.
//
//	It takes no View for the same reason: there is no seat this is done "as".
//	It is an operator action, and it writes an audit entry for every team it
//	touched.
func (s *Store) SubjectRecords(m SubjectMatch, at time.Time) []SubjectRecord {
	s.mu.RLock()
	var hits []*Node
	for _, n := range s.nodes {
		if m.matches(*n) {
			hits = append(hits, n)
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].ID < hits[j].ID })
	s.mu.RUnlock()

	out := []SubjectRecord{}
	for _, n := range hits {
		team := View{TeamID: n.TeamID, SeatID: "__subject_request__"}
		rec := SubjectRecord{
			TeamID: n.TeamID, Node: *n,
			Edges:        s.Edges(team, EdgeFilter{Endpoint: n.ID}),
			Touchpoints:  s.Touchpoints(team, n.ID),
			Observations: []Observation{},
			PrivateNotes: []Annotation{},
		}
		if p, ok := s.Provenance(team, n.ID); ok {
			rec.Observations = p.Observations
		}
		rec.PrivateNotes = s.annotationsAbout(n.ID, rec.Edges, rec.Touchpoints)
		out = append(out, rec)
		s.audit(team, AuditSubjectAccess, map[string]string{"subject": n.Label, "org": n.Org}, at)
	}
	return out
}

// annotationsAbout gathers every seat's overlay on a person and on everything
// that names them.
func (s *Store) annotationsAbout(nodeID string, edges []Edge, touches []Touchpoint) []Annotation {
	targets := map[string]bool{nodeID: true}
	for _, e := range edges {
		targets[e.ID] = true
	}
	for _, t := range touches {
		targets[t.ID] = true
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []Annotation{}
	for _, seat := range s.notes {
		for id, a := range seat {
			if targets[id] {
				out = append(out, *a)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SeatID != out[j].SeatID {
			return out[i].SeatID < out[j].SeatID
		}
		return out[i].TargetID < out[j].TargetID
	})
	return out
}

// SubjectDeletion is the receipt: what went, per team. A deletion nobody can
// evidence is indistinguishable from a deletion that did not happen, and the
// person asking has no way to check.
type SubjectDeletion struct {
	TeamID       string `json:"team_id"`
	Node         string `json:"node"`
	Edges        int    `json:"edges"`
	Touchpoints  int    `json:"touchpoints"`
	Annotations  int    `json:"annotations"`
	Observations int    `json:"observations"`
}

// ForgetSubject erases a person everywhere, and says what it erased.
//
// Observations produced by a document that named this person go too. The limit
// of that is worth stating: a document we fetched and kept an excerpt of, which
// mentioned them but produced nothing, is not found by this - we index what a
// document produced, not who it mentioned. That gap is named in the PRD rather
// than papered over with a substring search that would quietly miss just as
// much while looking thorough.
func (s *Store) ForgetSubject(m SubjectMatch, at time.Time) []SubjectDeletion {
	s.mu.RLock()
	var hits []*Node
	for _, n := range s.nodes {
		if m.matches(*n) {
			hits = append(hits, n)
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].ID < hits[j].ID })
	s.mu.RUnlock()

	out := []SubjectDeletion{}
	for _, n := range hits {
		team := View{TeamID: n.TeamID, SeatID: "__subject_request__"}
		d := SubjectDeletion{TeamID: n.TeamID, Node: n.ID}
		d.Edges = len(s.Edges(team, EdgeFilter{Endpoint: n.ID}))
		d.Touchpoints = len(s.Touchpoints(team, n.ID))
		d.Annotations = len(s.annotationsAbout(n.ID, s.Edges(team, EdgeFilter{Endpoint: n.ID}), s.Touchpoints(team, n.ID)))

		s.mu.Lock()
		for id, o := range s.obs {
			if o.TeamID != n.TeamID || !contains(o.Produced, n.ID) {
				continue
			}
			delete(s.byKey, observationKey(*o))
			delete(s.obs, id)
			d.Observations++
		}
		for id, a := range s.alerts {
			if a.TeamID != n.TeamID {
				continue
			}
			kept := a.People[:0]
			for _, p := range a.People {
				if p.NodeID != n.ID {
					kept = append(kept, p)
				}
			}
			a.People = kept
			if len(a.People) == 0 && strings.TrimSpace(a.EventLabel) == "" {
				delete(s.alerts, id)
			}
		}
		s.mu.Unlock()

		s.Forget(team, n.ID)
		out = append(out, d)
		s.audit(team, AuditSubjectDelete, map[string]string{
			"subject": n.Label, "org": n.Org, "node": n.ID,
		}, at)
	}
	return out
}

// ---- export ----

// ExportBundle is what leaves the building. Only the requesting seat's own
// private notes are in it: an export is the easiest way for one seat to walk
// out with everybody else's judgements, and it is the least visible.
type ExportBundle struct {
	TeamID       string        `json:"team_id"`
	SeatID       string        `json:"seat_id"`
	At           time.Time     `json:"at"`
	Nodes        []Node        `json:"nodes"`
	Edges        []Edge        `json:"edges"`
	Touchpoints  []Touchpoint  `json:"touchpoints"`
	Observations []Observation `json:"observations"`
}

// Export builds the bundle and records that it happened. The audit entry is not
// optional decoration: "who took a copy, and when" is the first question after
// a leak, and it can only be answered if it was written down at the time.
func (s *Store) Export(v View, at time.Time) (ExportBundle, error) {
	if !v.valid() {
		return ExportBundle{}, ErrViewRequired
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	b := ExportBundle{
		TeamID: v.TeamID, SeatID: v.SeatID, At: at,
		Nodes: s.Nodes(v, NodeFilter{}), Edges: s.Edges(v, EdgeFilter{}),
		Observations: s.Observations(v, ""),
		Touchpoints:  []Touchpoint{},
	}
	for _, n := range b.Nodes {
		if n.Kind == KindPerson {
			b.Touchpoints = append(b.Touchpoints, s.Touchpoints(v, n.ID)...)
		}
	}
	s.audit(v, AuditExport, map[string]string{
		"nodes": itoa(len(b.Nodes)), "edges": itoa(len(b.Edges)),
		"touchpoints": itoa(len(b.Touchpoints)),
	}, at)
	return b, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}
