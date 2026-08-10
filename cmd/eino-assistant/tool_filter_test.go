package main

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"

	"eino-local-assistant/internal/tools"
)

func TestInteractiveToolSelectionFlagsWireSessionStart(t *testing.T) {
	want := tools.ToolSelection{
		Allowed:    []string{"shell", "read_artifact"},
		AllowedSet: true,
		Disallowed: []string{"shell"},
	}
	for _, args := range [][]string{
		{"--tools", "shell,read_artifact", "--disallowed-tools", "shell"},
		{"chat", "--tools", "shell,read_artifact", "--disallowed-tools", "shell"},
		{"resume", "thread", "--tools", "shell,read_artifact", "--disallowed-tools", "shell"},
		{"fork", "thread", "--tools", "shell,read_artifact", "--disallowed-tools", "shell"},
	} {
		args := args
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var got sessionStart
			root := newRootCommandWithDeps(commandDeps{interactive: func(_ string, start sessionStart, _ io.Writer) error {
				got = start
				return nil
			}})
			root.SetArgs(args)
			if err := root.Execute(); err != nil {
				t.Fatalf("execute(%v): %v", args, err)
			}
			if !reflect.DeepEqual(got.toolSelection, want) {
				t.Fatalf("selection = %#v, want %#v", got.toolSelection, want)
			}
		})
	}
}

func TestExecToolSelectionFlagsWireRuntimeStart(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want tools.ToolSelection
	}{
		{
			name: "fresh",
			args: []string{"exec", "--ephemeral", "--tools", "shell,read_artifact", "--disallowed-tools", "shell", "inspect"},
			want: tools.ToolSelection{Allowed: []string{"shell", "read_artifact"}, AllowedSet: true, Disallowed: []string{"shell"}},
		},
		{
			name: "resume",
			args: []string{"exec", "resume", "thread", "--tools", "shell,read_artifact", "--disallowed-tools", "shell", "inspect"},
			want: tools.ToolSelection{Allowed: []string{"shell", "read_artifact"}, AllowedSet: true, Disallowed: []string{"shell"}},
		},
		{
			name: "explicit empty",
			args: []string{"exec", "--ephemeral", "--tools", "", "inspect"},
			want: tools.ToolSelection{AllowedSet: true},
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var got sessionStart
			deps := execCommandDeps{newRuntime: func(_ context.Context, _ string, start sessionStart) (execSession, io.Closer, error) {
				got = start
				return &fakeExecSession{chunks: []string{"done"}}, nil, nil
			}}
			_, _, err := executeExecForTest(strings.NewReader(""), deps, tc.args...)
			if err != nil {
				t.Fatalf("execute(%v): %v", tc.args, err)
			}
			if !reflect.DeepEqual(got.toolSelection, tc.want) {
				t.Fatalf("selection = %#v, want %#v", got.toolSelection, tc.want)
			}
		})
	}
}

func TestToolFilterFlagsAreVisibleOnAgentEntrypoints(t *testing.T) {
	for _, args := range [][]string{
		{"--help"},
		{"chat", "--help"},
		{"resume", "--help"},
		{"fork", "--help"},
		{"exec", "--help"},
		{"exec", "resume", "--help"},
	} {
		args := args
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdout, _, err := executeForTest(args...)
			if err != nil {
				t.Fatalf("help %v: %v", args, err)
			}
			for _, flag := range []string{"--tools", "--disallowed-tools"} {
				if !strings.Contains(stdout, flag) {
					t.Fatalf("help %v missing %q:\n%s", args, flag, stdout)
				}
			}
		})
	}
}
