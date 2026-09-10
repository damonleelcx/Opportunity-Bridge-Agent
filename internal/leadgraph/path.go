package leadgraph

import (
	"sort"
	"time"
)

// Introduction paths: 引荐路径.
//
// WHY THIS IS THE MOST VALUABLE VIEW IN THE PRODUCT
//
//	"张三让我联系你" and a cold approach are two orders of magnitude apart in
//	reply rate. This view answers the question that decides which one you get,
//	it is built entirely on records the user made themselves - so it carries no
//	third-party data risk at all - and a competitor can buy the same candidate
//	database without being able to buy this.
//
// WHERE A PATH STARTS, AND WHY THAT QUESTION HAS ONLY ONE HONEST ANSWER
//
//	There is no node for "me": the graph holds external people, not the team.
//	So "who can I actually ask" has to be derived from something the user
//	stated, and there is exactly one such thing in this model - the relationship
//	STRENGTH they recorded, which is their own statement that they know somebody
//	and how well.
//
//	That makes Strength load-bearing rather than decorative, and it makes the
//	empty state honest: with no strength recorded anywhere, the answer is not
//	"no path exists", it is "you have not told me who you know". Those need
//	different remedies, so PathResult distinguishes them.
//
// WHY ONLY RELATIONSHIP EDGES ARE WALKED
//
//	belongs_to and reports_to are structure. Somebody's manager is not somebody
//	who will introduce you just because the org chart connects them; treating a
//	reporting line as a warm path is exactly the sort of inference that produces
//	an introduction that embarrasses the person who asked for it.

// PathOptions bounds the search. Zero values take the defaults below.
type PathOptions struct {
	MaxHops  int       // default 3
	MaxPaths int       // default 3
	At       time.Time // only edges still open at this moment; zero means now
}

const (
	defaultMaxHops  = 3
	defaultMaxPaths = 3
)

// Hop is one link in a chain, carrying enough to be argued with.
type Hop struct {
	EdgeID        string        `json:"edge_id"`
	Kind          EdgeKind      `json:"kind"`
	FromID        string        `json:"from_id"`
	FromLabel     string        `json:"from_label"`
	ToID          string        `json:"to_id"`
	ToLabel       string        `json:"to_label"`
	Context       string        `json:"context,omitempty"`
	Corroboration Corroboration `json:"corroboration"`
	// Strength is YOUR OWN judgement of this link, and Mine says whether you
	// made one. For a link between two other people the normal answer is "no":
	// how well THEY know each other is not something you can see, because
	// strength is private to the seat that recorded it. The interface should
	// say so rather than showing a confident-looking zero.
	Strength int  `json:"strength,omitempty"`
	Mine     bool `json:"mine"`
}

// Path is one chain from somebody you know to the target.
type Path struct {
	Hops []Hop `json:"hops"`
	// Weakest is the weakest strength YOU stated along the chain, and 0 when
	// any link is not yours. A chain is only as strong as its weakest link, so
	// this - not the hop count - is what paths are ordered by.
	Weakest int `json:"weakest"`
	// Hearsay counts links still resting on a single unconfirmed source.
	Hearsay int `json:"hearsay"`
}

// PathResult answers "how do I reach this person".
type PathResult struct {
	TargetID    string `json:"target_id"`
	TargetLabel string `json:"target_label"`
	Paths       []Path `json:"paths"`
	// YouKnowThem short-circuits everything: you recorded a strength on this
	// person yourself, so there is nobody to go through.
	YouKnowThem bool `json:"you_know_them"`
	// NoSeeds distinguishes "no route" from "no starting point". They look the
	// same on screen and need completely different next steps: one is "nobody
	// you know connects to him", the other is "tell me who you know".
	NoSeeds bool `json:"no_seeds"`
	// LastTouch is the most recent contact ANYBODY on the team recorded with
	// the target. It leads the result because acting without it is how two
	// consultants cold-call the same person in one week.
	LastTouch *Touchpoint `json:"last_touch,omitempty"`
}

// relationEdges are the kinds a warm introduction can travel along.
var relationEdges = map[EdgeKind]bool{
	EdgeColleague:  true,
	EdgeKnows:      true,
	EdgeReferredBy: true,
}

// Seeds are the people this seat said they know: the only places a path may
// start. Ordered by strength (strongest first), then label, so the search is
// deterministic and the best routes surface first.
func (s *Store) Seeds(v View) []Node {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seat := s.notes[v.SeatID]
	type scored struct {
		n Node
		w int
	}
	var out []scored
	for id, a := range seat {
		if a.TeamID != v.TeamID || a.Strength < 1 {
			continue
		}
		n, ok := s.nodes[id]
		if !ok || n.TeamID != v.TeamID || n.Kind != KindPerson {
			continue
		}
		out = append(out, scored{s.project(v, *n), a.Strength})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].w != out[j].w {
			return out[i].w > out[j].w
		}
		if norm(out[i].n.Label) != norm(out[j].n.Label) {
			return norm(out[i].n.Label) < norm(out[j].n.Label)
		}
		return out[i].n.ID < out[j].n.ID
	})
	ns := make([]Node, 0, len(out))
	for _, x := range out {
		ns = append(ns, x.n)
	}
	return ns
}

// PathsTo finds up to MaxPaths routes from somebody you know to the target.
func (s *Store) PathsTo(v View, targetID string, opt PathOptions) PathResult {
	res := PathResult{TargetID: targetID, Paths: []Path{}}
	if !v.valid() {
		return res
	}
	target, ok := s.Node(v, targetID)
	if !ok {
		return res
	}
	res.TargetLabel = target.Label
	res.LastTouch = s.LastTouch(v, targetID)

	if opt.MaxHops <= 0 {
		opt.MaxHops = defaultMaxHops
	}
	if opt.MaxPaths <= 0 {
		opt.MaxPaths = defaultMaxPaths
	}
	at := opt.At
	if at.IsZero() {
		at = time.Now().UTC()
	}

	seeds := s.Seeds(v)
	if len(seeds) == 0 {
		res.NoSeeds = true
		return res
	}
	for _, sd := range seeds {
		if sd.ID == targetID {
			res.YouKnowThem = true
			return res
		}
	}

	adj := s.relationAdjacency(v, at)
	mine := s.myStrengths(v)
	// Labels are resolved once. Reaching back into the store per hop would take
	// the read lock inside the search and would need a View to do it with,
	// which is how a fake View gets constructed and starts projecting somebody
	// else's private annotations.
	labels := map[string]string{}
	for _, n := range s.Nodes(v, NodeFilter{}) {
		labels[n.ID] = n.Label
	}

	var found []Path
	for _, sd := range seeds {
		walk(sd.ID, targetID, adj, mine, labels, opt.MaxHops, map[string]bool{sd.ID: true}, nil, &found)
	}
	sortPaths(found)
	if len(found) > opt.MaxPaths {
		found = found[:opt.MaxPaths]
	}
	res.Paths = found
	return res
}

type link struct {
	edge Edge
	to   string
}

// relationAdjacency builds the walkable graph once per query, sorted, so the
// search cannot depend on map order.
func (s *Store) relationAdjacency(v View, at time.Time) map[string][]link {
	adj := map[string][]link{}
	for _, e := range s.Edges(v, EdgeFilter{OpenAt: &at}) {
		if !relationEdges[e.Kind] {
			continue
		}
		adj[e.From] = append(adj[e.From], link{e, e.To})
		adj[e.To] = append(adj[e.To], link{e, e.From})
	}
	for k := range adj {
		ls := adj[k]
		sort.Slice(ls, func(i, j int) bool { return ls[i].edge.ID < ls[j].edge.ID })
		adj[k] = ls
	}
	return adj
}

func (s *Store) myStrengths(v View) map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]int{}
	for id, a := range s.notes[v.SeatID] {
		if a.TeamID == v.TeamID && a.Strength > 0 {
			out[id] = a.Strength
		}
	}
	return out
}

// walk enumerates simple paths depth-first. Bounded by MaxHops, which is what
// keeps an exhaustive search cheap: three hops over a personal network is a
// handful of branches, not a graph traversal problem.
func walk(cur, target string, adj map[string][]link, mine map[string]int, labels map[string]string,
	left int, seen map[string]bool, trail []Hop, out *[]Path) {
	if left == 0 {
		return
	}
	for _, l := range adj[cur] {
		if seen[l.to] {
			continue
		}
		st, isMine := mine[l.edge.ID]
		hop := Hop{
			EdgeID: l.edge.ID, Kind: l.edge.Kind,
			FromID: cur, FromLabel: labels[cur], ToID: l.to, ToLabel: labels[l.to],
			Context: l.edge.Context, Corroboration: l.edge.Corroboration(),
			Strength: st, Mine: isMine,
		}
		next := append(append([]Hop{}, trail...), hop)
		if l.to == target {
			*out = append(*out, buildPath(next))
			continue
		}
		seen[l.to] = true
		walk(l.to, target, adj, mine, labels, left-1, seen, next, out)
		delete(seen, l.to)
	}
}

func buildPath(hops []Hop) Path {
	p := Path{Hops: hops}
	for i, h := range hops {
		if h.Corroboration == Hearsay {
			p.Hearsay++
		}
		// Strength is 0 exactly when the hop is not yours - mine[edgeID] is
		// absent for anything you never rated - so ONE comparison covers both
		// "you rated it weakly" and "you cannot see how well they know each
		// other".
		//
		// There used to be a separate `if !h.Mine` branch here. It could never
		// change the result, which makes it worse than useless: a mutation
		// drill deleted it and every test stayed green, so it read as a guard
		// that was not guarding anything. One rule, one line.
		if i == 0 || h.Strength < p.Weakest {
			p.Weakest = h.Strength
		}
	}
	return p
}

// sortPaths orders by how usable a route is, not by how short it is.
//
//	FEWER HOPS IS NOT BETTER. A single hop through somebody you barely know is
//	a worse route than two hops through two strong ties, and offering the short
//	one first is how a user picks a route that goes nowhere. So: strongest
//	weakest-link first, then shortest, then least hearsay, then stable.
func sortPaths(ps []Path) {
	sort.SliceStable(ps, func(i, j int) bool {
		if ps[i].Weakest != ps[j].Weakest {
			return ps[i].Weakest > ps[j].Weakest
		}
		if len(ps[i].Hops) != len(ps[j].Hops) {
			return len(ps[i].Hops) < len(ps[j].Hops)
		}
		if ps[i].Hearsay != ps[j].Hearsay {
			return ps[i].Hearsay < ps[j].Hearsay
		}
		return pathID(ps[i]) < pathID(ps[j])
	})
}

func pathID(p Path) string {
	out := ""
	for _, h := range p.Hops {
		out += h.EdgeID + ">"
	}
	return out
}
