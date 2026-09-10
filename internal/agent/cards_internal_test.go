package agent

import (
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/tools"
)

// Every 猎源图谱 tool the model can call must survive a reload.
//
// A tool result is only written into the session record when it is in
// cardBearingTools. All seventeen graph tools were given a renderer in
// cardFor() and not one was added here, so a recruiter watched 阿桥 build the
// graph, saw the cards, refreshed, and got the prose with nothing under it —
// reported as "the generated cards are gone after refreshing the page". The
// comment above the register had named that exact failure mode for a release
// before it happened, which is what a comment can do and a fence cannot be
// replaced by.
//
// The list comes from tools.LeadGraphToolNames(), the same source the interface
// fence reads (TestEveryLeadGraphToolIsPresentedInTheConversation), so a tool
// added on the Go side turns BOTH sides red rather than only the side somebody
// remembered to look at.
//
// This test is in package agent, not agent_test, deliberately: reaching the
// register needs no exported accessor, and an accessor added for a test is a
// piece of public API that outlives the reason it existed.
// See docs/bugfix/2026-09-10-the-graph-card-could-not-be-read.md
func TestEveryGraphToolTheModelCanCallSurvivesAReload(t *testing.T) {
	names := tools.LeadGraphToolNames()
	if len(names) == 0 {
		t.Fatal("no graph tools are exposed - this fence would prove nothing")
	}
	for _, n := range names {
		if !cardBearingTools[n] {
			t.Errorf("%s draws a card in the conversation but its result is not kept, "+
				"so that card disappears on the reader's next reload", n)
		}
	}
	// And the register has to be able to say no, or this fence is measuring a
	// map that answers yes to everything.
	if cardBearingTools["knowledge_search"] {
		t.Error("a result with no renderer is being kept; the register no longer " +
			"distinguishes anything and this fence cannot fail")
	}
}
