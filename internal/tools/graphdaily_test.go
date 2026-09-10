package tools_test

// Fences over the pass that makes 离职提醒 arrive rather than wait to be asked
// for, and over what it hands 阿桥 to say.

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/leadgraph"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/tools"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func dailyStore(t *testing.T) *store.Store {
	t.Helper()
	return store.New(t.TempDir()+"/state.json", quiet())
}

// recordReorg puts the event this whole feature was asked for into one seat's
// book: A司's c業務組 folding into b業務組, with two people in it.
func recordReorg(t *testing.T, g *leadgraph.Store, v leadgraph.View) {
	t.Helper()
	said := func(x string) []leadgraph.Intel {
		return []leadgraph.Intel{{Kind: leadgraph.IntelUserSaid, TurnRef: "t1", Excerpt: x}}
	}
	for _, n := range []leadgraph.Node{
		{Kind: leadgraph.KindPerson, Org: "A司", Label: "王五", RoleTitle: "组长",
			UnitPath: []string{"A司", "c业务组"}, Intel: said("王五是组长")},
		{Kind: leadgraph.KindPerson, Org: "A司", Label: "张三",
			UnitPath: []string{"A司", "c业务组"}, Intel: said("张三在同组")},
		{Kind: leadgraph.KindEvent, Org: "A司", Label: "c业务组并入b业务组",
			UnitPath: []string{"A司", "c业务组"}, Intel: said("c组要并进b组")},
	} {
		if _, err := g.UpsertNode(v, leadgraph.ActorUser, n); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

// The pass runs for the seats that exist, and it raises the alert.
func TestTheDailyPassRunsForEverySeatAndRaisesTheAlert(t *testing.T) {
	st := dailyStore(t)
	acct, err := st.CreateAccount("nora-recruiter", "hash")
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	g := leadgraph.New(quiet())
	v := leadgraph.View{TeamID: tools.GraphTeamFor(st, acct.SubjectID), SeatID: acct.SubjectID}
	recordReorg(t, g, v)

	seats, alerts, _ := tools.RunGraphDaily(context.Background(), st, g, quiet(), time.Now().UTC())
	if seats == 0 {
		t.Fatal("the pass ran for no seats at all")
	}
	if alerts != 1 {
		t.Fatalf("the pass raised %d alerts for one reorganisation", alerts)
	}
}

// What 阿桥 is handed names the company, the change and how many of the
// recruiter's own people are in it — and it is handed over ONCE.
func TestTheNewsIsToldOnceAndNamesWhoIsAffected(t *testing.T) {
	st := dailyStore(t)
	acct, err := st.CreateAccount("olive-recruiter", "hash")
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	g := leadgraph.New(quiet())
	v := leadgraph.View{TeamID: tools.GraphTeamFor(st, acct.SubjectID), SeatID: acct.SubjectID}
	recordReorg(t, g, v)
	tools.RunGraphDaily(context.Background(), st, g, quiet(), time.Now().UTC())

	news := tools.GraphNews(st, g, acct.SubjectID, time.Now().UTC())
	if len(news) != 1 {
		t.Fatalf("news: %d lines, want 1: %v", len(news), news)
	}
	for _, want := range []string{"A司", "c业务组并入b业务组", "2 people"} {
		if !strings.Contains(news[0], want) {
			t.Errorf("the news does not mention %q: %s", want, news[0])
		}
	}
	// Told once. A line that reappears at the top of every turn is a line
	// people learn to skip, and then the next one is skipped too.
	if again := tools.GraphNews(st, g, acct.SubjectID, time.Now().UTC()); len(again) != 0 {
		t.Errorf("the same news was told a second time: %v", again)
	}
	// And it is not lost: it is still on the graph screen.
	if got := g.Alerts(v, true); len(got) != 1 {
		t.Errorf("acknowledging the news deleted it: %d alerts remain", len(got))
	}
}

// A seat with nothing to say produces nothing. Silence is the normal case, and
// a daily pass that always had something to announce would be noise by design.
func TestASeatWithNoChangesHasNoNews(t *testing.T) {
	st := dailyStore(t)
	acct, err := st.CreateAccount("pete-recruiter", "hash")
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	g := leadgraph.New(quiet())
	tools.RunGraphDaily(context.Background(), st, g, quiet(), time.Now().UTC())
	if news := tools.GraphNews(st, g, acct.SubjectID, time.Now().UTC()); len(news) != 0 {
		t.Errorf("a seat with an empty book produced news: %v", news)
	}
}

// With no graph at all, both are silent rather than panicking. A deployment
// without a graph database is a supported deployment.
func TestTheDailyPassIsSilentWithoutAGraph(t *testing.T) {
	st := dailyStore(t)
	if seats, alerts, stale := tools.RunGraphDaily(context.Background(), st, nil, quiet(), time.Now().UTC()); seats+alerts+stale != 0 {
		t.Error("the pass did something without a graph")
	}
	if news := tools.GraphNews(st, nil, "sub_1", time.Now().UTC()); news != nil {
		t.Error("news came from a graph that does not exist")
	}
}
