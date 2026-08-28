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

import "context"

// Speech is one rendered utterance, ready to hand to the browser.
type Speech struct {
	Audio       []byte
	ContentType string
}

// Provider renders text as speech.
//
// An error means the render FAILED and the caller should fall back. There is no
// "returned nothing" case: unlike a search, a request to speak either produces
// audio or does not.
type Provider interface {
	Name() string
	Speak(ctx context.Context, text string) (Speech, error)
}
