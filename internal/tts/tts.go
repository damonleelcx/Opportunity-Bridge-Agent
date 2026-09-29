// Package tts turns an answer into speech.
//
// ── Why this is a package and not three lines in the HTTP handler ────────────
//
// Read-aloud is a SIDE channel. By the time any of this runs the answer is
// already on the reader's screen, and nothing here may delay it, block it or
// fail it. Putting the vendor behind an interface is what keeps "the vendor is
// down, use the browser's own voice" a decision at the edge, instead of an
// error path threaded back through the agent loop.
//
// It is deliberately the same shape as internal/livesource: a Provider, a key
// read from the environment, and OFF by default with a startup log line saying
// so. A feature that silently degrades is worse than one that says it is not
// switched on — that lesson is already written down in docs/16-live-lookup.md
// and it applies here unchanged.
package tts

import (
	"context"
	"io"
)

// Speech is one utterance, STILL BEING RENDERED when it is handed back.
//
// Audio is a stream, not a byte slice, and that is the fix for read-aloud
// failing on every long answer. The free backbone renders at roughly a tenth of
// real time — a 420-character answer took 39s, and anything near the 3,000
// character cap ran past the old 90s client timeout and came back as a 502 after
// a minute and a half of silence. The first bytes, though, arrive in about a
// second. Passing them on as they come is what turns "wait for the whole answer
// to be rendered" into "start hearing it almost at once".
// See docs/bugfix/2026-09-29-read-aloud-waited-for-the-whole-answer.md
//
// The caller must Close Audio.
type Speech struct {
	Audio       io.ReadCloser
	ContentType string
}

// Provider renders text as speech.
//
// An error means the render FAILED before any audio existed and the caller
// should fall back. There is no "returned nothing" case: a provider has already
// seen the first byte of audio by the time it returns a Speech. A failure AFTER
// that surfaces as an error from reading Audio, when the reader has already been
// hearing it and falling back would read the answer from the top a second time.
type Provider interface {
	Name() string
	Speak(ctx context.Context, text string) (Speech, error)
}
