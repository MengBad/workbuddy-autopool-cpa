package main

import "testing"

// lifecycle_test.go already covers the decision functions themselves
// (shouldActOnCredits / isHardCreditError / isSoftRateLimit /
// lifecycleActionFor / shouldReenableCN / displayNote / labelForAuth). What it
// does not cover is the runtime toggle and the action's string form, which is
// what the logs and the CPAMP note actually print.

func TestLifecycleEnabledDefault(t *testing.T) {
	old := lifecycleAuto
	t.Cleanup(func() { lifecycleAuto = old })

	lifecycleAutoMu.Lock()
	lifecycleAuto = true
	lifecycleAutoMu.Unlock()
	if !lifecycleEnabled() {
		t.Fatal("default should be enabled")
	}

	lifecycleAutoMu.Lock()
	lifecycleAuto = false
	lifecycleAutoMu.Unlock()
	if lifecycleEnabled() {
		t.Fatal("disabled state not honoured")
	}
}

func TestLifecycleActionString(t *testing.T) {
	for action, want := range map[lifecycleAction]string{
		lifecycleNone:     "none",
		lifecycleDisable:  "disable",
		lifecycleDelete:   "delete",
		lifecycleReenable: "reenable",
	} {
		if got := action.String(); got != want {
			t.Fatalf("%d.String() = %q, want %q", int(action), got, want)
		}
	}
}
