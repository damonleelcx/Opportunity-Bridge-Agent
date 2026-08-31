package talentsource

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/guardrail"
)

// Live smoke against the real vendors. Skipped unless the keys are present, so
// `go test ./...` stays offline and CI is unaffected. Run it with `make talent-smoke`.
//
// Why it exists when the fixture tests already pass: a fixture proves this
// package drops the fields it knows about. It cannot prove the VENDOR does not
// put a person's name somewhere this package does decode - a full name inside
// job_title, an email inside location_name. Only live data shows that, and the
// PII assertion below is the point of the whole file.
//
// It prints counts and de-identified leads only. Raw response bodies are never
// logged, because they contain real people's records.

// liveQuery defaults to what this product actually cares about - a CNC operator
// in China - and is overridable, because probing a vendor's coverage is the
// whole point of running this. A run that returns zero is NOT a pass worth
// having: it exercises auth and the envelope, and none of the record decoding.
// Override to find data, then read the coverage answer off the totals.
func liveQuery() Query {
	q := Query{
		Skills:  splitEnv("OBA_SMOKE_SKILLS", "cnc"),
		Title:   envOr("OBA_SMOKE_TITLE", "cnc operator"),
		Country: envOr("OBA_SMOKE_COUNTRY", "china"),
		Region:  os.Getenv("OBA_SMOKE_REGION"),
		Limit:   5,
	}
	return q
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		if v == "-" {
			return ""
		}
		return v
	}
	return def
}

func splitEnv(k, def string) []string {
	v := envOr(k, def)
	if v == "" {
		return nil
	}
	return strings.Split(v, ",")
}

func assertNoPII(t *testing.T, vendor string, leads []Lead) {
	t.Helper()
	for _, lead := range leads {
		raw, err := json.Marshal(lead)
		if err != nil {
			t.Fatalf("%s: marshal: %v", vendor, err)
		}
		body := string(raw)
		// The same detector the answer-level guardrail uses, so "what may not
		// reach a reader" is one definition rather than two.
		if f := guardrail.HasPII(body); len(f) > 0 {
			codes := make([]string, 0, len(f))
			for _, x := range f {
				codes = append(codes, x.Code)
			}
			t.Errorf("%s returned a lead containing PII (%s). The vendor put an identifier in a field this "+
				"adapter decodes; find which and stop decoding it.\n  lead: %s",
				vendor, strings.Join(codes, ","), body)
		}
		if lead.ConsentBasis == "" || lead.Caveat == "" {
			t.Errorf("%s: a lead reached the caller with no consent basis or caveat: %s", vendor, body)
		}
	}
}

func TestLivePDL(t *testing.T) {
	key := os.Getenv("OBA_PDL_API_KEY")
	if key == "" {
		t.Skip("OBA_PDL_API_KEY not set")
	}
	endpoint := os.Getenv("OBA_PDL_API_URL")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	found, err := Chain{NewPDL(endpoint, key)}.Find(ctx, liveQuery())
	if err != nil {
		t.Fatalf("PDL live call failed: %v", err)
	}
	t.Logf("PDL endpoint=%s total=%d leads=%d", endpointName(endpoint, DefaultPDLEndpoint), found.Total, len(found.Leads))
	for _, l := range found.Leads {
		t.Logf("  %s | title=%q region=%q skills=%v years=%v reachable=%v",
			l.ID, l.Title, l.Region, l.Skills, l.Years, l.Reachable)
	}
	// A run that returns nothing exercises auth and the envelope and NONE of the
	// record decoding, so it is a pass that proves almost nothing. Say so, rather
	// than letting a green line imply the adapter was validated.
	if found.Total == 0 {
		t.Logf("PDL returned no records: this run did NOT exercise record decoding. " +
			"That is a coverage answer for this query, not a validation of the adapter. " +
			"Set OBA_SMOKE_TITLE / OBA_SMOKE_SKILLS / OBA_SMOKE_COUNTRY (use \"-\" to clear one) to find data.")
	}
	assertNoPII(t, "PDL", found.Leads)
}

func TestLiveApollo(t *testing.T) {
	key := os.Getenv("OBA_APOLLO_API_KEY")
	if key == "" {
		t.Skip("OBA_APOLLO_API_KEY not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	found, err := Chain{NewApollo(os.Getenv("OBA_APOLLO_API_URL"), key)}.Find(ctx, liveQuery())
	if err != nil {
		t.Fatalf("Apollo live call failed: %v", err)
	}
	t.Logf("Apollo total=%d (floor=%v) leads=%d", found.Total, found.Truncated, len(found.Leads))
	for _, l := range found.Leads {
		t.Logf("  %s | title=%q region=%q reachable=%v", l.ID, l.Title, l.Region, l.Reachable)
	}
	if found.Total == 0 {
		t.Logf("Apollo returned no records: this run did NOT exercise record decoding.")
	}
	assertNoPII(t, "Apollo", found.Leads)
}

func endpointName(got, def string) string {
	if strings.TrimSpace(got) == "" {
		return def + " (default)"
	}
	return got
}

// The combined lookup, exactly as external_talent_scan calls it: every
// configured vendor at once, with the breakdown the reader is shown.
func TestLiveCombined(t *testing.T) {
	var chain Chain
	if k := os.Getenv("OBA_PDL_API_KEY"); k != "" {
		chain = append(chain, NewPDL(os.Getenv("OBA_PDL_API_URL"), k))
	}
	if k := os.Getenv("OBA_APOLLO_API_KEY"); k != "" {
		chain = append(chain, NewApollo(os.Getenv("OBA_APOLLO_API_URL"), k))
	}
	if len(chain) == 0 {
		t.Skip("no vendor keys set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	found, err := chain.Find(ctx, liveQuery())
	// A partial answer is the normal case when one vendor is unentitled, and it
	// is still the answer. Only a total silence is a failure.
	if err != nil && len(found.PerVendor) == 0 {
		t.Fatalf("combined lookup produced nothing at all: %v", err)
	}
	t.Logf("COMBINED: between %d and %d (%d vendors, %d sampled)",
		found.AtLeast, found.Total, len(found.PerVendor), len(found.Leads))
	for _, v := range found.PerVendor {
		if v.Error != "" {
			t.Logf("  %-18s UNAVAILABLE: %s", v.Name, truncate(v.Error, 120))
			continue
		}
		t.Logf("  %-18s total=%-8d sampled=%d floor=%v", v.Name, v.Total, v.Leads, v.Truncated)
	}
	for _, l := range found.Leads {
		t.Logf("  %s [%s] title=%q region=%q skills=%v reachable=%v",
			l.ID, l.Source, l.Title, l.Region, l.Skills, l.Reachable)
	}
	assertNoPII(t, "combined", found.Leads)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
