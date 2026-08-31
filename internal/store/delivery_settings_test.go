package store_test

import (
	"io"
	"log/slog"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
)

// A delivery setting belongs to the person, so the next conversation has to
// start with it already in force.
//
// It used to be written to the profile and read only from the session, which
// meant the record said "answer me in plain words" while every new conversation
// silently ignored it. The panel then showed a setting that was not in effect —
// two truths, and the visible one was the wrong one. Somebody who needs short
// sentences needs them tomorrow too; asking again each time is the friction this
// product exists to remove.
// See docs/bugfix/2026-08-28-plain-language-could-not-be-turned-off.md
func TestNewConversationInheritsDeliverySettingsFromThePerson(t *testing.T) {
	st := store.New("", slog.New(slog.NewTextHandler(io.Discard, nil)))
	first := st.CreateSession(domain.RoleResident, "sub_1", "zh-CN")
	if len(first.AccessNeeds) != 0 {
		t.Fatalf("a first conversation began with %v; nothing has been set yet", first.AccessNeeds)
	}

	p := st.Profile(first.SubjectID)
	p.AccessNeeds = []domain.AccessNeed{domain.AccessPlainLanguage}
	st.SaveProfile(p)

	next := st.CreateSession(domain.RoleResident, "sub_1", "zh-CN")
	if len(next.AccessNeeds) != 1 || next.AccessNeeds[0] != domain.AccessPlainLanguage {
		t.Fatalf("a new conversation started with AccessNeeds=%v; the person's setting was ignored, "+
			"so they would have to ask for plain language again every time", next.AccessNeeds)
	}

	// NOT asserted here: that the session holds a COPY of the profile's slice.
	// CreateSession does copy it, and should, but the assertion would be
	// unfalsifiable — cloneSession copies AccessNeeds on the way out of every
	// read, and MutateSession clones before it mutates, so no path through the
	// store can reach the profile through an aliased session slice. A test that
	// cannot fail is worse than no test: it reads as coverage. Verified by
	// mutation on 2026-08-28 — replacing the copy with a plain assignment left
	// this file green.

	// Somebody else must not pick it up.
	if other := st.CreateSession(domain.RoleResident, "sub_2", "zh-CN"); len(other.AccessNeeds) != 0 {
		t.Errorf("a different person's conversation inherited %v", other.AccessNeeds)
	}
}
