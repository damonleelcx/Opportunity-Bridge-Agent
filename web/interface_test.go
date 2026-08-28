package web_test

// Fences over the embedded interface.
//
// There is no JavaScript test runner in this repository, and adding one to
// assert two properties would be a build system nobody asked for. These read
// the shipped assets out of the embed and assert on their source. That is a
// weaker instrument than executing the code — it can only catch a rule being
// removed, not a rule being wrong — so it is used only where the failure is
// silent and the cost is somebody's wasted journey.

import (
	"regexp"
	"strings"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/web"
)

func asset(t *testing.T, name string) string {
	t.Helper()
	b, err := web.Files.ReadFile("static/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// The opportunity panel must consult the live results before it says nothing
// was found.
//
// It used to test only `results`, the corpus records, which are empty for every
// city the corpus does not cover — which is every city but one. So the panel
// said "nothing matched this time" directly underneath an answer listing five
// real openings the live lookup had just returned. The panel is the part people
// scan, and it was contradicting the agent.
// See docs/bugfix/2026-08-28-live-results-shown-as-nothing.md
func TestOpportunityPanelConsultsLiveResultsBeforeSayingNothingMatched(t *testing.T) {
	src := asset(t, "app.js")

	start := strings.Index(src, "function opportunityList(")
	if start < 0 {
		t.Fatal("opportunityList is gone; this fence no longer guards anything")
	}
	end := strings.Index(src[start:], "\nfunction ")
	if end < 0 {
		t.Fatal("could not find the end of opportunityList")
	}
	body := src[start : start+end]

	if !strings.Contains(body, "live_results") {
		t.Error("opportunityList does not read live_results: for any city outside the corpus " +
			"the panel will claim nothing was found while the answer lists real openings")
	}
	// The empty state must depend on BOTH collections. Guarding only on
	// `!r.results?.length` is the exact defect this replaced.
	guard := regexp.MustCompile(`if \(!r\.results\?\.length && !live\.length\)`)
	if !guard.MatchString(body) {
		t.Error(`the "nothing matched" branch is not guarded on both r.results and the live results; ` +
			`it must only be reached when there is genuinely nothing to show`)
	}
}

// Every string the interface can render must exist in every language.
//
// A missing key does not fail loudly — it renders as the key itself or as
// nothing at all, in the one language nobody testing in Chinese would look at.
// This is the check that a string added to one table was added to both.
func TestEveryInterfaceStringExistsInEveryLanguage(t *testing.T) {
	src := asset(t, "i18n.js")

	// STRINGS is the table; it ends at the first line that is exactly "};".
	tableStart := strings.Index(src, "const STRINGS = {")
	if tableStart < 0 {
		t.Fatal("the STRINGS table is gone; this fence no longer guards anything")
	}
	table := src[tableStart:]
	if end := strings.Index(table, "\n};"); end > 0 {
		table = table[:end]
	}

	zhStart := strings.Index(table, `"zh-CN": {`)
	enStart := strings.Index(table, "\n  en: {")
	if zhStart < 0 || enStart < 0 || enStart < zhStart {
		t.Fatalf("could not locate both language blocks (zh at %d, en at %d)", zhStart, enStart)
	}
	key := regexp.MustCompile(`"([a-zA-Z][\w.]*)":`)
	keysIn := func(s string) map[string]bool {
		out := map[string]bool{}
		for _, m := range key.FindAllStringSubmatch(s, -1) {
			out[m[1]] = true
		}
		return out
	}
	zh := keysIn(table[zhStart:enStart])
	en := keysIn(table[enStart:])
	if len(zh) < 50 || len(en) < 50 {
		t.Fatalf("parsed too few keys (zh=%d en=%d); the fence is not reading the table",
			len(zh), len(en))
	}

	for k := range zh {
		if k == "zh-CN" {
			continue
		}
		if !en[k] {
			t.Errorf("%q exists in zh-CN but not in en: it renders as nothing for an English reader", k)
		}
	}
	for k := range en {
		if !zh[k] {
			t.Errorf("%q exists in en but not in zh-CN: it renders as nothing for the default audience", k)
		}
	}
}
