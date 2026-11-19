package state

import (
	"testing"
	"time"
)

func TestAResetIsRecordedOnceAndInOrder(t *testing.T) {
	a := &Account{}
	r := Reset{ResetsAt: now, Util: 60, Weight: 10, Wasted: 400}
	if !a.AddReset(r) {
		t.Fatal("the first reset is recorded")
	}
	if a.AddReset(Reset{ResetsAt: now.Add(30 * time.Second), Util: 61}) {
		t.Fatal("the same reset, seen from another source, is not recorded twice")
	}
	if a.AddReset(Reset{ResetsAt: now.Add(-7 * 24 * time.Hour), Util: 10}) {
		t.Fatal("a reset before the latest one recorded is not recorded")
	}
	if !a.AddReset(Reset{ResetsAt: now.Add(7 * 24 * time.Hour), Util: 90}) || len(a.Resets) != 2 {
		t.Fatalf("the next week's reset is recorded: %+v", a.Resets)
	}
}

func TestTheResetsAreBoundedAndTheOldestGoFirst(t *testing.T) {
	a := &Account{}
	for i := range MaxResets + 3 {
		a.AddReset(Reset{ResetsAt: now.Add(time.Duration(i) * 7 * 24 * time.Hour)})
	}
	if len(a.Resets) != MaxResets || !a.Resets[0].ResetsAt.Equal(now.Add(3*7*24*time.Hour)) {
		t.Fatalf("the last %d resets stay: %d, first %v", MaxResets, len(a.Resets), a.Resets[0].ResetsAt)
	}
}
