package httpapi_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/config"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/httpapi"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/obs"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/store"
)

// The request line and the run's events can be joined in the server log.
//
// The production line for a message turn was
//   msg="http request" method=POST path=/api/sessions/ses_0105/messages status=200
// with no request id, no run id, and no other line about the turn at all.
// See docs/bugfix/2026-09-11-agent-events-never-reached-the-logs.md

// syncBuffer: the server writes log lines from its own goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitForLine polls for a line containing every needle. The middleware writes
// its line after the handler returns, which can be after the client has read
// the last byte of the stream.
func waitForLine(t *testing.T, b *syncBuffer, needles ...string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, l := range strings.Split(b.String(), "\n") {
			ok := true
			for _, n := range needles {
				if !strings.Contains(l, n) {
					ok = false
					break
				}
			}
			if ok {
				return l
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return ""
}

func TestAMessageTurnCanBeJoinedToItsRequestInTheLog(t *testing.T) {
	logs := &syncBuffer{}
	// Two loggers on one buffer, on purpose. The Server gets a PLAIN handler, so
	// request_id and run_id on the request line can only come from the
	// middleware's own attrs; with a ContextHandler there too they would also
	// arrive through ctx, and deleting the middleware's attrs would leave this
	// fence green. The agent gets the ContextHandler, which is what joins its
	// tool lines to the request. ContextHandler itself is fenced in internal/obs.
	logger := slog.New(obs.NewContextHandler(slog.NewTextHandler(logs, nil)))
	plain := slog.New(slog.NewTextHandler(logs, nil))
	srv := newServerTweaking(t, llm.Script{Turns: []llm.ScriptedTurn{
		{ToolCalls: []struct {
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}{{Name: "opportunity_search", Input: json.RawMessage(`{"query":"养老 护理 白班","city":"成都"}`)}}},
		{ToolCalls: []struct {
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}{{Name: "case_task_create", Input: json.RawMessage(`{"domain":"employment","title":"Ask the Qingyang day centre about the day-shift care post (job-002)","owner":"resident","linked_ref":"job-002","channel_phone":"028-5550-2244","channel_window":"12 Shudu Ave, Qingyang","channel_hours":"Mon-Fri 09:00-17:00"}`)}}},
		{Text: "job-002 fits. Call 028-5550-2244, or the Qingyang window at 12 Shudu Ave, Mon-Fri 09:00-17:00."},
	}}, func(*config.Config) {}, func(s *httpapi.Server) {
		s.Log = plain
		s.Agent.Log = logger
	})
	c := signedIn(t, srv, "joiner")

	res := postAs(t, c, srv.URL+"/api/sessions", map[string]string{"role": "resident", "locale": "en"})
	var ses store.Session
	if err := json.NewDecoder(res.Body).Decode(&ses); err != nil {
		t.Fatalf("session: %v", err)
	}

	body, _ := json.Marshal(map[string]string{"message": "成都的养老护理岗", "intent": "individual_pathway"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/sessions/"+ses.ID+"/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// A client-chosen id must be ignored: it would be written into every line.
	req.Header.Set("X-Request-Id", "forged-id-from-the-client")
	res, err := c.Do(req)
	if err != nil {
		t.Fatalf("message: %v", err)
	}
	var runID string
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimPrefix(sc.Text(), "data: ")
		var ev struct {
			Kind  string `json:"kind"`
			Final *struct {
				RunID string `json:"run_id"`
			} `json:"final"`
		}
		if json.Unmarshal([]byte(line), &ev) == nil && ev.Kind == "final" && ev.Final != nil {
			runID = ev.Final.RunID
		}
	}
	res.Body.Close()
	if runID == "" {
		t.Fatal("the stream carried no final event; this fence proves nothing")
	}

	id := res.Header.Get("X-Request-Id")
	if !regexp.MustCompile(`^req_[0-9a-f]{16}$`).MatchString(id) {
		t.Fatalf("X-Request-Id = %q, want a minted id", id)
	}
	httpLine := waitForLine(t, logs, "event.name=http.request.served", "path=/api/sessions/"+ses.ID+"/messages")
	if httpLine == "" {
		t.Fatalf("no request line for the message:\n%s", logs.String())
	}
	for _, want := range []string{"request_id=" + id, "run_id=" + runID} {
		if strings.Count(httpLine, want) != 1 {
			t.Errorf("the request line does not carry %q exactly once: %s", want, httpLine)
		}
	}
	if waitForLine(t, logs, "event.name=agent.tool.succeeded", "tool=opportunity_search",
		"run_id="+runID, "request_id="+id) == "" {
		t.Errorf("the tool line cannot be joined to its request:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), "forged-id-from-the-client") {
		t.Error("a client-chosen request id reached the log")
	}
}
