package leadgraph

import (
	"sort"
	"strings"
	"time"
)

// Contact records: 接触记录.
//
// WHY THIS IS ITS OWN RECORD TYPE AND NOT A NODE
//
//	Nodes are identified by name inside an organisation - that is what
//	naturalKey means, and it is what makes recording the same person twice an
//	update. A contact has no name and no identity of its own: it is identified
//	by who, when and by whom. Forcing it into Node would have made naturalKey
//	mean two different things depending on the kind, which is how a key that
//	everything else depends on starts to rot.
//
//	The NodeKind "outreach" that used to sit in the enum for this had no
//	producer and no consumer. It is gone: a placeholder enum value reads as the
//	mechanism and quietly absorbs the attention that should go to building one.
//
// WHY THEY ARE THE TEAM'S AND NOT THE SEAT'S
//
//	"Somebody already called him last week" is the single fact that stops two
//	consultants cold-calling the same person - which is the failure that makes
//	a firm look disorganised to exactly the people it is trying to impress. It
//	has to survive the consultant who made the call leaving, so the record is
//	the team's and only the private impression on it (Note) is the seat's.
//
// PROVENANCE, AND WHY THIS ONE TYPE NEEDS NO Intel
//
//	Everywhere else in this package a record must say where it came from,
//	because it is a claim about a third party that somebody will act on. A
//	contact record IS its own provenance: SeatID says who, At says when, and
//	TurnRef points at the conversation it was mentioned in. There is no outside
//	source to cite because the source is the person filing it.

// Channel is how a contact actually happened. A closed list because it feeds
// grouping ("三次电话没打通") and free text cannot be grouped.
type Channel string

const (
	ChannelPhone    Channel = "phone"
	ChannelMessage  Channel = "message" // 微信 / 短信
	ChannelEmail    Channel = "email"
	ChannelInPerson Channel = "in_person"
	ChannelOther    Channel = "other"
)

func (c Channel) valid() bool {
	switch c {
	case ChannelPhone, ChannelMessage, ChannelEmail, ChannelInPerson, ChannelOther:
		return true
	}
	return false
}

// Touchpoint is one contact with one person.
type Touchpoint struct {
	ID       string    `json:"id"`
	TeamID   string    `json:"team_id"`
	SeatID   string    `json:"seat_id"` // who made the contact
	PersonID string    `json:"person_id"`
	At       time.Time `json:"at"`
	Via      Channel   `json:"via"`
	// Topic and Outcome are FACTS: what was discussed, what was said. They are
	// the team's, because they are why nobody needs to ask the person to repeat
	// themselves. "他听起来很敷衍" is not an outcome - it is a judgement, and it
	// belongs in Note.
	Topic    string     `json:"topic,omitempty"`
	Outcome  string     `json:"outcome,omitempty"`
	NextStep string     `json:"next_step,omitempty"`
	DueAt    *time.Time `json:"due_at,omitempty"`
	// Note is the reader's OWN private impression, projected from their
	// Annotation exactly like Node.Note. Never stored here.
	Note      string    `json:"note,omitempty"`
	TurnRef   string    `json:"turn_ref,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

func touchKey(t Touchpoint) string {
	return strings.Join([]string{
		t.TeamID, "touch", t.PersonID, t.SeatID,
		t.At.UTC().Format(time.RFC3339), string(t.Via),
	}, "|")
}

// AddTouchpoint records a contact. The same contact reported twice - the user
// mentions Monday's call again on Wednesday - is one record, not two.
func (s *Store) AddTouchpoint(v View, actor Actor, in Touchpoint) (Touchpoint, error) {
	if !v.valid() {
		return Touchpoint{}, ErrViewRequired
	}
	if in.At.IsZero() {
		return Touchpoint{}, ErrTouchpointTimeRequired
	}
	if in.Via == "" {
		in.Via = ChannelOther
	}
	if !in.Via.valid() {
		return Touchpoint{}, ErrUnknownKind
	}
	if err := scanSensitive(map[string]string{
		"topic": in.Topic, "outcome": in.Outcome, "next_step": in.NextStep, "note": in.Note,
	}); err != nil {
		return Touchpoint{}, err
	}
	in.TeamID, in.SeatID = v.TeamID, v.SeatID
	in.At = in.At.UTC()
	note := in.Note
	in.Note = ""

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.teamHasNode(v.TeamID, in.PersonID) {
		return Touchpoint{}, ErrNotFound
	}
	now := s.now()

	key := touchKey(in)
	var out *Touchpoint
	if id, ok := s.byKey[key]; ok {
		cur := s.touches[id]
		// Filling in what was left blank is normal - the user remembers the
		// next step later. Replacing what is already there is the same
		// judgement call as anywhere else, so the same rule applies.
		for _, f := range []struct {
			cur *string
			in  string
		}{{&cur.Topic, in.Topic}, {&cur.Outcome, in.Outcome}, {&cur.NextStep, in.NextStep}} {
			next := strings.TrimSpace(f.in)
			if next == "" || next == *f.cur {
				continue
			}
			if *f.cur != "" && actor != ActorUser {
				return Touchpoint{}, ErrConflictNeedsUser
			}
			*f.cur = next
		}
		if in.DueAt != nil && cur.DueAt == nil {
			cur.DueAt = in.DueAt
		}
		out = cur
	} else {
		t := in
		t.ID = s.nextID("tp")
		t.CreatedAt = now
		s.touches[t.ID] = &t
		s.byKey[key] = t.ID
		out = &t
	}
	if err := s.putTouch(out); err != nil {
		return Touchpoint{}, err
	}
	if strings.TrimSpace(note) != "" {
		s.setNote(v, out.ID, note, now)
		if err := s.putAnnotation(s.annotation(v, out.ID)); err != nil {
			return Touchpoint{}, err
		}
	}
	return s.projectTouch(v, *out), nil
}

func (s *Store) projectTouch(v View, t Touchpoint) Touchpoint {
	if a, ok := s.notes[v.SeatID][t.ID]; ok && a.TeamID == v.TeamID {
		t.Note = a.Note
	}
	return t
}

// Touchpoints lists every contact the TEAM has had with one person, newest
// first. Seeing a teammate's call before making your own is the entire point.
func (s *Store) Touchpoints(v View, personID string) []Touchpoint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.touchesFor(v, personID)
}

func (s *Store) touchesFor(v View, personID string) []Touchpoint {
	var out []Touchpoint
	for _, t := range s.touches {
		if t.TeamID != v.TeamID || t.PersonID != personID {
			continue
		}
		out = append(out, s.projectTouch(v, *t))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.After(out[j].At)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// LastTouch is the most recent contact anybody on the team recorded with this
// person, or nil. It is what a path result leads with.
func (s *Store) LastTouch(v View, personID string) *Touchpoint {
	ts := s.Touchpoints(v, personID)
	if len(ts) == 0 {
		return nil
	}
	return &ts[0]
}

// DueTouchpoints lists the follow-ups this seat owes, oldest due first.
//
// Only this seat's own: a next step is a promise the person who made it
// remembers making, and putting a teammate's promises in your list is how a
// follow-up list becomes something everybody ignores.
func (s *Store) DueTouchpoints(v View, by time.Time) []Touchpoint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Touchpoint
	for _, t := range s.touches {
		if t.TeamID != v.TeamID || t.SeatID != v.SeatID || t.DueAt == nil {
			continue
		}
		if t.DueAt.After(by) {
			continue
		}
		out = append(out, s.projectTouch(v, *t))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].DueAt.Equal(*out[j].DueAt) {
			return out[i].DueAt.Before(*out[j].DueAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}
