package httpapi_test

// Fences over the one route 猎源图谱 needs: staging a file the model cannot
// produce.
//
// The claim: it only stages, it belongs to the employer workflow, and it puts
// the file in the SAME graph the conversation tools will read - a route and a
// tool disagreeing about that would stage into one book and import into another.

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/httpapi"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/leadgraph"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/tools"
)

const contactsCSV = "姓名,公司,部门,职位,熟悉程度\n" +
	"王五,A司,c业务组,组长,3\n" +
	"张三,A司,c业务组,高级工程师,2\n"

// graphServer wires a real graph into the harness.
func graphServer(t *testing.T) (*httptest.Server, *leadgraph.Store, *store.Store) {
	t.Helper()
	graph := leadgraph.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	var st *store.Store
	ts := newServerTweaking(t, llm.Script{}, nil, func(s *httpapi.Server) {
		s.Agent.Graph = graph
		st = s.Store
	})
	return ts, graph, st
}

// recruiterSession opens a session in the employer role and returns its id and
// the view the tools would compute for it.
func recruiterSession(t *testing.T, c *http.Client, ts *httptest.Server, role domain.Role) (string, leadgraph.View) {
	t.Helper()
	res := postAs(t, c, ts.URL+"/api/sessions", map[string]string{"role": string(role), "locale": "zh-CN"})
	defer res.Body.Close()
	var ses store.Session
	if err := json.NewDecoder(res.Body).Decode(&ses); err != nil {
		t.Fatalf("session: %v", err)
	}
	if ses.ID == "" {
		t.Fatalf("no session came back for role %s", role)
	}
	return ses.ID, leadgraph.View{TeamID: ses.SubjectID, SeatID: ses.SubjectID}
}

func upload(t *testing.T, c *http.Client, url, name, body string) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	f, err := w.CreateFormFile("file", name)
	if err != nil {
		t.Fatalf("form: %v", err)
	}
	if _, err := f.Write([]byte(body)); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = w.Close()
	req, err := http.NewRequest(http.MethodPost, url, &buf)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	res, err := c.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	return res
}

// A 200 here means "I read your file", not "I imported it". The two being
// different is what lets somebody see the rows that need deciding first.
func TestUploadingAFileOnlyStagesIt(t *testing.T) {
	ts, graph, _ := graphServer(t)
	c := signedIn(t, ts, "amy-recruiter")
	ses, seat := recruiterSession(t, c, ts, domain.RoleRecruiter)

	res := upload(t, c, ts.URL+"/api/sessions/"+ses+"/graph/imports", "contacts.csv", contactsCSV)
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("upload: %d %s", res.StatusCode, b)
	}
	var got struct {
		Staged  bool                    `json:"staged"`
		Summary leadgraph.ImportSummary `json:"summary"`
	}
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Staged || got.Summary.Counts["create"] != 2 {
		t.Fatalf("the plan is not described: %+v", got.Summary)
	}
	// And nothing landed.
	if n := len(graph.Nodes(seat, leadgraph.NodeFilter{})); n != 0 {
		t.Errorf("staging wrote %d records", n)
	}
}

// The route and the conversation must be looking at the same book.
//
// The account is placed in a FIRM on purpose: with no org both sides compute
// the same answer by accident, and a route that had stopped sharing the rule
// would still look correct. In a firm, the two answers differ unless they come
// from the same place.
func TestTheStagedFileIsVisibleToTheConversation(t *testing.T) {
	ts, graph, st := graphServer(t)
	c := signedIn(t, ts, "ben-recruiter")
	if err := st.SetAccountOrg("ben-recruiter", "Acme猎头"); err != nil {
		t.Fatalf("place: %v", err)
	}
	ses, seat := recruiterSession(t, c, ts, domain.RoleRecruiter)
	// What the CONVERSATION would compute, through the one shared rule.
	seat.TeamID = tools.GraphTeamFor(st, seat.SeatID)

	res := upload(t, c, ts.URL+"/api/sessions/"+ses+"/graph/imports", "contacts.csv", contactsCSV)
	res.Body.Close()

	sum, ok := graph.ImportSummary(seat, "")
	if !ok {
		t.Fatal("the conversation cannot see the file that was just uploaded")
	}
	if sum.File != "contacts.csv" {
		t.Errorf("a different file: %q", sum.File)
	}
}

// Importing a contact list is the employer workflow. An upload route that was
// not role-gated would be the way around the tools that are.
func TestTheUploadRouteIsForTheEmployerRoleOnly(t *testing.T) {
	ts, graph, _ := graphServer(t)
	c := signedIn(t, ts, "resident-person")
	ses, seat := recruiterSession(t, c, ts, domain.RoleResident)

	res := upload(t, c, ts.URL+"/api/sessions/"+ses+"/graph/imports", "contacts.csv", contactsCSV)
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("a resident staged a contact list: %d", res.StatusCode)
	}
	if _, ok := graph.ImportSummary(seat, ""); ok {
		t.Error("the refused upload was staged anyway")
	}
}

// A request with no file says which field to use, rather than failing vaguely.
func TestAnUploadWithNoFileSaysWhatToAttach(t *testing.T) {
	ts, _, _ := graphServer(t)
	c := signedIn(t, ts, "carl-recruiter")
	ses, _ := recruiterSession(t, c, ts, domain.RoleRecruiter)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/sessions/"+ses+"/graph/imports",
		strings.NewReader(""))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	res, err := c.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", res.StatusCode)
	}
	b, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(b), "file") {
		t.Errorf("the refusal does not say what to attach: %s", b)
	}
}

// Somebody else's session is not a way in.
func TestAnUploadToSomebodyElsesSessionIsRefused(t *testing.T) {
	ts, graph, _ := graphServer(t)
	owner := signedIn(t, ts, "dana-recruiter")
	ses, seat := recruiterSession(t, owner, ts, domain.RoleRecruiter)
	intruder := signedIn(t, ts, "eve-recruiter")

	res := upload(t, intruder, ts.URL+"/api/sessions/"+ses+"/graph/imports", "contacts.csv", contactsCSV)
	defer res.Body.Close()
	if res.StatusCode == http.StatusOK {
		t.Fatal("an upload landed in somebody else's session")
	}
	if _, ok := graph.ImportSummary(seat, ""); ok {
		t.Error("the intruder's file was staged into the owner's graph")
	}
}

// A deployment with no graph says so.
func TestUploadingWithNoGraphSaysSo(t *testing.T) {
	ts := newServer(t, llm.Script{}) // no graph wired
	c := signedIn(t, ts, "frank-recruiter")
	ses, _ := recruiterSession(t, c, ts, domain.RoleRecruiter)

	res := upload(t, c, ts.URL+"/api/sessions/"+ses+"/graph/imports", "contacts.csv", contactsCSV)
	defer res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d", res.StatusCode)
	}
	b, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(b), "GRAPH_UNAVAILABLE") {
		t.Errorf("the refusal does not say what is missing: %s", b)
	}
}

var _ = store.NormaliseUsername
