// Package talentsource is the seam for looking for people OUTSIDE the
// first-party opt-in pool.
//
// It exists because the pool is only ever as large as the number of people who
// switched on discoverable_by_employers, and an employer deciding whether this
// service is worth using needs to know whether that is three people or three
// thousand — and if it is three, whether three is the market or just the pool.
//
// ── What this package deliberately does NOT do ───────────────────────────────
//
// It does not return candidates. It returns LEADS: de-identified profile shapes
// and counts. No name, no email, no phone, no profile URL, ever, from any
// provider, even when the provider returns them.
//
// That is not caution, it is the only coherent position. This service refuses to
// hand a recruiter the contact details of somebody who OPTED IN, until that
// person accepts a specific job. Handing over the details of a stranger who was
// never asked would be a stricter rule for our own users than for everybody
// else. And under PIPL, holding personal information obtained indirectly
// requires the source to have taken separate consent for that sharing plus a
// processor-to-processor contract; "publicly available" is not that consent, and
// neither vendor here supplies one. A Lead therefore carries its ConsentBasis
// written out, and for both shipped providers that basis is "none on file".
//
// The recruiter who wants to contact these people licenses the vendor directly
// and does it under their own lawful basis. That is what they would have to do
// regardless — routing it through us would only launder the provenance.
//
// ── What it does do ──────────────────────────────────────────────────────────
//
// It answers "how many people with this shape exist, and where would you have to
// go to reach them" — which is the question an employer looking at a pool of
// three actually needs answered, and the one the first-party pool cannot answer
// about itself.
//
// Two providers ship, both off unless keyed:
//
//	PDL     — People Data Labs Person Search. Has a skills field, which is what
//	          this product matches on. Fill rate for it is ~8.6%.
//	Apollo  — Apollo.io People Search. ‼️ NOT usable on the free plan: the
//	          endpoint costs no CREDITS but is gated by TIER, and a free-plan key
//	          gets 403 "not included in your Free plan". Any paid plan includes it.
//
// Adding a licensed domestic source later is one implementation of Provider.
package talentsource

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Lead is one de-identified profile shape found outside the pool.
//
// Every field here is either a job fact or a statement about provenance. There
// is no field for a name, a contact or a URL, and that absence is the design:
// a field that does not exist cannot be filled in by a future edit that did not
// think about consent.
type Lead struct {
	// ID is stable within a turn and citable, e.g. "lead-001".
	ID string `json:"id"`
	// Source names the vendor, so an answer can attribute it.
	Source string `json:"source"`

	Title  string   `json:"title,omitempty"`
	Region string   `json:"region,omitempty"`
	Skills []string `json:"skills,omitempty"`
	Years  float64  `json:"years,omitempty"`

	// Reachable says whether the VENDOR holds a contact route for this person.
	// It is a boolean and never a value: it answers "would a licence get you
	// there" without disclosing anything about the person.
	Reachable bool `json:"reachable_via_vendor"`

	// ConsentBasis is what makes this record lawful for the VENDOR to hold, in
	// plain words, including when the honest answer is that there is none on
	// file. It is required rather than optional because a lead without it cannot
	// be told apart from one that was scraped.
	ConsentBasis string `json:"consent_basis"`
	// Caveat travels to the model and on to the reader.
	Caveat string `json:"caveat"`
}

// Query is one search for a profile shape.
type Query struct {
	Skills  []string
	Title   string
	Country string
	Region  string
	Limit   int
}

func (q Query) limit() int {
	if q.Limit <= 0 || q.Limit > 25 {
		return 10
	}
	return q.Limit
}

// VendorResult is one vendor's own answer, kept separate in a combined lookup.
//
// It exists because merging two indexes into a single number hides the two
// things a reader most needs: WHICH vendor found what, and whether one of them
// answered at all. A vendor that is down or unentitled contributes zero, and a
// zero that is silent is indistinguishable from "nobody like that exists".
type VendorResult struct {
	Name      string `json:"vendor"`
	Total     int    `json:"total"`
	Leads     int    `json:"sampled"`
	Truncated bool   `json:"total_is_a_floor,omitempty"`
	// Error is the vendor's own reason for not answering, in full. It is carried
	// rather than logged because the reader is deciding whether the market is
	// small or whether they simply are not paying for this index.
	Error string `json:"unavailable,omitempty"`
	// Caveat is what this vendor's numbers must be read with.
	Caveat string `json:"caveat,omitempty"`
}

// Found is what a lookup returns.
type Found struct {
	Leads []Lead
	// Total is one vendor's count. For a Chain it is the UPPER bound - see
	// AtLeast, and read the comment in Chain.Find before using it alone.
	Total int
	// AtLeast is the lower bound of the combined count. For a single vendor it
	// equals Total.
	AtLeast int
	// Truncated is set when a vendor caps what it will count, so an answer can
	// say "at least N" rather than "N".
	Truncated bool
	// PerVendor is the breakdown, present on a Chain lookup.
	PerVendor []VendorResult
}

// Provider looks for people outside the pool.
type Provider interface {
	// Name is the vendor, as it should appear to a reader.
	Name() string
	Find(ctx context.Context, q Query) (Found, error)
}

// Chain queries every provider and merges what comes back.
//
// A provider that fails does not fail the lookup: the others still answer, and
// the error is reported alongside so the caller can say which vendor was silent
// rather than implying the market is smaller than it is. A partial answer
// presented as a complete one is the failure mode this shape exists to avoid.
type Chain []Provider

func (c Chain) Name() string { return "chain" }

func (c Chain) Find(ctx context.Context, q Query) (Found, error) {
	var (
		mu   sync.Mutex
		out  Found
		errs []string
		wg   sync.WaitGroup
	)
	for _, p := range c {
		if p == nil {
			continue
		}
		wg.Add(1)
		go func(p Provider) {
			defer wg.Done()
			f, err := p.Find(ctx, q)
			mu.Lock()
			defer mu.Unlock()
			row := VendorResult{Name: p.Name()}
			if err != nil {
				row.Error = err.Error()
				errs = append(errs, fmt.Sprintf("%s: %v", p.Name(), err))
				out.PerVendor = append(out.PerVendor, row)
				return
			}
			row.Total, row.Leads, row.Truncated = f.Total, len(f.Leads), f.Truncated
			if len(f.Leads) > 0 {
				row.Caveat = f.Leads[0].Caveat
			}
			out.PerVendor = append(out.PerVendor, row)
			out.Leads = append(out.Leads, f.Leads...)
			out.Truncated = out.Truncated || f.Truncated
		}(p)
	}
	wg.Wait()

	// ── The combined count is a RANGE, not a sum ────────────────────────────
	//
	// Adding the vendors' totals was the first version and it is wrong: the two
	// indexes overlap by an unknown amount, so a person in both is counted twice.
	// With PDL reporting 4,261 and Apollo reporting 900, the truthful statement
	// is "somewhere between 4,261 and 5,161", not "5,161".
	//
	// The bounds hold whatever the overlap is:
	//   lower = the largest single index — the union cannot be smaller than that
	//   upper = the sum — reached only if the indexes share nobody
	//
	// Total carries the upper bound so a single-vendor lookup still reads
	// naturally, and AtLeast carries the lower. The tool reports both, and the
	// intent directive requires the answer to state it as a range.
	for _, v := range out.PerVendor {
		out.Total += v.Total
		if v.Total > out.AtLeast {
			out.AtLeast = v.Total
		}
	}

	// Sorted by vendor then by the lead's own content, NOT by any relevance the
	// vendor supplied. Provider ordering is a ranking of people, and this product
	// does not rank people - see guardrail.verifyNoCandidateScoring. Discarding
	// it costs some of what a vendor is paid for, and that is the trade.
	sort.SliceStable(out.Leads, func(i, j int) bool {
		if out.Leads[i].Source != out.Leads[j].Source {
			return out.Leads[i].Source < out.Leads[j].Source
		}
		if out.Leads[i].Title != out.Leads[j].Title {
			return out.Leads[i].Title < out.Leads[j].Title
		}
		return out.Leads[i].Region < out.Leads[j].Region
	})
	sort.SliceStable(out.PerVendor, func(i, j int) bool { return out.PerVendor[i].Name < out.PerVendor[j].Name })
	for i := range out.Leads {
		out.Leads[i].ID = fmt.Sprintf("lead-%03d", i+1)
	}
	if len(errs) > 0 {
		return out, fmt.Errorf("PROVIDER_PARTIAL: %s", strings.Join(errs, "; "))
	}
	return out, nil
}

// dedupeSkills keeps a provider's skill list short, lowercase-unique and in the
// order given, so two leads with the same skills read the same.
func dedupeSkills(in []string, max int) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		t := strings.TrimSpace(s)
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		seen[strings.ToLower(t)] = true
		out = append(out, t)
		if len(out) >= max {
			break
		}
	}
	return out
}
