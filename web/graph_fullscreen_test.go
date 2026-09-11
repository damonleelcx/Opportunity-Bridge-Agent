package web_test

import (
	"regexp"
	"strings"
	"testing"
)

// graphFullscreenFunc returns one top-level function's source out of app.js.
func graphFullscreenFunc(t *testing.T, src, signature string) string {
	t.Helper()
	i := strings.Index(src, signature)
	if i < 0 {
		t.Fatalf("%s is gone; this fence no longer guards anything", signature)
	}
	body := src[i:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		body = body[:end]
	}
	return body
}

// The relationship graph card goes full screen in place.
//
// In place, because the standalone graph screen was removed (拍板 2026-09-10):
// a control that opened the graph in a new tab or window would be that screen
// back under another name. And two legs, because iPhone Safari cannot put an
// element in full screen: the class has to cover the screen on its own, with
// the Fullscreen API on top where the browser has one. A version with only the
// API leg is a button that does nothing on an iPhone; a version that forgets
// fullscreenchange leaves the card stretched over the page after Esc.
func TestTheGraphCardGoesFullScreenInPlace(t *testing.T) {
	src := asset(t, "app.js")

	card := graphFullscreenFunc(t, src, "function graphPictureCard(")
	if !strings.Contains(card, `t("graph.fullscreen")`) {
		t.Error("the graph card has no full-screen control labelled from the string table")
	}
	if !strings.Contains(card, "toggleGraphFullscreen(card)") {
		t.Error("the full-screen control is not wired to the card")
	}

	toggle := graphFullscreenFunc(t, src, "async function toggleGraphFullscreen(")
	if !strings.Contains(toggle, "requestFullscreen") || !strings.Contains(toggle, "exitFullscreen") {
		t.Error("real full screen is not entered and left where the browser offers it")
	}
	if regexp.MustCompile(`window\.open\(|target="_blank"|location\.(href|assign)`).MatchString(card + toggle) {
		t.Error("full screen navigates away from the conversation: that is the removed standalone screen again")
	}

	set := graphFullscreenFunc(t, src, "function setGraphExpanded(")
	if !strings.Contains(set, `classList.toggle("is-expanded", on)`) {
		t.Error("the in-place class is not applied, so a browser without element full screen (iPhone Safari) gets nothing")
	}
	if !strings.Contains(set, "aria-pressed") || !strings.Contains(set, `"graph.exitFullscreen"`) {
		t.Error("the control does not say whether full screen is on, or how to leave it")
	}

	if !regexp.MustCompile(`"fullscreenchange"[\s\S]{0,120}collapseExpandedGraphs\(\)`).MatchString(src) {
		t.Error("leaving real full screen with the browser's Esc does not collapse the card")
	}
	if !regexp.MustCompile(`"Escape"\) collapseExpandedGraphs\(\)`).MatchString(src) {
		t.Error("where there is no real full screen, Esc is not a way out")
	}

	css := asset(t, "styles.css")
	if !regexp.MustCompile(`\.gcard\.is-expanded\s*\{[^}]*position:\s*fixed`).MatchString(css) {
		t.Error("the expanded card is not fixed over the screen")
	}
	if !regexp.MustCompile(`\.gcard\.is-expanded \.gstage\s*\{[^}]*flex:`).MatchString(css) {
		t.Error("the expanded picture keeps its 480px height instead of taking the screen")
	}

	icons := asset(t, "icons.js")
	for _, name := range []string{"expand", "shrink"} {
		if !strings.Contains(icons, "\n  "+name+": '") {
			t.Errorf("icons.js has no %q glyph, so the control falls back to the chat bubble", name)
		}
	}
}
