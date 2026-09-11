package config_test

import (
	"strings"
	"testing"

	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/config"
	"github.com/damonleelcx/Opportunity-Bridge-Agent/internal/llm"
)

// A model that cannot see answers an image request with HTTP 200 and an invented
// description. The vision model is therefore held to the list of ids proven to
// read images, and anything else is refused at startup rather than warned about.
// See internal/vision and llm.QwenVisionModels.

func TestVisionModelDefaultsToTheProvenOne(t *testing.T) {
	t.Setenv("OBA_BACKEND", "qwen")
	t.Setenv("QWEN_API_KEY", "qw-test")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.VisionModel != llm.QwenVisionModel {
		t.Errorf("vision model = %q, want %q", cfg.VisionModel, llm.QwenVisionModel)
	}
}

func TestAVisionModelNotProvenToSeeIsRefused(t *testing.T) {
	// glm-5.2 and deepseek-v4-pro answered 200 and invented; qwen3.7-max refused
	// images outright. All three on the 2026-09-11 probe.
	for _, model := range []string{"glm-5.2", "deepseek-v4-pro", "qwen3.7-max"} {
		t.Setenv("OBA_BACKEND", "qwen")
		t.Setenv("QWEN_API_KEY", "qw-test")
		t.Setenv("OBA_VISION_MODEL", model)
		_, err := config.Load()
		if err == nil {
			t.Errorf("OBA_VISION_MODEL=%s was accepted", model)
			continue
		}
		for _, want := range []string{model, "OBA_VISION_MODEL", "make vision-probe", llm.QwenVisionModel} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal for %s does not say %q: %v", model, want, err)
			}
		}
	}
}

func TestEveryProvenVisionModelIsAccepted(t *testing.T) {
	for _, model := range llm.QwenVisionModels {
		t.Setenv("OBA_BACKEND", "qwen")
		t.Setenv("QWEN_API_KEY", "qw-test")
		t.Setenv("OBA_VISION_MODEL", model)
		if _, err := config.Load(); err != nil {
			t.Errorf("proven vision model %s was refused: %v", model, err)
		}
	}
}
