package leadgraph

import (
	"context"
	"sort"
	"strings"
	"time"
)

// Public sources: the half of the product that tells the user something they
// did not already know.
//
// WHY A NARROW INTERFACE AND A REGISTRATION-TIME REFUSAL
//
//	The forbidden-source list (see blockedSourceHosts) is worth nothing if the
//	request goes out and only the STORAGE refuses it: by then the scraping has
//	happened, the terms of service are breached and the log on the other side
//	exists. So a source declares the hosts it will touch, and a source whose
//	hosts are forbidden is refused before it is ever called. The per-document
//	check that follows is defence in depth, not the primary guard.
//
// WHAT THE FIRST BATCH DELIBERATELY IS
//
//	Company-registry changes and public job postings (PRD Q2). Both are highly
//	structured, both are unambiguously public, and the second carries the
//	densest signal in the product: a group that STOPS advertising is telling you
//	something, and it costs nothing to notice.
//
//	News and prose announcements are a harder extraction problem and are not in
//	this batch. Pretending otherwise would mean an extractor that mostly guesses.

// DocKind is the shape of a fetched document. Closed, because each kind has its
// own extraction rule and a kind with no rule would silently produce nothing.
type DocKind string

const (
	// DocJobPosting is a public job advert.
	DocJobPosting DocKind = "job_posting"
	// DocRegistryChange is a company-registry filing. Official by definition.
	DocRegistryChange DocKind = "registry_change"
	// DocAnnouncement is a company's own published notice. Official.
	DocAnnouncement DocKind = "announcement"
)

func (k DocKind) valid() bool {
	switch k {
	case DocJobPosting, DocRegistryChange, DocAnnouncement:
		return true
	}
	return false
}

// Document is one fetched item, kept with the words it actually contained.
// Excerpts, never summaries: a summary cannot be checked against the source and
// is exactly what a user needs when deciding whether to believe a signal.
type Document struct {
	URL       string     `json:"url"`
	Title     string     `json:"title"`
	Text      string     `json:"text"`
	Org       string     `json:"org"`
	Unit      string     `json:"unit,omitempty"`
	Kind      DocKind    `json:"kind"`
	PostedAt  *time.Time `json:"posted_at,omitempty"`
	FetchedAt time.Time  `json:"fetched_at"`
}

// FetchRequest is what the caller wants looked at.
type FetchRequest struct {
	Org   string
	Since time.Time
}

// Source is one place documents come from.
type Source interface {
	Name() string
	// Hosts are every host this source may contact. Declaring them is what
	// allows the refusal to happen before the request.
	Hosts() []string
	Fetch(ctx context.Context, req FetchRequest) ([]Document, error)
}

// RefusedFetch records a source or document that was turned away, and why. It
// goes in the ledger: a refusal that leaves no trace is indistinguishable from
// a fetch that never happened, and the difference is the whole audit.
type RefusedFetch struct {
	Source string    `json:"source"`
	Host   string    `json:"host,omitempty"`
	URL    string    `json:"url,omitempty"`
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

// FetchFrom runs a source through both guards and returns what survived.
//
// It never returns a partial fetch as an error: a forbidden host among several
// is a refusal to record, not a reason to lose the documents that were fine.
func FetchFrom(ctx context.Context, src Source, req FetchRequest, now time.Time) ([]Document, []RefusedFetch, error) {
	var refused []RefusedFetch
	for _, h := range sortedHosts(src.Hosts()) {
		if forbiddenSource(h) {
			refused = append(refused, RefusedFetch{
				Source: src.Name(), Host: h, Reason: ErrSourceForbidden.Error(), At: now,
			})
		}
	}
	if len(refused) > 0 {
		// Not one request is made. See the file comment: storage-only refusal
		// is too late to matter.
		return nil, refused, ErrSourceForbidden
	}

	docs, err := src.Fetch(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	out := make([]Document, 0, len(docs))
	for _, d := range docs {
		if forbiddenSource(d.URL) {
			refused = append(refused, RefusedFetch{
				Source: src.Name(), URL: d.URL, Reason: ErrSourceForbidden.Error(), At: now,
			})
			continue
		}
		if !d.Kind.valid() || strings.TrimSpace(d.URL) == "" {
			refused = append(refused, RefusedFetch{
				Source: src.Name(), URL: d.URL, Reason: ErrUnknownKind.Error(), At: now,
			})
			continue
		}
		if d.FetchedAt.IsZero() {
			d.FetchedAt = now
		}
		out = append(out, d)
	}
	return out, refused, nil
}

func sortedHosts(hs []string) []string {
	out := append([]string{}, hs...)
	sort.Strings(out)
	return out
}
