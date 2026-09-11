package httpapi

import (
	"context"
	"encoding/json"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/agent"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/leadgraph"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
)

// recordUpload writes a staged upload into the conversation it was made in, as
// the person's turn and the plan it produced.
//
// WHY THIS EXISTS
//
//	The conversation list shows a conversation only once the person has done
//	something in it; a session with no user turn is an empty shell from a page
//	load and is hidden (docs/bugfix/2026-08-28-session-list.md). Uploads bypass
//	the agent, which until now was the only thing that wrote a turn, so a
//	recruiter who only uploaded a contact list or a screenshot had that
//	conversation vanish from the list, and reopening it showed an empty page.
//
// WHY A USER TURN, AND NOT A CHANGE TO THE LIST RULE
//
//	Showing sessions with no user turn would bring every empty shell back and
//	reverse TestSessionSummariesIgnoresAssistantOnlySessions. An upload IS
//	something the person did, so recording it as their turn is true, and every
//	existing rule about the list keeps holding.
//
// WHY A FAILURE HERE DOES NOT FAIL THE UPLOAD
//
//	The file is already staged when this runs. Losing the history entry costs
//	the list row; failing the request would cost the import itself, which is
//	the thing the person came to do. So it is logged, loudly, and the upload
//	stands.
//
// See docs/bugfix/2026-09-11-upload-only-conversation-was-hidden.md
func (s *Server) recordUpload(ctx context.Context, sessionID, fileName string, sum leadgraph.ImportSummary) {
	card, err := json.Marshal(sum)
	if err != nil {
		s.Log.WarnContext(ctx, "upload staged but not recorded in the conversation",
			"code", "UPLOAD_TURN_NOT_RECORDED", "session_id", sessionID, "error", err.Error())
		return
	}
	now := time.Now().UTC()
	err = s.Store.MutateSession(sessionID, func(ses *store.Session) error {
		said, reply := agent.UploadTurns(s.Cfg, ses, fileName)
		ses.History = append(ses.History,
			store.Turn{Role: "user", Text: said, At: now},
			store.Turn{Role: "assistant", Text: reply, At: now,
				Cards: []store.TurnCard{{Tool: leadgraph.ToolImportSummary, Result: card}}},
		)
		return nil
	})
	if err != nil {
		s.Log.WarnContext(ctx, "upload staged but not recorded in the conversation",
			"code", "UPLOAD_TURN_NOT_RECORDED", "session_id", sessionID, "error", err.Error())
	}
}
