package main

import (
	"log/slog"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/config"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/leadgraph"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/vision"
)

// visionReader is what reads a screenshot for the lead-graph import, or nil when
// this run cannot - and nil is reported, so the upload says "not available here"
// instead of failing on the first picture.
//
// Only the qwen backend has a vision model, and config refuses to start with one
// that is not proven to read images (llm.QwenVisionModels). The scripted backend
// replays text and counts no input tokens, so a reading through it would be
// refused as blind every time; saying the feature is off is the honest answer.
func visionReader(cfg config.Config, client llm.Client, graph *leadgraph.Store, log *slog.Logger) *vision.Reader {
	if graph == nil {
		return nil
	}
	if cfg.Backend != config.BackendQwen {
		log.Info("screenshot import is off: this backend has no model that reads pictures",
			"code", "SCREENSHOT_IMPORT_DISABLED", "backend", string(cfg.Backend))
		return nil
	}
	log.Info("screenshot import is on", "code", "SCREENSHOT_IMPORT_ENABLED", "model", cfg.VisionModel)
	return &vision.Reader{LLM: client, Model: cfg.VisionModel}
}
