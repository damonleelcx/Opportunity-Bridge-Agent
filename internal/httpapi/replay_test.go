package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/tools"
)

// Persisting a candidate card so it survives a refresh must NOT outlive the
// consent it was drawn from.
//
// The product's rule is that withdrawing discoverable_by_employers takes effect
// on the next read — fenced on the search path by
// TestWithdrawingDiscoverabilityEmptiesThePoolAndKillsTheRef. Storing the card
// would quietly break it in the one place nobody looks: somebody who opted out
// would keep reappearing in a recruiter's window on every reload, forever,
// because the answer had been written down once.
//
// So the card is stored and the consent is applied again on the way out.
func TestAWithdrawnCandidateDoesNotComeBackWithTheTranscript(t *testing.T) {
	st := store.New("", slog.New(slog.NewTextHandler(io.Discard, nil)))
	ses := st.CreateSession(domain.RoleRecruiter, "", "en")

	const candidate = "subj_pool_1"
	st.SaveProfile(domain.Profile{SubjectID: candidate, City: "成都", Skills: []string{"数控"}})
	st.SetConsent(candidate, domain.ConsentDiscoverable, true, "test")
	ref := tools.CandidateRef(ses.SubjectID, candidate)

	card, err := json.Marshal(map[string]any{
		"candidates": []any{map[string]any{"candidate_ref": ref, "city": "成都", "skills": []string{"数控"}}},
		"matched":    1, "returned": 1, "pool_size": 1,
	})
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if err := st.MutateSession(ses.ID, func(s *store.Session) error {
		s.History = append(s.History, store.Turn{
			Role: "assistant", Text: "one match",
			Cards: []store.TurnCard{{Tool: "candidate_search", Result: card}},
		})
		return nil
	}); err != nil {
		t.Fatalf("seed history: %v", err)
	}
	live, _ := st.Session(ses.ID)

	// While the permission stands, the card replays with the person in it.
	before := candidatesIn(t, freshenCards(st, live))
	if len(before) != 1 {
		t.Fatalf("a consented candidate did not survive the replay: %v", before)
	}

	// The moment it is withdrawn, the same stored card must come back empty.
	st.SetConsent(candidate, domain.ConsentDiscoverable, false, "withdrawn")
	after := freshenCards(st, live)
	got := candidatesIn(t, after)
	if len(got) != 0 {
		t.Errorf("a withdrawn person came back with the transcript: %v", got)
	}

	// And the counts are restated, or the card claims people it does not show.
	body := cardBody(t, after)
	for _, k := range []string{"matched", "returned", "pool_size"} {
		if n, _ := body[k].(float64); n != 0 {
			t.Errorf("%s still reads %v after the pool emptied", k, body[k])
		}
	}
	if n, _ := body["withdrawn_since"].(float64); n != 1 {
		t.Errorf("the card does not say somebody left: withdrawn_since=%v", body["withdrawn_since"])
	}

	// The stored record itself is untouched: freshening is a view, not an edit.
	stored, _ := st.Session(ses.ID)
	if len(candidatesIn(t, stored)) != 1 {
		t.Error("freshenCards mutated the stored history; it must only shape what is served")
	}
}

// A card that cannot be re-checked is dropped rather than drawn.
func TestAnUnparseableCandidateCardIsDroppedNotShown(t *testing.T) {
	st := store.New("", slog.New(slog.NewTextHandler(io.Discard, nil)))
	ses := st.CreateSession(domain.RoleRecruiter, "", "en")
	if err := st.MutateSession(ses.ID, func(s *store.Session) error {
		s.History = append(s.History, store.Turn{
			Role: "assistant", Text: "x",
			Cards: []store.TurnCard{
				{Tool: "candidate_search", Result: json.RawMessage(`{"candidates": "not-a-list"`)},
				{Tool: "external_talent_scan", Result: json.RawMessage(`{"estimated_total_at_least":7}`)},
			},
		})
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	live, _ := st.Session(ses.ID)
	out := freshenCards(st, live)
	cards := out.History[0].Cards
	if len(cards) != 1 || cards[0].Tool != "external_talent_scan" {
		t.Errorf("expected only the scan card to survive, got %+v", cards)
	}
}

func cardBody(t *testing.T, s *store.Session) map[string]any {
	t.Helper()
	for _, turn := range s.History {
		for _, c := range turn.Cards {
			if c.Tool == "candidate_search" {
				var body map[string]any
				if err := json.Unmarshal(c.Result, &body); err != nil {
					t.Fatalf("card body: %v", err)
				}
				return body
			}
		}
	}
	t.Fatal("no candidate_search card in the history")
	return nil
}

func candidatesIn(t *testing.T, s *store.Session) []any {
	t.Helper()
	list, _ := cardBody(t, s)["candidates"].([]any)
	return list
}
