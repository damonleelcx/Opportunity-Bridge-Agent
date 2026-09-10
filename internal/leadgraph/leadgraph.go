// Package leadgraph is the storage layer of 猎源图谱 (Lead Graph): the graph of
// organisations, the units inside them, the people in those units, the events
// that move them, and the relationships that connect them across companies.
//
// WHY THIS IS ITS OWN PACKAGE, NOT A FEATURE OF internal/store
//
//	Opportunity Bridge Agent's founding sentence is "it does not decide
//	eligibility, and it does not score people". Lead Graph's whole job is to
//	rank situations and say when to act. Those two value systems cannot share
//	one intent registry without the newer one quietly overriding the older.
//	Keeping this tree self-contained is also what makes the split cheap if the
//	two products end up in separate repositories - see Q4 in
//	docs/20-lead-graph.zh-CN.md.
//
// THE ONE RULE EVERYTHING ELSE HANGS OFF
//
//	Nothing enters this graph without saying where it came from. A node or an
//	edge with no Intel is refused by the store itself, not by a verifier
//	further up. A verifier is a second line of defence; a caller that forgets
//	one is a plausible mistake, and the failure mode of an unsourced edge is
//	the worst one this product has: it changes who the user calls, and the user
//	cannot tell an invented relationship from a recorded one by looking at it.
//
// WHO OWNS WHAT (拍板 2026-09-10, Q1 in the PRD)
//
//	Facts belong to the TEAM: where somebody works, which group merged into
//	which, that two people were colleagues. Judgements belong to the SEAT: how
//	well I personally know somebody, and what I privately noted about them.
//
//	The split is not a filter, it is two different records - see Annotation.
//	A promise that "teammates cannot see your private note" enforced by a WHERE
//	clause is one forgotten join away from being false; enforced by the note
//	living in a row keyed by seat, it cannot be got wrong.
//
//	It is also the answer to the question that kills products in this category:
//	when a consultant leaves, what goes with them? Their judgements do; the
//	facts the team paid for stay. A single user is a team of one, so there is
//	one code path rather than a personal mode and a team mode.
//
// WHAT THIS PACKAGE DELIBERATELY DOES NOT DO
//
//	It does not decide that "王五" and "王总" are the same person. Exact-key
//	upserts are idempotent here; everything fuzzier is reconciliation (P2),
//	which is a separate, deterministic, testable function whose output a human
//	confirms. Doing it here would make merging a side effect of writing, and a
//	wrong merge is close to unrecoverable - two people become one and the
//	evidence that they were two is gone.
package leadgraph

import (
	"errors"
	"strings"
	"time"
)

// Actor is who is performing a write. It is not decoration: three rules in this
// package resolve differently for a person than for the agent, because the
// agent is allowed to observe and to propose, and is not allowed to decide.
type Actor string

const (
	// ActorUser is a person typing or confirming.
	ActorUser Actor = "user"
	// ActorAgent is the agent writing what it heard in a conversation or read
	// from a public source.
	ActorAgent Actor = "agent"
)

// View is who is looking. Every read and write takes one: facts are shared
// inside TeamID, annotations belong to SeatID. There is no way to ask this
// package a question without saying who is asking.
type View struct {
	TeamID string
	SeatID string
}

func (v View) valid() bool {
	return strings.TrimSpace(v.TeamID) != "" && strings.TrimSpace(v.SeatID) != ""
}

// Annotation is one seat's private overlay on a shared record: how well they
// know this person, and what they noted about them. Never visible to another
// seat, never merged into the team's facts, and deleted with the seat.
type Annotation struct {
	TeamID   string `json:"team_id"`
	SeatID   string `json:"seat_id"`
	TargetID string `json:"target_id"` // a node id or an edge id
	// Strength is 1..3 on a relationship edge, 0 for "not stated". Only a person
	// may set it; see ErrStrengthIsUserOnly.
	Strength  int       `json:"strength,omitempty"`
	Note      string    `json:"note,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// IntelKind is where one piece of evidence came from.
type IntelKind string

const (
	// IntelUserSaid is the user's own words. Defaults to hearsay: what somebody
	// heard about a reorganisation is often wrong, and the cost of acting on a
	// wrong rumour is paid by the person who gets contacted.
	IntelUserSaid IntelKind = "user_said"
	// IntelPublicSource is a lawfully public document: an announcement, a filing,
	// a published job posting, a company-registry change.
	IntelPublicSource IntelKind = "public_source"
	// IntelTeamShared is another seat in the same team.
	IntelTeamShared IntelKind = "team_shared"
	// IntelImported is a row of a file the user brought with them. TurnRef
	// identifies the FILE, not the row - two rows naming the same person are one
	// file saying it twice, and treating them as independent sources would
	// promote it to corroborated on its own. The row goes in Excerpt.
	IntelImported IntelKind = "imported"
)

// Corroboration is how well supported a claim is. It is DERIVED from the intel
// on a record rather than stored on it, so it cannot go stale: see
// Node.Corroboration.
type Corroboration string

const (
	Hearsay      Corroboration = "hearsay"      // 听说
	Corroborated Corroboration = "corroborated" // 多源印证
	Announced    Corroboration = "announced"    // 官方公布
)

// Intel is one piece of evidence. Excerpt holds the words themselves rather
// than a summary, because a summary cannot be checked against the source later
// and it is the thing a user needs when deciding whether to trust a claim.
type Intel struct {
	Kind      IntelKind `json:"kind"`
	SourceURL string    `json:"source_url,omitempty"` // required for public_source
	TurnRef   string    `json:"turn_ref,omitempty"`   // conversation turn, for user_said
	Excerpt   string    `json:"excerpt"`
	FetchedAt time.Time `json:"fetched_at"`
	// Announced marks an official statement (a filing, a company announcement)
	// as opposed to a report about one. Only this raises a record to Announced.
	Announced bool `json:"announced,omitempty"`
}

// blockedSourceHosts is the forbidden-source list. It is a list plus a store-level
// refusal plus a fence test, and deliberately NOT a configuration flag: a flag
// gets turned on. Turning this on has to mean editing code and passing review.
//
// Scraping these platforms' profiles, feeds or contact lists at once breaches
// their terms of service, risks 《刑法》285 (unlawfully obtaining computer system
// data) where anti-scraping measures are circumvented, and has no lawful basis
// under 《个人信息保护法》. See docs/20-lead-graph.zh-CN.md §07.
var blockedSourceHosts = []string{
	"linkedin.com",
	"maimai.cn",
	"zhipin.com",
	"liepin.com",
	"lagou.com",
	"51job.com/resume",
}

var (
	ErrViewRequired  = errors.New("VIEW_REQUIRED: every read and write must say which team and which seat is asking")
	ErrUnknownKind   = errors.New("UNKNOWN_KIND: node or edge kind is not one this graph recognises")
	ErrLabelRequired = errors.New("LABEL_REQUIRED: a node needs a label to be findable by a human")

	// ErrIntelRequired is the store's headline refusal. See the package comment.
	ErrIntelRequired   = errors.New("INTEL_REQUIRED: a node or edge must carry at least one piece of intel saying where it came from")
	ErrIntelIncomplete = errors.New("INTEL_INCOMPLETE: intel needs an excerpt, and a public source needs its URL")
	ErrSourceForbidden = errors.New("SOURCE_FORBIDDEN: this source host is on the forbidden list and cannot enter the graph")

	// ErrStrengthIsUserOnly guards the one number in this package that a machine
	// may not produce. Nothing can infer how well two people know each other;
	// a guess that reads as "strong" gets used as an introduction and embarrasses
	// the user in front of the person they were trying to reach.
	ErrStrengthIsUserOnly = errors.New("STRENGTH_IS_USER_ONLY: relationship strength is the user's judgement and cannot be written by the agent")
	ErrStrengthRange      = errors.New("STRENGTH_RANGE: relationship strength is 1..3")

	// ErrConflictNeedsUser is how "never overwrite silently" is enforced at the
	// only layer that cannot be bypassed. The agent may add what is missing; it
	// may not change what is already there. Changing it is a conversation.
	ErrConflictNeedsUser = errors.New("CONFLICT_NEEDS_USER: the agent may not replace an existing value; surface the conflict and let the user decide")

	ErrEndpointMissing = errors.New("EDGE_ENDPOINT_MISSING: both ends of an edge must be nodes this owner already has")
	ErrNotFound        = errors.New("NOT_FOUND: no such record for this team")

	// ErrResolutionRequired is how the two-tool split is enforced. A proposal the
	// store could not decide alone cannot be applied by simply passing it back:
	// somebody has to say which way it goes, and that answer is recorded.
	ErrResolutionRequired = errors.New("RESOLUTION_REQUIRED: this proposal needs a person to choose before it can be applied")
	ErrNotMergeable       = errors.New("NOT_MERGEABLE: two records of different kinds, or from different teams, cannot be merged")

	// ErrTouchpointTimeRequired: a contact with no time cannot answer the one
	// question contacts exist to answer - "has anybody spoken to him lately".
	ErrTouchpointTimeRequired = errors.New("TOUCHPOINT_TIME_REQUIRED: a contact record must say when it happened")
	// ErrNoSeeds is not an error the caller shows as a failure: it means the
	// question is unanswerable until the user says who they actually know.
	ErrNoSeeds = errors.New("NO_SEEDS: no relationship strength has been recorded, so there is nowhere to start from")

	// ErrSensitiveField refuses sensitive personal information at the write.
	// See sensitive.go for the two reasons and the false-positive trade.
	ErrSensitiveField = errors.New("SENSITIVE_FIELD: sensitive personal information cannot enter this graph")
)

// NodeKind is the closed set of things this graph holds. Closed rather than a
// free string because every new kind has to answer three questions before it
// exists: what edges may it carry, what colour is it on the graph, and can it
// be scored.
type NodeKind string

const (
	KindOrg    NodeKind = "org"    // 公司
	KindUnit   NodeKind = "unit"   // 部门 / 业务组 - the subject of a reorganisation
	KindPerson NodeKind = "person" // 人
	KindEvent  NodeKind = "event"  // 组织变动. Appended, never overwritten
	KindLead   NodeKind = "lead"   // 线索. Its subject is a unit or a role, never a person
)

// There is deliberately no "outreach" node kind. A contact record has no name
// and no identity of its own, so it cannot share naturalKey's meaning with the
// kinds above; it lives in its own type. See touchpoint.go.

func (k NodeKind) valid() bool {
	switch k {
	case KindOrg, KindUnit, KindPerson, KindEvent, KindLead:
		return true
	}
	return false
}

// EdgeKind is the closed set of lines. Three families: structure (the org
// chart), relation (who knows whom, across companies), influence (what feeds a
// lead).
type EdgeKind string

const (
	EdgeBelongsTo  EdgeKind = "belongs_to"
	EdgeReportsTo  EdgeKind = "reports_to"
	EdgeColleague  EdgeKind = "colleague_of"
	EdgeKnows      EdgeKind = "knows"
	EdgeReferredBy EdgeKind = "referred_by"
	EdgeInfluences EdgeKind = "influences"
)

func (k EdgeKind) valid() bool {
	switch k {
	case EdgeBelongsTo, EdgeReportsTo, EdgeColleague, EdgeKnows, EdgeReferredBy, EdgeInfluences:
		return true
	}
	return false
}

// FieldChange is the trail left by every change to a value that was already
// there. Without it a user can act, weeks later, on a fact that was quietly
// edited, with nothing on screen that would let them notice.
type FieldChange struct {
	At    time.Time `json:"at"`
	Field string    `json:"field"`
	From  string    `json:"from"`
	To    string    `json:"to"`
	By    Actor     `json:"by"`
	Why   Intel     `json:"why"`
	// Rejected marks a claim that was proposed and turned down. The value did
	// NOT change; the entry exists because "somebody said this and we decided
	// it was wrong" is worth keeping - it stops the same rumour being processed
	// as news the next time it arrives, and it is what lets a timeline show a
	// report that a later announcement contradicted.
	Rejected bool `json:"rejected,omitempty"`
}

// Node is one point on the graph.
type Node struct {
	ID string `json:"id"`
	// TeamID is the sharing boundary. Everything else on this struct except
	// Note is a fact and every seat in the team sees it.
	TeamID string   `json:"team_id"`
	Kind   NodeKind `json:"kind"`
	Label  string   `json:"label"`
	Org    string   `json:"org,omitempty"`
	// UnitPath is the org chart as this owner knows it, e.g.
	// ["A司","技术中心","c业务组"]. It is a path rather than a parent pointer
	// because most of the tree is unknown: a user knows the group they dealt
	// with and rarely the levels around it.
	UnitPath   []string   `json:"unit_path,omitempty"`
	RoleTitle  string     `json:"role_title,omitempty"`
	Duty       string     `json:"duty,omitempty"`
	OccurredAt *time.Time `json:"occurred_at,omitempty"`
	// Note is the reader's OWN private note, projected in at read time from
	// their Annotation. It is never stored on this record and never travels to
	// another seat. It also never feeds sorting, grouping, filtering or any
	// visual encoding: the moment a judgement about somebody becomes a sortable
	// field, this product is ranking people.
	Note string `json:"note,omitempty"`
	// Unconfirmed lists the fields that have no value yet. It is a state, not an
	// absence: "not asked" and "asked, nobody said" both read as empty otherwise.
	// Serialised as [] and never null - the interface counts it.
	Unconfirmed []string      `json:"unconfirmed"`
	Intel       []Intel       `json:"intel"`
	History     []FieldChange `json:"history,omitempty"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

// Corroboration is derived, never stored: one official source outranks any
// number of rumours, and two independent sources outrank one. Deriving it means
// it cannot drift out of step with the evidence actually on the record.
func (n Node) Corroboration() Corroboration { return corroborationOf(n.Intel) }

// Edge is one line on the graph.
type Edge struct {
	ID     string   `json:"id"`
	TeamID string   `json:"team_id"`
	Kind   EdgeKind `json:"kind"`
	From   string   `json:"from"`
	To     string   `json:"to"`
	// Strength is the READER'S OWN 1..3 judgement of this relationship,
	// projected in from their Annotation at read time. Two seats can hold
	// different numbers for the same edge, which is correct: they are different
	// relationships that happen to connect the same two people.
	Strength int `json:"strength,omitempty"`
	// Context is a fact ("2019-21 同项目") and therefore the team's.
	Context string `json:"context,omitempty"`
	// Note is the reader's OWN private note on this relationship, projected in
	// from their Annotation. Never stored here, never seen by another seat.
	Note string `json:"note,omitempty"`
	// ValidUntil closes an edge instead of deleting it. Three months later the
	// question "which group was he in at the time" still has to have an answer,
	// and that answer is this product's core asset.
	ValidUntil *time.Time    `json:"valid_until,omitempty"`
	Intel      []Intel       `json:"intel"`
	History    []FieldChange `json:"history,omitempty"`
	CreatedAt  time.Time     `json:"created_at"`
	UpdatedAt  time.Time     `json:"updated_at"`
}

func (e Edge) Corroboration() Corroboration { return corroborationOf(e.Intel) }

// Open reports whether this edge still holds at t.
func (e Edge) Open(t time.Time) bool { return e.ValidUntil == nil || e.ValidUntil.After(t) }

func corroborationOf(in []Intel) Corroboration {
	sources := map[string]bool{}
	for _, i := range in {
		if i.Announced {
			return Announced
		}
		sources[intelSource(i)] = true
	}
	if len(sources) > 1 {
		return Corroborated
	}
	return Hearsay
}

// intelSource identifies WHO said it, so that the same claim arriving twice from
// the same place does not look like two independent confirmations.
func intelSource(i Intel) string {
	switch {
	case i.SourceURL != "":
		return strings.ToLower(i.SourceURL)
	case i.TurnRef != "":
		return string(i.Kind) + ":" + i.TurnRef
	default:
		return string(i.Kind)
	}
}

func (i Intel) validate() error {
	switch i.Kind {
	case IntelUserSaid, IntelTeamShared, IntelImported:
		if strings.TrimSpace(i.Excerpt) == "" {
			return ErrIntelIncomplete
		}
	case IntelPublicSource:
		if strings.TrimSpace(i.Excerpt) == "" || strings.TrimSpace(i.SourceURL) == "" {
			return ErrIntelIncomplete
		}
	default:
		return ErrUnknownKind
	}
	if forbiddenSource(i.SourceURL) {
		return ErrSourceForbidden
	}
	return nil
}

// forbiddenSource matches on host and on host+path prefix, so that a resume
// section of an otherwise usable job board can be excluded without excluding the
// board's public postings.
func forbiddenSource(raw string) bool {
	if raw == "" {
		return false
	}
	s := strings.ToLower(raw)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	s = strings.TrimPrefix(s, "www.")
	for _, blocked := range blockedSourceHosts {
		if s == blocked || strings.HasPrefix(s, blocked+"/") || strings.HasPrefix(s, blocked+"?") {
			return true
		}
		if i := strings.IndexByte(s, '/'); i > 0 {
			host := s[:i]
			if host == blocked || strings.HasSuffix(host, "."+blocked) {
				return true
			}
		}
		if strings.HasSuffix(s, "."+blocked) {
			return true
		}
	}
	return false
}

// ---- names ----
//
// Chinese address forms are the reason reconciliation exists at all: the same
// person is 王五 in one turn, 王总 in the next and 老王 in the third. These two
// helpers are the ONLY name intelligence in this package, they are pure, and
// they are what the merge rules quote as their reason - so a proposal can always
// answer "why do you think these are the same person".

var honorificPrefixes = []string{"老", "小", "大"}
var honorificSuffixes = []string{"老师", "经理", "主管", "总监", "总", "工", "姐", "哥", "总裁"}

// nameCore strips the address form and leaves what is left of the name.
//
//	王总 -> 王      老王 -> 王      王五 -> 王五      李经理 -> 李
func nameCore(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > 1 {
		for _, p := range honorificPrefixes {
			if strings.HasPrefix(s, p) {
				s = string(r[1:])
				r = []rune(s)
				break
			}
		}
	}
	for _, suf := range honorificSuffixes {
		if len([]rune(s)) > len([]rune(suf)) && strings.HasSuffix(s, suf) {
			s = strings.TrimSuffix(s, suf)
			break
		}
	}
	return strings.TrimSpace(s)
}

// surname is the first character of the core. One character is all Chinese
// address forms preserve, which is exactly why it is a weak signal on its own
// and is never used without a second one (same organisation, same unit).
func surname(s string) string {
	r := []rune(nameCore(s))
	if len(r) == 0 {
		return ""
	}
	return string(r[0])
}
