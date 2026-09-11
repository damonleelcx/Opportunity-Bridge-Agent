package web_test

import (
	"strings"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/agent"
)

// The thinking tier is offered from what the server accepts, remembered in the
// browser, and sent with every message.
//
// A list of tiers kept in the page would outlive a tier the server stops
// accepting and turn every message into a 400; a choice that is not sent does
// nothing; a label missing in one language shows the raw key.
// See docs/14-interface.md, "Thinking tier".
func TestThinkingTierIsOfferedFromTheServerAndSentWithEveryMessage(t *testing.T) {
	html := asset(t, "app.html")
	js := asset(t, "app.js")
	i18n := asset(t, "i18n.js")

	if !strings.Contains(html, `id="thinkingTier"`) {
		t.Fatal("app.html has no thinking-tier control")
	}

	build := section(t, js, "function buildThinkingSelect(")
	for _, want := range []string{"state.meta.thinking_tiers", "thinking_tier_default", `recall("oba.thinking"`, "o.dataset.i18n"} {
		if !strings.Contains(build, want) {
			t.Errorf("buildThinkingSelect does not use %s", want)
		}
	}
	for _, name := range agent.ThinkingTierNames() {
		if strings.Contains(build, `"`+name+`"`) {
			t.Errorf("buildThinkingSelect names the tier %q itself; the options must come from meta", name)
		}
		if n := strings.Count(i18n, `"thinking.`+name+`"`); n != 2 {
			t.Errorf("thinking.%s appears %d times in i18n.js, want once per language", name, n)
		}
	}
	if !strings.Contains(js, `remember("oba.thinking"`) {
		t.Error("a chosen tier is not remembered, so it is lost on the next reload")
	}
	if !strings.Contains(js, "buildThinkingSelect()") {
		t.Error("nothing builds the thinking-tier options")
	}

	send := section(t, js, "async function send(")
	if !strings.Contains(send, `thinking: $("#thinkingTier")`) {
		t.Error("send does not carry the chosen tier, so choosing one changes nothing")
	}
	if !strings.Contains(send, "status.answering") {
		t.Error("with thinking off the status line still says the model is thinking")
	}
	// That the control can actually be hidden until the tiers arrive is guarded
	// by TestEveryHiddenToggledControlCanActuallyBeHidden, not repeated here.
}
