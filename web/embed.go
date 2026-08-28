// Package web carries the conversational interface, embedded in the binary.
//
// Embedding is a deployment decision: one file to copy, no asset path to get
// wrong, no build step between a checkout and a running service. It costs
// recompiling to change the interface, which for a service like this is the
// right trade.
package web

import "embed"

//go:embed static
var Files embed.FS
