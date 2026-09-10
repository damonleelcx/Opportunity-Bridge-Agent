package main

// The one thing this repository has got wrong three times: building a capability
// and never wiring anything to it.
//
//	leadgraph.Handler — one caller, and it was a test.
//	cardFor           — seventeen tools, no case for any of them.
//	RunDaily          — no caller anywhere in the tree, so 离职提醒 never fired.
//
// So the producers are asserted here, in the binary that is supposed to have
// them, by reading this file's own source. It is a weak instrument — it proves a
// call is written, not that it runs — and it is exactly strong enough for the
// failure it exists to catch, which is nobody calling the thing at all.

import (
	"os"
	"strings"
	"testing"
)

func mainSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	// Comments are stripped: this file names the very calls it requires, and a
	// fence that its own explanation can satisfy measures nothing.
	var out strings.Builder
	for _, line := range strings.Split(string(b), "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.String()
}

func TestTheDailyGraphPassHasAProducer(t *testing.T) {
	src := mainSource(t)
	if !strings.Contains(src, "startGraphDaily(") {
		t.Fatal("nothing starts 猎源图谱's daily pass: 离职提醒 would never fire, " +
			"exactly as it did not before this existed")
	}
	if !strings.Contains(src, "tools.RunGraphDaily(") {
		t.Error("startGraphDaily never calls the pass")
	}
	// It must be behind the nil check: a deployment with no graph database is
	// supported, and a ticker calling into nothing every day is noise at best.
	if !strings.Contains(src, "if graph != nil {") {
		t.Error("the pass is started without checking there is a graph to run it against")
	}
	// And it must be stoppable, or a test binary or a restart leaks it.
	if !strings.Contains(src, "defer stopDaily()") {
		t.Error("the pass is never stopped")
	}
}
