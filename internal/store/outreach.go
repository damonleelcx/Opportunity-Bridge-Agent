package store

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
)

// This file holds the two reads and the one write that the recruiter role adds
// to the store, kept together because they share a single rule:
//
//	Nothing here may return a person who did not ask to be here.
//
// The filter lives in the store rather than in the tool that calls it. A tool
// that forgets the check is a plausible mistake and it fails open - it would
// return the whole population and nothing in the type system would object. Here
// there is one place to audit and the caller has no way to skip it.

// DiscoverableProfiles returns the profiles of everyone who granted
// ConsentDiscoverable, and nobody else.
//
// This is the only cross-subject read of identified records in the service. It
// exists because a recruiter's question ("who can weld, in 成都") is inherently
// about many people, which every other tool here is not.
//
// Withdrawal takes effect on the next call: consent is read live rather than
// copied into a pool at grant time, so "you can withdraw this at any time" is
// true in the only sense that matters. There is deliberately no cached index.
func (s *Store) DiscoverableProfiles() []domain.Profile {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Profile
	for id, p := range s.s.Profiles {
		if g, ok := s.s.Consent[id][domain.ConsentDiscoverable]; !ok || !g.Granted {
			continue
		}
		out = append(out, *p)
	}
	// Sorted by subject id so that two identical searches return the same order.
	// Map iteration order would make the result set look like it changed when it
	// did not, and a recruiter comparing two runs would read that as movement in
	// the pool.
	sort.Slice(out, func(i, j int) bool { return out[i].SubjectID < out[j].SubjectID })
	return out
}

// ---- outreach ----

func (s *Store) CreateOutreach(o domain.Outreach) domain.Outreach {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.s.Outreach == nil {
		s.s.Outreach = map[string]*domain.Outreach{}
	}
	o.ID = s.nextID("out")
	o.Status = domain.OutreachPending
	o.CreatedAt = time.Now().UTC()
	// A request never carries a channel. The channel is what acceptance releases,
	// so trusting a caller-supplied one here would let a recruiter hand itself
	// the thing the handshake exists to withhold.
	o.Channel = domain.Channel{}
	cp := o
	s.s.Outreach[o.ID] = &cp
	s.persist()
	return cp
}

func (s *Store) Outreach(id string) (domain.Outreach, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	o, ok := s.s.Outreach[id]
	if !ok {
		return domain.Outreach{}, false
	}
	return *o, true
}

// OutreachFor returns every request addressed to one person, newest first. This
// is the person's own list: it is how they see who asked.
func (s *Store) OutreachFor(subjectID string) []domain.Outreach {
	return s.outreachWhere(func(o *domain.Outreach) bool { return o.SubjectID == subjectID })
}

// OutreachFrom returns every request one recruiter sent, newest first.
func (s *Store) OutreachFrom(recruiterID string) []domain.Outreach {
	return s.outreachWhere(func(o *domain.Outreach) bool { return o.RecruiterID == recruiterID })
}

func (s *Store) outreachWhere(match func(*domain.Outreach) bool) []domain.Outreach {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Outreach
	for _, o := range s.s.Outreach {
		if match(o) {
			out = append(out, *o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// PendingOutreachCount is what the interface shows the person as "somebody is
// waiting on you", without loading the requests themselves.
func (s *Store) PendingOutreachCount(subjectID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, o := range s.s.Outreach {
		if o.SubjectID == subjectID && o.Status == domain.OutreachPending {
			n++
		}
	}
	return n
}

// DecideOutreach records the candidate's answer. It is the only way an Outreach
// leaves OutreachPending, and the only place a contact channel is ever attached.
//
// subjectID is passed and checked rather than trusted from the caller's lookup:
// this is the one write in the service where the obvious bug - deciding somebody
// else's request - is both easy to write and invisible afterwards.
func (s *Store) DecideOutreach(id, subjectID string, status domain.OutreachStatus, channel domain.Channel, reason string) (domain.Outreach, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.s.Outreach[id]
	if !ok {
		return domain.Outreach{}, fmt.Errorf("OUTREACH_NOT_FOUND: no contact request %q", id)
	}
	if o.SubjectID != subjectID {
		return domain.Outreach{}, fmt.Errorf(
			"OUTREACH_NOT_YOURS: contact request %q was not addressed to this person; only the person asked may answer it", id)
	}
	switch status {
	case domain.OutreachAccepted, domain.OutreachDeclined, domain.OutreachWithdrawn:
	default:
		return domain.Outreach{}, fmt.Errorf(
			"OUTREACH_STATUS_INVALID: %q is not an answer; expected accepted, declined or withdrawn", status)
	}
	if status == domain.OutreachWithdrawn && o.Status != domain.OutreachAccepted {
		return domain.Outreach{}, fmt.Errorf(
			"OUTREACH_NOT_ACCEPTED: request %q is %q, and only an accepted one can be withdrawn", id, o.Status)
	}
	if status != domain.OutreachWithdrawn && o.Status != domain.OutreachPending {
		return domain.Outreach{}, fmt.Errorf(
			"OUTREACH_ALREADY_DECIDED: request %q is already %q; it cannot be answered twice", id, o.Status)
	}
	o.Status = status
	o.DecidedAt = time.Now().UTC()
	o.Reason = strings.TrimSpace(reason)
	if status == domain.OutreachAccepted {
		o.Channel = channel
	} else {
		// Declining and withdrawing both close the channel. Leaving a stale one on
		// a withdrawn record would mean withdrawal changed a label and nothing else.
		o.Channel = domain.Channel{}
	}
	s.persist()
	return *o, nil
}
