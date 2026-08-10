package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func setComposer(m *model, value string) {
	m.textarea.SetValue(value)
	m.syncComposerHeight()
	m.syncSlashMenu()
	m.layout()
	m.refreshViewport()
}

func TestSlashMenuOpenRefilterClose(t *testing.T) {
	m := newTestModel(t)

	setComposer(m, "/")
	if !m.slashMenuOpen() || len(m.slashItems) != len(slashCatalog()) {
		t.Fatalf("full menu: open=%v n=%d", m.slashMenuOpen(), len(m.slashItems))
	}

	setComposer(m, "/s")
	if got := namesOf(m.slashItems); !equalStrings(got, []string{"/status", "/skills", "/btw", "/steer", "/statusline", "/sessions"}) {
		t.Fatalf("/s => %v", got)
	}

	setComposer(m, "/re")
	if got := namesOf(m.slashItems); !equalStrings(got, []string{"/review", "/resume"}) {
		t.Fatalf("/re => %v", got)
	}

	setComposer(m, "/q")
	if got := namesOf(m.slashItems); !equalStrings(got, []string{"/queue", "/exit"}) {
		t.Fatalf("/q => %v", got)
	}

	setComposer(m, "/zzz")
	if m.slashMenuOpen() {
		t.Fatalf("unknown prefix should close menu")
	}

	setComposer(m, "/new ")
	if m.slashMenuOpen() {
		t.Fatalf("args phase should close menu")
	}

	setComposer(m, "hello")
	if m.slashMenuOpen() {
		t.Fatalf("normal text should close menu")
	}

	steps := []struct {
		v    string
		want []string
	}{
		{"/", namesOf(slashCatalog())},
		{"/c", []string{"/context", "/compact", "/clear"}},
		{"/cl", []string{"/clear"}},
		{"/cle", []string{"/clear"}},
		{"/clear", []string{"/clear"}},
		{"/co", []string{"/context", "/compact"}},
		{"/com", []string{"/compact"}},
	}
	for _, step := range steps {
		setComposer(m, step.v)
		if got := namesOf(m.slashItems); !equalStrings(got, step.want) {
			t.Fatalf("step %q => %v want %v", step.v, got, step.want)
		}
	}
}

func TestSlashMenuNavigationClampsAndBeatsHistory(t *testing.T) {
	m := newTestModel(t)
	m.inputHist.push("older")
	m.inputHist.push("newer")

	setComposer(m, "/s")
	if len(m.slashItems) < 2 {
		t.Fatalf("need multi-item menu, got %v", namesOf(m.slashItems))
	}
	if m.slashSel != 0 {
		t.Fatalf("initial sel=%d", m.slashSel)
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	mm := next.(*model)
	if mm.slashSel != 1 {
		t.Fatalf("down sel=%d", mm.slashSel)
	}
	if mm.textarea.Value() != "/s" {
		t.Fatalf("menu nav must not rewrite composer, got %q", mm.textarea.Value())
	}
	if mm.inputHist.browsing() {
		t.Fatal("menu nav must not enter history browse")
	}

	for range 10 {
		next, _ = mm.Update(tea.KeyMsg{Type: tea.KeyDown})
		mm = next.(*model)
	}
	if mm.slashSel != len(mm.slashItems)-1 {
		t.Fatalf("clamp end sel=%d n=%d", mm.slashSel, len(mm.slashItems))
	}

	for range 10 {
		next, _ = mm.Update(tea.KeyMsg{Type: tea.KeyUp})
		mm = next.(*model)
	}
	if mm.slashSel != 0 {
		t.Fatalf("clamp start sel=%d", mm.slashSel)
	}
	if mm.textarea.Value() != "/s" || mm.inputHist.browsing() {
		t.Fatalf("history leaked: value=%q browsing=%v", mm.textarea.Value(), mm.inputHist.browsing())
	}
}

func TestSlashMenuTabCompleteNeedsArgClasses(t *testing.T) {
	m := newTestModel(t)

	setComposer(m, "/cl")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	mm := next.(*model)
	if mm.textarea.Value() != "/clear" {
		t.Fatalf("tab /clear = %q", mm.textarea.Value())
	}
	if !mm.slashMenuOpen() || namesOf(mm.slashItems)[0] != "/clear" {
		t.Fatalf("exact no-arg token keeps single-row menu: %#v", namesOf(mm.slashItems))
	}

	setComposer(mm, "/com")
	next, _ = mm.Update(tea.KeyMsg{Type: tea.KeyTab})
	mm = next.(*model)
	if mm.textarea.Value() != "/compact " {
		t.Fatalf("tab /compact = %q", mm.textarea.Value())
	}
	if mm.slashMenuOpen() {
		t.Fatal("trailing space should close menu")
	}

	setComposer(mm, "/n")
	next, _ = mm.Update(tea.KeyMsg{Type: tea.KeyTab})
	mm = next.(*model)
	if mm.textarea.Value() != "/new " {
		t.Fatalf("tab /new = %q", mm.textarea.Value())
	}
	if mm.slashMenuOpen() {
		t.Fatal("trailing space should close menu")
	}

	setComposer(mm, "/re")
	next, _ = mm.Update(tea.KeyMsg{Type: tea.KeyTab})
	mm = next.(*model)
	if mm.textarea.Value() != "/review" {
		t.Fatalf("tab /re = %q", mm.textarea.Value())
	}

	setComposer(mm, "/res")
	next, _ = mm.Update(tea.KeyMsg{Type: tea.KeyTab})
	mm = next.(*model)
	if mm.textarea.Value() != "/resume " {
		t.Fatalf("tab /resume = %q", mm.textarea.Value())
	}

	setComposer(mm, "/sk")
	next, _ = mm.Update(tea.KeyMsg{Type: tea.KeyTab})
	mm = next.(*model)
	if mm.textarea.Value() != "/skills " {
		t.Fatalf("tab /skills = %q", mm.textarea.Value())
	}

	setComposer(mm, "/fo")
	next, _ = mm.Update(tea.KeyMsg{Type: tea.KeyTab})
	mm = next.(*model)
	if mm.textarea.Value() != "/fork " {
		t.Fatalf("tab /fork = %q", mm.textarea.Value())
	}

	setComposer(mm, "/h")
	next, _ = mm.Update(tea.KeyMsg{Type: tea.KeyTab})
	mm = next.(*model)
	if mm.textarea.Value() != "/help" {
		t.Fatalf("tab /help = %q", mm.textarea.Value())
	}

	setComposer(mm, "/pl")
	next, _ = mm.Update(tea.KeyMsg{Type: tea.KeyTab})
	mm = next.(*model)
	if mm.textarea.Value() != "/plan" {
		t.Fatalf("tab /plan alias = %q", mm.textarea.Value())
	}
	if !mm.slashMenuOpen() {
		t.Fatal("exact /plan should keep its no-argument menu row")
	}

	setComposer(mm, "/q")
	next, _ = mm.Update(tea.KeyMsg{Type: tea.KeyDown})
	mm = next.(*model)
	next, _ = mm.Update(tea.KeyMsg{Type: tea.KeyTab})
	mm = next.(*model)
	if mm.textarea.Value() != "/exit" {
		t.Fatalf("tab selected /exit = %q (items=%v sel=%d)", mm.textarea.Value(), namesOf(mm.slashItems), mm.slashSel)
	}
}

func TestSlashMenuEnterAutoSubmitNoArg(t *testing.T) {
	m := newTestModel(t)

	setComposer(m, "/h")
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := next.(*model)
	if cmd != nil {
		t.Fatalf("help enter should not return turn cmd")
	}
	if !hasLineContaining(mm.lines, lineSystem, "Commands:") {
		t.Fatalf("help not submitted: %#v", mm.lines)
	}
	if mm.slashMenuOpen() || strings.TrimSpace(mm.textarea.Value()) != "" {
		t.Fatalf("menu/composer should clear after submit")
	}

	setComposer(mm, "/st")
	next, _ = mm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm = next.(*model)
	if !hasLineContaining(mm.lines, lineSystem, "Model:") {
		t.Fatalf("status not submitted: %#v", mm.lines)
	}

	setComposer(mm, "/ex")
	next, cmd = mm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm = next.(*model)
	if !mm.quitting {
		t.Fatal("exit should set quitting")
	}
	if cmd == nil {
		t.Fatal("exit should return Quit cmd")
	}
}

func TestSlashMenuEnterArgCommandsDoNotSubmit(t *testing.T) {
	m := newTestModel(t)
	beforeID := m.deps.Session.ID()

	cases := []struct {
		prefix string
		want   string
	}{
		{"/n", "/new "},
		{"/res", "/resume "},
		{"/ti", "/title "},
		{"/del", "/delete "},
		{"/que", "/queue "},
		{"/per", "/permissions "},
	}
	for _, tc := range cases {
		setComposer(m, tc.prefix)
		next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = next.(*model)
		if cmd != nil {
			t.Fatalf("%s: unexpected cmd", tc.prefix)
		}
		if m.textarea.Value() != tc.want {
			t.Fatalf("%s: composer=%q want %q", tc.prefix, m.textarea.Value(), tc.want)
		}
		if m.slashMenuOpen() {
			t.Fatalf("%s: menu should close after trailing space", tc.prefix)
		}
	}
	if m.deps.Session.ID() != beforeID {
		t.Fatalf("arg accepts must not create sessions")
	}
}

func TestSlashMenuEscDismissIdle(t *testing.T) {
	m := newTestModel(t)
	setComposer(m, "/c")
	if !m.slashMenuOpen() {
		t.Fatal("expected open menu")
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	mm := next.(*model)
	if mm.slashMenuOpen() {
		t.Fatal("esc should dismiss menu")
	}
	if mm.textarea.Value() != "/c" {
		t.Fatalf("composer preserved, got %q", mm.textarea.Value())
	}
	if mm.quitting {
		t.Fatal("esc must not quit")
	}
}

func TestSlashMenuEscBusyInterrupts(t *testing.T) {
	m := newTestModel(t)
	m.mode = modeBusy
	m.turnID = 1
	canceled := false
	m.turnCancel = func() { canceled = true }
	setComposer(m, "/st")
	if !m.slashMenuOpen() {
		t.Fatal("menu should open while busy")
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	_ = next.(*model)
	if !canceled {
		t.Fatal("busy Esc must interrupt turn (cancel), not only dismiss menu")
	}
}

func TestSlashMenuHistoryAfterDismiss(t *testing.T) {
	m := newTestModel(t)
	m.inputHist.push("first")
	m.inputHist.push("second")
	setComposer(m, "/s")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	mm := next.(*model)
	setComposer(mm, "")
	next, _ = mm.Update(tea.KeyMsg{Type: tea.KeyUp})
	mm = next.(*model)
	if mm.textarea.Value() != "second" {
		t.Fatalf("history after dismiss = %q", mm.textarea.Value())
	}
}

func TestSlashMenuBusyMutativeStillRejected(t *testing.T) {
	m := newTestModel(t)
	m.mode = modeBusy
	m.turnID = 1
	setComposer(m, "/cl")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := next.(*model)
	if !hasLineContaining(mm.lines, lineError, "cannot queue mutative") {
		t.Fatalf("expected mutative reject, lines=%#v", mm.lines)
	}
}

func TestSlashMenuViewAndLayout(t *testing.T) {
	m := newTestModel(t)
	m.width = 80
	m.height = 24
	m.layout()
	closedH := m.viewport.Height

	setComposer(m, "/s")
	m.layout()
	openH := m.viewport.Height
	if openH >= closedH {
		t.Fatalf("viewport should shrink when menu open: closed=%d open=%d menuH=%d", closedH, openH, m.slashMenuHeight())
	}
	view := m.View()
	for _, name := range []string{"/status", "/btw", "/steer", "/statusline", "/sessions"} {
		if !strings.Contains(view, name) {
			t.Fatalf("view missing %s:\n%s", name, view)
		}
	}
	if !strings.Contains(view, "select") {
		t.Fatalf("help hint should switch while menu open:\n%s", view)
	}

	setComposer(m, "hello")
	m.layout()
	if m.viewport.Height != closedH {
		t.Fatalf("viewport should restore: got %d want %d", m.viewport.Height, closedH)
	}
	view = m.View()
	if strings.Contains(view, "↑↓ select") {
		t.Fatalf("menu help should be gone:\n%s", view)
	}
}

func TestSlashMenuAliasCompleteCanonical(t *testing.T) {
	m := newTestModel(t)
	setComposer(m, "/qui")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	mm := next.(*model)
	if mm.textarea.Value() != "/exit" {
		t.Fatalf("alias complete writes canonical, got %q", mm.textarea.Value())
	}

	setComposer(mm, "/?")
	next, _ = mm.Update(tea.KeyMsg{Type: tea.KeyTab})
	mm = next.(*model)
	if mm.textarea.Value() != "/help" {
		t.Fatalf("? alias complete = %q", mm.textarea.Value())
	}
}

func TestSlashMenuWindowFollowsSelection(t *testing.T) {
	start, end := slashMenuWindow(12, 0, 8)
	if start != 0 || end != 8 {
		t.Fatalf("top window=%d,%d", start, end)
	}
	start, end = slashMenuWindow(12, 11, 8)
	if end != 12 || start != 4 {
		t.Fatalf("bottom window=%d,%d", start, end)
	}
	items := slashCatalog()
	if len(items) < 2 {
		t.Skip("catalog too small")
	}
	out := renderSlashMenu(60, items, len(items)-1)
	if !strings.Contains(out, items[len(items)-1].Name) {
		t.Fatalf("selected row missing from render: %q", out)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
