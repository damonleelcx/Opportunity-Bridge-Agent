package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/obs"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/talentsource"
)

// This file is the recruiter's entire action surface: search a pool of people
// who opted in, ask one of them for permission to make contact, and see the
// answers. Nothing else.
//
// The design rule the whole file serves, stated once so every function below can
// be checked against it:
//
//	A recruiter learns a person's identity only when that person decides they
//	should, for one named job, and can take it back afterwards.
//
// Everything here is a consequence of that sentence. Search returns cards with
// no name and no channel. Outreach is irreversible and therefore approval-gated.
// Acceptance is what releases a channel, and the channel is the one the person
// typed themselves. See docs/18-recruiter-and-outreach.md.

// CandidateRef derives the handle a recruiter uses to refer to a person.
//
// Two properties are wanted and both come from hashing the pair rather than
// storing a mapping:
//
//   - It cannot be turned back into a subject id. A ref that leaks - in a log,
//     in a screenshot, in an answer the model wrote - discloses nothing.
//   - It differs per recruiter. Two recruiters comparing their result lists
//     cannot tell they are looking at the same person, so the pool cannot be
//     re-identified by intersecting several searches.
//
// It is deterministic, so the same person is the same ref across turns and
// across restarts, which is what makes a two-step conversation possible at all.
func CandidateRef(recruiterID, subjectID string) string {
	sum := sha256.Sum256([]byte("oba-candidate-ref\x00" + recruiterID + "\x00" + subjectID))
	return "cand_" + hex.EncodeToString(sum[:5])
}

// candidateExperience is one job, without the free-text detail field.
//
// Details is dropped rather than trimmed because it is prose the person wrote
// about themselves, and prose is where names, employers and neighbourhoods end
// up. The structured three - what they did, for how long, in what sector - are
// what a match actually needs.
type candidateExperience struct {
	Title  string  `json:"title"`
	Years  float64 `json:"years,omitempty"`
	Sector string  `json:"sector,omitempty"`
}

// candidateCard is everything a recruiter may see about a person before that
// person has agreed to be contacted.
//
// This struct is the enforcement point, not a convenience: because the tool
// returns cards and never domain.Profile, a field that is not listed here cannot
// reach a recruiter by accident. What is deliberately absent, and why:
//
//   - SubjectID       - the ref exists so this never travels.
//   - HukouCity       - hiring on registered residence is discrimination that
//     Chinese labour rules specifically address, and this product will not be
//     the tool that makes it one filter away.
//   - Cohorts         - "migrant worker", "older worker", "disability". These
//     exist to ADD support on the resident's side. Exposed here they invert into
//     exactly the screening the product refuses to automate.
//   - AccessNeeds     - a person needing large text or a dialect speaker is a
//     fact about serving them, never about employing them.
//   - Constraints     - "must be home by 17:00 (childcare)" reads as caregiving
//     status. It belongs in a conversation the person is part of.
//   - Provenance/UpdatedAt - correlation handles, no matching value.
type candidateCard struct {
	Ref           string                `json:"candidate_ref"`
	City          string                `json:"city,omitempty"`
	Skills        []string              `json:"skills,omitempty"`
	MatchedSkills []string              `json:"matched_skills,omitempty"`
	Experience    []candidateExperience `json:"experience,omitempty"`
	Education     string                `json:"education,omitempty"`
	Seeking       []string              `json:"seeking,omitempty"`
	// ContactStatus is where this recruiter stands with this person:
	// not_requested, pending, accepted, declined or withdrawn.
	ContactStatus string `json:"contact_status"`
	// Channel is present only when ContactStatus is "accepted".
	Channel *domain.Channel `json:"channel,omitempty"`
}

func newCandidateCard(p domain.Profile, ref string, matched []string) candidateCard {
	c := candidateCard{
		Ref: ref, City: p.City, Skills: p.Skills, MatchedSkills: matched,
		Education: p.Education, Seeking: p.Interests,
		ContactStatus: "not_requested",
	}
	for _, e := range p.Experience {
		c.Experience = append(c.Experience, candidateExperience{Title: e.Title, Years: e.Years, Sector: e.Sector})
	}
	return c
}

// ---------------------------------------------------------- candidate_search

func candidateSearch() Tool {
	return Tool{
		Name:  "candidate_search",
		Roles: []domain.Role{domain.RoleRecruiter},
		Risk:  RiskRead,
		Description: "Search people who have OPTED IN to being found by employers. " +
			"This is not a resume database and it is not the whole population: it contains only people who " +
			"switched on 'discoverable by employers', and it empties again the moment they switch it off. " +
			"Results carry NO names and NO contact details - each is a card with an opaque candidate_ref, " +
			"the person's own stated skills, experience and city. To reach anybody you must call " +
			"outreach_request and they must accept; there is no other route and asking for one is refused. " +
			"THE POOL IS IN CHINESE - search with Chinese skill words (数控, 焊工, 养老护理, 保洁). " +
			"Ordering is by how many of the skills you asked for the person actually listed, and every card " +
			"shows which ones matched, so you can see the reason rather than trust a score. " +
			"You may NOT search by age, gender, household registration (户籍), marital or caregiving status, " +
			"disability, or any group label: those are not fields here and asking for them is refused. " +
			"State plainly to the user how many people are in the pool for their query - if it is small, say " +
			"so rather than implying the market is small.",
		Schema: Obj("Who you are looking for.", map[string]*Schema{
			"skills":    Arr("Skills required, in Chinese. Matching is on these.", Str("A skill"), 12),
			"city":      Str("City to look in, in Chinese (成都、深圳…). Omit to search nationwide."),
			"sectors":   Arr("Sectors the experience should be in.", Str("A sector"), 6),
			"min_years": Int("Minimum total years of relevant experience.", 0, 40),
			"limit":     Int("How many cards to return.", 1, 20),
		}),
		Run: func(ctx context.Context, env Env, a map[string]any) (Result, error) {
			want := lowerSet(argStrs(a, "skills"))
			city := strings.TrimSpace(argStr(a, "city"))
			sectors := lowerSet(argStrs(a, "sectors"))
			minYears := float64(argInt(a, "min_years", 0))
			limit := argInt(a, "limit", 8)

			recruiterID := env.Session.SubjectID
			sent := map[string]domain.Outreach{}
			for _, o := range env.Store.OutreachFrom(recruiterID) {
				// Newest first from the store, so the first one seen for a ref is the
				// current state of that relationship.
				if _, seen := sent[o.CandidateRef]; !seen {
					sent[o.CandidateRef] = o
				}
			}

			pool := env.Store.DiscoverableProfiles()
			var cards []candidateCard
			for _, p := range pool {
				if city != "" && !strings.EqualFold(strings.TrimSpace(p.City), city) {
					continue
				}
				var matched []string
				for _, s := range p.Skills {
					if want[strings.ToLower(strings.TrimSpace(s))] {
						matched = append(matched, s)
					}
				}
				if len(want) > 0 && len(matched) == 0 {
					continue
				}
				years := 0.0
				sectorHit := len(sectors) == 0
				for _, e := range p.Experience {
					years += e.Years
					if sectors[strings.ToLower(strings.TrimSpace(e.Sector))] {
						sectorHit = true
					}
				}
				if !sectorHit || years < minYears {
					continue
				}
				ref := CandidateRef(recruiterID, p.SubjectID)
				card := newCandidateCard(p, ref, matched)
				if o, ok := sent[ref]; ok {
					card.ContactStatus = string(o.Status)
					if o.Status == domain.OutreachAccepted {
						ch := o.Channel
						card.Channel = &ch
					}
				}
				cards = append(cards, card)
			}

			// Most matched skills first, then most experience, then ref for a stable
			// order. Deliberately NOT a composite score: the recruiter is shown the
			// two facts the order came from, so they can disagree with it. A single
			// number would be a ranking of people that nobody could argue with.
			sort.SliceStable(cards, func(i, j int) bool {
				if len(cards[i].MatchedSkills) != len(cards[j].MatchedSkills) {
					return len(cards[i].MatchedSkills) > len(cards[j].MatchedSkills)
				}
				yi, yj := totalYears(cards[i]), totalYears(cards[j])
				if yi != yj {
					return yi > yj
				}
				return cards[i].Ref < cards[j].Ref
			})
			total := len(cards)
			if len(cards) > limit {
				cards = cards[:limit]
			}

			env.Rec.Info(obs.CandidateSearched, "opt-in candidate pool searched",
				map[string]any{"pool_size": len(pool), "matched": total, "returned": len(cards)})

			return Result{
				Content: map[string]any{
					"candidates": cards,
					"matched":    total,
					"returned":   len(cards),
					"pool_size":  len(pool),
					"how_to_contact": "Contact details are not in this result and cannot be requested. " +
						"Call outreach_request with a candidate_ref and a named job; the person decides.",
				},
				Meta: map[string]any{
					"pool_size": len(pool), "matched": total, "returned": len(cards),
				},
			}, nil
		},
	}
}

// lowerSet is the case-insensitive membership test skill and sector matching
// both need. Kept here rather than in schema.go because it is about matching
// user-entered words, not about argument decoding.
func lowerSet(vals []string) map[string]bool {
	if len(vals) == 0 {
		return map[string]bool{}
	}
	m := make(map[string]bool, len(vals))
	for _, v := range vals {
		if t := strings.ToLower(strings.TrimSpace(v)); t != "" {
			m[t] = true
		}
	}
	return m
}

func totalYears(c candidateCard) float64 {
	t := 0.0
	for _, e := range c.Experience {
		t += e.Years
	}
	return t
}

// --------------------------------------------------------- outreach_request

func outreachRequest() Tool {
	return Tool{
		Name:  "outreach_request",
		Roles: []domain.Role{domain.RoleRecruiter},
		// Irreversible because it puts a request in front of a real person under
		// this recruiter's name. It cannot be unsent, and a careless or mistargeted
		// one costs the person attention they did not ask to spend.
		Risk: RiskIrreversible,
		Description: "Ask ONE person for permission to contact them about ONE named job. " +
			"Nothing is sent until a human approves this call, and the person still has to accept afterwards. " +
			"You do not get a name, a phone number or an email from this - only they can release that, and only " +
			"by accepting. Say the job, the pay and the place plainly in the message: the person is deciding " +
			"whether to hand a stranger their phone number, and a vague message is a reason to say no. " +
			"Write the message in the language the person's profile is in (Chinese unless told otherwise).",
		Schema: Obj("Who to ask, and about what.", map[string]*Schema{
			"candidate_ref": StrMin("The candidate_ref from candidate_search. Not a name.", 6),
			"position":      StrMin("The job on offer. A request with no named job is refused.", 2),
			"org":           StrMin("The employer's name, as the person will see it.", 2),
			"city":          Str("Where the work is."),
			"message": StrMin("What you want to say: the work, the pay, the hours, the place. "+
				"This is shown to the person verbatim.", 10),
		}, "candidate_ref", "position", "org", "message"),
		Run: func(ctx context.Context, env Env, a map[string]any) (Result, error) {
			recruiterID := env.Session.SubjectID
			ref := argStr(a, "candidate_ref")
			subjectID, ok := resolveCandidateRef(env, recruiterID, ref)
			if !ok {
				// Covers both "never existed" and "they left the pool", and says so
				// without distinguishing them: which of the two it is, is itself
				// information about a person who has opted out.
				return Result{}, fmt.Errorf(
					"CANDIDATE_NOT_AVAILABLE: %q does not match anybody currently open to being contacted. "+
						"They may have turned off discoverability. Run candidate_search again; do not retry this ref", ref)
			}
			o := env.Store.CreateOutreach(domain.Outreach{
				CandidateRef: ref,
				SubjectID:    subjectID,
				RecruiterID:  recruiterID,
				RecruiterOrg: argStr(a, "org"),
				Position:     argStr(a, "position"),
				City:         argStr(a, "city"),
				Message:      argStr(a, "message"),
			})
			env.Rec.Info(obs.OutreachRequested, "outreach request created",
				map[string]any{"outreach_id": o.ID, "candidate_ref": ref})
			return Result{
				Content: map[string]any{
					"outreach_id":    o.ID,
					"candidate_ref":  o.CandidateRef,
					"status":         string(o.Status),
					"what_happens":   "The person sees your message and decides. Nothing is shared with you unless they accept.",
					"contact_shared": false,
				},
				Meta: map[string]any{"outreach_created": true, "status": string(o.Status)},
			}, nil
		},
	}
}

// resolveCandidateRef turns a ref back into a subject by recomputing the hash
// over the people currently in the pool.
//
// Recomputing rather than storing a lookup table is what makes withdrawal work:
// somebody who turned discoverability off is simply not in the set being
// hashed, so their old ref stops resolving and no new request can reach them.
// A stored table would keep answering after they left.
func resolveCandidateRef(env Env, recruiterID, ref string) (string, bool) {
	for _, p := range env.Store.DiscoverableProfiles() {
		if CandidateRef(recruiterID, p.SubjectID) == ref {
			return p.SubjectID, true
		}
	}
	return "", false
}

// ------------------------------------------------------------ outreach_list

func outreachList() Tool {
	return Tool{
		Name:  "outreach_list",
		Roles: []domain.Role{domain.RoleRecruiter, domain.RoleResident, domain.RoleCaseworker},
		Risk:  RiskRead,
		// A caseworker reading the requests sent to a resident is reading that
		// resident's record, so it needs the same consent every other such read
		// needs. The resident reading their own needs nobody's permission.
		RoleConsent: caseworkerNeedsShare,
		Description: "List contact requests. For a recruiter: the ones you sent and where each one stands. " +
			"For a person: the employers who asked to contact you, and what they said. " +
			"A person answers with outreach_respond; a recruiter cannot answer on their behalf and must wait.",
		Schema: Obj("No arguments.", map[string]*Schema{}),
		Run: func(ctx context.Context, env Env, a map[string]any) (Result, error) {
			if env.Session.Role == domain.RoleRecruiter {
				var out []map[string]any
				pending := 0
				for _, o := range env.Store.OutreachFrom(env.Session.SubjectID) {
					if o.Status == domain.OutreachPending {
						pending++
					}
					// Built field by field rather than marshalling domain.Outreach,
					// because that struct carries SubjectID and this is the recruiter's
					// side of the wire.
					row := map[string]any{
						"outreach_id": o.ID, "candidate_ref": o.CandidateRef,
						"position": o.Position, "status": string(o.Status),
						"created_at": o.CreatedAt,
					}
					if o.Status == domain.OutreachAccepted {
						row["channel"] = o.Channel
					}
					if o.Status == domain.OutreachDeclined && o.Reason != "" {
						row["reason"] = o.Reason
					}
					out = append(out, row)
				}
				return Result{
					Content: map[string]any{"outreach": out, "pending": pending, "total": len(out)},
					Meta:    map[string]any{"outreach_count": len(out), "side": "recruiter"},
				}, nil
			}
			reqs := env.Store.OutreachFor(env.Session.SubjectID)
			pending := 0
			for _, o := range reqs {
				if o.Status == domain.OutreachPending {
					pending++
				}
			}
			return Result{
				Content: map[string]any{"outreach": reqs, "pending": pending, "total": len(reqs)},
				Meta:    map[string]any{"outreach_count": len(reqs), "side": "candidate"},
			}, nil
		},
	}
}

// --------------------------------------------------------- outreach_respond

func outreachRespond() Tool {
	return Tool{
		Name:  "outreach_respond",
		Roles: []domain.Role{domain.RoleResident, domain.RoleCaseworker},
		// A write, not an irreversible act: it changes our own record, and the
		// person can withdraw an acceptance afterwards. The thing that leaves our
		// boundary - the contact detail - is typed by the person in the same
		// breath, so the approval gate would be asking them to approve themselves.
		Risk:        RiskWrite,
		RoleConsent: caseworkerNeedsShare,
		Description: "Answer an employer's contact request: accept, decline, or withdraw an earlier acceptance. " +
			"Accepting REQUIRES the person to say what contact detail to hand over - a phone number or an " +
			"email - and nothing is released without it. Do not guess it, do not reuse one from elsewhere in " +
			"the conversation, and do not accept on the person's behalf: read them the employer, the job and " +
			"the message first, and let them say yes in their own words. Declining needs no reason and you " +
			"must not press for one.",
		Schema: Obj("The person's answer.", map[string]*Schema{
			"outreach_id": StrMin("Which request, from outreach_list.", 3),
			"decision":    Str("What the person decided.", "accepted", "declined", "withdrawn"),
			"contact": Str("Required to accept: the phone number or email THE PERSON said to give this employer. " +
				"Nothing is shared without it."),
			"contact_note": Str("Anything they want attached, e.g. when it is a good time to call."),
			"reason":       Str("Only if the person volunteered one when declining. Never invented."),
		}, "outreach_id", "decision"),
		Run: func(ctx context.Context, env Env, a map[string]any) (Result, error) {
			id := argStr(a, "outreach_id")
			decision := domain.OutreachStatus(argStr(a, "decision"))
			contact := strings.TrimSpace(argStr(a, "contact"))

			var ch domain.Channel
			if decision == domain.OutreachAccepted {
				if contact == "" {
					// Refused rather than accepted-with-nothing. An acceptance that
					// releases no channel looks like a yes to both sides and reaches
					// nobody, and the person would believe they had answered.
					return Result{}, fmt.Errorf(
						"CONTACT_REQUIRED: accepting releases a contact detail, and none was given. " +
							"Ask the person what number or email they want this employer to use, in their own words, " +
							"and pass exactly that. If they would rather not give one, the answer is decline")
				}
				ch = domain.Channel{Phone: contact, Language: env.Session.Locale}
				if note := strings.TrimSpace(argStr(a, "contact_note")); note != "" {
					ch.Hours = note
				}
			}
			o, err := env.Store.DecideOutreach(id, env.Session.SubjectID, decision, ch, argStr(a, "reason"))
			if err != nil {
				return Result{}, err
			}
			env.Rec.Info(obs.OutreachDecided, "outreach answered by the candidate",
				map[string]any{"outreach_id": o.ID, "status": string(o.Status)})
			return Result{
				Content: map[string]any{
					"outreach_id": o.ID, "status": string(o.Status),
					"contact_shared": o.Status == domain.OutreachAccepted,
					"what_happens":   outcomeSentence(o.Status),
				},
				Meta: map[string]any{
					"outreach_decided": string(o.Status),
					"contact_shared":   o.Status == domain.OutreachAccepted,
				},
			}, nil
		},
	}
}

func outcomeSentence(s domain.OutreachStatus) string {
	switch s {
	case domain.OutreachAccepted:
		return "The employer now has the contact detail given, and nothing else. It can be taken back at any time with 'withdrawn'."
	case domain.OutreachDeclined:
		return "The employer is told only that the answer was no. They are not told why unless a reason was given."
	case domain.OutreachWithdrawn:
		return "The contact detail is removed. The employer keeps whatever they already wrote down, which is why withdrawing early matters."
	}
	return ""
}

// ---------------------------------------------------- external_talent_scan

// externalTalentScan answers "how big is the market outside our pool".
//
// Why it is a separate tool rather than extra fields on candidate_search: the
// two return things with different legal status, and merging them would put a
// contactable person and an uncontactable stranger in one list for the model to
// tell apart by reading a flag. It would also make candidateCard's projection
// answer to two masters. gap_analysis is separate from opportunity_search for
// the same reason - a different question, over a different population, under
// different rules.
//
// What it exists FOR: a recruiter shown three people cannot tell whether three
// is the market or just the pool, and that is the question that decides whether
// they use this service at all. Answering it honestly is worth a tool. Answering
// it by handing over strangers' contact details is not, which is why it cannot.
func externalTalentScan() Tool {
	return Tool{
		Name:  "external_talent_scan",
		Roles: []domain.Role{domain.RoleRecruiter},
		Risk:  RiskRead,
		Description: "Estimate how many people of a given shape exist OUTSIDE this service's opt-in pool, " +
			"using external vendor indexes. This returns COUNTS and de-identified profile shapes - never a " +
			"name, an email, a phone number or a profile link, because these people were never asked. " +
			"You cannot contact anybody found here through this service and outreach_request will not accept " +
			"them; say so plainly rather than implying a route exists. " +
			"Every configured vendor is searched and the results come back COMBINED, with a by_vendor " +
			"breakdown. Show that breakdown: name each vendor, its own count, and - if it was unavailable - " +
			"say so and why, because a vendor you are not entitled to use contributes zero and an unmentioned " +
			"zero reads as 'nobody like that exists'. " +
			"The combined figure is a RANGE (estimated_total_at_least .. estimated_total_at_most), because the " +
			"indexes overlap by an unknown amount; never quote the upper bound on its own. " +
			"Use it for one purpose: telling an employer whether the pool being small means the MARKET is " +
			"small. Always report the pool count from candidate_search alongside it, and always pass on each " +
			"vendor's caveat, because a low count usually says more about what that vendor indexes than about " +
			"how many such workers exist.",
		Schema: Obj("The profile shape to size.", map[string]*Schema{
			"skills": Arr("Skills, in the vendor's language. These indexes are English-first. "+
				"‼️ Terms are combined with AND, so each one you add NARROWS the result. Pass ONE broad skill "+
				"(\"cnc\"), not a list of near-synonyms - \"cnc\" AND \"cnc machining\" AND \"machining\" matches "+
				"almost nobody and reads as an empty market.", Str("A skill"), 6),
			"title": Str("A job title to match, in English. ANDed with skills, and these indexes carry " +
				"office-work titles, so adding one usually collapses the count to zero. Prefer skills alone " +
				"and omit this unless a skills-only scan already returned something."),
			"country": Str("Country, e.g. china."),
			"region":  Str("City or province."),
			"limit":   Int("How many sample shapes to return.", 1, 25),
		}),
		Run: func(ctx context.Context, env Env, a map[string]any) (Result, error) {
			if env.Talent == nil {
				// A configured-off vendor must say so. Returning an empty result here
				// would read as "nobody like that exists anywhere", which is the most
				// misleading answer this tool could give.
				return Result{}, fmt.Errorf("EXTERNAL_SCAN_NOT_CONFIGURED: no external talent vendor is switched on " +
					"for this deployment, so the size of the market outside the pool is unknown. Say that it is " +
					"unknown - do NOT say it is small, and do not guess a number")
			}
			q := talentsource.Query{
				Skills:  argStrs(a, "skills"),
				Title:   argStr(a, "title"),
				Country: argStr(a, "country"),
				Region:  argStr(a, "region"),
				Limit:   argInt(a, "limit", 8),
			}
			found, err := env.Talent.Find(ctx, q)
			// Fail ONLY when no vendor answered at all.
			//
			// The first version bailed whenever the combined total was zero, which
			// collapsed two different facts: "every vendor was unreachable" and "a
			// vendor answered, and the honest answer was none". A live run hit
			// exactly that - PDL answered 0 for an over-narrow query while Apollo
			// 403'd - and the tool returned only Apollo's error. The model then told
			// the recruiter the scan "is configured to use Apollo.io", because PDL's
			// participation had been thrown away. That is the same silent-zero
			// failure this whole tool exists to prevent, committed by the tool.
			//
			// A vendor answering zero is a RESULT and must reach the reader with the
			// breakdown attached, so they can see which index was searched.
			var partial string
			if err != nil {
				answered := false
				for _, v := range found.PerVendor {
					if v.Error == "" {
						answered = true
						break
					}
				}
				if !answered {
					return Result{}, err
				}
				partial = err.Error()
			}
			vendors := make([]string, 0, len(found.PerVendor))
			for _, v := range found.PerVendor {
				vendors = append(vendors, fmt.Sprintf("%s=%d", v.Name, v.Total))
			}
			env.Rec.Info(obs.ExternalTalentScanned, "external talent indexes queried",
				map[string]any{
					"at_least": found.AtLeast, "at_most": found.Total,
					"leads": len(found.Leads), "truncated": found.Truncated, "by_vendor": vendors,
				})

			content := map[string]any{
				// Reported as a RANGE because the vendor indexes overlap by an
				// unknown amount: adding their totals double-counts anybody in both.
				// The lower bound is the largest single index, the upper is the sum.
				"estimated_total_at_least": found.AtLeast,
				"estimated_total_at_most":  found.Total,
				"is_a_floor":               found.Truncated,
				// The per-vendor rows are the point of a combined search. A vendor
				// that returned nothing and a vendor that was REFUSED both contribute
				// zero, and only this breakdown tells them apart.
				"by_vendor": found.PerVendor,
				"sample":    found.Leads,
				"how_to_read": "The two indexes overlap, so the true figure is somewhere in the range - " +
					"never quote the upper bound alone. Report each vendor separately as well, including any " +
					"that was unavailable: a vendor you are not entitled to use contributes zero, and a zero " +
					"that goes unmentioned reads as 'nobody like that exists'.",
				"pool_note": "These people are NOT in this service's pool and did not opt in. " +
					"No contact route exists for them here; outreach_request will refuse them.",
			}
			if partial != "" {
				// Named separately from by_vendor so the model cannot miss it: the
				// range below was computed WITHOUT whatever this vendor would have
				// contributed, so it is a floor rather than an estimate.
				content["incomplete"] = partial +
					" — the range above was computed without that vendor, so treat it as a floor, not an estimate."
			}
			return Result{
				Content: content,
				Meta: map[string]any{
					"external_total": found.Total, "external_at_least": found.AtLeast,
					"external_leads": len(found.Leads), "external_vendors": len(found.PerVendor),
					"external_truncated": found.Truncated, "external_partial": partial != "",
				},
			}, nil
		},
	}
}
