// Package domain holds the vocabulary of the Opportunity Bridge Agent.
//
// One noun dictionary is used everywhere - Go code, JSON wire format, UI labels
// and documentation - so that a "case task" is never called a "workflow item" in
// one surface and a "todo" in another. See docs/03-intents.md.
package domain

import "time"

// Role is who is talking to the agent. Role gates which intents are reachable
// and which tools may run, because the four intents serve four different
// audiences with materially different data rights.
type Role string

const (
	// RoleResident is an individual acting for themselves (or a family member).
	RoleResident Role = "resident"
	// RoleCaseworker is frontline staff at an employment/social-service window,
	// acting on behalf of a resident who is present and has consented.
	RoleCaseworker Role = "caseworker"
	// RoleAnalyst is planning/policy staff. Analysts never see identified
	// records - only de-identified aggregates above the k-anonymity floor.
	RoleAnalyst Role = "analyst"
	// RoleRecruiter is an employer or agency looking for people to hire.
	//
	// This is the only role whose interest points the other way down the bridge:
	// every other role is trying to get one person to an opportunity, and a
	// recruiter is trying to get one opportunity to a person. That inversion is
	// why it carries the tightest limits in the product. A recruiter reaches
	// exactly one intent, sees only people who asked to be seen
	// (ConsentDiscoverable), sees them without names, and cannot reach anybody
	// until that person accepts. See docs/18-recruiter-and-outreach.md.
	RoleRecruiter Role = "recruiter"
)

// Roles is every actor this service recognises, in the order they appear in the
// interface's role picker.
//
// It exists for the same reason ConsentScopes does: the list had been written
// out by hand in the constants, the API's /api/meta payload and the interface,
// and a role missing from any one of them fails differently and quietly. Missing
// from meta, the role cannot be selected and the intent behind it is dead code
// that still passes its tests.
func Roles() []Role {
	return []Role{RoleResident, RoleCaseworker, RoleAnalyst, RoleRecruiter}
}

func (r Role) Valid() bool {
	for _, k := range Roles() {
		if k == r {
			return true
		}
	}
	return false
}

// AccessNeed describes a friction that would otherwise keep somebody out of the
// service. These are set explicitly by the user (or by a caseworker on their
// behalf) - never inferred from demographics, because inferring them would be
// exactly the kind of opaque profiling this product forbids.
type AccessNeed string

const (
	AccessPlainLanguage AccessNeed = "plain_language" // short sentences, no jargon
	AccessLargeText     AccessNeed = "large_text"
	AccessVoice         AccessNeed = "voice"         // read answers aloud / accept speech
	AccessDialect       AccessNeed = "dialect"       // user speaks a regional variety
	AccessAssisted      AccessNeed = "assisted"      // a human is helping at a window
	AccessLowBandwidth  AccessNeed = "low_bandwidth" // avoid long answers, no rich media
)

// CohortTag marks the high-friction populations the product explicitly covers.
// Tags are self-declared and used only to select which guidance and programs to
// surface. They must never be used to withhold an opportunity.
type CohortTag string

const (
	CohortGraduate      CohortTag = "graduate"       // 毕业生
	CohortTransitioning CohortTag = "transitioning"  // 转岗工人
	CohortGigWorker     CohortTag = "gig_worker"     // 灵活就业者
	CohortMigrantWorker CohortTag = "migrant_worker" // 农民工
	CohortCaregiver     CohortTag = "caregiver"      // 照护家庭
	CohortOlderWorker   CohortTag = "older_worker"   // 大龄劳动者
	CohortDisability    CohortTag = "disability"     // 残障人士
)

// Profile is the consented picture of one person: skills, history, place and
// constraints. It is the input to matching and the thing the user can inspect,
// correct and delete. Nothing here is inferred silently - every field carries
// the turn that wrote it.
type Profile struct {
	SubjectID   string       `json:"subject_id"`
	City        string       `json:"city,omitempty"`
	HukouCity   string       `json:"hukou_city,omitempty"` // registered residence, drives program eligibility
	Skills      []string     `json:"skills,omitempty"`
	Experience  []Experience `json:"experience,omitempty"`
	Education   string       `json:"education,omitempty"`
	Constraints []string     `json:"constraints,omitempty"` // e.g. "must be home by 17:00 (childcare)"
	Cohorts     []CohortTag  `json:"cohorts,omitempty"`
	AccessNeeds []AccessNeed `json:"access_needs,omitempty"`
	Interests   []string     `json:"interests,omitempty"`
	UpdatedAt   time.Time    `json:"updated_at"`
	// Provenance records, per field name, which conversation turn asserted it.
	Provenance map[string]string `json:"provenance,omitempty"`
}

type Experience struct {
	Title   string  `json:"title"`
	Years   float64 `json:"years,omitempty"`
	Sector  string  `json:"sector,omitempty"`
	Details string  `json:"details,omitempty"`
}

// OpportunityKind separates the four things a person can be pointed at. They
// share one record type because matching, explaining and follow-up are the same
// motions for all four; only the eligibility text differs.
type OpportunityKind string

const (
	KindJob          OpportunityKind = "job"
	KindTraining     OpportunityKind = "training"
	KindEntrepreneur OpportunityKind = "entrepreneurship"
	KindSubsidy      OpportunityKind = "subsidy"
)

// Opportunity is one concrete thing a person can apply to or enrol in.
type Opportunity struct {
	ID        string          `json:"id"`
	Kind      OpportunityKind `json:"kind"`
	Title     string          `json:"title"`
	Org       string          `json:"org"`
	City      string          `json:"city"`
	District  string          `json:"district,omitempty"`
	Summary   string          `json:"summary"`
	Skills    []string        `json:"skills,omitempty"`
	Sectors   []string        `json:"sectors,omitempty"`
	Cohorts   []CohortTag     `json:"cohorts,omitempty"` // populations this is designed for
	SalaryMin int             `json:"salary_min,omitempty"`
	SalaryMax int             `json:"salary_max,omitempty"`
	Amount    string          `json:"amount,omitempty"` // for subsidies: what is paid
	Schedule  string          `json:"schedule,omitempty"`
	Remote    bool            `json:"remote,omitempty"`
	Criteria  []Criterion     `json:"criteria,omitempty"`
	Channel   Channel         `json:"channel"`
	SourceRef string          `json:"source_ref"` // citation the answer must carry
	Deadline  string          `json:"deadline,omitempty"`
	// Scope is "national" for records that apply anywhere in the country. A
	// national record has an empty City and is returned for every city, because
	// the alternative — telling somebody in an uncovered city that there is
	// nothing for them — is false. The national framework is real, and for most
	// people it is the part that matters.
	Scope    string `json:"scope,omitempty"`
	Openings int    `json:"openings,omitempty"`
	Demand   int    `json:"demand,omitempty"` // recorded interest, for gap analysis
}

// Criterion is one published requirement, quoted from the source document.
//
// The agent may report whether a criterion looks met, unmet or unknown given
// what the user told it. It may never collapse that list into an eligibility
// verdict - only the issuing authority does that. See docs/02-boundaries.md.
type Criterion struct {
	Code      string `json:"code"`
	Text      string `json:"text"`
	Evidence  string `json:"evidence,omitempty"` // what document proves it
	SourceRef string `json:"source_ref,omitempty"`
}

// Channel is how a person actually gets this done, including the offline route.
type Channel struct {
	Online   string `json:"online,omitempty"`
	Phone    string `json:"phone,omitempty"`
	Window   string `json:"window,omitempty"` // physical service window address
	Hours    string `json:"hours,omitempty"`
	Language string `json:"language,omitempty"`
}

// ServiceDomain is the set of siloed systems a person otherwise has to walk
// between. Bridging them into one tracked list is intent 3's whole job.
type ServiceDomain string

const (
	ServiceEmployment ServiceDomain = "employment"
	ServiceTraining   ServiceDomain = "training"
	ServiceSocialIns  ServiceDomain = "social_insurance"
	ServiceMedical    ServiceDomain = "medical_insurance"
	ServiceChildcare  ServiceDomain = "childcare"
	ServiceEldercare  ServiceDomain = "eldercare"
	ServiceHousing    ServiceDomain = "housing"
)

// TaskStatus is the small, closed lifecycle of a case task. Kept deliberately
// short: every extra state is a state somebody has to explain at a window.
type TaskStatus string

const (
	TaskOpen      TaskStatus = "open"
	TaskWaiting   TaskStatus = "waiting" // waiting on the authority or on a document
	TaskBlocked   TaskStatus = "blocked" // needs a human to unblock
	TaskDone      TaskStatus = "done"
	TaskCancelled TaskStatus = "cancelled"
)

// CaseTask is one tracked step in a person's path across service silos.
type CaseTask struct {
	ID        string        `json:"id"`
	SubjectID string        `json:"subject_id"`
	Domain    ServiceDomain `json:"domain"`
	Title     string        `json:"title"`
	Detail    string        `json:"detail,omitempty"`
	Status    TaskStatus    `json:"status"`
	Owner     string        `json:"owner,omitempty"` // "resident" or a caseworker id
	DueDate   string        `json:"due_date,omitempty"`
	LinkedRef string        `json:"linked_ref,omitempty"` // opportunity or program id
	Channel   Channel       `json:"channel,omitempty"`
	Blocker   string        `json:"blocker,omitempty"`
	History   []TaskEvent   `json:"history,omitempty"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
}

type TaskEvent struct {
	At     time.Time  `json:"at"`
	Status TaskStatus `json:"status"`
	Note   string     `json:"note,omitempty"`
	By     string     `json:"by,omitempty"`
}

// ConsentScope is the unit of permission. Nothing that leaves the individual's
// own session - aggregation, caseworker visibility, submission on their behalf -
// happens without the matching scope being granted first.
type ConsentScope string

const (
	ConsentStoreProfile    ConsentScope = "store_profile"
	ConsentShareCaseworker ConsentScope = "share_with_caseworker"
	ConsentSubmitOnBehalf  ConsentScope = "submit_on_behalf"
	ConsentAggregate       ConsentScope = "aggregate_deidentified"
	// ConsentReadAloudVendor covers sending the ANSWER TEXT to a speech vendor so
	// it can be read aloud. It is a scope rather than a setting because the text
	// is the person's situation - their city, their unemployment, the benefit
	// they are claiming - and this service already asks permission merely to
	// STORE those same facts. Disclosing that it leaves is not the same as
	// asking. Nothing is sent unless the person presses read-aloud, and refusing
	// costs them nothing: the browser's own voice reads it instead.
	// See docs/bugfix/2026-08-31-read-aloud-needs-consent.md
	ConsentReadAloudVendor ConsentScope = "read_aloud_via_vendor"
	// ConsentDiscoverable puts this person into the pool a recruiter may search.
	//
	// It is opt-in and it is separate from every other scope on purpose. Being
	// counted in a statistic (ConsentAggregate) and being findable by an employer
	// are different exposures with different consequences, and a person who
	// agreed to the first has not agreed to the second. Nobody is in the pool by
	// default, no other scope implies it, and withdrawing it removes them from
	// the next search - see Store.DiscoverableProfiles, which filters on this
	// scope so that no caller can forget to.
	//
	// What it does NOT grant: it never releases a name or a contact channel.
	// Those move only when the person accepts a specific Outreach.
	ConsentDiscoverable ConsentScope = "discoverable_by_employers"
)

// ConsentScopes is every permission this service asks for, in the order a person
// meets them.
//
// It exists because the list was written out by hand in four places - these
// constants, the API's validation, the interface's revoke panel and the
// consent_request tool schema - and a scope missing from any one of them fails
// differently and quietly. Missing from the API, granting it answers 400.
// Missing from the panel, the person cannot withdraw it, which turns "you can
// withdraw this at any time" into something this service says and does not do.
func ConsentScopes() []ConsentScope {
	return []ConsentScope{
		ConsentStoreProfile,
		ConsentShareCaseworker,
		ConsentSubmitOnBehalf,
		ConsentAggregate,
		ConsentReadAloudVendor,
		ConsentDiscoverable,
	}
}

// notYetOfferedScopes exist in this file but are deliberately NOT offered to
// anybody yet, because nothing reads them.
//
// It is a LIST, not a deletion, so the omission is a written decision rather
// than a line somebody forgot. TestEveryScopeIsOfferedOrExplained refuses to let
// a scope be in neither list, which is the failure this replaces: a scope
// missing from ConsentScopes() is one the API rejects, the model cannot ask for,
// and the interface renders no control to withdraw - all silently.
// See docs/bugfix/2026-08-31-read-aloud-needs-consent.md
//
// It is EMPTY, and that is the correct state, not a sign the mechanism is unused.
// ConsentDiscoverable sat here for the few minutes between the scope being
// declared and the tools that read it landing: at that moment nothing reached
// Store.DiscoverableProfiles, so offering the permission would have put somebody
// into a pool nothing searched. candidate_search now reaches it and
// talent_sourcing routes to it, so the premise for withholding it is gone and
// the line moved back - which is exactly the move the withholding commit said
// wiring the feature up would require.
// See docs/18-recruiter-and-outreach.md
func notYetOfferedScopes() []ConsentScope {
	return nil
}

// NotYetOffered reports whether a scope exists but is withheld. Exported so an
// operator surface can say "known, not switched on" rather than "unknown".
func NotYetOffered(s string) bool {
	for _, k := range notYetOfferedScopes() {
		if string(k) == s {
			return true
		}
	}
	return false
}

// IsConsentScope reports whether a string names a permission this service asks
// for. One membership test, so the API and the tools cannot disagree about it.
func IsConsentScope(s string) bool {
	for _, k := range ConsentScopes() {
		if string(k) == s {
			return true
		}
	}
	return false
}

type ConsentGrant struct {
	Scope     ConsentScope `json:"scope"`
	Granted   bool         `json:"granted"`
	GrantedAt time.Time    `json:"granted_at"`
	Note      string       `json:"note,omitempty"`
}

// OutreachStatus is the lifecycle of one contact request. It is short for the
// same reason TaskStatus is: every extra state is a state somebody has to be
// told about.
type OutreachStatus string

const (
	// OutreachPending means the candidate has not answered yet. The recruiter
	// has no name and no channel while a request sits here.
	OutreachPending OutreachStatus = "pending"
	// OutreachAccepted is the ONLY state in which a contact channel is released.
	OutreachAccepted OutreachStatus = "accepted"
	OutreachDeclined OutreachStatus = "declined"
	// OutreachWithdrawn is the candidate taking back an acceptance. It closes the
	// channel again; consent that cannot be withdrawn was never consent.
	OutreachWithdrawn OutreachStatus = "withdrawn"
)

// Outreach is one recruiter asking to contact one person, and that person's
// answer. It is the hinge the whole recruiter feature turns on.
//
// Why a record and not a message: "the employer may contact you" is a decision
// with two parties, a before and an after, and a state that must survive the
// conversation that created it. Held only in chat it would be unauditable - the
// person could not later see who asked, what they were told, or withdraw it. The
// record is also what the guardrails read: an answer may carry a contact channel
// only if an Outreach in state OutreachAccepted says it may.
//
// SubjectID never leaves the server on a recruiter's behalf. The recruiter knows
// only CandidateRef, which is derived per recruiter so that two recruiters
// comparing notes cannot tell they are looking at the same person.
type Outreach struct {
	ID string `json:"id"`
	// CandidateRef is the handle the recruiter sees. See tools.CandidateRef.
	CandidateRef string `json:"candidate_ref"`
	// SubjectID is who this is actually about. Recruiter-facing payloads must
	// strip it; resident-facing ones may keep it, because it is their own.
	SubjectID    string `json:"subject_id"`
	RecruiterID  string `json:"recruiter_id"`
	RecruiterOrg string `json:"recruiter_org"`
	// Position is the concrete job on offer. A request with no named job is a
	// fishing expedition and the tool schema refuses it.
	Position string         `json:"position"`
	City     string         `json:"city,omitempty"`
	Message  string         `json:"message"`
	Status   OutreachStatus `json:"status"`
	// Channel is how the two sides actually reach each other once accepted. It is
	// empty in every other state, including after a withdrawal.
	Channel   Channel   `json:"channel,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	DecidedAt time.Time `json:"decided_at,omitempty"`
	// Reason is what the candidate said when declining, if they chose to say.
	Reason string `json:"reason,omitempty"`
}

// DemandSignal is one de-identified record contributed to gap analysis. It is
// written only when ConsentAggregate is held, and it carries no subject id - the
// link back to a person is deliberately not recoverable.
type DemandSignal struct {
	At       time.Time       `json:"at"`
	City     string          `json:"city"`
	District string          `json:"district,omitempty"`
	Kind     OpportunityKind `json:"kind"`
	Sector   string          `json:"sector,omitempty"`
	Cohort   CohortTag       `json:"cohort,omitempty"`
	// Outcome says what happened to the person's attempt, which is what turns a
	// search log into a usable gap signal.
	Outcome string `json:"outcome"` // "matched" | "no_match" | "blocked" | "abandoned"
	Blocker string `json:"blocker,omitempty"`
}
