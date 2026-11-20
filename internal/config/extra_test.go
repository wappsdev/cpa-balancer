package config

import (
	"slices"
	"strings"
	"testing"
)

const extraBase = "accounts: [{auth_file: claude-w.json}, {auth_file: claude-g.json}]\nbuckets: {main: [claude-opus-5-5], fable: [claude-fable-5-2, claude-mythos-9-5]}\n"

const extraOne = `extra_models:
  - id: claude-mythos-9-1
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
	want := ExtraModel{ID: "claude-mythos-9-1", DisplayName: "Claude Extra 1", Accounts: []string{"claude-g.json"},
		ContextLength: 1000000, MaxCompletionTokens: 128000, ThinkingLevels: []string{"low", "high"}}
	if len(cfg.ExtraModels) != 1 || !equalExtra(cfg.ExtraModels[0], want) {
		t.Fatalf("extra models = %+v", cfg.ExtraModels)
	}
	if m, ok := cfg.ExtraModel("claude-mythos-9-1"); !ok || m.ID != "claude-mythos-9-1" {
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
		{"no id", strings.Replace(extraOne, "id: claude-mythos-9-1", `id: ""`, 1), "id"},
		{"an id with a suffix", strings.Replace(extraOne, "id: claude-mythos-9-1", `id: "claude-mythos-9-1(high)"`, 1), "id"},
		{"an id with a prefix", strings.Replace(extraOne, "id: claude-mythos-9-1", "id: p/claude-mythos-9-1", 1), "id"},
		{"a built-in claude model", strings.Replace(extraOne, "id: claude-mythos-9-1", "id: claude-opus-5-5", 1), "claude-mythos-"},
		{"a model of the catalog's newer list", strings.Replace(extraOne, "id: claude-mythos-9-1", "id: claude-fable-5-2", 1), "claude-mythos-"},
		{"a name of another family", strings.Replace(extraOne, "id: claude-mythos-9-1", "id: claude-new-1", 1), "claude-mythos-"},
		{"upper case", strings.Replace(extraOne, "id: claude-mythos-9-1", "id: Claude-Mythos-9-1", 1), "claude-mythos-"},
		{"the bare prefix", strings.Replace(extraOne, "id: claude-mythos-9-1", "id: claude-mythos-", 1), "claude-mythos-"},
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
	for _, name := range []string{"claude-mythos-9-1", "claude-mythos-9-1(high)", "claude-mythos-9-1[1m]", "p/claude-mythos-9-1", "p/claude-mythos-9-1(8192)"} {
		if _, ok := cfg.ExtraModel(name); !ok {
			t.Errorf("%q is the extra model", name)
		}
	}
	// CPA trims the name, cuts a "(...)" suffix at the last "(", trims again, and matches the
	// provider and the auth's models without regard to case. Each of these routes to the model.
	for _, name := range []string{" claude-mythos-9-1", "claude-mythos-9-1 ", "\tclaude-mythos-9-1\n", "Claude-Mythos-9-1", "CLAUDE-MYTHOS-9-1(high)",
		"claude-mythos-9-1 (high)", " claude-mythos-9-1 (high) ", "P/Claude-Mythos-9-1 (8192)"} {
		if _, ok := cfg.ExtraModel(name); !ok {
			t.Errorf("%q routes to the extra model and must be pinned", name)
		}
	}
	for _, name := range []string{"claude-mythos-9-10", "claude-mythos-9", "", "claude-mythos-9-1x(high)"} {
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

// An extra model may be listed in the bucket whose quota it spends: the bucket names the quota, the
// extra model names the accounts.
func TestAnExtraModelMayBeInTheFableBucket(t *testing.T) {
	cfg, err := Parse([]byte(extraBase + strings.Replace(extraOne, "id: claude-mythos-9-1", "id: claude-mythos-9-5", 1)))
	if err != nil {
		t.Fatalf("an extra model listed in buckets.fable is valid, got %v", err)
	}
	if m, ok := cfg.ExtraModel("claude-mythos-9-5"); !ok || !slices.Equal(m.Accounts, []string{"claude-g.json"}) {
		t.Fatalf("ExtraModel = %+v, %v", m, ok)
	}
}

// A Mythos model spends the Fable/Mythos share, so listing it in the main bucket would put its use
// in two quotas at once; the config refuses it and names the bucket to use.
func TestAnExtraModelInTheMainBucketIsRefused(t *testing.T) {
	cfg := strings.Replace(extraBase, "main: [claude-opus-5-5]", "main: [claude-opus-5-5, claude-mythos-9-1]", 1)
	_, err := Parse([]byte(cfg + extraOne))
	if err == nil || !strings.Contains(err.Error(), "main bucket") || !strings.Contains(err.Error(), "buckets.fable") {
		t.Fatalf("err = %v, want one naming the main bucket and buckets.fable", err)
	}
}
