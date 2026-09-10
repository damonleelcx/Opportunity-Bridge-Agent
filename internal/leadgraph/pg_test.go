package leadgraph_test

// These run against a REAL postgres, on the schema this package ships and
// applies at start. A test that builds its own simplified tables proves that
// the simplification works.
//
// Set LEADGRAPH_TEST_DATABASE_URL to cover them. When it is unset they SKIP,
// which is the dangerous state - a skipped test is green - so the skip message
// says exactly how to run them.

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/leadgraph"
)

func pgStore(t *testing.T) *leadgraph.Store {
	t.Helper()
	dsn := os.Getenv("LEADGRAPH_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("LEADGRAPH_TEST_DATABASE_URL is not set: the postgres backend is NOT covered by this run. " +
			"Run `make test-leadgraph-pg`.")
	}
	s, err := leadgraph.NewWithPostgres(context.Background(), dsn,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(s.Close)
	// Each test starts from an empty graph, on the schema the migrations made.
	for _, team := range []string{"team_a", "team_b"} {
		s.ForgetTeam(team)
	}
	return s
}

// The whole point: what a consultant records on Friday is there on Monday.
func TestTheGraphSurvivesARestart(t *testing.T) {
	dsn := os.Getenv("LEADGRAPH_TEST_DATABASE_URL")
	s := pgStore(t)

	n := person("A司", "王五")
	n.RoleTitle = "组长"
	n.Note = "amy 的私人印象"
	n.Contacts = []leadgraph.ContactPoint{{Kind: leadgraph.ContactPhone, Value: "13800000000"}}
	created := mustNode(t, s, amy, leadgraph.ActorUser, n)
	b2 := mustNode(t, s, amy, leadgraph.ActorUser, person("C司", "b2"))
	e := mustEdge(t, s, amy, leadgraph.ActorUser, leadgraph.Edge{
		Kind: leadgraph.EdgeKnows, From: created.ID, To: b2.ID, Strength: 3,
		Intel: []leadgraph.Intel{said("认识")},
	})
	if _, err := s.AddTouchpoint(amy, leadgraph.ActorUser, leadgraph.Touchpoint{
		PersonID: created.ID, At: day, Via: leadgraph.ChannelPhone,
		Topic: "初次沟通", Outcome: "让我下月再联系",
	}); err != nil {
		t.Fatalf("touchpoint: %v", err)
	}
	// A seed is a PERSON this seat said they know - not a strength on an edge.
	// Rating 王五 is what gives path_find somewhere to start.
	if err := s.Annotate(amy, leadgraph.ActorUser, created.ID, 3, ""); err != nil {
		t.Fatalf("annotate: %v", err)
	}
	if _, err := s.Export(amy, day); err != nil {
		t.Fatalf("export: %v", err)
	}
	s.Close()

	// A different process, same database.
	again, err := leadgraph.NewWithPostgres(context.Background(), dsn,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer again.Close()

	got, ok := again.Node(amy, created.ID)
	if !ok {
		t.Fatal("the person is gone after a restart")
	}
	if got.RoleTitle != "组长" {
		t.Errorf("a fact was lost: %+v", got)
	}
	if len(got.Contacts) != 1 || got.Contacts[0].Value != "13800000000" {
		t.Errorf("the way to reach him was lost: %+v", got.Contacts)
	}
	if got.Note != "amy 的私人印象" {
		t.Errorf("the private note was lost: %q", got.Note)
	}
	if mate, _ := again.Node(ben, created.ID); mate.Note != "" {
		t.Errorf("the note stopped being private across a restart: %q", mate.Note)
	}
	if es := again.Edges(amy, leadgraph.EdgeFilter{Endpoint: created.ID}); len(es) != 1 || es[0].Strength != 3 {
		t.Errorf("the relationship or its strength was lost: %+v", es)
	}
	if ts := again.Touchpoints(amy, created.ID); len(ts) != 1 || ts[0].Outcome == "" {
		t.Errorf("the call log was lost: %+v", ts)
	}
	var exported bool
	for _, a := range again.AuditTrail(amy) {
		if a.Action == leadgraph.AuditExport {
			exported = true
		}
	}
	if !exported {
		t.Error("the audit trail did not survive - the one record that has to")
	}
	// And the route still works, which needs the seed, the edge and the strength
	// to have survived together.
	p := again.PathsTo(amy, b2.ID, leadgraph.PathOptions{})
	if p.NoSeeds {
		t.Fatalf("path_find has no starting point after a restart: %+v", p)
	}
	if len(p.Paths) != 1 || p.Paths[0].Hops[0].EdgeID != e.ID {
		t.Errorf("the route was lost: %+v", p.Paths)
	}
}

// Ids must not restart from one: minting an id that already exists silently
// overwrites the record holding it.
func TestIdsDoNotCollideAfterARestart(t *testing.T) {
	dsn := os.Getenv("LEADGRAPH_TEST_DATABASE_URL")
	s := pgStore(t)
	first := mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "王五"))
	s.Close()

	again, err := leadgraph.NewWithPostgres(context.Background(), dsn,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer again.Close()

	second := mustNode(t, again, amy, leadgraph.ActorUser, person("C司", "b2"))
	if second.ID == first.ID {
		t.Fatalf("the new record took the old one's id: %s", second.ID)
	}
	if _, ok := again.Node(amy, first.ID); !ok {
		t.Error("the first record was overwritten")
	}
}

// A deletion has to reach the database, or it comes back on the next restart.
func TestDeletionSurvivesARestart(t *testing.T) {
	dsn := os.Getenv("LEADGRAPH_TEST_DATABASE_URL")
	s := pgStore(t)
	n := mustNode(t, s, amy, leadgraph.ActorUser, person("A司", "王五"))
	if _, err := s.AddTouchpoint(amy, leadgraph.ActorUser, leadgraph.Touchpoint{
		PersonID: n.ID, At: day, Via: leadgraph.ChannelPhone, Topic: "初次沟通",
	}); err != nil {
		t.Fatalf("touchpoint: %v", err)
	}
	s.ForgetSubject(leadgraph.SubjectMatch{Org: "A司", Label: "王五"}, day)
	s.Close()

	again, err := leadgraph.NewWithPostgres(context.Background(), dsn,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer again.Close()
	if _, ok := again.Node(amy, n.ID); ok {
		t.Fatal("an erased person came back after a restart")
	}
	if len(again.Touchpoints(amy, n.ID)) != 0 {
		t.Error("the contact log for an erased person came back")
	}
}

// The schema is applied on every start, so the second start must succeed.
func TestMigrationsAreIdempotentAgainstARealDatabase(t *testing.T) {
	// Through pgStore so the skip happens; reading the env directly made this
	// the one test that FAILED rather than skipped without a database.
	pgStore(t).Close()
	dsn := os.Getenv("LEADGRAPH_TEST_DATABASE_URL")
	for i := range 3 {
		s, err := leadgraph.NewWithPostgres(context.Background(), dsn,
			slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatalf("start %d: %v", i+1, err)
		}
		s.Close()
	}
}

// A seat's token opens exactly its own view, and revoking closes it.
func TestSeatTokensResolveAndRevoke(t *testing.T) {
	s := pgStore(t)
	// Unique per run: seats SURVIVE, which is the behaviour being tested two
	// tests down, so a fixed id would collide with the previous run.
	uniq := time.Now().UTC().Format("150405.000000")
	seat, token, err := s.CreateSeat("team_a", "seat_amy_"+uniq, "Amy")
	if err != nil {
		t.Fatalf("create seat: %v", err)
	}
	v, err := s.ResolveToken(token)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if v.TeamID != "team_a" || v.SeatID != seat.SeatID {
		t.Fatalf("wrong view: %+v", v)
	}
	if _, err := s.ResolveToken(token + "x"); err == nil {
		t.Error("a wrong token resolved")
	}
	if err := s.RevokeSeat(seat.SeatID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := s.ResolveToken(token); err == nil {
		t.Error("a revoked seat still resolves")
	}
}

// Seats survive a restart, or every consultant is locked out after a deploy.
func TestSeatsSurviveARestart(t *testing.T) {
	dsn := os.Getenv("LEADGRAPH_TEST_DATABASE_URL")
	s := pgStore(t)
	uniq := time.Now().UTC().Format("150405.000000")
	if _, _, err := s.CreateSeat("team_a", "seat_ben_"+uniq, "Ben"); err != nil {
		t.Fatalf("create seat: %v", err)
	}
	_, token, err := s.CreateSeat("team_a", "seat_amy2_"+uniq, "Amy")
	if err != nil {
		t.Fatalf("create seat: %v", err)
	}
	s.Close()

	again, err := leadgraph.NewWithPostgres(context.Background(), dsn,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer again.Close()
	if _, err := again.ResolveToken(token); err != nil {
		t.Fatalf("a seat was locked out by a restart: %v", err)
	}
	if len(again.Seats("team_a")) < 2 {
		t.Errorf("seats were lost: %+v", again.Seats("team_a"))
	}
}

var _ = time.Now
