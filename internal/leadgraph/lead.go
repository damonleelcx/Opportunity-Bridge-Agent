package leadgraph

import (
	"sort"
	"time"
)

// The lead board: which situations are worth time right now, and why.
//
// THREE CONSTRAINTS, AND HOW EACH ONE IS ENFORCED RATHER THAN PROMISED
//
//  1. The subject is a GROUP, not a person. Lead has no person field at all.
//     Organisational signals cannot support a claim about an individual's
//     intentions; a fabricated "王五 87%" gets used in the first sentence of a
//     call and falls apart there, which is worse than saying nothing.
//
//  2. A score may not exist without the signals that make it. Score is a
//     METHOD over Signals, not a stored field - so there is no way to hold
//     one without the other, and no future code path that forgets to attach
//     them. A verifier would have been the weaker version of this.
//
//  3. Scores do not leave. They are rendered on the product's own screen and
//     are absent from Export and from the machine-readable snapshot. A
//     departure-likelihood ranking that escapes can cost somebody their job.
//
// WHY WEIGHTS ARE A TABLE
//
//	Every number below is on screen next to the thing it came from. Scattering
//	them through conditionals would make the board unexplainable exactly when
//	somebody disagrees with it - which is the moment the explanation is for.

// SignalKind is what kind of evidence a contribution is. Closed: a kind with no
// derivation rule would be a weight nobody can trace.
type SignalKind string

const (
	// SignalUnitEvent - something happened to this group: a merger, a split, a
	// restructure. Weight rises with how well attested it is.
	SignalUnitEvent SignalKind = "unit_event"
	// SignalHiringStopped - the group has not advertised in a long time. Weak on
	// its own, meaningful next to an event.
	SignalHiringStopped SignalKind = "hiring_stopped"
	// SignalHiringActive - it is still advertising. A NEGATIVE contribution: a
	// group that is still hiring is a group that is not dissolving.
	SignalHiringActive SignalKind = "hiring_active"
)

// Thresholds and weights, in one place, all named.
const (
	// HiringStoppedAfter is how long without an advert counts as stopped. Two
	// quarters: shorter than that is ordinary seasonality in most companies.
	HiringStoppedAfter = 180 * 24 * time.Hour
	// HiringActiveWithin is how recent an advert has to be to count as active.
	HiringActiveWithin = 30 * 24 * time.Hour
	// LeadWindow is how long a signal is treated as current. It is a stated
	// convention, not a measurement, and the screen says so next to the number.
	LeadWindow = 90 * 24 * time.Hour
)

// signalWeights maps a signal to its contribution. An event's weight rises with
// corroboration: a filing is worth more than something somebody heard, and that
// difference is the whole reason corroboration is tracked.
var signalWeights = map[SignalKind]map[Corroboration]int{
	SignalUnitEvent: {
		Announced:    3,
		Corroborated: 2,
		Hearsay:      1,
	},
	SignalHiringStopped: {
		Announced: 2, Corroborated: 2, Hearsay: 2,
	},
	SignalHiringActive: {
		Announced: -1, Corroborated: -1, Hearsay: -1,
	},
}

// Signal is one named, sourced contribution. Everything needed to argue with it
// travels with it: what it is, what it is worth, where it came from, and when.
type Signal struct {
	Kind   SignalKind `json:"kind"`
	Weight int        `json:"weight"`
	// Because is the thing itself when there is one - an event's own label,
	// which is the user's data. It is EMPTY for derived signals: their sentence
	// is built by the template from Kind and Days.
	//
	// Why not a ready-made sentence here: the screen is Chinese and this file
	// is Go. Writing "no advert seen for 240 days" into the struct put English
	// on a Chinese page and scattered the wording across two languages'
	// worth of files - the same mistake the turn receipt avoided by carrying
	// counts instead of prose.
	Because string `json:"because,omitempty"`
	// Days is the measurement a derived signal rests on.
	Days          int           `json:"days,omitempty"`
	SourceURL     string        `json:"source_url,omitempty"`
	Corroboration Corroboration `json:"corroboration"`
	At            time.Time     `json:"at"`
}

// Lead is one group worth looking at.
type Lead struct {
	Org       string `json:"org"`
	UnitLabel string `json:"unit_label"`
	// Paths is every place in the chart this group name sits. Usually one. More
	// than one is the same-name-in-two-places ambiguity the chart reports, and
	// it is shown here rather than resolved: the board is one row per SITUATION
	// (an event named a company and a group), and there is one such situation
	// however many places the name turns up.
	//
	// It used to be one row per chart node, which printed the same group twice
	// with the same score - once with the people and once without. The first
	// look at a realistic board caught it.
	Paths   [][]string `json:"paths"`
	Signals []Signal   `json:"signals"`
	// People are who you know there, in NAME order. Deliberately not ordered by
	// anything else: the group is what carries a score, and the moment the list
	// under it is ranked, the product is ranking people again.
	People    []ChartPerson `json:"people"`
	LastTouch *Touchpoint   `json:"last_touch,omitempty"`
}

// Score is the sum of the signals, and exists only through them.
//
// A method rather than a field: a score cannot be constructed, stored,
// deserialised or copied without the evidence, because there is nothing to
// copy. This is the structural form of "分数必须附带构成它的具名信号".
func (l Lead) Score() int {
	n := 0
	for _, s := range l.Signals {
		n += s.Weight
	}
	return n
}

// Newest is when the most recent signal happened - what the window counts from.
func (l Lead) Newest() time.Time {
	var t time.Time
	for _, s := range l.Signals {
		if s.At.After(t) {
			t = s.At
		}
	}
	return t
}

// WindowLeft is how much of LeadWindow remains from the newest signal. It is a
// convention applied to a date, not a prediction, and the screen prints the
// convention next to it.
func (l Lead) WindowLeft(at time.Time) time.Duration {
	n := l.Newest()
	if n.IsZero() {
		return 0
	}
	left := LeadWindow - at.Sub(n)
	if left < 0 {
		return 0
	}
	return left
}

// WindowDays is WindowLeft in whole days, for the screen.
func (l Lead) WindowDays(at time.Time) int { return int(l.WindowLeft(at).Hours() / 24) }

// Bar renders a signal's weight as the plus and minus marks the board shows.
// A symbol rather than the number because the board is read at a glance and the
// exact integer is meaningless on its own - it is the NAME beside it that
// carries the meaning.
func (s Signal) Bar() string {
	if s.Weight == 0 {
		return "·"
	}
	mark, n := "+", s.Weight
	if n < 0 {
		mark, n = "−", -n
	}
	out := ""
	for range n {
		out += mark
	}
	return out
}

// LeadBoard assembles the board for a team.
//
// Groups with no signals are absent rather than listed at zero: a board that
// lists everything is a directory, and the reader stops scanning it.
func (s *Store) LeadBoard(v View, at time.Time) []Lead {
	out := []Lead{}
	if !v.valid() {
		return out
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}

	events := s.Nodes(v, NodeFilter{Kind: KindEvent})
	byUnit := map[string]*Lead{}
	var order []string

	for _, c := range s.chartsFor(v, at) {
		var walk func(ns []ChartNode)
		walk = func(ns []ChartNode) {
			for _, x := range ns {
				key := norm(c.Org) + "|" + norm(x.Label)
				l, seen := byUnit[key]
				if !seen {
					l = &Lead{Org: c.Org, UnitLabel: x.Label, Paths: [][]string{},
						Signals: []Signal{}, People: []ChartPerson{}}
					byUnit[key] = l
					order = append(order, key)
				}
				l.Paths = append(l.Paths, x.Path)
				l.People = append(l.People, x.People...)

				// Signals belong to the NAME, so they are gathered once however
				// many places in the chart that name turns up.
				if !seen {
					for _, e := range events {
						if norm(e.Org) != norm(c.Org) || !unitNamed(e.UnitPath, x.Label) {
							continue
						}
						cor := e.Corroboration()
						when := e.UpdatedAt
						if e.OccurredAt != nil {
							when = *e.OccurredAt
						}
						l.Signals = append(l.Signals, Signal{
							Kind: SignalUnitEvent, Weight: signalWeights[SignalUnitEvent][cor],
							Because: e.Label, SourceURL: sourceOf(e), Corroboration: cor, At: when,
						})
					}
					if gap := s.HiringGap(v, c.Org, x.Label, at); gap != nil {
						switch {
						case *gap > HiringStoppedAfter:
							l.Signals = append(l.Signals, Signal{
								Kind: SignalHiringStopped, Weight: signalWeights[SignalHiringStopped][Corroborated],
								Days: days(*gap), Corroboration: Corroborated, At: at.Add(-*gap),
							})
						case *gap < HiringActiveWithin:
							l.Signals = append(l.Signals, Signal{
								Kind: SignalHiringActive, Weight: signalWeights[SignalHiringActive][Corroborated],
								Days: days(*gap), Corroboration: Corroborated, At: at.Add(-*gap),
							})
						}
					}
				}
				walk(x.Children)
			}
		}
		walk(c.Root)
	}

	for _, key := range order {
		l := byUnit[key]
		if len(l.Signals) == 0 {
			continue
		}
		sort.Slice(l.Signals, func(i, j int) bool {
			if l.Signals[i].Weight != l.Signals[j].Weight {
				return l.Signals[i].Weight > l.Signals[j].Weight
			}
			return l.Signals[i].Kind < l.Signals[j].Kind
		})
		l.People = dedupePeople(l.People)
		if len(l.People) > 0 {
			l.LastTouch = s.LastTouch(v, l.People[0].NodeID)
		}
		out = append(out, *l)
	}

	// Groups are ranked. People are not - see Lead.People.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score() != out[j].Score() {
			return out[i].Score() > out[j].Score()
		}
		if !out[i].Newest().Equal(out[j].Newest()) {
			return out[i].Newest().After(out[j].Newest())
		}
		if norm(out[i].Org) != norm(out[j].Org) {
			return norm(out[i].Org) < norm(out[j].Org)
		}
		return norm(out[i].UnitLabel) < norm(out[j].UnitLabel)
	})
	return out
}

// chartsFor is the org list plus their charts, in one place so the board and
// the screen ask the same question.
func (s *Store) chartsFor(v View, at time.Time) []Chart {
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
	out := make([]Chart, 0, len(names))
	for _, o := range names {
		out = append(out, s.OrgChart(v, o, at))
	}
	return out
}

// unitNamed reports whether a path names this unit. Same rule as the alerts use
// and for the same reason: an event knows a company and a unit, while the chart
// holds that unit wherever the user placed it. See peopleUnder.
func unitNamed(path []string, label string) bool {
	return len(path) > 0 && norm(path[len(path)-1]) == norm(label)
}

func sourceOf(n Node) string {
	for _, i := range n.Intel {
		if i.SourceURL != "" {
			return i.SourceURL
		}
	}
	return ""
}

func days(d time.Duration) int { return int(d.Hours()/24 + 0.5) }

// dedupePeople folds the same person appearing under two spellings of one group
// name into one entry, in name order.
func dedupePeople(ps []ChartPerson) []ChartPerson {
	seen := map[string]bool{}
	out := make([]ChartPerson, 0, len(ps))
	for _, p := range ps {
		if seen[p.NodeID] {
			continue
		}
		seen[p.NodeID] = true
		out = append(out, p)
	}
	sortPeople(out)
	return out
}
