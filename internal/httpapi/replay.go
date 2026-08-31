package httpapi

import (
	"encoding/json"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/tools"
)

// freshenCards re-checks a stored conversation against consent as it stands NOW,
// before the history is handed back.
//
// Why this exists at all: a candidate_search card is a projection of OTHER
// PEOPLE's live permission, and the product's rule is that withdrawing
// discoverable_by_employers takes effect on the next read — fenced by
// TestWithdrawingDiscoverabilityEmptiesThePoolAndKillsTheRef. Storing the card so
// it survives a refresh would quietly break that: somebody who opted out would
// keep appearing in a recruiter's window every time the page reloaded, forever,
// because the answer had been written down once.
//
// So the card is stored, and the consent is applied again on the way out. A
// reopened conversation shows the pool as it is, not as it was. That keeps
// "withdrawal works on the next read" literally true, with the read being this
// one.
//
// Nothing else needs this treatment: an opportunity, a gap aggregate and a
// vendor lead are not projections of a living permission. The vendor leads carry
// no identity at all - see internal/talentsource.
func freshenCards(st *store.Store, ses *store.Session) *store.Session {
	if ses == nil {
		return ses
	}
	var pool map[string]bool // candidate_ref -> still discoverable, built once and only if needed
	out := *ses
	out.History = make([]store.Turn, len(ses.History))
	copy(out.History, ses.History)

	for i, turn := range out.History {
		if len(turn.Cards) == 0 {
			continue
		}
		cards := make([]store.TurnCard, 0, len(turn.Cards))
		for _, card := range turn.Cards {
			if card.Tool != "candidate_search" {
				cards = append(cards, card)
				continue
			}
			if pool == nil {
				pool = discoverableRefs(st, ses.SubjectID)
			}
			if filtered, ok := filterCandidates(card.Result, pool); ok {
				cards = append(cards, store.TurnCard{Tool: card.Tool, Result: filtered})
			}
		}
		out.History[i].Cards = cards
	}
	return &out
}

// discoverableRefs is every candidate_ref THIS recruiter can currently see.
//
// The refs are recomputed rather than stored, exactly as resolveCandidateRef
// does, so a person who left the pool is simply not in the set and their card
// cannot be matched back.
func discoverableRefs(st *store.Store, recruiterID string) map[string]bool {
	refs := map[string]bool{}
	for _, p := range st.DiscoverableProfiles() {
		refs[tools.CandidateRef(recruiterID, p.SubjectID)] = true
	}
	return refs
}

// filterCandidates drops candidates who are no longer in the pool and restates
// the counts, so the card cannot claim more people than it shows.
//
// It returns ok=false when the result does not parse, in which case the card is
// dropped rather than shown: a candidate card whose consent could not be
// re-checked is one this service should not draw.
func filterCandidates(raw json.RawMessage, pool map[string]bool) (json.RawMessage, bool) {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, false
	}
	list, _ := body["candidates"].([]any)
	kept := make([]any, 0, len(list))
	for _, item := range list {
		c, ok := item.(map[string]any)
		if !ok {
			continue
		}
		ref, _ := c["candidate_ref"].(string)
		if pool[ref] {
			kept = append(kept, c)
		}
	}
	body["candidates"] = kept
	body["returned"] = len(kept)
	// matched and pool_size are restated too. Leaving the old totals beside a
	// shortened list would say people were withheld, when in fact they left.
	body["matched"] = len(kept)
	body["pool_size"] = len(pool)
	if len(kept) < len(list) {
		body["withdrawn_since"] = len(list) - len(kept)
	}
	out, err := json.Marshal(body)
	if err != nil {
		return nil, false
	}
	return out, true
}

var _ = domain.ConsentDiscoverable // the scope this file exists to honour
