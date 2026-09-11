package agent

import (
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/config"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
)

// UploadTurns renders the two turns a staged upload writes into a conversation:
// what the person did, and what was said back.
//
// They live here, beside every other system-authored sentence, and in the SAME
// language a turn in this conversation would be answered in — replyLanguage is
// the one rule for that, so an upload cannot pick a language a typed turn would
// not.
// See docs/bugfix/2026-09-11-upload-only-conversation-was-hidden.md
func UploadTurns(cfg config.Config, ses *store.Session, fileName string) (said, reply string) {
	lang := replyLanguage(cfg, ses)
	return sysMsg(lang, msgImportUploaded, fileName), sysMsg(lang, msgImportStaged)
}
