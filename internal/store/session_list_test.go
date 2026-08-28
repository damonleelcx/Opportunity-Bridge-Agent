package store_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
)

// The conversation list in the sidebar used to show one row per page load,
// labelled with a raw internal id, ordered by creation. These are the fences for
// the three rules that replaced that behaviour.
//
// See docs/bugfix/2026-08-28-session-list.md.

func newStore(t *testing.T) *store.Store {
	t.Helper()
	return store.New("", slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func say(t *testing.T, st *store.Store, id, role, text string) {
	t.Helper()
	if err := st.MutateSession(id, func(s *store.Session) error {
		s.History = append(s.History, store.Turn{Role: role, Text: text, At: time.Now().UTC()})
		return nil
	}); err != nil {
		t.Fatalf("mutate %s: %v", id, err)
	}
}

// A session nobody has spoken in is a shell the client minted on page load. It
// is not a conversation and must not appear in the picker.
func TestSessionSummariesHidesSessionsWithNoUserTurn(t *testing.T) {
	st := newStore(t)
	shell := st.CreateSession("resident", "", "zh-CN")
	real := st.CreateSession("resident", "", "zh-CN")
	say(t, st, real.ID, "user", "我在深圳，跑过外卖")

	got := st.SessionSummaries()
	if len(got) != 1 {
		t.Fatalf("want 1 conversation listed, got %d: %+v", len(got), got)
	}
	if got[0].ID != real.ID {
		t.Fatalf("listed %s, want the one with a user turn (%s)", got[0].ID, real.ID)
	}
	if _, ok := st.Session(shell.ID); !ok {
		t.Fatalf("the shell must still be readable by id; hiding is not deleting")
	}
}

// An assistant turn alone (a greeting, a system message) is still nobody having
// spoken, so it does not promote a shell into the list.
func TestSessionSummariesIgnoresAssistantOnlySessions(t *testing.T) {
	st := newStore(t)
	ses := st.CreateSession("resident", "", "zh-CN")
	say(t, st, ses.ID, "assistant", "我是阿桥。")
	if got := st.SessionSummaries(); len(got) != 0 {
		t.Fatalf("assistant-only session was listed: %+v", got)
	}
}

// Ordering is by last activity. Sorting on creation buried a conversation you
// had just carried on underneath newer, idler ones.
func TestSessionSummariesOrdersByLastActivity(t *testing.T) {
	st := newStore(t)
	older := st.CreateSession("resident", "", "zh-CN")
	say(t, st, older.ID, "user", "第一个对话")
	newer := st.CreateSession("resident", "", "zh-CN")
	say(t, st, newer.ID, "user", "第二个对话")

	if got := st.SessionSummaries(); got[0].ID != newer.ID {
		t.Fatalf("before continuing: want %s first, got %s", newer.ID, got[0].ID)
	}

	time.Sleep(2 * time.Millisecond)
	say(t, st, older.ID, "user", "我又回来了")

	got := st.SessionSummaries()
	if got[0].ID != older.ID {
		t.Fatalf("after continuing %s it must sort first, got %s", older.ID, got[0].ID)
	}
	if got[0].Turns != 2 {
		t.Fatalf("user turns = %d, want 2", got[0].Turns)
	}
}

// The title is what the person opened with, on one line.
func TestSessionSummaryTitleIsTheFirstUserTurn(t *testing.T) {
	st := newStore(t)
	ses := st.CreateSession("resident", "", "zh-CN")
	say(t, st, ses.ID, "user", "  我在深圳，跑过外卖，\n\n想找个稳定点的活  ")
	say(t, st, ses.ID, "assistant", "好的。")
	say(t, st, ses.ID, "user", "还有别的吗")

	got := st.SessionSummaries()[0]
	want := "我在深圳，跑过外卖， 想找个稳定点的活"
	if got.Title != want {
		t.Fatalf("title = %q, want %q", got.Title, want)
	}
}

// Cutting a long title by byte would split a Chinese character and render a
// replacement glyph mid-word.
func TestSessionSummaryTitleClipsByRuneNotByte(t *testing.T) {
	st := newStore(t)
	ses := st.CreateSession("resident", "", "zh-CN")
	long := strings.Repeat("我在深圳跑外卖", 40) // 280 runes, 840 bytes
	say(t, st, ses.ID, "user", long)

	title := st.SessionSummaries()[0].Title
	if !strings.HasSuffix(title, "…") {
		t.Fatalf("clipped title should end in an ellipsis, got %q", title)
	}
	if n := len([]rune(title)); n != 81 {
		t.Fatalf("title is %d runes, want 80 plus the ellipsis", n)
	}
	if strings.ContainsRune(title, '�') {
		t.Fatalf("title contains a broken character: %q", title)
	}
}

// A conversation opened with only whitespace is still a conversation; it gets an
// empty title so the client can label it in the reader's language, rather than
// falling back to a raw internal id that means nothing to anyone.
func TestSessionSummaryTitleEmptyRatherThanRawID(t *testing.T) {
	st := newStore(t)
	ses := st.CreateSession("resident", "", "zh-CN")
	say(t, st, ses.ID, "user", "   \n  ")

	got := st.SessionSummaries()
	if len(got) != 1 {
		t.Fatalf("want the session listed, got %d", len(got))
	}
	if got[0].Title != "" {
		t.Fatalf("title = %q, want empty so the client supplies the label", got[0].Title)
	}
}

// The picker refetches after every turn. It must not carry transcripts.
func TestSessionSummaryCarriesNoTranscript(t *testing.T) {
	st := newStore(t)
	ses := st.CreateSession("resident", "", "zh-CN")
	say(t, st, ses.ID, "user", "secret-needle")

	b, err := json.Marshal(st.SessionSummaries())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), `"history"`) {
		t.Fatalf("summary payload carries history: %s", b)
	}
	var rows []map[string]any
	if err := json.Unmarshal(b, &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := rows[0]["task"]; ok {
		t.Fatalf("summary payload carries task state: %s", b)
	}
}
