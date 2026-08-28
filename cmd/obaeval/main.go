// Command obaeval runs the evaluation suite against the real agent path.
//
//	obaeval                       run every dataset in ./evals
//	obaeval evals/routing.jsonl   run one dataset
//	obaeval -json report.json     also write the full machine-readable report
//	obaeval -live                 allow "route" cases to call the classifier model
//
// Exit status is non-zero when any case fails, so this is usable as a gate.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/config"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/corpus"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/eval"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
)

func main() {
	jsonOut := flag.String("json", "", "write the full report as JSON to this path")
	live := flag.Bool("live", false, "allow route cases to call the real classifier model (costs tokens)")
	timeout := flag.Duration("timeout", 5*time.Minute, "overall timeout")
	flag.Parse()

	if err := run(flag.Args(), *jsonOut, *live, *timeout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}

func run(args []string, jsonOut string, live bool, timeout time.Duration) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	c, err := corpus.Load(cfg.CorpusDir)
	if err != nil {
		return err
	}

	paths := args
	if len(paths) == 0 {
		paths, err = filepath.Glob("evals/*.jsonl")
		if err != nil || len(paths) == 0 {
			return fmt.Errorf("EVAL_NO_DATASETS: no .jsonl files found in ./evals; pass a path explicitly")
		}
	}

	var cases []eval.Case
	for _, p := range paths {
		cs, err := eval.LoadCases(p)
		if err != nil {
			return err
		}
		cases = append(cases, cs...)
	}

	runner := &eval.Runner{Corpus: c, Cfg: cfg}
	if live {
		// Routing is measured against whichever provider this deployment
		// actually uses, so the number means something for this deployment.
		switch cfg.Backend {
		case config.BackendDeepSeek:
			runner.LiveClient = llm.NewDeepSeek(cfg.APIKey, cfg.DeepSeekBaseURL)
		case config.BackendAnthropic:
			runner.LiveClient = llm.NewAnthropic(cfg.APIKey)
		default:
			return fmt.Errorf("EVAL_LIVE_UNSUPPORTED: -live needs a real backend, but OBA_BACKEND=%s", cfg.Backend)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	report := runner.Run(ctx, cases)

	fmt.Print(report.Text())
	if jsonOut != "" {
		b, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(jsonOut, b, 0o644); err != nil {
			return fmt.Errorf("EVAL_REPORT_WRITE_FAILED: %w", err)
		}
		fmt.Printf("\nfull report written to %s\n", jsonOut)
	}
	if report.Passed < report.Total {
		return fmt.Errorf("%d of %d cases failed", report.Total-report.Passed, report.Total)
	}
	return nil
}
