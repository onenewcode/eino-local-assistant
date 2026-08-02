package tui

import "testing"

func TestFormatResumeHint(t *testing.T) {
	t.Parallel()
	cases := []struct {
		app, id, want string
	}{
		{"eino-assistant", "20260715-120000-abc123", "eino-assistant resume 20260715-120000-abc123"},
		{"  eino-assistant  ", "  sid  ", "eino-assistant resume sid"},
		{"", "sid", ""},
		{"eino-assistant", "", ""},
		{"", "", ""},
	}
	for _, tc := range cases {
		if got := FormatResumeHint(tc.app, tc.id); got != tc.want {
			t.Fatalf("FormatResumeHint(%q, %q) = %q, want %q", tc.app, tc.id, got, tc.want)
		}
	}
}

func TestSessionIDAtExitEmpty(t *testing.T) {
	t.Parallel()
	if got := sessionIDAtExit(nil, Deps{}); got != "" {
		t.Fatalf("nil model + empty deps = %q, want empty", got)
	}
	// *model with nil Session and empty deps → empty.
	if got := sessionIDAtExit(&model{}, Deps{}); got != "" {
		t.Fatalf("empty model = %q, want empty", got)
	}
}
