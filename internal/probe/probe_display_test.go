package probe

import "testing"

func TestDisplayNameUsesTheSoldPlanNames(t *testing.T) {
	for id, want := range map[string]string{"max_20x": "Ultra", "max_5x": "Max", "pro": "Pro", "other": ""} {
		if got := DisplayName(id); got != want {
			t.Fatalf("DisplayName(%q) = %q, want %q", id, got, want)
		}
	}
}
