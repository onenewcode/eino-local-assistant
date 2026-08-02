package tools

import (
	"context"
	"testing"
	"time"
)

func TestDefaultRegistryIncludesBuiltInTools(t *testing.T) {
	fixed := time.Date(2026, 7, 14, 15, 30, 0, 0, time.FixedZone("CST", 8*3600))
	reg, err := Default(func() time.Time { return fixed })
	if err != nil {
		t.Fatalf("Default() error = %v", err)
	}

	tools := reg.All()
	if got, want := len(tools), 3; got != want {
		t.Fatalf("tools = %d, want %d", got, want)
	}

	infos, err := reg.Infos(context.Background())
	if err != nil {
		t.Fatalf("Infos() error = %v", err)
	}
	if got, want := len(infos), 3; got != want {
		t.Fatalf("infos = %d, want %d", got, want)
	}
	got := map[string]bool{}
	for _, info := range infos {
		got[info.Name] = true
	}
	for _, name := range []string{"get_current_time", "read_artifact", "run_command"} {
		if !got[name] {
			t.Errorf("missing tool %q in %#v", name, infos)
		}
	}
}

func TestDefaultRegistryCanDisableRunCommand(t *testing.T) {
	reg, err := Default(time.Now, RunCommandOptions{Disabled: true})
	if err != nil {
		t.Fatalf("Default() error = %v", err)
	}
	infos, err := reg.Infos(context.Background())
	if err != nil {
		t.Fatalf("Infos() error = %v", err)
	}
	if got, want := len(infos), 2; got != want {
		t.Fatalf("infos = %d, want %d", got, want)
	}
	for _, info := range infos {
		if info.Name == "run_command" {
			t.Fatalf("run_command should be disabled, got %#v", infos)
		}
	}
}
