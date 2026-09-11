package agent

// ThinkingTier is how long the agent model may think on one turn, as the person
// chose it in the interface.
//
// WHY A PERSON CHOOSES THIS
//
//	Every intent asks for effort "high" with thinking on, so a person who wants
//	a quick answer waits for the model to think anyway. damon decided
//	(2026-09-11) to put the choice in the person's hands, and to build it before
//	the latency measurement finished: whether a lower tier is meaningfully faster
//	on real turns is NOT yet measured. The only numbers (one question, one or two
//	runs each) had "off" at 7s against ~28s for "thorough" - by skipping its
//	lookups - and "fast" slowest at 46s, because a draft was sent back.
//	See docs/03-model-and-prompt.md.
//
// WHY "off" IS A ROW AND NOT AN EFFORT
//
//	On Qwen an effort the budget table does not know, sent with thinking on,
//	carries no thinking budget at all: "off" travelling as an effort would be
//	UNCAPPED thinking under the label off. Off is thinking false, with no effort.
type ThinkingTier struct {
	Name     string
	Thinking bool
	Effort   string
}

// ThinkingTiers is the one list: the API validates against it, /api/meta offers
// it, and the interface draws its options from what meta sends. Listed in the
// order they are shown.
var ThinkingTiers = []ThinkingTier{
	{Name: "off", Thinking: false},
	{Name: "fast", Thinking: true, Effort: "low"},
	{Name: "balanced", Thinking: true, Effort: "medium"},
	{Name: "thorough", Thinking: true, Effort: "high"},
}

// DefaultThinkingTier is what the interface shows before a person chooses. It is
// the tier that changes nothing: every intent already asks for high with
// thinking on. TestTheDefaultTierIsWhatEveryIntentAlreadyAsksFor fails the day
// an intent asks for something else, because then "thorough" would no longer
// mean "as before".
const DefaultThinkingTier = "thorough"

// LookupThinkingTier finds a tier by name. The empty name is not a tier.
func LookupThinkingTier(name string) (ThinkingTier, bool) {
	for _, t := range ThinkingTiers {
		if t.Name == name {
			return t, true
		}
	}
	return ThinkingTier{}, false
}

// ThinkingTierNames lists the tier names in display order.
func ThinkingTierNames() []string {
	out := make([]string, len(ThinkingTiers))
	for i, t := range ThinkingTiers {
		out[i] = t.Name
	}
	return out
}
