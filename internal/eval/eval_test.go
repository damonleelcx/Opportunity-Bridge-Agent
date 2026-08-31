package eval_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/config"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/corpus"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/eval"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/livesource"
)

// TestShippedDatasets runs the evaluation suite as an ordinary Go test, so the
// suite is part of `go test ./...` rather than something somebody remembers to
// run. It is the end-to-end gate (step 19): every case goes through the real
// router, tool registry, guardrails, verifiers and budgets.
func TestShippedDatasets(t *testing.T) {
	cfg := config.Config{
		AgentModel: "test", Effort: "high", MaxTokens: 4096,
		MaxIterations: 8, MaxToolCalls: 12, MaxWallClock: 30 * time.Second,
		MaxOutputTokens: 100000, KAnonymityFloor: 5,
		// The fixture corpus, not the shipped one: the evaluation cases cite
		// local programmes, and those left the product when the invented
		// organisations did. See testdata/README.md.
		CorpusDir: "../../testdata/corpus",
	}
	c, err := corpus.Load(cfg.CorpusDir)
	if err != nil {
		t.Fatalf("corpus: %v", err)
	}

	paths, err := filepath.Glob("../../evals/*.jsonl")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no datasets found: %v", err)
	}
	var cases []eval.Case
	for _, p := range paths {
		cs, err := eval.LoadCases(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		cases = append(cases, cs...)
	}
	if len(cases) < 20 {
		t.Fatalf("only %d cases; the suite has been gutted", len(cases))
	}

	// Every category must be represented. A suite of happy paths measures
	// nothing that matters here.
	seen := map[eval.Category]int{}
	for _, c := range cases {
		seen[c.Category]++
	}
	for _, cat := range []eval.Category{eval.CatSuccess, eval.CatEdge, eval.CatAdversarial} {
		if seen[cat] == 0 {
			t.Errorf("no %s cases", cat)
		}
	}

	dir, err := livesource.LoadDirectory(cfg.CorpusDir)
	if err != nil {
		t.Fatalf("directory: %v", err)
	}
	report := (&eval.Runner{Corpus: c, Cfg: cfg, Live: livesource.Chain{dir}}).
		Run(context.Background(), cases)
	if report.Passed != report.Total {
		t.Errorf("%d of %d cases failed:\n%s", report.Total-report.Passed, report.Total, report.Text())
	}
	if report.ToolAccuracy < 1 {
		t.Errorf("tool-call accuracy %.0f%%, want 100%% on a scripted suite", report.ToolAccuracy*100)
	}
}
