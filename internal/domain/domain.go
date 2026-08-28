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
)

func (r Role) Valid() bool {
	switch r {
	case RoleResident, RoleCaseworker, RoleAnalyst:
		return true
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
)

type ConsentGrant struct {
	Scope     ConsentScope `json:"scope"`
	Granted   bool         `json:"granted"`
	GrantedAt time.Time    `json:"granted_at"`
	Note      string       `json:"note,omitempty"`
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
