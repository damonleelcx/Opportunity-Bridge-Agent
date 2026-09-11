package leadgraph_test

// The fence that makes persistence safe to add at eighteen call sites.
//
// A forgotten write is the worst failure this package can have: everything
// looks right on screen for the life of the process and is simply absent after
// the next restart, and nobody finds out until somebody's contact is missing.
// Nothing catches it at review time - the code that should be there is not
// there, which is exactly what a reviewer's eye slides over.
//
// So this reads the shipped source and asserts a structural property instead:
// every method that takes the WRITE lock either persists what it changed, or is
// on a list that says in words why it does not.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// readOnlyUnderWriteLock are the methods that take the write lock without
// changing anything that outlives the process. Each one needs a reason here,
// which is the point: adding a name is a decision somebody has to write down.
var readOnlyUnderWriteLock = map[string]string{
	"SetClock":        "test seam; changes a function pointer, not data",
	"stage":           "a staged plan is in-memory by design - see importer.go; StageImport and StageScreenshot both stage through it",
	"RestageImport":   "same",
	"DiscardImport":   "same",
	"CommitImport":    "persists through ApplyImport; only drops the staged plan itself",
	"nextQuestion":    "marks a queued item as asked; persisted by its caller",
	"annotation":      "allocates the overlay; the caller persists it",
	"setNote":         "sets the overlay; the caller persists it",
	"dropAnnotations": "helper called from methods that persist the deletion",
	"repointEdges":    "helper inside MergeNodes, which persists",
	"moveAnnotations": "helper inside MergeNodes, which persists",
	"load":            "reads the database INTO memory; there is nothing to write back",
}

func TestEveryWriteIsPersisted(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	fset := token.NewFileSet()
	var missing []string
	var checked int

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(".", e.Name()), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Body == nil {
				continue
			}
			if !isStoreReceiver(fn.Recv) {
				continue
			}
			body := src(fset, fn)
			if !strings.Contains(body, "s.mu.Lock()") {
				continue
			}
			checked++
			if _, excused := readOnlyUnderWriteLock[fn.Name.Name]; excused {
				continue
			}
			// Exact markers. An earlier version looked for "s.drop", which
			// matched s.dropAnnotations and quietly excused Forget and
			// ForgetTeam - a fence with a hole in exactly the shape of the bug
			// it was watching for.
			if strings.Contains(body, "s.put") || strings.Contains(body, "s.deleteRecord(") {
				continue
			}
			missing = append(missing, e.Name()+":"+fn.Name.Name)
		}
	}

	if checked < 10 {
		t.Fatalf("only %d write methods found - this fence is not looking at the package", checked)
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("these change data and never persist it; after a restart the change is simply gone:\n  %s",
			strings.Join(missing, "\n  "))
	}
}

func isStoreReceiver(r *ast.FieldList) bool {
	if len(r.List) == 0 {
		return false
	}
	star, ok := r.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	id, ok := star.X.(*ast.Ident)
	return ok && id.Name == "Store"
}

func src(fset *token.FileSet, n ast.Node) string {
	start := fset.Position(n.Pos())
	end := fset.Position(n.End())
	b, err := os.ReadFile(start.Filename)
	if err != nil {
		return ""
	}
	if end.Offset > len(b) {
		return string(b[start.Offset:])
	}
	return string(b[start.Offset:end.Offset])
}
