package board

import (
	"testing"
	"time"
)

func TestStatusRoundTripAndMerge(t *testing.T) {
	t.Setenv("SESSION_NOTES_DIR", t.TempDir())

	// Missing sidecar -> not ok.
	if _, ok := ReadStatus("sess"); ok {
		t.Fatal("expected no status before any write")
	}

	// Statusline-style write.
	UpdateStatus("sess", func(s *SessionStatus) {
		s.Model = "Opus"
		s.ContextPct = 62
		s.CostUSD = 1.25
	})
	// Lifecycle-style write must not clobber model/context.
	UpdateStatus("sess", func(s *SessionStatus) { s.Activity = "idle" })

	st, ok := ReadStatus("sess")
	if !ok {
		t.Fatal("expected status after writes")
	}
	if st.Model != "Opus" || st.ContextPct != 62 || st.CostUSD != 1.25 {
		t.Fatalf("statusline fields lost after merge: %+v", st)
	}
	if st.Activity != "idle" {
		t.Fatalf("activity not merged: %+v", st)
	}
	if !st.HasContext() {
		t.Fatal("HasContext should be true for 62%%")
	}
}

func TestStatusFreshAndStale(t *testing.T) {
	now := time.Now()
	fresh := &SessionStatus{Updated: now.Format(time.RFC3339)}
	if !fresh.Fresh(now) {
		t.Fatal("just-written status should be fresh")
	}
	old := &SessionStatus{Updated: now.Add(-StatusStaleAfter - time.Minute).Format(time.RFC3339)}
	if old.Fresh(now) {
		t.Fatal("old status should be stale")
	}
	// Missing/garbage timestamp -> stale.
	bad := &SessionStatus{Updated: "not-a-time"}
	if bad.Fresh(now) {
		t.Fatal("unparseable timestamp should read as stale")
	}
}

func TestStatusUnknownContext(t *testing.T) {
	t.Setenv("SESSION_NOTES_DIR", t.TempDir())
	UpdateStatus("s2", func(s *SessionStatus) { s.Model = "Haiku" }) // ContextPct stays -1
	st, _ := ReadStatus("s2")
	if st.HasContext() {
		t.Fatalf("fresh record with untouched context should be unknown: %+v", st)
	}
}

func TestUpdateStatusEmptySessionNoop(t *testing.T) {
	t.Setenv("SESSION_NOTES_DIR", t.TempDir())
	UpdateStatus("", func(s *SessionStatus) { s.Model = "x" })
	if _, ok := ReadStatus(""); ok {
		t.Fatal("empty session id must not write a sidecar")
	}
}
