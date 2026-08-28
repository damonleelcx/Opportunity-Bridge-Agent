package main

import "github.com/damonleelcx/Opportunity-Bridge-Agent/internal/tools"

// toolsRegistry is separated so a deployment that wants a narrower action
// surface has one obvious place to change, rather than editing the loop.
func toolsRegistry() *tools.Registry { return tools.Default() }
