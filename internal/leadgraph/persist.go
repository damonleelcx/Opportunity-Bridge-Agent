package leadgraph

import (
	"log/slog"
	"time"
)

// The write side of persistence: one helper per record type, all of them
// funnelling into pgBackend.upsert.
//
// Every one of these is called WHILE HOLDING THE WRITE LOCK, on purpose. Doing
// the I/O outside it would mean a second writer could interleave between the
// memory change and the disk write, and the two would disagree about the order
// things happened. At this product's scale - one process, a few writes a second
// - holding the lock across a local database round trip costs nothing worth
// having back.
//
// A nil backend makes every one of these a no-op, which is what makes the
// in-memory store a real store rather than a test double.

// degrade records that the database stopped accepting writes.
//
// The flag exists for the methods whose signature cannot carry an error - a
// deletion count, a boolean. Those cannot tell their caller, so the store tells
// the HTTP layer instead, which stops accepting writes rather than taking
// changes it cannot keep.
func (s *Store) degrade(err error) {
	if s.degraded {
		return
	}
	s.degraded = true
	if s.log != nil {
		s.log.Error("persistence failed; refusing further writes",
			"code", "STORE_DEGRADED", "error", err)
	}
}

// Degraded reports whether writes are still being kept. Read by the HTTP layer
// on every write, and by the health endpoint.
func (s *Store) Degraded() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.degraded
}

func (s *Store) putRecord(table string, key []string, cols map[string]any, doc any) error {
	if s.pg == nil {
		return nil
	}
	if err := s.pg.upsert(table, key, cols, doc); err != nil {
		s.degrade(err)
		return err
	}
	return nil
}

func (s *Store) deleteRecord(table, where string, args ...any) error {
	if s.pg == nil {
		return nil
	}
	if err := s.pg.delete(table, where, args...); err != nil {
		s.degrade(err)
		return err
	}
	return nil
}

func (s *Store) putNode(n *Node) error {
	return s.putRecord("lead_nodes", []string{"id"}, map[string]any{
		"id": n.ID, "team_id": n.TeamID, "kind": string(n.Kind),
		"natural_key": naturalKey(*n), "created_at": n.CreatedAt, "updated_at": n.UpdatedAt,
	}, n)
}

func (s *Store) putEdge(e *Edge) error {
	return s.putRecord("lead_edges", []string{"id"}, map[string]any{
		"id": e.ID, "team_id": e.TeamID, "kind": string(e.Kind), "natural_key": edgeKey(*e),
		"from_id": e.From, "to_id": e.To, "valid_until": e.ValidUntil,
		"created_at": e.CreatedAt, "updated_at": e.UpdatedAt,
	}, e)
}

func (s *Store) putAnnotation(a *Annotation) error {
	return s.putRecord("lead_annotations", []string{"seat_id", "target_id"}, map[string]any{
		"seat_id": a.SeatID, "target_id": a.TargetID, "team_id": a.TeamID,
		"strength": a.Strength, "note": a.Note, "updated_at": a.UpdatedAt,
	}, a)
}

func (s *Store) putTouch(t *Touchpoint) error {
	return s.putRecord("lead_touchpoints", []string{"id"}, map[string]any{
		"id": t.ID, "team_id": t.TeamID, "seat_id": t.SeatID, "person_id": t.PersonID,
		"natural_key": touchKey(*t), "at": t.At, "via": string(t.Via),
		"due_at": t.DueAt, "created_at": t.CreatedAt,
	}, t)
}

func (s *Store) putObs(o *Observation) error {
	return s.putRecord("lead_observations", []string{"id"}, map[string]any{
		"id": o.ID, "team_id": o.TeamID, "source": o.Source, "url": o.URL,
		"kind": string(o.Kind), "org": o.Org, "unit": o.Unit, "digest": o.Digest,
		"natural_key": observationKey(*o), "posted_at": o.PostedAt, "at": o.At,
		"created_at": o.At,
	}, o)
}

func (s *Store) putAlert(a *Alert) error {
	return s.putRecord("lead_alerts", []string{"id"}, map[string]any{
		"id": a.ID, "team_id": a.TeamID, "event_id": a.EventID,
		"ack": a.Ack, "raised_at": a.RaisedAt,
	}, a)
}

func (s *Store) putPending(p *PendingItem, dedupe string) error {
	return s.putRecord("lead_pending", []string{"id"}, map[string]any{
		"id": p.ID, "team_id": p.TeamID, "seat_id": p.SeatID,
		"dedupe_key": dedupe, "at": p.At,
	}, p)
}

func (s *Store) putAudit(e AuditEntry) error {
	return s.putRecord("lead_audit", []string{"id"}, map[string]any{
		"id": e.ID, "team_id": e.TeamID, "seat_id": e.SeatID,
		"action": e.Action, "at": e.At,
	}, e)
}

// Seat is who may use the graph, and what turns a request into a View.
type Seat struct {
	SeatID    string     `json:"seat_id"`
	TeamID    string     `json:"team_id"`
	Label     string     `json:"label"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

func (s *Store) putSeat(seat Seat, tokenHash string) error {
	return s.putRecord("lead_seats", []string{"seat_id"}, map[string]any{
		"seat_id": seat.SeatID, "team_id": seat.TeamID, "label": seat.Label,
		"token_hash": tokenHash, "created_at": seat.CreatedAt, "revoked_at": seat.RevokedAt,
	}, seat)
}

var _ = slog.Default
