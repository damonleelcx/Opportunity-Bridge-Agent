package leadgraph

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"
)

// Path two: the daily pass that tells the user something they did not already
// know, and the ledger that makes every claim it produces traceable.
//
// ONE RECORD SERVES TWO PHASES
//
//	P6 needs to know what a source said last time, to notice that a group has
//	stopped advertising. P9 needs to know where every fact came from, to answer
//	"who told you that" and a subject access request. Both are the same record:
//	one row per fetched document, kept whether or not it produced anything.
//
//	Keeping the ones that produced nothing is the point. "We looked and there
//	was nothing" and "we never looked" are different answers, and only the
//	ledger can tell them apart.

// Observation is one fetched document, and what became of it.
type Observation struct {
	ID       string     `json:"id"`
	TeamID   string     `json:"team_id"`
	Source   string     `json:"source"`
	URL      string     `json:"url"`
	Kind     DocKind    `json:"kind"`
	Org      string     `json:"org"`
	Unit     string     `json:"unit,omitempty"`
	Title    string     `json:"title,omitempty"`
	Excerpt  string     `json:"excerpt"`
	PostedAt *time.Time `json:"posted_at,omitempty"`
	At       time.Time  `json:"at"`
	// Digest is a hash of the document text, so that the SAME url returning
	// different content is visible as a change rather than silently replacing
	// what was there.
	Digest string `json:"digest"`
	// Produced lists the node ids this document created or updated. Empty is a
	// real and useful answer.
	Produced []string `json:"produced,omitempty"`
}

// excerptLimit keeps a quotable fragment rather than a whole page. Long enough
// to be checkable against the source, short enough that the ledger is not a
// second copy of the internet.
const excerptLimit = 280

func digestOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

func excerpt(d Document) string {
	t := strings.TrimSpace(d.Title)
	body := strings.TrimSpace(d.Text)
	if t != "" && body != "" {
		t += " — " + body
	} else if t == "" {
		t = body
	}
	if r := []rune(t); len(r) > excerptLimit {
		return string(r[:excerptLimit])
	}
	return t
}

func observationKey(o Observation) string {
	return strings.Join([]string{o.TeamID, "obs", o.URL, o.Digest}, "|")
}

// record files one document in the ledger. The same document unchanged is one
// row however often it is seen; changed content is a new row, which is what
// makes a quiet edit at the source visible.
func (s *Store) record(v View, d Document, produced []string) Observation {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := Observation{
		TeamID: v.TeamID, Source: d.Kind.source(), URL: d.URL, Kind: d.Kind,
		Org: d.Org, Unit: d.Unit, Title: d.Title, Excerpt: excerpt(d),
		PostedAt: d.PostedAt, At: d.FetchedAt, Digest: digestOf(d.Text),
		Produced: produced,
	}
	key := observationKey(o)
	if id, ok := s.byKey[key]; ok {
		cur := s.obs[id]
		cur.At = d.FetchedAt
		cur.Produced = mergeStrings(cur.Produced, produced)
		_ = s.putObs(cur)
		return *cur
	}
	o.ID = s.nextID("ob")
	s.obs[o.ID] = &o
	s.byKey[key] = o.ID
	_ = s.putObs(&o)
	return o
}

func (k DocKind) source() string { return string(k) }

func mergeStrings(a, b []string) []string {
	seen := map[string]bool{}
	for _, x := range a {
		seen[x] = true
	}
	for _, x := range b {
		if !seen[x] {
			seen[x] = true
			a = append(a, x)
		}
	}
	sort.Strings(a)
	return a
}

// Observations lists the ledger for a team, newest first.
func (s *Store) Observations(v View, org string) []Observation {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Observation
	for _, o := range s.obs {
		if o.TeamID != v.TeamID {
			continue
		}
		if org != "" && norm(o.Org) != norm(org) {
			continue
		}
		out = append(out, *o)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.After(out[j].At)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// ---- extraction ----

// ExtractSignals turns fetched documents into candidate facts.
//
// DETERMINISTIC ON PURPOSE, AND ONLY FOR STRUCTURED KINDS
//
//	Every candidate quotes the document it came from. A registry filing or a
//	company notice IS an organisational event and becomes one; a job advert is
//	not an event at all - its signal is the pattern over time, which is computed
//	from the ledger by HiringGap rather than guessed at from one advert.
func ExtractSignals(docs []Document) []Node {
	out := []Node{}
	for _, d := range docs {
		switch d.Kind {
		case DocRegistryChange, DocAnnouncement:
			label := strings.TrimSpace(d.Title)
			if label == "" {
				continue
			}
			ev := Node{
				Kind: KindEvent, Label: label, Org: d.Org,
				OccurredAt: d.PostedAt,
				Intel: []Intel{{
					Kind: IntelPublicSource, SourceURL: d.URL, Excerpt: excerpt(d),
					FetchedAt: d.FetchedAt,
					// A registry filing and a company's own notice are the
					// company speaking. That is what Announced means, and it is
					// the only thing in this package that produces it.
					Announced: true,
				}},
			}
			if d.Unit != "" {
				ev.UnitPath = []string{d.Org, d.Unit}
			}
			out = append(out, ev)
		case DocJobPosting:
			if strings.TrimSpace(d.Unit) == "" {
				continue
			}
			// A job advert says a group exists and is hiring. It does not say
			// anything happened, so it produces a unit, never an event.
			out = append(out, Node{
				Kind: KindUnit, Label: d.Unit, Org: d.Org, UnitPath: []string{d.Org, d.Unit},
				Intel: []Intel{{
					Kind: IntelPublicSource, SourceURL: d.URL, Excerpt: excerpt(d),
					FetchedAt: d.FetchedAt,
				}},
			})
		}
	}
	return out
}

// HiringGap answers "how long since this group last advertised", from the
// ledger. Nil means it never has, in what we have looked at - which is not the
// same as never, and the caller must not render it as one.
func (s *Store) HiringGap(v View, org, unit string, now time.Time) *time.Duration {
	var last *time.Time
	for _, o := range s.Observations(v, org) {
		if o.Kind != DocJobPosting || norm(o.Unit) != norm(unit) {
			continue
		}
		at := o.At
		if o.PostedAt != nil {
			at = *o.PostedAt
		}
		if last == nil || at.After(*last) {
			last = &at
		}
	}
	if last == nil {
		return nil
	}
	d := now.Sub(*last)
	return &d
}

// ---- alerts ----

// Alert is "something moved, and you know people there".
//
// It states two facts the user already gave us - what happened, and who they
// know in that group - and stops. It carries no score and no prediction about
// anybody's intentions: see the PRD's §5.7. Ordering by "likelihood" would be
// the departure-probability ranking this product refuses to build.
type Alert struct {
	ID            string        `json:"id"`
	TeamID        string        `json:"team_id"`
	EventID       string        `json:"event_id"`
	EventLabel    string        `json:"event_label"`
	Org           string        `json:"org"`
	UnitPath      []string      `json:"unit_path,omitempty"`
	People        []ChartPerson `json:"people"`
	Corroboration Corroboration `json:"corroboration"`
	RaisedAt      time.Time     `json:"raised_at"`
	Ack           bool          `json:"ack"`
}

// RaiseAlert files one alert for an event, at most once ever.
//
// Once, because a repeated alert is a nudge to act, and this feature exists so
// nothing is missed - not to push anybody into a call.
func (s *Store) RaiseAlert(v View, eventID string, at time.Time) (Alert, bool) {
	ev, ok := s.Node(v, eventID)
	if !ok || ev.Kind != KindEvent {
		return Alert{}, false
	}
	s.mu.Lock()
	key := v.TeamID + "|alert|" + eventID
	if _, exists := s.byKey[key]; exists {
		s.mu.Unlock()
		return Alert{}, false
	}
	s.mu.Unlock()

	a := Alert{
		TeamID: v.TeamID, EventID: ev.ID, EventLabel: ev.Label, Org: ev.Org,
		UnitPath: ev.UnitPath, Corroboration: ev.Corroboration(), RaisedAt: at,
		People: []ChartPerson{},
	}
	if len(ev.UnitPath) > 0 {
		// Who is in that group comes from the chart, so an alert inherits the
		// rule that a closed membership is not resurrected by a stale path.
		c := s.OrgChart(v, ev.Org, at)
		a.People = peopleUnder(c.Root, ev.UnitPath)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	a.ID = s.nextID("al")
	s.alerts[a.ID] = &a
	s.byKey[key] = a.ID
	_ = s.putAlert(&a)
	return a, true
}

// peopleUnder collects everybody in the unit an event names, and below it.
//
// WHY IT MATCHES ON THE UNIT'S NAME AND NOT ON THE WHOLE PATH
//
//	An event names a company and a unit - "c业务组" at "A司" - because that is
//	all an extractor ever has. The chart holds that unit wherever the USER put
//	it, which is often deeper: A司 › 技术中心 › c业务组.
//
//	Matching the full path therefore found nothing exactly when the user had
//	told us MORE about the structure, which is backwards. It shipped that way
//	and the first look at a realistic graph caught it: the alert said "你图上
//	这个组还没有人" above three people who were in that group.
//
//	A name appearing in two places matches both. That ambiguity is real, and the
//	chart already reports it (AmbiguitySameLabelTwoPlaces) rather than having
//	this function silently pick one.
func peopleUnder(ns []ChartNode, want []string) []ChartPerson {
	out := []ChartPerson{}
	if len(want) == 0 {
		return out
	}
	target := norm(want[len(want)-1])
	var walk func(xs []ChartNode, inside bool)
	walk = func(xs []ChartNode, inside bool) {
		for _, x := range xs {
			hit := inside || norm(x.Label) == target
			if hit {
				out = append(out, x.People...)
			}
			walk(x.Children, hit)
		}
	}
	walk(ns, false)
	sortPeople(out)
	return out
}

// Alerts lists a team's alerts, newest first. Team-wide: a reorganisation at a
// company matters to everybody who works that company, not only to whoever's
// daily pass happened to notice it.
func (s *Store) Alerts(v View, includeAcked bool) []Alert {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Alert
	for _, a := range s.alerts {
		if a.TeamID != v.TeamID || (!includeAcked && a.Ack) {
			continue
		}
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].RaisedAt.Equal(out[j].RaisedAt) {
			return out[i].RaisedAt.After(out[j].RaisedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (s *Store) AckAlert(v View, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.alerts[id]
	if !ok || a.TeamID != v.TeamID {
		return false
	}
	a.Ack = true
	_ = s.putAlert(a)
	return true
}

// ---- staleness ----

// StaleItem is one thing worth going back to. Each carries what to do, because
// a list of problems with no next step gets read once.
type StaleItem struct {
	Kind    string    `json:"kind"` // unconfirmed_fields | cold_relationship | overdue_follow_up
	NodeID  string    `json:"node_id"`
	Label   string    `json:"label"`
	Fields  []string  `json:"fields,omitempty"`
	SinceAt time.Time `json:"since_at"`
}

const (
	StaleUnconfirmed  = "unconfirmed_fields"
	StaleColdContact  = "cold_relationship"
	StaleOverdueTouch = "overdue_follow_up"
)

// StaleScan finds what has gone quiet. Ordered, so the list is the same twice.
func (s *Store) StaleScan(v View, now time.Time, cold time.Duration) []StaleItem {
	out := []StaleItem{}
	for _, n := range s.Nodes(v, NodeFilter{Kind: KindPerson}) {
		if len(n.Unconfirmed) > 0 {
			out = append(out, StaleItem{
				Kind: StaleUnconfirmed, NodeID: n.ID, Label: n.Label,
				Fields: n.Unconfirmed, SinceAt: n.UpdatedAt,
			})
		}
		last := s.LastTouch(v, n.ID)
		if last != nil && now.Sub(last.At) > cold {
			out = append(out, StaleItem{
				Kind: StaleColdContact, NodeID: n.ID, Label: n.Label, SinceAt: last.At,
			})
		}
	}
	for _, t := range s.DueTouchpoints(v, now) {
		n, _ := s.Node(v, t.PersonID)
		out = append(out, StaleItem{
			Kind: StaleOverdueTouch, NodeID: t.PersonID, Label: n.Label, SinceAt: *t.DueAt,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if !out[i].SinceAt.Equal(out[j].SinceAt) {
			return out[i].SinceAt.Before(out[j].SinceAt)
		}
		return out[i].NodeID < out[j].NodeID
	})
	return out
}

// ---- the daily pass ----

// DailyReport is what one run of path two did. Everything it names is
// something the caller can show or act on; nothing is summarised away.
type DailyReport struct {
	Fetched      int            `json:"fetched"`
	Refused      []RefusedFetch `json:"refused,omitempty"`
	Observations []string       `json:"observations,omitempty"`
	Applied      []string       `json:"applied,omitempty"`
	Queued       []string       `json:"queued,omitempty"`
	Alerts       []string       `json:"alerts,omitempty"`
	Stale        []StaleItem    `json:"stale,omitempty"`
}

// RunDaily is path two.
//
// It runs AS A SEAT, not as a team, for one reason: anything it finds that
// needs judgement goes into that seat's queue, and a queue with no owner is a
// backlog nobody answers. What it produces for everybody - the alerts - is
// team-wide.
//
// It never asks a question. Path one asks, when the user is already talking;
// interrupting them because a scheduled job woke up is how a notification
// becomes something people turn off.
func (s *Store) RunDaily(ctx context.Context, v View, srcs []Source, req FetchRequest, now time.Time, cold time.Duration) (DailyReport, error) {
	rep := DailyReport{}
	if !v.valid() {
		return rep, ErrViewRequired
	}
	var docs []Document
	for _, src := range srcs {
		got, refused, err := FetchFrom(ctx, src, req, now)
		rep.Refused = append(rep.Refused, refused...)
		for _, r := range refused {
			s.audit(v, AuditFetchRefused, map[string]string{"source": r.Source, "host": r.Host, "url": r.URL}, now)
		}
		if err != nil {
			// A refused or broken source does not stop the pass: the other
			// sources' findings are still worth having, and the refusal is in
			// the ledger.
			continue
		}
		docs = append(docs, got...)
	}
	rep.Fetched = len(docs)

	candidates := ExtractSignals(docs)
	proposals := s.Reconcile(v, candidates)
	applied, err := s.Apply(v, ActorAgent, proposals, nil)
	if err != nil {
		return rep, err
	}
	rep.Applied = append(append([]string{}, applied.Created...), applied.Updated...)
	for _, i := range applied.Pending {
		if id, queued := s.queuePending(v, proposals[i]); queued {
			rep.Queued = append(rep.Queued, id)
		}
	}

	// The ledger is written whether or not anything came of a document.
	for _, d := range docs {
		rep.Observations = append(rep.Observations, s.record(v, d, producedFrom(d, rep.Applied, s, v)).ID)
	}

	// EVERY event in the team, not only the ones this pass just fetched.
	//
	// WHY A SWEEP AND NOT A REACTION TO WHAT WAS APPLIED
	//
	//	It used to iterate rep.Applied, which meant an alert existed only if a
	//	scheduled fetch had produced the event. The event a recruiter TELLS the
	//	agent about - "c业务组要并进b业务组", the whole reason this feature was
	//	asked for - arrives through record_turn and was never in that list, so
	//	it never raised anything. 离职提醒 was a pull: you had to go and look.
	//
	//	So the invariant is stated as a reconciliation instead of a reaction:
	//	every event that should carry an alert carries one, checked on a pass
	//	that is guaranteed to run. RaiseAlert is already at-most-once per event,
	//	which is what makes re-running this free.
	for _, n := range s.Nodes(v, NodeFilter{Kind: KindEvent}) {
		if a, raised := s.RaiseAlert(v, n.ID, now); raised {
			rep.Alerts = append(rep.Alerts, a.ID)
		}
	}
	rep.Stale = s.StaleScan(v, now, cold)
	return rep, nil
}

// producedFrom links a document to the nodes that carry its URL as evidence.
// Matching on the intel already stored is what keeps the link honest: a node
// only counts as produced by a document if it actually cites it.
func producedFrom(d Document, applied []string, s *Store, v View) []string {
	var out []string
	for _, id := range applied {
		n, ok := s.Node(v, id)
		if !ok {
			continue
		}
		for _, i := range n.Intel {
			if i.SourceURL == d.URL {
				out = append(out, id)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}
