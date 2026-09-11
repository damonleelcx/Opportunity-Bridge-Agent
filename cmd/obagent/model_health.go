package main

import (
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/config"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
)

// modelCheckStaleAfter is how old the newest model result may be before
// /api/health sends one check, and the least time between two checks: at about
// 15 tokens a check, at most ~2k tokens a day.
const modelCheckStaleAfter = 10 * time.Minute

// modelHealth wraps the model client so every call is recorded for
// /api/health, and on a real backend lets health send one small check when the
// record is stale.
//
// Not on the scripted backend: a check would consume a turn of the script that
// a demo or a test is replaying, and scripted health says nothing about a real
// model anyway.
// See docs/bugfix/2026-09-11-quota-429-retried-and-health-always-ok.md
func modelHealth(cfg config.Config, client llm.Client) *llm.Health {
	opts := llm.HealthOptions{StaleAfter: modelCheckStaleAfter}
	if cfg.Backend == config.BackendQwen {
		opts.Check = llm.ModelCheck(cfg.AgentModel)
	}
	return llm.NewHealth(client, opts)
}
