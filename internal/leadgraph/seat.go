package leadgraph

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Identity: who is asking, and how a request becomes a View.
//
// WHY A TOKEN PER SEAT AND NOT A SHARED ONE
//
//	Half of this design rests on the difference between two people on one team.
//	Private notes, relationship strengths, "only the seat that was asked may
//	answer", "a departing consultant's judgements go and the team's facts stay"
//	- all of it is meaningless if two people share a credential, because then
//	the seat is not a person.
//
// WHY THE TOKEN IS STORED HASHED
//
//	A token table that can be read back is a credential store, and the whole
//	property of a bearer token is that only the bearer has it. The plaintext
//	exists exactly once, in the response to the call that created it.

var (
	ErrSeatUnknown = errors.New("SEAT_UNKNOWN: no seat holds that token")
	ErrSeatRevoked = errors.New("SEAT_REVOKED: that seat's access was withdrawn")
	ErrSeatExists  = errors.New("SEAT_EXISTS: a seat with that id already exists")
)

func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// CreateSeat mints a seat and its token. The plaintext token is returned once
// and never stored; losing it means minting another.
func (s *Store) CreateSeat(teamID, seatID, label string) (Seat, string, error) {
	if strings.TrimSpace(teamID) == "" || strings.TrimSpace(seatID) == "" {
		return Seat{}, "", ErrViewRequired
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return Seat{}, "", err
	}
	token := "lg_" + base64.RawURLEncoding.EncodeToString(buf)

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, taken := s.seats[seatID]; taken {
		return Seat{}, "", ErrSeatExists
	}
	seat := Seat{SeatID: seatID, TeamID: teamID, Label: label, CreatedAt: s.now()}
	s.seats[seatID] = &seat
	s.seatByToken[hashToken(token)] = &seat
	if err := s.putSeat(seat, hashToken(token)); err != nil {
		delete(s.seats, seatID)
		delete(s.seatByToken, hashToken(token))
		return Seat{}, "", err
	}
	return seat, token, nil
}

// RevokeSeat withdraws access without deleting the seat: its private notes and
// its place in the audit trail are still the record of what happened.
func (s *Store) RevokeSeat(seatID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	seat, ok := s.seats[seatID]
	if !ok {
		return ErrSeatUnknown
	}
	at := s.now()
	seat.RevokedAt = &at
	var hash string
	for h, x := range s.seatByToken {
		if x.SeatID == seatID {
			hash = h
		}
	}
	return s.putSeat(*seat, hash)
}

// Seats lists a team's seats, without any token material.
func (s *Store) Seats(teamID string) []Seat {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Seat
	for _, seat := range s.seats {
		if seat.TeamID == teamID {
			out = append(out, *seat)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SeatID < out[j].SeatID })
	return out
}

// ResolveToken turns a bearer token into a View.
//
// The comparison is constant-time. A timing side channel on a 32-byte random
// token is not the likeliest way in, but the fix is one function call and the
// alternative is explaining why we did not.
func (s *Store) ResolveToken(token string) (View, error) {
	if strings.TrimSpace(token) == "" {
		return View{}, ErrSeatUnknown
	}
	want := hashToken(token)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for h, seat := range s.seatByToken {
		if subtle.ConstantTimeCompare([]byte(h), []byte(want)) != 1 {
			continue
		}
		if seat.RevokedAt != nil {
			return View{}, ErrSeatRevoked
		}
		return View{TeamID: seat.TeamID, SeatID: seat.SeatID}, nil
	}
	return View{}, ErrSeatUnknown
}

// BearerResolver is the seam Handler asks for: Authorization: Bearer <token>.
//
// It is a function rather than something Handler does itself so that a
// deployment can put its own scheme in front - an SSO proxy, a session cookie -
// without this package growing an opinion about authentication it cannot keep
// up to date.
func BearerResolver(s *Store) func(*http.Request) (View, bool) {
	return func(r *http.Request) (View, bool) {
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			return View{}, false
		}
		v, err := s.ResolveToken(strings.TrimPrefix(h, "Bearer "))
		return v, err == nil
	}
}

// ---- loading ----

// NewWithPostgres opens the database, applies the schema and reads the graph
// back into memory.
//
// It fails rather than starting empty. A service that comes up with no data
// because it could not read its own database looks exactly like a service whose
// data was deleted, and the first thing somebody would do is start writing into
// it.
func NewWithPostgres(ctx context.Context, dsn string, log *slog.Logger) (*Store, error) {
	s := New(log)
	pg, err := openPG(ctx, dsn, log)
	if err != nil {
		return nil, err
	}
	s.pg = pg
	if err := s.load(ctx); err != nil {
		pg.close()
		return nil, err
	}
	return s, nil
}

// Close releases the database.
func (s *Store) Close() {
	if s.pg != nil {
		s.pg.close()
	}
}

// load reads every table back and rebuilds the derived indexes.
//
// The indexes are rebuilt rather than stored: byKey is a function of the
// records, and a stored copy is a second truth that can disagree with the first.
func (s *Store) load(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.pg.loadAll(ctx, "lead_nodes", func(raw json.RawMessage) error {
		var n Node
		if err := json.Unmarshal(raw, &n); err != nil {
			return err
		}
		s.nodes[n.ID] = &n
		s.byKey[naturalKey(n)] = n.ID
		return nil
	}); err != nil {
		return err
	}
	if err := s.pg.loadAll(ctx, "lead_edges", func(raw json.RawMessage) error {
		var e Edge
		if err := json.Unmarshal(raw, &e); err != nil {
			return err
		}
		s.edges[e.ID] = &e
		s.byKey[edgeKey(e)] = e.ID
		return nil
	}); err != nil {
		return err
	}
	if err := s.pg.loadAll(ctx, "lead_annotations", func(raw json.RawMessage) error {
		var a Annotation
		if err := json.Unmarshal(raw, &a); err != nil {
			return err
		}
		if s.notes[a.SeatID] == nil {
			s.notes[a.SeatID] = map[string]*Annotation{}
		}
		s.notes[a.SeatID][a.TargetID] = &a
		return nil
	}); err != nil {
		return err
	}
	if err := s.pg.loadAll(ctx, "lead_touchpoints", func(raw json.RawMessage) error {
		var t Touchpoint
		if err := json.Unmarshal(raw, &t); err != nil {
			return err
		}
		s.touches[t.ID] = &t
		s.byKey[touchKey(t)] = t.ID
		return nil
	}); err != nil {
		return err
	}
	if err := s.pg.loadAll(ctx, "lead_observations", func(raw json.RawMessage) error {
		var o Observation
		if err := json.Unmarshal(raw, &o); err != nil {
			return err
		}
		s.obs[o.ID] = &o
		s.byKey[observationKey(o)] = o.ID
		return nil
	}); err != nil {
		return err
	}
	if err := s.pg.loadAll(ctx, "lead_alerts", func(raw json.RawMessage) error {
		var a Alert
		if err := json.Unmarshal(raw, &a); err != nil {
			return err
		}
		s.alerts[a.ID] = &a
		s.byKey[a.TeamID+"|alert|"+a.EventID] = a.ID
		return nil
	}); err != nil {
		return err
	}
	if err := s.pg.loadAll(ctx, "lead_pending", func(raw json.RawMessage) error {
		var p PendingItem
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		s.pending[p.ID] = &p
		s.byKey[p.SeatID+"|pending|"+pendingKey(p.Proposal)] = p.ID
		return nil
	}); err != nil {
		return err
	}
	if err := s.pg.loadAll(ctx, "lead_audit", func(raw json.RawMessage) error {
		var e AuditEntry
		if err := json.Unmarshal(raw, &e); err != nil {
			return err
		}
		s.auditLog = append(s.auditLog, e)
		return nil
	}); err != nil {
		return err
	}

	// Seats need their token hash, which is a column and deliberately not in
	// the document - a document that carried it would put credential material
	// into every export of this table.
	rows, err := s.pg.pool.Query(ctx, "SELECT doc, token_hash FROM lead_seats")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var hash string
		if err := rows.Scan(&raw, &hash); err != nil {
			return err
		}
		var seat Seat
		if err := json.Unmarshal(raw, &seat); err != nil {
			return err
		}
		s.seats[seat.SeatID] = &seat
		s.seatByToken[hash] = &seat
	}
	if err := rows.Err(); err != nil {
		return err
	}

	s.seq = highestSeq(s)
	sort.Slice(s.auditLog, func(i, j int) bool { return s.auditLog[i].At.Before(s.auditLog[j].At) })
	s.log.Info("graph loaded", "code", "STORE_READY",
		"nodes", len(s.nodes), "edges", len(s.edges), "touchpoints", len(s.touches),
		"seats", len(s.seats), "pending", len(s.pending))
	return nil
}

// highestSeq finds where id minting left off. Ids look like nd_41; restarting
// the counter would mint an id that already exists and silently overwrite the
// record holding it.
func highestSeq(s *Store) int {
	max := 0
	consider := func(id string) {
		if i := strings.LastIndex(id, "_"); i >= 0 {
			if n, err := strconv.Atoi(id[i+1:]); err == nil && n > max {
				max = n
			}
		}
	}
	for id := range s.nodes {
		consider(id)
	}
	for id := range s.edges {
		consider(id)
	}
	for id := range s.touches {
		consider(id)
	}
	for id := range s.obs {
		consider(id)
	}
	for id := range s.alerts {
		consider(id)
	}
	for id := range s.pending {
		consider(id)
	}
	for _, e := range s.auditLog {
		consider(e.ID)
	}
	return max
}

var _ = time.Now
