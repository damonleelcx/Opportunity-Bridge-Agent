package domain

import (
	"os"
	"regexp"
	"testing"
)

// Every scope declared in this file must be either offered or explicitly
// withheld — never neither.
//
// A scope in neither list is the failure this arrangement exists to prevent, and
// it is silent in three places at once: the API rejects granting it, the model
// cannot ask for it, and the interface renders no control to withdraw it. Adding
// a scope should force a decision about which list it joins.
// See docs/bugfix/2026-08-31-read-aloud-needs-consent.md
func TestEveryScopeIsOfferedOrExplained(t *testing.T) {
	src, err := os.ReadFile("domain.go")
	if err != nil {
		t.Fatalf("read domain.go: %v", err)
	}
	declared := regexp.MustCompile(`Consent\w+\s+ConsentScope\s*=\s*"([a-z_]+)"`).FindAllStringSubmatch(string(src), -1)
	if len(declared) < 5 {
		t.Fatalf("found %d scope declarations; this fence reads the source and has stopped finding them",
			len(declared))
	}
	offered := map[string]bool{}
	for _, s := range ConsentScopes() {
		offered[string(s)] = true
	}
	for _, m := range declared {
		id := m[1]
		switch {
		case offered[id] && NotYetOffered(id):
			t.Errorf("%s is both offered and withheld", id)
		case !offered[id] && !NotYetOffered(id):
			t.Errorf("%s is declared but neither offered nor listed as withheld. Granting it would be "+
				"rejected by the API, the model could never ask for it, and no control would exist to "+
				"withdraw it — all without a word", id)
		}
	}
}

// A withheld scope must not be grantable. Otherwise somebody agrees to something
// that does nothing, and is put into a pool nothing searches.
func TestWithheldScopesAreNotGrantable(t *testing.T) {
	for _, s := range notYetOfferedScopes() {
		if IsConsentScope(string(s)) {
			t.Errorf("%s is withheld but the API would accept a grant for it", s)
		}
	}
}
