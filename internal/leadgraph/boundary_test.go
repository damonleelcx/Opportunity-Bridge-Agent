package leadgraph_test

// The boundary that Q4 was really about.
//
// The PRD asked whether this product should live in its own repository. The
// thing a separate repository would have protected is the IMPORT boundary -
// that Lead Graph never takes on the sibling product's intent registry, its
// consent scopes or its roles, because those encode a value system that
// contradicts this one (see docs/20-lead-graph.zh-CN.md §01).
//
// A repository split is an expensive proxy for that. This is the thing itself,
// measured directly: if it ever goes red, the boundary is actually gone, which
// is more than a split would have told us.
//
// 拍板 2026-09-10 (revised): Lead Graph is a CAPABILITY OF THE SIBLING AGENT,
// not a separate product - it attaches to the talent_sourcing intent, which is
// already recruiter-only and already carries a RecruiterOrg.
//
// That makes this fence MORE important, not less. The dependency has to point
// one way: the agent may depend on this package, and this package may never
// depend on the agent. Pointing it the other way would put the agent's consent
// scopes and roles inside the thing they are supposed to constrain, and the
// two would start defining each other.

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// forbidden are the sibling product's packages. Not "everything in internal/":
// a genuinely shared, value-neutral library could be extracted one day, and
// this list says which ones carry the other product's boundaries.
var forbidden = []string{
	"internal/intent",    // its five audiences and their limits
	"internal/domain",    // its consent scopes and roles
	"internal/guardrail", // its verifiers, including the ones forbidding scoring
	"internal/tools",     // its action surface, which drags the three above
	"internal/store",     // its records and its consent-gated reads
	"internal/prompt",
	"internal/agent",
	"internal/corpus",
	"internal/retrieval",
}

func TestLeadGraphImportsNothingFromTheSiblingProduct(t *testing.T) {
	dir := "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	fset := token.NewFileSet()
	var checked int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		checked++
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			for _, bad := range forbidden {
				if strings.HasSuffix(path, bad) {
					t.Errorf("%s imports %s: the boundary Q4 was about is gone", e.Name(), path)
				}
			}
		}
	}
	if checked < 10 {
		t.Fatalf("only %d files scanned - this fence is not looking at the package", checked)
	}
}
