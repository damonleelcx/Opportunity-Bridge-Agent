package leadgraph

import (
	"sort"
	"time"
)

// The read the interface renders.
//
// WHAT IS DELIBERATELY ABSENT FROM THIS TYPE
//
//	No size, weight, rank, score or importance - on a node or on a link. The
//	graph view is where those would arrive first and be hardest to argue with:
//	a bigger circle is a claim about a person that nobody can disagree with,
//	because it never says what it means. Colour encodes KIND and line style
//	encodes CORROBORATION; both are facts the user supplied. Everything else on
//	screen is the same size as everything else.
//
//	Strength travels as a NUMBER for the detail panel, because it is the
//	reader's own statement and they are entitled to see it. It is not encoded
//	visually: the moment thickness means "close", the eye starts ranking people
//	and the rule above is gone in practice while still being true in the code.

// SnapshotNode is one point for the graph view.
type SnapshotNode struct {
	ID    string   `json:"id"`
	Label string   `json:"label"`
	Kind  NodeKind `json:"kind"`
	Org   string   `json:"org,omitempty"`
	// Presence is "recorded" or "mentioned" for units - the dashed box. Empty
	// for kinds where the distinction does not apply.
	Presence      Presence      `json:"presence,omitempty"`
	Corroboration Corroboration `json:"corroboration"`
	Unconfirmed   []string      `json:"unconfirmed"`
	UnitPath      []string      `json:"unit_path,omitempty"`
	RoleTitle     string        `json:"role_title,omitempty"`
	Duty          string        `json:"duty,omitempty"`
	// Contacts travel to the screen because the point of the screen is to make
	// the call. They are the team's, so a teammate sees them too - unlike Note
	// below, which is the reader's alone.
	Contacts []ContactPoint `json:"contacts,omitempty"`
	// Note is the reader's OWN note, already projected by the store.
	Note string `json:"note,omitempty"`
}

// SnapshotLink is one line.
type SnapshotLink struct {
	ID            string        `json:"id"`
	From          string        `json:"from"`
	To            string        `json:"to"`
	Kind          EdgeKind      `json:"kind"`
	Corroboration Corroboration `json:"corroboration"`
	Context       string        `json:"context,omitempty"`
	// Mine and Strength are the reader's own judgement. Rendered in the panel,
	// never in the line's thickness. See the file comment.
	Mine     bool `json:"mine"`
	Strength int  `json:"strength,omitempty"`
	// Derived marks a line the chart worked out rather than one somebody
	// recorded: person → group, group → parent group. It is flagged so the
	// interface can never present it as a stated fact, and so a test can tell
	// the two apart.
	Derived bool `json:"derived,omitempty"`
}

// SnapshotCounts is what the header says. Computed from the arrays below it, so
// the number and the picture cannot disagree.
type SnapshotCounts struct {
	People      int `json:"people"`
	Units       int `json:"units"`
	Events      int `json:"events"`
	Links       int `json:"links"`
	Unconfirmed int `json:"unconfirmed"`
	Alerts      int `json:"alerts"`
	Pending     int `json:"pending"`
}

// Snapshot is everything one seat may see, in one read.
type Snapshot struct {
	TeamID string         `json:"team_id"`
	SeatID string         `json:"seat_id"`
	At     time.Time      `json:"at"`
	Nodes  []SnapshotNode `json:"nodes"`
	Links  []SnapshotLink `json:"links"`
	// Charts and Roster are the SAME truth as the graph, not a second query.
	// Roster is what the page shows when the graph does not draw.
	Charts []Chart        `json:"charts"`
	Roster []RosterGroup  `json:"roster"`
	Alerts []Alert        `json:"alerts"`
	Counts SnapshotCounts `json:"counts"`
}

// Snapshot assembles the view. Everything in it is ordered, so two identical
// reads produce identical bytes and the picture does not reshuffle.
func (s *Store) Snapshot(v View, at time.Time) Snapshot {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	snap := Snapshot{
		TeamID: v.TeamID, SeatID: v.SeatID, At: at,
		Nodes: []SnapshotNode{}, Links: []SnapshotLink{},
		Charts: []Chart{}, Roster: []RosterGroup{}, Alerts: []Alert{},
	}
	if !v.valid() {
		return snap
	}

	// Which units are recorded rather than merely mentioned is already decided
	// by the chart. Asking it here, instead of deciding again, is what keeps the
	// dashed box in the graph and the 「未记录」 in the text list agreeing.
	orgs := map[string]bool{}
	for _, n := range s.Nodes(v, NodeFilter{}) {
		if n.Org != "" {
			orgs[n.Org] = true
		}
	}
	names := make([]string, 0, len(orgs))
	for o := range orgs {
		names = append(names, o)
	}
	sort.Slice(names, func(i, j int) bool { return norm(names[i]) < norm(names[j]) })
	for _, o := range names {
		snap.Charts = append(snap.Charts, s.OrgChart(v, o, at))
	}
	snap.Roster = s.Roster(v, at)

	presence := map[string]Presence{}
	for _, c := range snap.Charts {
		var walk func(ns []ChartNode)
		walk = func(ns []ChartNode) {
			for _, x := range ns {
				if x.NodeID != "" {
					presence[x.NodeID] = x.Presence
				}
				walk(x.Children)
			}
		}
		walk(c.Root)
	}

	seen := map[string]bool{}
	for _, n := range s.Nodes(v, NodeFilter{}) {
		seen[n.ID] = true
		sn := SnapshotNode{
			ID: n.ID, Label: n.Label, Kind: n.Kind, Org: n.Org,
			Corroboration: n.Corroboration(), Unconfirmed: n.Unconfirmed,
			UnitPath: n.UnitPath, RoleTitle: n.RoleTitle, Duty: n.Duty, Note: n.Note,
			Contacts: n.Contacts,
			Presence: presence[n.ID],
		}
		if sn.Unconfirmed == nil {
			sn.Unconfirmed = []string{}
		}
		snap.Nodes = append(snap.Nodes, sn)
		switch n.Kind {
		case KindPerson:
			snap.Counts.People++
		case KindUnit:
			snap.Counts.Units++
		case KindEvent:
			snap.Counts.Events++
		}
		if len(n.Unconfirmed) > 0 {
			snap.Counts.Unconfirmed++
		}
	}

	for _, e := range s.Edges(v, EdgeFilter{OpenAt: &at}) {
		snap.Links = append(snap.Links, SnapshotLink{
			ID: e.ID, From: e.From, To: e.To, Kind: e.Kind,
			Corroboration: e.Corroboration(), Context: e.Context,
			Mine: e.Strength > 0, Strength: e.Strength,
		})
	}
	snap.addStructure(seen)
	snap.Counts.Links = len(snap.Links)

	snap.Alerts = s.Alerts(v, false)
	snap.Counts.Alerts = len(snap.Alerts)
	snap.Counts.Pending = len(s.Pending(v))
	return snap
}

// mentionedID is the id a group gets when nobody ever recorded it and it exists
// only inside somebody's unit path. Prefixed so it can never collide with a
// stored id, and so a reader of the payload can see at a glance that this box
// is not a record.
func mentionedID(org string, path []string) string {
	return "mentioned:" + norm(org) + "|" + pathKey(path)
}

// addStructure puts the ORG CHART on the graph.
//
// WHY THIS EXISTS AT ALL
//
//	Without it the graph draws only relationship lines, and a company's
//	structure appears in the text list and nowhere in the picture - so the
//	PRD's "架构图 + 关系线" was half built, and worse, the picture looked TIDIER
//	than the list it is supposed to be the richer view of. The first walkthrough
//	of a realistic graph is what showed it: three people in 技术中心 › c业务组,
//	and a graph with no 技术中心 in it.
//
// WHY THE LINES ARE FLAGGED Derived
//
//	Nobody recorded "王五 belongs to c业务组" as a fact; the chart worked it out
//	from a sentence. Presenting that identically to a recorded membership would
//	quietly upgrade an inference into a record, which is the one thing this
//	package refuses to do everywhere else.
func (snap *Snapshot) addStructure(recorded map[string]bool) {
	add := func(n SnapshotNode) {
		if recorded[n.ID] {
			return
		}
		recorded[n.ID] = true
		snap.Nodes = append(snap.Nodes, n)
		if n.Kind == KindUnit {
			snap.Counts.Units++
		}
	}
	for _, c := range snap.Charts {
		var walk func(ns []ChartNode, parentID string)
		walk = func(ns []ChartNode, parentID string) {
			for _, x := range ns {
				id := x.NodeID
				if id == "" {
					id = mentionedID(c.Org, x.Path)
					add(SnapshotNode{
						ID: id, Label: x.Label, Kind: KindUnit, Org: c.Org,
						Presence: PresenceMentioned, Corroboration: Hearsay,
						Unconfirmed: []string{}, UnitPath: x.Path,
					})
				}
				if parentID != "" {
					snap.Links = append(snap.Links, SnapshotLink{
						ID: "d:" + parentID + ">" + id, From: parentID, To: id,
						Kind: EdgeBelongsTo, Corroboration: x.Corroboration, Derived: true,
					})
				}
				for _, p := range x.People {
					// How we know where somebody sits is carried through as
					// corroboration: a membership we recorded is solid, a
					// placement inferred from one sentence is not.
					cor := p.Corroboration
					if p.Placement == PlacedByPath {
						cor = Hearsay
					}
					snap.Links = append(snap.Links, SnapshotLink{
						ID: "d:" + id + ">" + p.NodeID, From: id, To: p.NodeID,
						Kind: EdgeBelongsTo, Corroboration: cor, Derived: true,
					})
				}
				walk(x.Children, id)
			}
		}
		walk(c.Root, "")
	}
}
