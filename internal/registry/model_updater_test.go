package registry

import (
	"strings"
	"testing"
)

func TestValidateModelsCatalogRequiresKiroSection(t *testing.T) {
	model := func(id string) []*ModelInfo {
		return []*ModelInfo{{ID: id}}
	}
	catalog := &staticModelsJSON{
		Claude:      model("claude-test"),
		Gemini:      model("gemini-test"),
		Vertex:      model("vertex-test"),
		GeminiCLI:   model("gemini-cli-test"),
		AIStudio:    model("aistudio-test"),
		CodexFree:   model("codex-free-test"),
		CodexTeam:   model("codex-team-test"),
		CodexPlus:   model("codex-plus-test"),
		CodexPro:    model("codex-pro-test"),
		Kimi:        model("kimi-test"),
		Antigravity: model("antigravity-test"),
	}

	err := validateModelsCatalog(catalog)
	if err == nil {
		t.Fatal("expected missing kiro section to be rejected")
	}
	if !strings.Contains(err.Error(), "kiro section is empty") {
		t.Fatalf("error = %q, want missing kiro section", err)
	}
}
