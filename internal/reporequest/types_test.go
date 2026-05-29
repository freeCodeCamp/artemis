package reporequest

import "testing"

func TestStatusValid(t *testing.T) {
	valid := []Status{StatusPending, StatusApproved, StatusActive, StatusRejected, StatusFailed}
	for _, s := range valid {
		if !s.Valid() {
			t.Errorf("Status(%q).Valid() = false, want true", s)
		}
	}
	for _, s := range []Status{"", "done", "Pending", "deleted"} {
		if s.Valid() {
			t.Errorf("Status(%q).Valid() = true, want false", s)
		}
	}
}

func TestStatusTerminal(t *testing.T) {
	terminal := map[Status]bool{
		StatusPending:  false,
		StatusApproved: false,
		StatusActive:   true,
		StatusRejected: true,
		StatusFailed:   true,
	}
	for s, want := range terminal {
		if got := s.Terminal(); got != want {
			t.Errorf("Status(%q).Terminal() = %v, want %v", s, got, want)
		}
	}
}

func TestStatusCanResolve(t *testing.T) {
	if !StatusPending.CanResolve() {
		t.Error("pending must be resolvable")
	}
	for _, s := range []Status{StatusApproved, StatusActive, StatusRejected, StatusFailed} {
		if s.CanResolve() {
			t.Errorf("Status(%q).CanResolve() = true, want false", s)
		}
	}
}

func TestVisibilityValid(t *testing.T) {
	if !VisibilityPublic.Valid() || !VisibilityPrivate.Valid() {
		t.Error("public and private must be valid")
	}
	for _, v := range []Visibility{"", "secret", "Private"} {
		if v.Valid() {
			t.Errorf("Visibility(%q).Valid() = true, want false", v)
		}
	}
}

func TestValidName(t *testing.T) {
	accept := []string{"a", "learn-python-rpg", "Hello_World.js", "0abc"}
	for _, n := range accept {
		if !ValidName(n) {
			t.Errorf("ValidName(%q) = false, want true", n)
		}
	}
	// exactly 100 chars accepted; 101 rejected.
	if !ValidName(repeat("a", 100)) {
		t.Error("100-char name must be accepted")
	}
	reject := []string{"", "-abc", ".abc", "_abc", "abc def", "abc/def", repeat("a", 101)}
	for _, n := range reject {
		if ValidName(n) {
			t.Errorf("ValidName(%q) = true, want false", n)
		}
	}
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
