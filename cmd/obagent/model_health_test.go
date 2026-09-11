package main

import (
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/config"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
)

// Health may send its check only against a real model. On the scripted backend
// a check would eat a turn of the script a demo or a test is replaying.
func TestModelHealthChecksOnlyARealBackend(t *testing.T) {
	scripted := modelHealth(config.Config{Backend: config.BackendScripted}, llm.NewScripted(llm.Script{}))
	if scripted.Snapshot().CheckEnabled {
		t.Error("the scripted backend has a health check; it would consume the script's turns")
	}
	qwen := modelHealth(config.Config{Backend: config.BackendQwen, AgentModel: "qwen3.8-max"},
		llm.NewQwen("qw-test-key", "http://127.0.0.1:1"))
	if !qwen.Snapshot().CheckEnabled {
		t.Error("the qwen backend has no health check; an outage with no traffic would read as unknown forever")
	}
}
