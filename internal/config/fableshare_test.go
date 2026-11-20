package config

import (
	"slices"
	"strings"
	"testing"
)

func TestTheFableWarningDefaultsToEightyPercent(t *testing.T) {
	cfg, err := Parse([]byte(minimal))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Thresholds.FableWarn != 80 {
		t.Fatalf("fable_warn = %v, want 80", cfg.Thresholds.FableWarn)
	}
}

func TestTheFableWarningIsSetAndChecked(t *testing.T) {
	cfg, err := Parse([]byte(minimal + "thresholds: {fable_warn: 70}\n"))
	if err != nil || cfg.Thresholds.FableWarn != 70 {
		t.Fatalf("fable_warn = %v, %v", cfg.Thresholds.FableWarn, err)
	}
	for _, v := range []string{"0", "-1", "101"} {
		if _, err := Parse([]byte(minimal + "thresholds: {fable_warn: " + v + "}\n")); err == nil || !strings.Contains(err.Error(), "fable_warn") {
			t.Errorf("fable_warn %s: err = %v", v, err)
		}
	}
}

// Every extra model is a Mythos model (its id must start with claude-mythos-), and Mythos draws on
// the same weekly share as Fable, so its requests count in that share by the name CPA routes.
func TestTheFableShareHoldsTheFableBucketAndEveryExtraModel(t *testing.T) {
	cfg, err := Parse([]byte(extraBase + extraOne))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"claude-fable-5-2", "claude-mythos-9-1", "claude-mythos-9-1(high)", "Claude-Mythos-9-1 [1m]"} {
		if !cfg.InFableShare(m) {
			t.Errorf("%q is in the Fable share", m)
		}
	}
	for _, m := range []string{"claude-opus-5-5", "claude-haiku-4-5-20251001", "claude-mythos-9-2", ""} {
		if cfg.InFableShare(m) {
			t.Errorf("%q is not in the Fable share", m)
		}
	}
	if got := cfg.FableShareModels(); !slices.Equal(got, []string{"claude-fable-5-2", "claude-mythos-9-5", "claude-mythos-9-1"}) {
		t.Fatalf("FableShareModels = %v", got)
	}
}

func TestWithoutExtraModelsTheFableShareIsTheFableBucket(t *testing.T) {
	cfg, err := Parse([]byte(full))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.FableShareModels(); !slices.Equal(got, []string{"claude-fable-5-1"}) {
		t.Fatalf("FableShareModels = %v", got)
	}
}

// An extra model that is also in the Fable bucket is one model: it counts once in the share.
func TestAnExtraModelInTheFableBucketCountsOnceInTheShare(t *testing.T) {
	cfg, err := Parse([]byte(extraBase + extraOne))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Buckets.Fable = append(cfg.Buckets.Fable, "claude-mythos-9-1")
	if got := cfg.FableShareModels(); !slices.Equal(got, []string{"claude-fable-5-2", "claude-mythos-9-5", "claude-mythos-9-1"}) {
		t.Fatalf("FableShareModels = %v, want each model once", got)
	}
	if !cfg.InFableShare("claude-mythos-9-1(high)") {
		t.Fatal("the model is in the share")
	}
}
