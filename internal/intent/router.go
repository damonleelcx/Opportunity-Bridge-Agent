package intent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
)

// Decision is the outcome of routing, including how it was reached. "How" is
// recorded because a wrong answer traced to a wrong route is a different bug
// from a wrong answer inside the right route.
type Decision struct {
	ID         ID      `json:"intent"`
	Confidence float64 `json:"confidence"`
	Rationale  string  `json:"rationale"`
	Method     string  `json:"method"` // explicit | only_option | model | keyword_fallback
	Rejected   string  `json:"rejected,omitempty"`
}

// Route resolves which intent a message belongs to.
//
// The order is cheapest-first, and each step is skipped only when it cannot
// decide. Most turns never reach the model at all: an analyst has exactly one
// reachable intent, and a person who picked an intent in the interface has
// already answered the question.
func Route(
	ctx context.Context, client llm.Client, model string,
	role domain.Role, explicit ID, message string, sticky ID,
) (Decision, error) {
	reachable := ForRole(role)
	if len(reachable) == 0 {
		return Decision{ID: Unknown, Method: "no_intent_for_role"},
			fmt.Errorf("ROLE_HAS_NO_INTENT: role %q can reach no intent; this is a configuration error", role)
	}

	if explicit != "" {
		if !Allows(explicit, role) {
			return Decision{ID: Unknown, Method: "explicit", Rejected: string(explicit)},
				fmt.Errorf("INTENT_NOT_PERMITTED_FOR_ROLE: %q is not available to role %q; available: %s",
					explicit, role, strings.Join(idsOf(reachable), ", "))
		}
		return Decision{ID: explicit, Confidence: 1, Method: "explicit",
			Rationale: "The person selected this in the interface."}, nil
	}

	if len(reachable) == 1 {
		return Decision{ID: reachable[0].ID, Confidence: 1, Method: "only_option",
			Rationale: fmt.Sprintf("Role %q can reach only this intent.", role)}, nil
	}

	if client != nil && model != "" {
		if d, err := classify(ctx, client, model, role, reachable, message, sticky); err == nil {
			return d, nil
		}
	}
	return keywordRoute(reachable, message, sticky), nil
}

const classifierSystem = `You route one message to exactly one audience of the Opportunity Bridge Agent.

You are not answering the message. You are deciding whose problem it is.

Reply with one JSON object and nothing else:
{"intent": "<id>", "confidence": <0.0-1.0>, "rationale": "<one short sentence>"}

How to choose:
- individual_pathway: one person, their own work / training / benefits, wants to
  know what exists or what to do next.
- low_access_support: the obstacle is access itself - language, literacy, time,
  distance, care duties, a document they cannot get, a form they already failed
  once, or they ask to be spoken to differently.
- service_orchestration: a staff member coordinating procedures on someone's
  behalf, tracking steps, handovers, blockers across offices.
- supply_demand_insight: a question about a population, not a person - where
  demand and supply fail to meet, uptake rates, which district lacks what.

If the message is about a person's own situation AND access is the main obstacle,
choose low_access_support.
If you cannot tell, pick the one you would regret least and set confidence below 0.5.

Write the rationale in the SAME LANGUAGE as the message. It is shown to the
person in the interface, not only to an operator.`

func classify(
	ctx context.Context, client llm.Client, model string,
	role domain.Role, reachable []Intent, message string, sticky ID,
) (Decision, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Role of the person writing: %s\n", role)
	fmt.Fprintf(&b, "Intents available to this role: %s\n", strings.Join(idsOf(reachable), ", "))
	if sticky != "" {
		fmt.Fprintf(&b, "The previous message in this conversation was routed to: %s\n", sticky)
	}
	fmt.Fprintf(&b, "\nMessage:\n%s", message)

	resp, err := client.Stream(ctx, llm.Request{
		Model:     model,
		System:    []llm.SystemBlock{{Text: classifierSystem, Cache: true}},
		Messages:  []llm.Message{llm.UserText(b.String())},
		MaxTokens: 300,
		Effort:    "low",
	}, nil)
	if err != nil {
		return Decision{}, err
	}
	var parsed struct {
		Intent     string  `json:"intent"`
		Confidence float64 `json:"confidence"`
		Rationale  string  `json:"rationale"`
	}
	raw := extractJSON(resp.TextContent())
	if raw == "" || json.Unmarshal([]byte(raw), &parsed) != nil {
		return Decision{}, fmt.Errorf("CLASSIFIER_UNPARSEABLE: the routing model did not return a JSON object: %s",
			truncate(resp.TextContent(), 200))
	}
	id := ID(strings.TrimSpace(parsed.Intent))
	if !Allows(id, role) {
		return Decision{}, fmt.Errorf("CLASSIFIER_OUT_OF_RANGE: routed to %q, which this role cannot reach", id)
	}
	return Decision{ID: id, Confidence: parsed.Confidence, Rationale: parsed.Rationale, Method: "model"}, nil
}

var jsonObject = regexp.MustCompile(`(?s)\{.*\}`)

func extractJSON(s string) string { return jsonObject.FindString(s) }

// keywordCues is the fallback route. It is not a second-rate classifier; it is
// the answer to "what happens when the routing model is unreachable". A product
// that stops working entirely because a classifier is down has put a convenience
// on the main path.
var keywordCues = map[ID][]string{
	LowAccessSupport: {
		"can't read", "cannot read", "don't understand the form", "too far", "no time",
		"look after", "caring for", "my child", "my mother", "my father", "dialect",
		"speak slowly", "simpler", "read it to me", "tried and failed", "app won't",
		"看不懂", "太远", "没时间", "照顾", "方言", "说慢点", "简单点", "念给我听", "弄不来", "不会用",
	},
	ServiceOrchestration: {
		"on behalf of", "my client", "the resident", "case", "handover", "track",
		"which office first", "coordinate", "blocked on",
		"代办", "群众", "居民", "工单", "对接", "先去哪个", "跟进",
	},
	SupplyDemandInsight: {
		"how many", "what share", "uptake", "across the district", "which district",
		"trend", "gap", "unclaimed", "coverage", "population",
		"多少人", "比例", "覆盖率", "哪个区", "缺口", "没人申领", "趋势",
	},
}

func keywordRoute(reachable []Intent, message string, sticky ID) Decision {
	low := strings.ToLower(message)
	best, bestScore := Unknown, 0
	for _, in := range reachable {
		score := 0
		for _, cue := range keywordCues[in.ID] {
			if strings.Contains(low, strings.ToLower(cue)) {
				score++
			}
		}
		if score > bestScore {
			best, bestScore = in.ID, score
		}
	}
	if bestScore > 0 {
		return Decision{ID: best, Confidence: 0.5, Method: "keyword_fallback",
			Rationale: "The routing model was unavailable; routed on wording cues."}
	}
	if sticky != "" && Allows(sticky, reachable[0].Roles[0]) {
		return Decision{ID: sticky, Confidence: 0.4, Method: "keyword_fallback",
			Rationale: "The routing model was unavailable; stayed with the previous intent."}
	}
	// Default to the first intent the role can reach, which is the most general
	// one by registry order. It can always ask a clarifying question.
	return Decision{ID: reachable[0].ID, Confidence: 0.3, Method: "keyword_fallback",
		Rationale: "The routing model was unavailable and nothing matched; using this role's default intent."}
}

func idsOf(in []Intent) []string {
	out := make([]string, len(in))
	for i, x := range in {
		out[i] = string(x.ID)
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
