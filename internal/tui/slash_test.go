package tui

import (
	"reflect"
	"testing"
)

func TestParseSlash(t *testing.T) {
	cases := []struct {
		in      string
		want    slashAction
		wantArg string
	}{
		{"hello", slashNone, "hello"},
		{"/help", slashHelp, ""},
		{"/?", slashHelp, ""},
		{"/exit", slashExit, ""},
		{"/quit", slashExit, ""},
		{"/clear", slashClear, ""},
		{"/status", slashStatus, ""},
		{"/rules", slashRules, ""},
		{"/btw what changed?", slashSide, "what changed?"},
		{"/side what changed?", slashSide, "what changed?"},
		{"/BTW", slashSide, ""},
		{"/usage", slashUsage, ""},
		{"/usage off", slashUsage, "off"},
		{"/context", slashContext, ""},
		{"/CONTEXT", slashContext, ""},
		{"/compact", slashCompact, ""},
		{"/compact retain failed approaches", slashCompact, "retain failed approaches"},
		{"/sessions", slashSessions, ""},
		{"/new", slashNew, ""},
		{"/new my topic", slashNew, "my topic"},
		{"/resume 20260715-120000-abc123", slashResume, "20260715-120000-abc123"},
		{"/fork", slashFork, ""},
		{"/fork unexpected-argument", slashFork, "unexpected-argument"},
		{"/title Hello World", slashTitle, "Hello World"},
		{"/delete 20260715-120000-abc123", slashDelete, "20260715-120000-abc123"},
		{"/queue", slashQueue, ""},
		{"/queue clear", slashQueue, "clear"},
		{"/permissions", slashPermissions, ""},
		{"/policy", slashPermissions, ""},
		{"/memory", slashMemory, ""},
		{"/memory list", slashMemory, "list"},
		{"/memory add prefer go", slashMemory, "add prefer go"},
		{"/unknown", slashUnknown, "/unknown"},
		{"  /HELP  ", slashHelp, ""},
	}
	for _, tc := range cases {
		got, arg := parseSlash(tc.in)
		if got != tc.want {
			t.Errorf("parseSlash(%q) action = %v, want %v", tc.in, got, tc.want)
		}
		if arg != tc.wantArg {
			t.Errorf("parseSlash(%q) arg = %q, want %q", tc.in, arg, tc.wantArg)
		}
	}
}

func TestSlashCatalogParseableAndComplete(t *testing.T) {
	catalog := slashCatalog()
	if len(catalog) == 0 {
		t.Fatal("empty catalog")
	}
	seenNames := map[string]bool{}
	for _, cmd := range catalog {
		if seenNames[cmd.Name] {
			t.Fatalf("duplicate catalog name %q", cmd.Name)
		}
		seenNames[cmd.Name] = true
		action, _ := parseSlash(cmd.Name)
		if action == slashNone || action == slashUnknown {
			t.Fatalf("catalog name %q parses as %v", cmd.Name, action)
		}
		for _, alias := range cmd.Aliases {
			action, _ = parseSlash(alias)
			if action == slashNone || action == slashUnknown {
				t.Fatalf("catalog alias %q of %q parses as %v", alias, cmd.Name, action)
			}
		}
	}
	tokens := []string{
		"/help", "/?", "/exit", "/quit", "/clear", "/status", "/rules", "/btw", "/side", "/usage",
		"/context", "/compact",
		"/new", "/sessions", "/resume", "/fork", "/title", "/delete", "/queue",
		"/permissions", "/policy",
	}
	for _, tok := range tokens {
		if !catalogCoversToken(catalog, tok) {
			t.Fatalf("parse token %q missing from catalog names/aliases", tok)
		}
	}
}

func catalogCoversToken(catalog []slashCommand, tok string) bool {
	for _, cmd := range catalog {
		if cmd.Name == tok {
			return true
		}
		for _, alias := range cmd.Aliases {
			if alias == tok {
				return true
			}
		}
	}
	return false
}

func TestSlashCatalogNeedsArg(t *testing.T) {
	want := map[string]bool{
		"/help": false, "/status": false, "/rules": false, "/context": false, "/sessions": false,
		"/clear": false, "/exit": false, "/permissions": false,
		"/btw":   true,
		"/usage": true, "/compact": true, "/new": true, "/resume": true, "/fork": false, "/title": true,
		"/delete": true, "/queue": true, "/memory": true,
	}
	for _, cmd := range slashCatalog() {
		need, ok := want[cmd.Name]
		if !ok {
			t.Fatalf("unexpected catalog command %q", cmd.Name)
		}
		if cmd.NeedsArg != need {
			t.Fatalf("%s NeedsArg=%v want %v", cmd.Name, cmd.NeedsArg, need)
		}
	}
}

func TestSlashMenuActive(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"hello", false},
		{"c", false},
		{"/", true},
		{"/c", true},
		{"/clear", true},
		{"  /c", true},
		{"/c  ", false},
		{"/clear now", false},
		{"/new topic", false},
		{"/clear ", false},
		{"/c\n", false},
		{"/c\nmore", false},
		{" \t ", false},
	}
	for _, tc := range cases {
		if got := slashMenuActive(tc.in); got != tc.want {
			t.Errorf("slashMenuActive(%q)=%v want %v", tc.in, got, tc.want)
		}
	}
}

func TestFilterSlashCommandsMatrix(t *testing.T) {
	all := namesOf(slashCatalog())
	cases := []struct {
		query string
		want  []string
	}{
		{"", nil},
		{"hello", nil},
		{"c", nil},
		{"/", all},
		{"/?", []string{"/help"}},
		{"/h", []string{"/help"}},
		{"/he", []string{"/help"}},
		{"/help", []string{"/help"}},
		{"/e", []string{"/exit"}},
		{"/ex", []string{"/exit"}},
		{"/exit", []string{"/exit"}},
		{"/q", []string{"/queue", "/exit"}},
		{"/qu", []string{"/queue", "/exit"}},
		{"/qui", []string{"/exit"}},
		{"/quit", []string{"/exit"}},
		{"/que", []string{"/queue"}},
		{"/queue", []string{"/queue"}},
		{"/c", []string{"/context", "/compact", "/clear"}},
		{"/co", []string{"/context", "/compact"}},
		{"/con", []string{"/context"}},
		{"/context", []string{"/context"}},
		{"/com", []string{"/compact"}},
		{"/compact", []string{"/compact"}},
		{"/cl", []string{"/clear"}},
		{"/cle", []string{"/clear"}},
		{"/clear", []string{"/clear"}},
		{"/s", []string{"/status", "/btw", "/sessions"}},
		{"/si", []string{"/btw"}},
		{"/st", []string{"/status"}},
		{"/se", []string{"/sessions"}},
		{"/stat", []string{"/status"}},
		{"/sess", []string{"/sessions"}},
		{"/n", []string{"/new"}},
		{"/new", []string{"/new"}},
		{"/r", []string{"/rules", "/resume"}},
		{"/re", []string{"/resume"}},
		{"/res", []string{"/resume"}},
		{"/f", []string{"/fork"}},
		{"/fo", []string{"/fork"}},
		{"/fork", []string{"/fork"}},
		{"/t", []string{"/title"}},
		{"/ti", []string{"/title"}},
		{"/d", []string{"/delete"}},
		{"/de", []string{"/delete"}},
		{"/del", []string{"/delete"}},
		{"/zzz", nil},
		{"/unknown", nil},
		{"/C", []string{"/context", "/compact", "/clear"}},
		{"/CLEAR", []string{"/clear"}},
		{"/clear ", nil},
		{"/clear x", nil},
		{"/new topic", nil},
		{"  /c", []string{"/context", "/compact", "/clear"}},
		{"/c\n", nil},
		{"/c\nmore", nil},
	}
	for _, tc := range cases {
		got := namesOf(filterSlashCommands(tc.query))
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("filterSlashCommands(%q)=%v want %v", tc.query, got, tc.want)
		}
	}
}

func TestFilterSlashCommandsNoDuplicates(t *testing.T) {
	got := filterSlashCommands("/quit")
	if len(got) != 1 || got[0].Name != "/exit" {
		t.Fatalf("got %#v", got)
	}
	got = filterSlashCommands("/")
	seen := map[string]bool{}
	for _, cmd := range got {
		if seen[cmd.Name] {
			t.Fatalf("duplicate %s in full list", cmd.Name)
		}
		seen[cmd.Name] = true
	}
}

func TestCompleteSlashCommand(t *testing.T) {
	if got := completeSlashCommand(slashCommand{Name: "/help"}); got != "/help" {
		t.Fatalf("no-arg complete = %q", got)
	}
	if got := completeSlashCommand(slashCommand{Name: "/new", NeedsArg: true}); got != "/new " {
		t.Fatalf("arg complete = %q", got)
	}
	if got := completeSlashCommand(slashCommand{Name: "/fork"}); got != "/fork" {
		t.Fatalf("fork complete = %q", got)
	}
}

func TestFilterFullListMatchesCatalogOrder(t *testing.T) {
	if !reflect.DeepEqual(namesOf(filterSlashCommands("/")), namesOf(slashCatalog())) {
		t.Fatalf("full filter order drifted from catalog")
	}
}

func namesOf(items []slashCommand) []string {
	if items == nil {
		return nil
	}
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.Name
	}
	return out
}
