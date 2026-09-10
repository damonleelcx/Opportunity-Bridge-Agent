package leadgraph

import (
	"sort"
	"strings"
	"time"
)

// The org chart, and the text roster that is its degraded form.
//
// THE ONE THING THIS FILE IS FOR
//
//	A user knows the group they dealt with and almost never the levels around
//	it. So the chart is ALWAYS incomplete, and the only honest way to draw it is
//	to draw the incompleteness too.
//
//	A tidy, complete-looking tree would be read as "the system knows this
//	company's structure", and the user would decide who to approach from it.
//	Every part of it they never told us could only have been invented. So:
//
//	  - no level is created that nobody mentioned;
//	  - a level that exists only inside somebody's unit path is marked
//	    Mentioned, not Recorded, so the interface can draw it as a dashed box;
//	  - a person we cannot place is LISTED as unplaced, never quietly dropped -
//	    a missing person looks exactly like a person who is not there;
//	  - two things we were told that cannot both be tidy are reported as
//	    ambiguities rather than resolved by picking one.
//
// WHY Roster IS DERIVED FROM Chart RATHER THAN QUERIED SEPARATELY
//
//	They answer the same question and would drift apart within two changes. The
//	text list is the fallback the graph view degrades to (低带宽 / 渲染失败), and a
//	fallback that disagrees with the thing it replaces is worse than no
//	fallback.

// Presence is how well we know a box on the chart exists.
type Presence string

const (
	// PresenceRecorded - a unit node of its own, with its own evidence. 实线.
	PresenceRecorded Presence = "recorded"
	// PresenceMentioned - it only ever appeared inside somebody's unit path.
	// We know the name; we hold nothing about it. 虚线.
	PresenceMentioned Presence = "mentioned"
)

// Placement is HOW we know where somebody sits - which is as important as
// where, because the two are trusted differently.
type Placement string

const (
	// PlacedByEdge - an explicit membership we recorded and can date.
	PlacedByEdge Placement = "belongs_to"
	// PlacedByPath - only the person's own unit path says so. Weaker: it came
	// from one sentence and was never confirmed as a membership.
	PlacedByPath Placement = "unit_path"
	// PlacedNowhere - we know the person and not where they sit.
	PlacedNowhere Placement = "unplaced"
)

// Ambiguity kinds. Each names something the chart refuses to resolve on its own.
const (
	// AmbiguitySameLabelTwoPlaces - the same group name appears at two different
	// depths. Merging them would be a guess; both are what we were told.
	AmbiguitySameLabelTwoPlaces = "same_label_two_places"
	// AmbiguityMembershipEnded - every membership this person had has been
	// closed. After a reorganisation that is the honest state: they are
	// somewhere, and we do not know where.
	AmbiguityMembershipEnded = "membership_ended"
)

type ChartPerson struct {
	NodeID        string        `json:"node_id"`
	Label         string        `json:"label"`
	RoleTitle     string        `json:"role_title,omitempty"`
	Duty          string        `json:"duty,omitempty"`
	Placement     Placement     `json:"placement"`
	Corroboration Corroboration `json:"corroboration"`
	Unconfirmed   []string      `json:"unconfirmed"`
}

type ChartNode struct {
	Path          []string      `json:"path"`
	Label         string        `json:"label"`
	Presence      Presence      `json:"presence"`
	NodeID        string        `json:"node_id,omitempty"` // empty when only mentioned
	Corroboration Corroboration `json:"corroboration,omitempty"`
	Unconfirmed   []string      `json:"unconfirmed,omitempty"`
	People        []ChartPerson `json:"people"`
	Children      []ChartNode   `json:"children"`
}

type Ambiguity struct {
	Kind   string     `json:"kind"`
	Label  string     `json:"label"`
	NodeID string     `json:"node_id,omitempty"`
	Paths  [][]string `json:"paths,omitempty"`
	// Since dates AmbiguityMembershipEnded, so the interface can say when.
	Since *time.Time `json:"since,omitempty"`
}

type ChartCounts struct {
	Units          int `json:"units"`
	UnitsMentioned int `json:"units_mentioned"`
	People         int `json:"people"`
	Unplaced       int `json:"unplaced"`
	Unconfirmed    int `json:"unconfirmed"` // people with at least one unconfirmed field
}

type Chart struct {
	Org  string      `json:"org"`
	At   time.Time   `json:"at"`
	Root []ChartNode `json:"root"`
	// Unplaced are people we know in this org and cannot put anywhere. They are
	// the most useful entries on the page: each one is a question worth asking.
	Unplaced    []ChartPerson `json:"unplaced"`
	Ambiguities []Ambiguity   `json:"ambiguities,omitempty"`
	Counts      ChartCounts   `json:"counts"`
}

// OrgChart assembles what this team knows about one company's structure, as of
// a moment. Pass the zero time for "now".
func (s *Store) OrgChart(v View, org string, at time.Time) Chart {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	c := Chart{Org: org, At: at, Root: []ChartNode{}, Unplaced: []ChartPerson{}}
	if !v.valid() || strings.TrimSpace(org) == "" {
		return c
	}

	units := s.Nodes(v, NodeFilter{Kind: KindUnit, Org: org})
	people := s.Nodes(v, NodeFilter{Kind: KindPerson, Org: org})

	// Recorded units, keyed by their path below the company.
	recorded := map[string]Node{}
	for _, u := range units {
		recorded[pathKey(trimOrg(u.UnitPath, org, u.Label))] = u
	}

	type placed struct {
		person ChartPerson
		path   []string
	}
	var placements []placed
	for _, p := range people {
		who := ChartPerson{
			NodeID: p.ID, Label: p.Label, RoleTitle: p.RoleTitle, Duty: p.Duty,
			Corroboration: p.Corroboration(), Unconfirmed: p.Unconfirmed,
		}
		path, how, ended := s.placeOf(v, p, at)
		who.Placement = how
		if how == PlacedNowhere {
			if ended != nil {
				c.Ambiguities = append(c.Ambiguities, Ambiguity{
					Kind: AmbiguityMembershipEnded, Label: p.Label, NodeID: p.ID, Since: ended,
				})
			}
			c.Unplaced = append(c.Unplaced, who)
			continue
		}
		placements = append(placements, placed{who, trimOrg(path, org, "")})
	}

	// The tree is built ONLY from paths somebody actually stated: recorded unit
	// paths, and the paths of people we could place. No level is interpolated.
	tree := map[string]*ChartNode{}
	ensure := func(path []string) *ChartNode {
		for i := 1; i <= len(path); i++ {
			k := pathKey(path[:i])
			if _, ok := tree[k]; ok {
				continue
			}
			n := &ChartNode{
				Path: append([]string{}, path[:i]...), Label: path[i-1],
				Presence: PresenceMentioned, People: []ChartPerson{}, Children: []ChartNode{},
			}
			if u, ok := recorded[k]; ok {
				n.Presence, n.NodeID = PresenceRecorded, u.ID
				n.Corroboration, n.Unconfirmed = u.Corroboration(), u.Unconfirmed
			}
			tree[k] = n
		}
		return tree[pathKey(path)]
	}
	for _, u := range units {
		if p := trimOrg(u.UnitPath, org, u.Label); len(p) > 0 {
			ensure(p)
		}
	}
	for _, pl := range placements {
		if len(pl.path) == 0 {
			// Placed at company level: known to be in the company, no group.
			c.Unplaced = append(c.Unplaced, pl.person)
			continue
		}
		n := ensure(pl.path)
		n.People = append(n.People, pl.person)
	}

	c.Root = assemble(tree)
	c.Ambiguities = append(c.Ambiguities, sameLabelTwice(tree)...)
	sortAmbiguities(c.Ambiguities)
	sortPeople(c.Unplaced)
	c.Counts = countChart(c)
	return c
}

// placeOf decides where one person sits, and how confidently.
//
// An explicitly CLOSED membership beats an implicit unit path. After a
// reorganisation, falling back to the path on the person's own record would put
// them back in the group they just left - which is the single most damaging
// thing this chart could get wrong, because it is exactly when somebody would
// act on it.
func (s *Store) placeOf(v View, p Node, at time.Time) ([]string, Placement, *time.Time) {
	edges := s.Edges(v, EdgeFilter{Kind: EdgeBelongsTo, Endpoint: p.ID})
	var latestEnd *time.Time
	sawMembership := false
	for _, e := range edges {
		if e.From != p.ID {
			continue
		}
		sawMembership = true
		if e.Open(at) {
			if u, ok := s.Node(v, e.To); ok {
				path := u.UnitPath
				if len(path) == 0 {
					path = []string{u.Label}
				}
				return path, PlacedByEdge, nil
			}
			continue
		}
		if e.ValidUntil != nil && (latestEnd == nil || e.ValidUntil.After(*latestEnd)) {
			latestEnd = e.ValidUntil
		}
	}
	if sawMembership {
		return nil, PlacedNowhere, latestEnd
	}
	if len(p.UnitPath) > 0 {
		return p.UnitPath, PlacedByPath, nil
	}
	return nil, PlacedNowhere, nil
}

// trimOrg drops a leading segment that is just the company name, so the tree
// under 「A司」 does not start with another 「A司」. A unit that carries no path at
// all falls back to its own label, which is the common case early on: somebody
// mentioned a group and nothing about where it sits.
func trimOrg(path []string, org, fallback string) []string {
	if len(path) == 0 {
		if fallback == "" {
			return nil
		}
		return []string{fallback}
	}
	if norm(path[0]) == norm(org) {
		path = path[1:]
	}
	out := make([]string, 0, len(path))
	for _, seg := range path {
		if strings.TrimSpace(seg) != "" {
			out = append(out, seg)
		}
	}
	if len(out) == 0 && fallback != "" {
		return []string{fallback}
	}
	return out
}

func pathKey(p []string) string {
	lower := make([]string, len(p))
	for i, s := range p {
		lower[i] = norm(s)
	}
	return strings.Join(lower, ">")
}

// assemble turns the flat map into a sorted tree. Ordering is by label so two
// identical reads draw the same picture; an unchanged chart that reshuffles
// looks like the company reorganised again.
func assemble(tree map[string]*ChartNode) []ChartNode {
	byParent := map[string][]*ChartNode{}
	for _, n := range tree {
		byParent[pathKey(n.Path[:len(n.Path)-1])] = append(byParent[pathKey(n.Path[:len(n.Path)-1])], n)
	}
	var build func(parent string) []ChartNode
	build = func(parent string) []ChartNode {
		kids := byParent[parent]
		sort.Slice(kids, func(i, j int) bool { return norm(kids[i].Label) < norm(kids[j].Label) })
		out := make([]ChartNode, 0, len(kids))
		for _, k := range kids {
			n := *k
			sortPeople(n.People)
			n.Children = build(pathKey(n.Path))
			out = append(out, n)
		}
		return out
	}
	return build("")
}

// sameLabelTwice reports a group name that appears at two different depths.
// Two people described the same group differently, or they are two groups with
// the same name. Both are possible; the chart says so instead of choosing.
func sameLabelTwice(tree map[string]*ChartNode) []Ambiguity {
	byLabel := map[string][][]string{}
	for _, n := range tree {
		byLabel[norm(n.Label)] = append(byLabel[norm(n.Label)], n.Path)
	}
	var out []Ambiguity
	for label, paths := range byLabel {
		if len(paths) < 2 {
			continue
		}
		sort.Slice(paths, func(i, j int) bool { return pathKey(paths[i]) < pathKey(paths[j]) })
		out = append(out, Ambiguity{Kind: AmbiguitySameLabelTwoPlaces, Label: label, Paths: paths})
	}
	return out
}

func sortPeople(ps []ChartPerson) {
	sort.Slice(ps, func(i, j int) bool {
		if norm(ps[i].Label) != norm(ps[j].Label) {
			return norm(ps[i].Label) < norm(ps[j].Label)
		}
		return ps[i].NodeID < ps[j].NodeID
	})
}

func sortAmbiguities(as []Ambiguity) {
	sort.Slice(as, func(i, j int) bool {
		if as[i].Kind != as[j].Kind {
			return as[i].Kind < as[j].Kind
		}
		if as[i].Label != as[j].Label {
			return as[i].Label < as[j].Label
		}
		return as[i].NodeID < as[j].NodeID
	})
}

// countChart counts the tree that was actually built, never the inputs. A
// summary computed from something other than what is on screen is how a page
// ends up saying "12 people" above a list of nine.
func countChart(c Chart) ChartCounts {
	n := ChartCounts{Unplaced: len(c.Unplaced), People: len(c.Unplaced)}
	for _, p := range c.Unplaced {
		if len(p.Unconfirmed) > 0 {
			n.Unconfirmed++
		}
	}
	var walk func(ns []ChartNode)
	walk = func(ns []ChartNode) {
		for _, x := range ns {
			if x.Presence == PresenceRecorded {
				n.Units++
			} else {
				n.UnitsMentioned++
			}
			for _, p := range x.People {
				n.People++
				if len(p.Unconfirmed) > 0 {
					n.Unconfirmed++
				}
			}
			walk(x.Children)
		}
	}
	walk(c.Root)
	return n
}

// ---- the text roster ----

// RosterGroup is one line-block of the text fallback: a group, and who we know
// in it.
type RosterGroup struct {
	Org  string   `json:"org"`
	Path []string `json:"path"` // empty means "in the company, group unknown"
	// Presence carries through so the text version can say 「未记录」 where the
	// graph would have drawn a dashed box. The fallback must not look MORE
	// certain than the thing it replaces.
	Presence Presence      `json:"presence"`
	People   []ChartPerson `json:"people"`
}

// Roster is the text fallback for the whole graph: every company this team
// knows, flattened, ordered, with the same truth the chart shows.
//
// Groups with nobody in them are included: an empty group is a real thing we
// were told about, and dropping it would make the list disagree with the chart.
func (s *Store) Roster(v View, at time.Time) []RosterGroup {
	out := []RosterGroup{}
	if !v.valid() {
		return out
	}
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

	for _, org := range names {
		c := s.OrgChart(v, org, at)
		var walk func(ns []ChartNode)
		walk = func(ns []ChartNode) {
			for _, x := range ns {
				out = append(out, RosterGroup{Org: org, Path: x.Path, Presence: x.Presence, People: x.People})
				walk(x.Children)
			}
		}
		walk(c.Root)
		if len(c.Unplaced) > 0 {
			out = append(out, RosterGroup{Org: org, Presence: PresenceMentioned, People: c.Unplaced})
		}
	}
	return out
}
