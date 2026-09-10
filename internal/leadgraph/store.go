package leadgraph

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

// Store holds the graph. Reads are served from memory and every one of them
// takes a View.
//
// WHY THE TEAM AND SEAT FILTERS LIVE IN HERE AND NOT IN THE CALLERS
//
//	A caller that forgets the check is a plausible mistake, and it FAILS OPEN:
//	it returns another team's graph, or another consultant's private notes, and
//	nothing in the type system objects. Here there is one place to audit and no
//	way for a caller to skip it. Same reasoning as store.DiscoverableProfiles in
//	the sibling product.
type Store struct {
	mu    sync.RWMutex
	log   *slog.Logger
	seq   int
	nodes map[string]*Node
	edges map[string]*Edge
	// notes is the private overlay, keyed seat-first so that one seat's whole
	// set can be handed over - or deleted when they leave - without walking the
	// team's facts.
	notes map[string]map[string]*Annotation // seatID -> targetID -> annotation
	// touches holds contact records. Team-visible facts; only the private
	// impression on them belongs to a seat. See touchpoint.go.
	touches map[string]*Touchpoint
	// obs is the source ledger: one row per fetched document. See observe.go.
	obs map[string]*Observation
	// alerts are team-wide "something moved and you know people there".
	alerts map[string]*Alert
	// audit records the actions a regulator or a customer would ask about.
	auditLog []AuditEntry
	// pending holds decisions parked until a person has room to make them. See
	// turn.go: a turn asks at most one question, and everything else waits here
	// rather than being applied or forgotten.
	pending map[string]*PendingItem
	// byKey maps a team's natural key to a record id. It is what makes the same
	// fact arriving twice an update rather than a duplicate.
	byKey map[string]string
	now   func() time.Time
}

func New(log *slog.Logger) *Store {
	return &Store{
		log:     log,
		nodes:   map[string]*Node{},
		edges:   map[string]*Edge{},
		notes:   map[string]map[string]*Annotation{},
		pending: map[string]*PendingItem{},
		touches: map[string]*Touchpoint{},
		obs:     map[string]*Observation{},
		alerts:  map[string]*Alert{},
		byKey:   map[string]string{},
		now:     func() time.Time { return time.Now().UTC() },
	}
}

// SetClock makes time injectable so tests can assert on ordering without
// sleeping. Production never calls it.
func (s *Store) SetClock(f func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = f
}

func (s *Store) nextID(prefix string) string {
	s.seq++
	return fmt.Sprintf("%s_%d", prefix, s.seq)
}

// ---- natural keys ----

// naturalKey is EXACT-MATCH identity: same team, same kind, same organisation,
// same unit path, same label. Nothing fuzzier belongs here.
//
//	"王五" and "王总" produce two different keys and therefore two different
//	nodes, on purpose. Deciding they are the same person is Reconcile's job: a
//	deterministic proposal that a human confirms. If this function guessed,
//	merging would become a silent side effect of writing, and a wrong merge
//	destroys the evidence that there were ever two people.
func naturalKey(n Node) string {
	parts := []string{
		n.TeamID,
		string(n.Kind),
		norm(n.Org),
		norm(strings.Join(n.UnitPath, ">")),
		norm(n.Label),
	}
	if n.Kind == KindEvent && n.OccurredAt != nil {
		// The same reorganisation reported twice on the same day is one event;
		// the same words a year later are a different one.
		parts = append(parts, n.OccurredAt.UTC().Format("2006-01-02"))
	}
	return strings.Join(parts, "|")
}

func edgeKey(e Edge) string {
	return strings.Join([]string{e.TeamID, "edge", string(e.Kind), e.From, e.To}, "|")
}

func norm(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// ---- writes ----

// UpsertNode records a node, or folds new information into the one already
// holding that natural key.
//
// Note travels through here for the caller's convenience and is routed to the
// writer's own Annotation rather than onto the shared record - see Annotation.
//
// Three refusals, in order of how much damage they prevent:
//
//	no intel                -> the record cannot say where it came from
//	agent replacing a value -> a silent overwrite; surface it instead
//	unknown kind / no label -> a record nobody can find or reason about
func (s *Store) UpsertNode(v View, actor Actor, in Node) (Node, error) {
	if !v.valid() {
		return Node{}, ErrViewRequired
	}
	if !in.Kind.valid() {
		return Node{}, ErrUnknownKind
	}
	if strings.TrimSpace(in.Label) == "" {
		return Node{}, ErrLabelRequired
	}
	if len(in.Intel) == 0 {
		return Node{}, ErrIntelRequired
	}
	for _, i := range in.Intel {
		if err := i.validate(); err != nil {
			return Node{}, err
		}
	}
	if err := scanSensitive(map[string]string{
		"label": in.Label, "role_title": in.RoleTitle, "duty": in.Duty, "note": in.Note,
		"unit_path": strings.Join(in.UnitPath, ">"),
	}); err != nil {
		return Node{}, err
	}
	in.TeamID = v.TeamID
	note := in.Note
	in.Note = ""

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	because := in.Intel[0]

	var out *Node
	key := naturalKey(in)
	if id, ok := s.byKey[key]; ok {
		cur := s.nodes[id]
		changes, err := mergeNodeFields(cur, in, actor, because, now)
		if err != nil {
			return Node{}, err
		}
		cur.Intel = mergeIntel(cur.Intel, in.Intel)
		cur.History = append(cur.History, changes...)
		if len(changes) > 0 {
			cur.UpdatedAt = now
		}
		normaliseNode(cur)
		out = cur
	} else {
		n := in
		n.ID = s.nextID("nd")
		n.CreatedAt, n.UpdatedAt = now, now
		n.History = nil
		n.Intel = mergeIntel(nil, in.Intel)
		normaliseNode(&n)
		s.nodes[n.ID] = &n
		s.byKey[key] = n.ID
		out = &n
	}

	if strings.TrimSpace(note) != "" {
		s.setNote(v, out.ID, note, now)
	}
	return s.project(v, *out), nil
}

// UpsertEdge records a line between two nodes the same team already has.
//
// The cross-team check is not a permission check, it is a correctness one: an
// edge whose ends live in two different graphs is a join nobody asked for.
func (s *Store) UpsertEdge(v View, actor Actor, in Edge) (Edge, error) {
	if !v.valid() {
		return Edge{}, ErrViewRequired
	}
	if !in.Kind.valid() {
		return Edge{}, ErrUnknownKind
	}
	if len(in.Intel) == 0 {
		return Edge{}, ErrIntelRequired
	}
	for _, i := range in.Intel {
		if err := i.validate(); err != nil {
			return Edge{}, err
		}
	}
	strength, note := in.Strength, in.Note
	if strength != 0 {
		if actor != ActorUser {
			return Edge{}, ErrStrengthIsUserOnly
		}
		if strength < 1 || strength > 3 {
			return Edge{}, ErrStrengthRange
		}
	}
	if err := scanSensitive(map[string]string{"context": in.Context, "note": in.Note}); err != nil {
		return Edge{}, err
	}
	in.TeamID = v.TeamID
	in.Strength, in.Note = 0, ""

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.teamHasNode(v.TeamID, in.From) || !s.teamHasNode(v.TeamID, in.To) {
		return Edge{}, ErrEndpointMissing
	}
	now := s.now()
	because := in.Intel[0]

	var out *Edge
	key := edgeKey(in)
	if id, ok := s.byKey[key]; ok {
		cur := s.edges[id]
		changes, err := mergeEdgeFields(cur, in, actor, because, now)
		if err != nil {
			return Edge{}, err
		}
		cur.Intel = mergeIntel(cur.Intel, in.Intel)
		cur.History = append(cur.History, changes...)
		if len(changes) > 0 {
			cur.UpdatedAt = now
		}
		out = cur
	} else {
		e := in
		e.ID = s.nextID("eg")
		e.CreatedAt, e.UpdatedAt = now, now
		e.History = nil
		e.Intel = mergeIntel(nil, in.Intel)
		s.edges[e.ID] = &e
		s.byKey[key] = e.ID
		out = &e
	}

	if strength != 0 || strings.TrimSpace(note) != "" {
		a := s.annotation(v, out.ID)
		if strength != 0 {
			a.Strength = strength
		}
		if strings.TrimSpace(note) != "" {
			a.Note = note
		}
		a.UpdatedAt = now
	}
	return s.projectEdge(v, *out), nil
}

// Annotate writes one seat's private judgement. Strength is refused to the
// agent for the same reason everywhere else: nothing can infer how well two
// people know each other, and a guess that reads as "strong" gets used as an
// introduction and embarrasses the user in front of the person they were
// trying to reach.
func (s *Store) Annotate(v View, actor Actor, targetID string, strength int, note string) error {
	if !v.valid() {
		return ErrViewRequired
	}
	if strength != 0 {
		if actor != ActorUser {
			return ErrStrengthIsUserOnly
		}
		if strength < 1 || strength > 3 {
			return ErrStrengthRange
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := scanSensitive(map[string]string{"note": note}); err != nil {
		return err
	}
	if !s.teamHasNode(v.TeamID, targetID) && !s.teamHasEdge(v.TeamID, targetID) &&
		!s.teamHasTouch(v.TeamID, targetID) {
		return ErrNotFound
	}
	a := s.annotation(v, targetID)
	if strength != 0 {
		a.Strength = strength
	}
	if note != "" {
		a.Note = note
	}
	a.UpdatedAt = s.now()
	return nil
}

// annotation returns this seat's overlay for a target, creating it if needed.
// Callers hold the write lock.
func (s *Store) annotation(v View, targetID string) *Annotation {
	seat := s.notes[v.SeatID]
	if seat == nil {
		seat = map[string]*Annotation{}
		s.notes[v.SeatID] = seat
	}
	a := seat[targetID]
	if a == nil {
		a = &Annotation{TeamID: v.TeamID, SeatID: v.SeatID, TargetID: targetID}
		seat[targetID] = a
	}
	return a
}

func (s *Store) setNote(v View, targetID, note string, now time.Time) {
	a := s.annotation(v, targetID)
	a.Note = note
	a.UpdatedAt = now
}

// project fills in the reader's own private overlay. It is the only path by
// which Note and Strength ever reach a caller, which is what makes "your notes
// stay yours" a property of the code rather than a promise in a document.
func (s *Store) project(v View, n Node) Node {
	if a, ok := s.notes[v.SeatID][n.ID]; ok && a.TeamID == v.TeamID {
		n.Note = a.Note
	}
	return n
}

func (s *Store) projectEdge(v View, e Edge) Edge {
	if a, ok := s.notes[v.SeatID][e.ID]; ok && a.TeamID == v.TeamID {
		e.Note, e.Strength = a.Note, a.Strength
	}
	return e
}

// CloseEdge ends an edge's validity instead of removing it. Used when a
// reorganisation moves people: the old membership stays answerable.
func (s *Store) CloseEdge(v View, edgeID string, at time.Time, because Intel) error {
	if !v.valid() {
		return ErrViewRequired
	}
	if err := because.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.edges[edgeID]
	if !ok || e.TeamID != v.TeamID {
		return ErrNotFound
	}
	from := ""
	if e.ValidUntil != nil {
		from = e.ValidUntil.UTC().Format(time.RFC3339)
	}
	at = at.UTC()
	e.ValidUntil = &at
	e.Intel = mergeIntel(e.Intel, []Intel{because})
	e.History = append(e.History, FieldChange{
		At: s.now(), Field: "valid_until", From: from, To: at.Format(time.RFC3339), By: ActorUser, Why: because,
	})
	e.UpdatedAt = s.now()
	return nil
}

// mergeNodeFields folds incoming values into an existing node.
//
// TWO RULES THAT LOOK SMALL AND ARE NOT
//
//	An omitted field never erases. Empty means "not mentioned this time", not
//	"clear it" - most updates mention one thing and say nothing about the rest.
//
//	The agent may fill a blank; it may not replace a value. A person changing
//	their own record is an edit. The agent changing it is a claim that the
//	world moved or that the record was wrong, and those are different
//	conclusions with different consequences - so it stops and asks.
func mergeNodeFields(cur *Node, in Node, actor Actor, because Intel, now time.Time) ([]FieldChange, error) {
	unit := strings.Join(in.UnitPath, ">")
	curUnit := strings.Join(cur.UnitPath, ">")
	fields := []struct {
		name string
		cur  *string
		in   string
	}{
		{"org", &cur.Org, in.Org},
		{"role_title", &cur.RoleTitle, in.RoleTitle},
		{"duty", &cur.Duty, in.Duty},
		{"unit_path", &curUnit, unit},
	}
	var changes []FieldChange
	for _, f := range fields {
		next := strings.TrimSpace(f.in)
		if next == "" || next == *f.cur {
			continue
		}
		if *f.cur != "" && actor != ActorUser {
			return nil, fmt.Errorf("%w: %s is %q and the agent proposed %q", ErrConflictNeedsUser, f.name, *f.cur, next)
		}
		changes = append(changes, FieldChange{At: now, Field: f.name, From: *f.cur, To: next, By: actor, Why: because})
		*f.cur = next
	}
	if curUnit != strings.Join(cur.UnitPath, ">") {
		cur.UnitPath = strings.Split(curUnit, ">")
	}
	if in.OccurredAt != nil && cur.OccurredAt == nil {
		cur.OccurredAt = in.OccurredAt
		changes = append(changes, FieldChange{At: now, Field: "occurred_at", To: in.OccurredAt.UTC().Format(time.RFC3339), By: actor, Why: because})
	}
	return changes, nil
}

func mergeEdgeFields(cur *Edge, in Edge, actor Actor, because Intel, now time.Time) ([]FieldChange, error) {
	var changes []FieldChange
	if next := strings.TrimSpace(in.Context); next != "" && next != cur.Context {
		if cur.Context != "" && actor != ActorUser {
			return nil, fmt.Errorf("%w: context is %q and the agent proposed %q", ErrConflictNeedsUser, cur.Context, next)
		}
		changes = append(changes, FieldChange{At: now, Field: "context", From: cur.Context, To: next, By: actor, Why: because})
		cur.Context = next
	}
	return changes, nil
}

// mergeIntel appends evidence, deduplicated by who said it and what they said.
// The same source repeating itself must not look like independent
// confirmation - that is what would silently promote a rumour.
func mergeIntel(cur []Intel, add []Intel) []Intel {
	seen := map[string]bool{}
	for _, i := range cur {
		seen[intelSource(i)+"|"+norm(i.Excerpt)] = true
	}
	for _, i := range add {
		k := intelSource(i) + "|" + norm(i.Excerpt)
		if seen[k] {
			continue
		}
		seen[k] = true
		cur = append(cur, i)
	}
	return cur
}

// normaliseNode recomputes Unconfirmed from the record itself, so the badge on
// screen and the data can never disagree, and guarantees it marshals as [].
func normaliseNode(n *Node) {
	out := []string{}
	for _, f := range unconfirmable[n.Kind] {
		switch f {
		case "role_title":
			if strings.TrimSpace(n.RoleTitle) == "" {
				out = append(out, f)
			}
		case "duty":
			if strings.TrimSpace(n.Duty) == "" {
				out = append(out, f)
			}
		case "org":
			if strings.TrimSpace(n.Org) == "" {
				out = append(out, f)
			}
		case "occurred_at":
			if n.OccurredAt == nil {
				out = append(out, f)
			}
		}
	}
	n.Unconfirmed = out
}

// unconfirmable is which fields are worth chasing per kind. A field nobody will
// ever ask about does not belong here: the list drives the "待确认 N 条" queue,
// and a queue that never empties gets ignored.
var unconfirmable = map[NodeKind][]string{
	KindPerson: {"role_title", "duty", "org"},
	KindUnit:   {"org"},
	KindEvent:  {"occurred_at"},
}

// ---- reads ----

func (s *Store) teamHasNode(teamID, id string) bool {
	n, ok := s.nodes[id]
	return ok && n.TeamID == teamID
}

func (s *Store) teamHasEdge(teamID, id string) bool {
	e, ok := s.edges[id]
	return ok && e.TeamID == teamID
}

func (s *Store) teamHasTouch(teamID, id string) bool {
	t, ok := s.touches[id]
	return ok && t.TeamID == teamID
}

func (s *Store) Node(v View, id string) (Node, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.nodes[id]
	if !ok || n.TeamID != v.TeamID {
		return Node{}, false
	}
	return s.project(v, *n), true
}

// NodeFilter narrows a read. Zero value means "everything this team has".
type NodeFilter struct {
	Kind     NodeKind
	Org      string
	UnitPath []string // matches nodes at or below this path
}

func (s *Store) Nodes(v View, f NodeFilter) []Node {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.nodesLocked(v, f)
}

func (s *Store) nodesLocked(v View, f NodeFilter) []Node {
	var out []Node
	for _, n := range s.nodes {
		if n.TeamID != v.TeamID {
			continue
		}
		if f.Kind != "" && n.Kind != f.Kind {
			continue
		}
		if f.Org != "" && norm(n.Org) != norm(f.Org) {
			continue
		}
		if len(f.UnitPath) > 0 && !underPath(n.UnitPath, f.UnitPath) {
			continue
		}
		out = append(out, s.project(v, *n))
	}
	// Sorted by id so two identical reads return the same order. Map order
	// would make an unchanged graph look like it had moved - and it would make
	// Reconcile's output depend on the runtime rather than on the data.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func underPath(have, want []string) bool {
	if len(have) < len(want) {
		return false
	}
	for i := range want {
		if norm(have[i]) != norm(want[i]) {
			return false
		}
	}
	return true
}

// EdgeFilter narrows an edge read. OpenAt, when set, excludes edges that had
// already been closed at that moment.
type EdgeFilter struct {
	Kind     EdgeKind
	Endpoint string // edges touching this node, in either direction
	OpenAt   *time.Time
}

func (s *Store) Edges(v View, f EdgeFilter) []Edge {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Edge
	for _, e := range s.edges {
		if e.TeamID != v.TeamID {
			continue
		}
		if f.Kind != "" && e.Kind != f.Kind {
			continue
		}
		if f.Endpoint != "" && e.From != f.Endpoint && e.To != f.Endpoint {
			continue
		}
		if f.OpenAt != nil && !e.Open(*f.OpenAt) {
			continue
		}
		out = append(out, s.projectEdge(v, *e))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ---- deletion ----

// Forget removes a node and every edge touching it, plus every seat's private
// overlay on all of them. Deletion is real: the promise made to a person
// exercising their rights under 《个人信息保护法》44-47 cannot be a status flag.
// Returns how many records went.
func (s *Store) Forget(v View, nodeID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, ok := s.nodes[nodeID]
	if !ok || n.TeamID != v.TeamID {
		return 0
	}
	gone := 0
	for id, e := range s.edges {
		if e.TeamID != v.TeamID || (e.From != nodeID && e.To != nodeID) {
			continue
		}
		delete(s.byKey, edgeKey(*e))
		delete(s.edges, id)
		s.dropAnnotations(id)
		gone++
	}
	for id, t := range s.touches {
		if t.TeamID != v.TeamID || t.PersonID != nodeID {
			continue
		}
		delete(s.byKey, touchKey(*t))
		delete(s.touches, id)
		s.dropAnnotations(id)
		gone++
	}
	delete(s.byKey, naturalKey(*n))
	delete(s.nodes, nodeID)
	s.dropAnnotations(nodeID)
	return gone + 1
}

// dropAnnotations removes every seat's overlay on a target. Callers hold the
// write lock. A note that outlived the person it was about would be both a
// dangling record and, for a deletion request, a lie.
func (s *Store) dropAnnotations(targetID string) {
	for _, seat := range s.notes {
		delete(seat, targetID)
	}
}

// ForgetSeat drops one seat's private overlay and nothing else. This is what
// happens when a consultant leaves: their judgements go, the team's facts stay.
func (s *Store) ForgetSeat(seatID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.notes[seatID])
	delete(s.notes, seatID)
	// Their unanswered questions go too. Nobody else heard the conversation
	// that raised them, so nobody else can answer them - leaving them queued
	// would be a backlog that can only ever grow.
	for id, item := range s.pending {
		if item.SeatID == seatID {
			delete(s.byKey, seatID+"|pending|"+pendingKey(item.Proposal))
			delete(s.pending, id)
			n++
		}
	}
	return n
}

// ForgetTeam drops a team's entire graph, private overlays included.
func (s *Store) ForgetTeam(teamID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	gone := 0
	for id, e := range s.edges {
		if e.TeamID == teamID {
			delete(s.byKey, edgeKey(*e))
			delete(s.edges, id)
			s.dropAnnotations(id)
			gone++
		}
	}
	for id, n := range s.nodes {
		if n.TeamID == teamID {
			delete(s.byKey, naturalKey(*n))
			delete(s.nodes, id)
			s.dropAnnotations(id)
			gone++
		}
	}
	return gone
}
