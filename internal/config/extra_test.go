package config

import (
	"slices"
	"strings"
	"testing"
)

const extraBase = "accounts: [{auth_file: claude-w.json}, {auth_file: claude-g.json}]\nbuckets: {main: [claude-opus-5-5], fable: [claude-fable-5-2]}\n"

const extraOne = `extra_models:
  - id: claude-extra-1
    display_name: Claude Extra 1
    accounts: [claude-g.json]
    context_length: 1000000
    max_completion_tokens: 128000
    thinking_levels: [low, high]
`

func TestExtraModelsAreParsed(t *testing.T) {
	cfg, err := Parse([]byte(extraBase + extraOne))
	if err != nil {
		t.Fatal(err)
	}
	want := ExtraModel{ID: "claude-extra-1", DisplayName: "Claude Extra 1", Accounts: []string{"claude-g.json"},
		ContextLength: 1000000, MaxCompletionTokens: 128000, ThinkingLevels: []string{"low", "high"}}
	if len(cfg.ExtraModels) != 1 || !equalExtra(cfg.ExtraModels[0], want) {
		t.Fatalf("extra models = %+v", cfg.ExtraModels)
	}
	if m, ok := cfg.ExtraModel("claude-extra-1"); !ok || m.ID != "claude-extra-1" {
		t.Fatalf("ExtraModel lookup = %+v, %v", m, ok)
	}
	if _, ok := cfg.ExtraModel("claude-opus-5-5"); ok {
		t.Fatal("a bucket model is not an extra model")
	}
}

func TestNoExtraModelsByDefault(t *testing.T) {
	cfg, err := Parse([]byte(extraBase))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.ExtraModels) != 0 {
		t.Fatalf("extra models = %+v", cfg.ExtraModels)
	}
}

func TestExtraModelsAreValidated(t *testing.T) {
	for _, c := range []struct{ name, yaml, want string }{
		{"no id", strings.Replace(extraOne, "id: claude-extra-1", `id: ""`, 1), "id"},
		{"an id with a suffix", strings.Replace(extraOne, "id: claude-extra-1", `id: "claude-extra-1(high)"`, 1), "id"},
		{"an id with a prefix", strings.Replace(extraOne, "id: claude-extra-1", "id: p/claude-extra-1", 1), "id"},
		{"a bucket model", strings.Replace(extraOne, "id: claude-extra-1", "id: claude-fable-5-2", 1), "bucket"},
		{"twice", extraOne + strings.TrimPrefix(extraOne, "extra_models:\n"), "twice"},
		{"no account", strings.Replace(extraOne, "accounts: [claude-g.json]", "accounts: []", 1), "account"},
		{"an account not configured", strings.Replace(extraOne, "accounts: [claude-g.json]", "accounts: [claude-x.json]", 1), "not configured"},
		{"an account twice", strings.Replace(extraOne, "accounts: [claude-g.json]", "accounts: [claude-g.json, claude-g.json]", 1), "twice"},
		{"no context length", strings.Replace(extraOne, "context_length: 1000000", "context_length: 0", 1), "context_length"},
		{"no output limit", strings.Replace(extraOne, "max_completion_tokens: 128000", "max_completion_tokens: 0", 1), "max_completion_tokens"},
		{"an empty thinking level", strings.Replace(extraOne, "thinking_levels: [low, high]", `thinking_levels: [low, ""]`, 1), "thinking_levels"},
	} {
		_, err := Parse([]byte(extraBase + c.yaml))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want one naming %q", c.name, err, c.want)
		}
	}
}

func TestExtraModelNamesTheModelARequestCarries(t *testing.T) {
	cfg, err := Parse([]byte(extraBase + extraOne))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude-extra-1", "claude-extra-1(high)", "claude-extra-1[1m]", "p/claude-extra-1", "p/claude-extra-1(8192)"} {
		if _, ok := cfg.ExtraModel(name); !ok {
			t.Errorf("%q is the extra model", name)
		}
	}
	for _, name := range []string{"claude-extra-10", "claude-extra", ""} {
		if _, ok := cfg.ExtraModel(name); ok {
			t.Errorf("%q is not the extra model", name)
		}
	}
}

func equalExtra(a, b ExtraModel) bool {
	return a.ID == b.ID && a.DisplayName == b.DisplayName && slices.Equal(a.Accounts, b.Accounts) &&
		a.ContextLength == b.ContextLength && a.MaxCompletionTokens == b.MaxCompletionTokens &&
		slices.Equal(a.ThinkingLevels, b.ThinkingLevels)
}
