package obs_test

import (
	"bytes"
	"context"
	"log/slog"
	"regexp"
	"strings"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/obs"
)

// Fences for what a run's events may put into the process log.
//
// Until 2026-09-11 nothing from a run reached the log at all; the fix routes the
// events through obs.LogSink, and the risk it introduces is the opposite one -
// the log carrying what the browser trace carries: the person's words in a route
// rationale, a verifier quoting the answer, tool arguments, a candidate_ref.
// See docs/bugfix/2026-09-11-agent-events-never-reached-the-logs.md

func captured(t *testing.T) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	return slog.New(obs.NewContextHandler(slog.NewTextHandler(&buf, nil))), &buf
}

func record(sink func(obs.Event), evs ...obs.Event) {
	for _, e := range evs {
		sink(e)
	}
}

func TestOnlyAllowlistedEventsAndFieldsReachTheLog(t *testing.T) {
	log, buf := captured(t)
	sink := obs.LogSink(context.Background(), log)
	record(sink,
		// Not on the list: must write nothing.
		obs.Event{Level: obs.Info, Name: obs.ModelRequested, RunID: "run_1",
			Fields: map[string]any{"model": "qwen"}},
		// On the list, carrying a field that is not, and a message with a person's words.
		obs.Event{Level: obs.Info, Name: obs.ToolSucceeded, RunID: "run_1", Session: "ses_1",
			Message: "我住在成都青羊区", Fields: map[string]any{
				"tool": "opportunity_search", "result_bytes": 812, "args": `{"query":"我住在成都青羊区"}`,
			}},
	)
	out := buf.String()
	if strings.Contains(out, "agent.model.requested") {
		t.Error("an event that is not on the list reached the log")
	}
	for _, want := range []string{"event.name=agent.tool.succeeded", "run_id=run_1",
		"session_id=ses_1", "tool=opportunity_search", "result_bytes=812"} {
		if !strings.Contains(out, want) {
			t.Errorf("the log line has no %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "args") || strings.Contains(out, "青羊区") {
		t.Errorf("a field or message outside the allowlist reached the log:\n%s", out)
	}
}

// A code is logged only if it is shaped like one. codeOf derives some codes from
// the text before a colon, so "city: 成都 not found" yields "city" - that is text,
// and text does not go into the log.
func TestOnlyCodeShapedCodesAreLogged(t *testing.T) {
	log, buf := captured(t)
	sink := obs.LogSink(context.Background(), log)
	record(sink,
		obs.Event{Level: obs.Warn, Name: obs.ToolFailed, RunID: "run_2", Code: "SEARCH_FAILED",
			Fields: map[string]any{"tool": "opportunity_search"}},
		obs.Event{Level: obs.Warn, Name: obs.ToolFailed, RunID: "run_3", Code: "city 成都",
			Fields: map[string]any{"tool": "opportunity_search"}},
	)
	out := buf.String()
	if !strings.Contains(out, "error.code=SEARCH_FAILED") {
		t.Errorf("a well-formed code was not logged:\n%s", out)
	}
	if strings.Contains(out, "成都") {
		t.Errorf("text shaped like a sentence was logged as a code:\n%s", out)
	}
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("a warn event was not logged at WARN:\n%s", out)
	}
}

// 拍板 2026-09-11: the outreach line names the outreach, never the person.
func TestTheOutreachLineNeverNamesTheCandidate(t *testing.T) {
	log, buf := captured(t)
	record(obs.LogSink(context.Background(), log),
		obs.Event{Level: obs.Info, Name: obs.OutreachRequested, RunID: "run_4",
			Fields: map[string]any{"outreach_id": "out_9", "candidate_ref": "cand_7f3a"}})
	out := buf.String()
	if !strings.Contains(out, "outreach_id=out_9") {
		t.Errorf("the outreach id is missing:\n%s", out)
	}
	if strings.Contains(out, "cand_7f3a") || strings.Contains(out, "candidate_ref") {
		t.Errorf("candidate_ref reached the log:\n%s", out)
	}
}

func TestARequestContextAddsItsIDsExactlyOnce(t *testing.T) {
	log, buf := captured(t)
	ctx, req := obs.WithRequest(context.Background())
	if !regexp.MustCompile(`^req_[0-9a-f]{16}$`).MatchString(req.ID) {
		t.Fatalf("request id %q is not a minted id", req.ID)
	}
	obs.SetRunID(ctx, "run_5")
	// LogSink adds run_id itself; the handler must not add a second one.
	record(obs.LogSink(ctx, log), obs.Event{Level: obs.Info, Name: obs.RunStarted, RunID: "run_5"})
	log.WarnContext(ctx, "an ordinary warning inside the request")
	out := buf.String()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d:\n%s", len(lines), out)
	}
	for _, l := range lines {
		if strings.Count(l, "request_id="+req.ID) != 1 {
			t.Errorf("request_id missing or repeated: %s", l)
		}
		if strings.Count(l, "run_id=run_5") != 1 {
			t.Errorf("run_id missing or repeated: %s", l)
		}
	}
}
