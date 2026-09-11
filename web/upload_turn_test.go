package web_test

import (
	"strings"
	"testing"
)

// An upload is the person's turn, and a successful one reaches the conversation
// list without a reload.
//
// The server now writes an upload into the conversation, which is what makes an
// upload-only conversation listable. Two things in the page have to agree with
// that: the upload is drawn as the person's turn (so the live conversation reads
// the same as the reopened one), and the list is refreshed once the upload is
// staged (or the row the server just made listable stays missing until a
// reload).
// See docs/bugfix/2026-09-11-upload-only-conversation-was-hidden.md
func TestAnUploadIsThePersonsTurnAndReachesTheConversationList(t *testing.T) {
	src := asset(t, "app.js")
	i := strings.Index(src, "async function stageImport(")
	if i < 0 {
		t.Fatal("stageImport is gone; this fence no longer guards anything")
	}
	body := src[i:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		body = body[:end]
	}

	said := strings.Index(body, `userTurn(t("import.uploaded")`)
	reply := strings.Index(body, "agentTurn()")
	switch {
	case said < 0:
		t.Error("the upload is not drawn as the person's turn, so a reopened conversation shows a turn the live one never did")
	case reply >= 0 && said > reply:
		t.Error("the person's upload is drawn after the reply to it")
	}

	staged := strings.Index(body, "importPlanCard(")
	refresh := strings.Index(body, "refreshSessions()")
	switch {
	case refresh < 0:
		t.Error("a staged upload does not refresh the conversation list, so the conversation stays missing until a reload")
	case staged < 0 || refresh < staged:
		t.Error("the list is refreshed before the upload is staged, when there is nothing new to list")
	case !strings.HasPrefix(body[refresh+len("refreshSessions()"):], ".catch("):
		t.Error("a failed list refresh is not contained, so it would repaint a successful import as a failed one")
	}
}
