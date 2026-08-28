// Package livesource is the seam for looking things up outside the static
// corpus.
//
// Why it exists: the corpus can only ever hold named employers and courses for
// cities somebody has loaded data for. A person in a city with no such data was
// getting the national framework and nothing local — correct, but half an
// answer. A Provider fills the other half, and does so without weakening the
// rule the whole product rests on: everything named in an answer must have come
// back from a tool this turn, with a source you can open.
//
// Two providers ship:
//
//	Directory  — official public-employment-service destinations, one per region,
//	             each URL verified. Always available, no network, no key.
//	WebSearch  — live results from a search API. This is the one that actually
//	             returns employers and courses nationwide; it needs a key.
//
// Adding a real feed later — a provincial open-data endpoint, a partner job
// board, an official API once granted — is one implementation of Provider, not
// a change to the agent.
package livesource

import (
	"context"
	"fmt"
	"strings"
)

// Kind separates what a result IS, because the answer has to say which.
type Kind string

const (
	// KindDirectory is an official destination: where to look, not a listing.
	KindDirectory Kind = "directory"
	// KindListing is a specific opening or course found live.
	KindListing Kind = "listing"
)

// Result is one thing a provider found. Every field is either something the
// provider was told or something it fetched — never something composed here.
type Result struct {
	// ID is stable and citable, e.g. "live-001". The answer quotes it, and the
	// invented-identifier check accepts it because it came back this turn.
	ID string `json:"id"`

	Kind    Kind   `json:"kind"`
	Title   string `json:"title"`
	Region  string `json:"region"`
	Summary string `json:"summary,omitempty"`

	// URL is the real page. A result without one is not returned: an
	// unverifiable "listing" is exactly what this product must not produce.
	URL string `json:"url"`

	Phone string `json:"phone,omitempty"`
	// Source names who published it, so the answer can attribute it.
	Source string `json:"source"`
	// Verified, when set, is when the destination was last confirmed to answer.
	Verified string `json:"verified_at,omitempty"`
	// Published, when set, is when the SITE published the listing. It is kept
	// separate from Verified rather than folded into it because the two say
	// different things — "we confirmed this destination answers" is not "a job
	// board posted this" — and a reader deciding whether an opening is still
	// live needs the second one. A listing with no date is worse, not better,
	// so the field being empty is itself informative.
	Published string `json:"published_at,omitempty"`
	// Caveat travels with the result to the model and on to the reader.
	Caveat string `json:"caveat,omitempty"`
}

// Query is a lookup for one city.
type Query struct {
	City    string
	Keyword string
	Limit   int
}

// Provider looks things up outside the corpus.
//
// A provider that cannot answer returns no results and no error. An error means
// the lookup FAILED — the distinction matters, because "nothing found" and "the
// source was unreachable" must read differently to the person.
type Provider interface {
	Name() string
	Lookup(ctx context.Context, q Query) ([]Result, error)
}

// Chain runs providers in order and concatenates what they return.
//
// A failing provider does not stop the others: the directory works offline and
// must still answer when a search API is down. Failures are returned alongside
// the results so the caller can say the lookup was partial rather than
// silently presenting it as complete.
type Chain []Provider

func (c Chain) Name() string {
	names := make([]string, 0, len(c))
	for _, p := range c {
		names = append(names, p.Name())
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, "+")
}

// LookupAll returns the merged results and, separately, the providers that
// failed. Never returns an error itself.
func (c Chain) LookupAll(ctx context.Context, q Query) ([]Result, []error) {
	var out []Result
	var errs []error
	for _, p := range c {
		res, err := p.Lookup(ctx, q)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p.Name(), err))
			continue
		}
		out = append(out, res...)
	}
	// Ids are assigned here, after the merge, so they are stable within a turn
	// regardless of which providers answered.
	for i := range out {
		out[i].ID = fmt.Sprintf("live-%03d", i+1)
	}
	return out, errs
}

func (c Chain) Lookup(ctx context.Context, q Query) ([]Result, error) {
	res, _ := c.LookupAll(ctx, q)
	return res, nil
}
