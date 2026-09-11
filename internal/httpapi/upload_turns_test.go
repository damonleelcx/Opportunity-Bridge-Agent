package httpapi_test

// Fences over what a staged upload leaves in the conversation.
//
// The conversation list shows a conversation only once it holds something the
// person did (docs/bugfix/2026-08-28-session-list.md hides the empty shells a
// page load creates). An upload used to leave nothing at all, so a recruiter who
// only uploaded a contact list or a mind-map screenshot saw that conversation
// vanish from the list, and reopening it showed an empty page.
// See docs/bugfix/2026-09-11-upload-only-conversation-was-hidden.md

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/domain"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
)

func listedSessions(t *testing.T, c *http.Client, ts *httptest.Server) []store.SessionSummary {
	t.Helper()
	res, err := c.Get(ts.URL + "/api/sessions")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defer res.Body.Close()
	var out []store.SessionSummary
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	return out
}

func listed(list []store.SessionSummary, id string) (store.SessionSummary, bool) {
	for _, s := range list {
		if s.ID == id {
			return s, true
		}
	}
	return store.SessionSummary{}, false
}

func reopened(t *testing.T, c *http.Client, ts *httptest.Server, id string) store.Session {
	t.Helper()
	res, err := c.Get(ts.URL + "/api/sessions/" + id)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer res.Body.Close()
	var out struct {
		Session store.Session `json:"session"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	return out.Session
}

func TestAnUploadOnlyConversationIsListed(t *testing.T) {
	ts, _, _ := graphServer(t)
	c := signedIn(t, ts, "amy-recruiter")
	ses, _ := recruiterSession(t, c, ts, domain.RoleRecruiter)

	res := upload(t, c, ts.URL+"/api/sessions/"+ses+"/graph/imports", "contacts.csv", contactsCSV)
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("upload: %d %s", res.StatusCode, b)
	}
	res.Body.Close()

	row, ok := listed(listedSessions(t, c, ts), ses)
	if !ok {
		t.Fatal("a conversation whose only activity was an upload is missing from the conversation list")
	}
	if !strings.Contains(row.Title, "contacts.csv") {
		t.Errorf("the row is titled %q; it should name the uploaded file so the reader can find it", row.Title)
	}
}

func TestAnUploadOnlyScreenshotConversationIsListed(t *testing.T) {
	ts, _, _ := screenshotServer(t, 600, 1871)
	c := signedIn(t, ts, "amy-recruiter")
	ses, _ := recruiterSession(t, c, ts, domain.RoleRecruiter)

	res := uploadPicture(t, c, ts.URL+"/api/sessions/"+ses+"/graph/imports", "导图.png", fixture(t, "mindmap.png"), true)
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("upload: %d %s", res.StatusCode, b)
	}
	res.Body.Close()

	if _, ok := listed(listedSessions(t, c, ts), ses); !ok {
		t.Fatal("a conversation whose only activity was a screenshot upload is missing from the conversation list")
	}
}

// Listing it is half the fix. Reopening it has to show what the reader saw:
// the upload, and the plan it produced.
func TestAReopenedUploadRedrawsItsPlan(t *testing.T) {
	ts, _, _ := graphServer(t)
	c := signedIn(t, ts, "amy-recruiter")
	ses, _ := recruiterSession(t, c, ts, domain.RoleRecruiter)

	res := upload(t, c, ts.URL+"/api/sessions/"+ses+"/graph/imports", "contacts.csv", contactsCSV)
	res.Body.Close()

	h := reopened(t, c, ts, ses).History
	if len(h) != 2 {
		t.Fatalf("history has %d turns, want the upload and its plan (2)", len(h))
	}
	if h[0].Role != "user" || !strings.Contains(h[0].Text, "contacts.csv") {
		t.Errorf("first turn = %s %q, want the person's upload naming the file", h[0].Role, h[0].Text)
	}
	if h[1].Role != "assistant" || strings.TrimSpace(h[1].Text) == "" {
		t.Errorf("second turn = %s %q, want the assistant saying the file was read", h[1].Role, h[1].Text)
	}
	if len(h[1].Cards) != 1 || h[1].Cards[0].Tool != "import_summary" {
		t.Fatalf("cards = %+v, want one import_summary card, which the interface redraws as the plan", h[1].Cards)
	}
	var plan struct {
		File   string         `json:"file"`
		Counts map[string]int `json:"counts"`
	}
	if err := json.Unmarshal(h[1].Cards[0].Result, &plan); err != nil {
		t.Fatalf("card result: %v", err)
	}
	if plan.File != "contacts.csv" || plan.Counts["create"] != 2 {
		t.Errorf("the kept plan = %+v, want contacts.csv creating 2", plan)
	}
}

// A refused upload did nothing, so it must leave nothing: the conversation stays
// an empty shell and stays hidden, as every other empty shell is.
func TestAFailedUploadLeavesTheConversationHidden(t *testing.T) {
	ts, _, _ := graphServer(t)
	c := signedIn(t, ts, "amy-recruiter")
	ses, _ := recruiterSession(t, c, ts, domain.RoleRecruiter)

	res := upload(t, c, ts.URL+"/api/sessions/"+ses+"/graph/imports", "empty.csv", "")
	if res.StatusCode != http.StatusBadRequest {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("an empty file was accepted (%d %s); this fence needs an upload that is refused", res.StatusCode, b)
	}
	res.Body.Close()

	if _, ok := listed(listedSessions(t, c, ts), ses); ok {
		t.Error("a refused upload promoted an empty conversation into the list")
	}
	if n := len(reopened(t, c, ts, ses).History); n != 0 {
		t.Errorf("a refused upload wrote %d turns", n)
	}
}
