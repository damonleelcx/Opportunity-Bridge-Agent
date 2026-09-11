package httpapi_test

import (
	"regexp"
	"strings"
	"testing"
	"unicode"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/agent"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/config"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/web"
)

// i18nPair reads one key's Chinese and English values out of the shipped
// string table. The Chinese table comes first in the file; that is checked
// rather than assumed, so a reordered file fails here instead of comparing the
// wrong languages and passing.
func i18nPair(t *testing.T, src, key string) (zh, en string) {
	t.Helper()
	re := regexp.MustCompile(`"` + regexp.QuoteMeta(key) + `": "([^"]*)"`)
	m := re.FindAllStringSubmatch(src, -1)
	if len(m) != 2 {
		t.Fatalf("%s appears %d times in i18n.js, want once per language (2)", key, len(m))
	}
	hasHan := func(s string) bool {
		for _, r := range s {
			if unicode.Is(unicode.Han, r) {
				return true
			}
		}
		return false
	}
	zh, en = m[0][1], m[1][1]
	if !hasHan(zh) || hasHan(en) {
		t.Fatalf("%s: expected the Chinese value first and the English second, got %q then %q", key, zh, en)
	}
	return zh, en
}

// A live upload is drawn from i18n.js; the same upload, reopened, is drawn from
// the turns the server kept. If the two tables drift, a conversation says one
// thing while it happens and another when it is read back.
// See docs/bugfix/2026-09-11-upload-only-conversation-was-hidden.md
func TestTheUploadTurnsSayWhatTheInterfaceSays(t *testing.T) {
	b, err := web.Files.ReadFile("static/i18n.js")
	if err != nil {
		t.Fatalf("read i18n.js: %v", err)
	}
	src := string(b)
	zhUploaded, enUploaded := i18nPair(t, src, "import.uploaded")
	zhStaged, enStaged := i18nPair(t, src, "import.staged")

	const file = "名单.csv"
	for _, c := range []struct{ locale, uploaded, staged string }{
		{"zh-CN", zhUploaded, zhStaged},
		{"en", enUploaded, enStaged},
	} {
		said, reply := agent.UploadTurns(config.Config{}, &store.Session{Locale: c.locale}, file)
		if want := strings.ReplaceAll(c.uploaded, "{file}", file); said != want {
			t.Errorf("%s upload turn: server %q, interface %q", c.locale, said, want)
		}
		if reply != c.staged {
			t.Errorf("%s reply turn: server %q, interface %q", c.locale, reply, c.staged)
		}
	}
}
