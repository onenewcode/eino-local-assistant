package agent

import "testing"

func TestShellMayHaveMutatedUsesToolImpact(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		output  string
		wantMut bool
	}{
		{name: "read only", output: `{"command":"git status","impact":"read_only","exit_code":0}`, wantMut: false},
		{name: "workspace write", output: `{"command":"go test ./...","impact":"workspace_write","exit_code":0}`, wantMut: true},
		{name: "external side effect", output: `{"command":"git push","impact":"external_side_effect","exit_code":0}`, wantMut: true},
		{name: "legacy result", output: `{"command":"git status","exit_code":0}`, wantMut: true},
		{name: "denied", output: `{"command":"touch changed.txt","impact":"workspace_write","exit_code":-1,"denied":true}`, wantMut: false},
		{name: "malformed", output: "[artifact unavailable]", wantMut: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shellMayHaveMutated(tt.output); got != tt.wantMut {
				t.Fatalf("shellMayHaveMutated(%q) = %t, want %t", tt.output, got, tt.wantMut)
			}
		})
	}
}
