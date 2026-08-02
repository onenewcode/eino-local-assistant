package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"eino-local-assistant/internal/chat"
	"eino-local-assistant/internal/store"
	"eino-local-assistant/internal/usage"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// StatusInfo is static metadata shown by /status.
type StatusInfo struct {
	Model string
	Tools []string
	// MaxStep is the ReAct step budget (0 omits from /status).
	MaxStep int
}

// Deps wires the interactive app to a chat session.
type Deps struct {
	// Ctx is the process-level context (e.g. SIGTERM). Each turn derives from it.
	Ctx     context.Context
	Session *chat.Session
	// Store backs thread commands (/new, /sessions, /resume, /title). When it
	// is omitted, newModel obtains the same required ledger from Session.
	Store        store.ThreadRepository
	SystemPrompt string
	// SessionOpts is reused for /new and /resume so pricing/context stay consistent.
	SessionOpts chat.SessionOptions
	Status      StatusInfo
}

type mode int

const (
	modeIdle mode = iota
	modeBusy
	modeCompacting
)

type transcriptLine struct {
	kind lineKind
	text string
}

type lineKind int

const (
	lineUser lineKind = iota
	lineAssistant
	lineTool
	lineError
	lineSystem
	lineSep
)

const (
	// Start as a single-line Claude/Codex-style input; grows with content.
	composerMinHeight = 1
	composerMaxHeight = 8
)

type model struct {
	deps Deps

	width  int
	height int

	viewport viewport.Model
	textarea textarea.Model
	spinner  spinner.Model

	lines []transcriptLine

	// stickBottom auto-follows new transcript content while the user is at the bottom.
	stickBottom bool
	// currentTool is the in-flight tool name for the status bar (cleared after tool ends).
	currentTool string
	// openToolCards maps stable tool call IDs to in-flight transcript cards.
	openToolCards map[string]int
	openToolNames map[string]string
	// queue holds follow-ups submitted while a turn is running (FIFO).
	queue []string
	// inputHist is shell-style Up/Down composer history for this TUI process.
	inputHist inputHistory
	// slashItems is the live prefix-filtered command menu (empty => closed).
	slashItems []slashCommand
	slashSel   int

	mode             mode
	turnID           int
	turnCancel       context.CancelFunc
	turnStart        time.Time
	events           chan tea.Msg
	turnDone         chan turnDoneMsg
	pendingTurnDone  *turnDoneMsg
	compactID        int
	compactCancel    context.CancelFunc
	compactStart     time.Time
	compactAutomatic bool

	streamingAssistant bool
	err                error
	quitting           bool
}

// tryHistoryUp handles ↑ when the cursor is on the first composer line.
// Multi-line drafts keep ↑ for moving within the textarea.
func (m *model) tryHistoryUp() (handled bool, cmd tea.Cmd) {
	if m.textarea.Line() != 0 {
		return false, nil
	}
	next, ok := m.inputHist.up(m.textarea.Value())
	if !ok {
		// No older entry (or empty history). If we just entered the same entry,
		// still treat as handled only when browsing already shows something new.
		if !m.inputHist.browsing() {
			return false, nil
		}
		// At oldest entry: swallow so we don't move into textarea line nav oddly.
		return true, nil
	}
	m.textarea.SetValue(next)
	m.textarea.CursorEnd()
	m.syncComposerHeight()
	m.syncSlashMenu()
	m.layout()
	m.refreshViewport()
	return true, nil
}

// tryHistoryDown handles ↓ when the cursor is on the last composer line.
func (m *model) tryHistoryDown() (handled bool, cmd tea.Cmd) {
	// When browsing history entries are usually single-line; allow down from any line
	// while browsing so users can return to the draft easily.
	if !m.inputHist.browsing() {
		last := m.textarea.LineCount() - 1
		if last < 0 {
			last = 0
		}
		if m.textarea.Line() != last {
			return false, nil
		}
		// Not browsing and on last line with empty history: let textarea handle.
		if len(m.inputHist.entries) == 0 {
			return false, nil
		}
		// On last line but not browsing: ↓ does nothing for history (no newer).
		// Let textarea keep default behavior (no-op at end).
		return false, nil
	}
	next, ok := m.inputHist.down()
	if !ok {
		return true, nil
	}
	m.textarea.SetValue(next)
	m.textarea.CursorEnd()
	m.syncComposerHeight()
	m.layout()
	m.refreshViewport()
	return true, nil
}

// slashMenuOpen reports whether the live command suggestion list is visible.
func (m *model) slashMenuOpen() bool {
	return len(m.slashItems) > 0
}

// syncSlashMenu recomputes prefix-filtered suggestions from the composer value.
// Selection is preserved by canonical name when the previous choice is still present.
func (m *model) syncSlashMenu() {
	prev := ""
	if m.slashMenuOpen() && m.slashSel >= 0 && m.slashSel < len(m.slashItems) {
		prev = m.slashItems[m.slashSel].Name
	}
	items := filterSlashCommands(m.textarea.Value())
	m.slashItems = items
	if len(items) == 0 {
		m.slashSel = 0
		return
	}
	if prev != "" {
		for i, item := range items {
			if item.Name == prev {
				m.slashSel = i
				return
			}
		}
	}
	if m.slashSel < 0 {
		m.slashSel = 0
	}
	if m.slashSel >= len(items) {
		m.slashSel = len(items) - 1
	}
}

func (m *model) clearSlashMenu() {
	m.slashItems = nil
	m.slashSel = 0
}

func (m *model) selectedSlashCommand() (slashCommand, bool) {
	if !m.slashMenuOpen() {
		return slashCommand{}, false
	}
	if m.slashSel < 0 || m.slashSel >= len(m.slashItems) {
		return m.slashItems[0], true
	}
	return m.slashItems[m.slashSel], true
}

// applySlashCompletion writes the selected command into the composer.
// NeedsArg commands keep a trailing space and stay unsubmitted.
func (m *model) applySlashCompletion(cmd slashCommand) {
	m.textarea.SetValue(completeSlashCommand(cmd))
	m.textarea.CursorEnd()
	m.syncComposerHeight()
	m.syncSlashMenu()
	m.layout()
	m.refreshViewport()
}

// acceptSlashSelection handles Enter while the menu is open.
func (m *model) acceptSlashSelection() (tea.Model, tea.Cmd) {
	cmd, ok := m.selectedSlashCommand()
	if !ok {
		return m, nil
	}
	if cmd.NeedsArg {
		m.applySlashCompletion(cmd)
		return m, nil
	}
	// Auto-submit no-arg commands through the normal paths so busy-mode
	// queue / immediate / reject policy stays centralized.
	text := completeSlashCommand(cmd)
	m.clearSlashMenu()
	m.textarea.Reset()
	m.syncComposerHeight()
	m.layout()
	m.refreshViewport()
	if m.mode != modeIdle {
		return m.queueWhileBusy(text)
	}
	return m.submit(text)
}

// slashMenuHeight is the number of terminal rows reserved for the suggestion list.
func (m *model) slashMenuHeight() int {
	n := len(m.slashItems)
	if n == 0 {
		return 0
	}
	if n > maxSlashMenuRows {
		return maxSlashMenuRows
	}
	return n
}

func (m *model) processCtx() context.Context {
	if m.deps.Ctx != nil {
		return m.deps.Ctx
	}
	return context.Background()
}

func newModel(deps Deps) *model {
	if deps.Session != nil && deps.Store == nil {
		// A Session is always ledger-backed; use that same repository for TUI
		// thread commands when the caller did not duplicate the dependency.
		deps.Store = deps.Session.Store()
	}
	if deps.SessionOpts.Store == nil {
		deps.SessionOpts.Store = deps.Store
	}
	ta := textarea.New()
	ta.Placeholder = "Message the assistant…  (/help)"
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetHeight(composerMinHeight)
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.BlurredStyle.CursorLine = lipgloss.NewStyle()
	ta.FocusedStyle.Base = lipgloss.NewStyle()
	ta.BlurredStyle.Base = lipgloss.NewStyle()
	ta.FocusedStyle.Prompt = userStyle
	ta.BlurredStyle.Prompt = statusStyle
	ta.FocusedStyle.Placeholder = statusStyle
	ta.BlurredStyle.Placeholder = statusStyle
	ta.FocusedStyle.Text = assistantStyle
	ta.BlurredStyle.Text = assistantStyle
	// First line gets the prompt; wrapped/continued lines stay clean (no repeated ›).
	ta.SetPromptFunc(2, func(lineIdx int) string {
		if lineIdx == 0 {
			return "› "
		}
		return "  "
	})
	// Enter sends; Ctrl+J inserts a newline.
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("ctrl+j"))
	ta.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = spinnerStyle

	vp := viewport.New(80, 20)
	vp.KeyMap = transcriptKeyMap()
	vp.MouseWheelEnabled = true

	m := &model{
		deps:          deps,
		viewport:      vp,
		textarea:      ta,
		spinner:       sp,
		stickBottom:   true,
		inputHist:     newInputHistory(),
		openToolCards: make(map[string]int),
		openToolNames: make(map[string]string),
	}
	// CLI resume (and any Session with a loaded transcript) must show prior turns.
	// In-TUI /resume uses the same seed helper for a single source of truth.
	if deps.Session != nil {
		transcript := deps.Session.Transcript()
		if hasReplayableTranscript(transcript) {
			m.lines = seedLinesFromTranscript(transcript, resumeBanner(deps.Session.ID(), len(transcript)), deps.Session.Title())
		}
		// Prefill Up/Down history from prior user turns.
		m.inputHist.seedFromMessages(transcript)
	}
	if len(m.lines) == 0 {
		m.lines = []transcriptLine{
			{kind: lineSystem, text: "Eino local assistant · type /help for commands"},
			{kind: lineSep, text: ""},
		}
	}
	m.refreshViewport()
	return m
}

func (m *model) Init() tea.Cmd {
	return textarea.Blink
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.layout()
		m.refreshViewport()
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			if m.mode == modeBusy {
				m.interruptTurn("interrupted")
				return m, nil
			}
			if m.mode == modeCompacting {
				m.interruptCompaction()
				return m, nil
			}
			m.quitting = true
			return m, tea.Quit
		case tea.KeyEsc:
			// Interrupt always wins over menu dismiss while work is running.
			if m.mode == modeBusy {
				m.interruptTurn("interrupted")
				return m, nil
			}
			if m.mode == modeCompacting {
				m.interruptCompaction()
				return m, nil
			}
			if m.slashMenuOpen() {
				m.clearSlashMenu()
				return m, nil
			}
			return m, nil
		case tea.KeyCtrlD:
			if m.mode == modeIdle && strings.TrimSpace(m.textarea.Value()) == "" {
				m.quitting = true
				return m, tea.Quit
			}
		case tea.KeyTab:
			// Complete the selected slash command; never insert a literal tab.
			if m.slashMenuOpen() {
				if cmd, ok := m.selectedSlashCommand(); ok {
					m.applySlashCompletion(cmd)
				}
				return m, nil
			}
		case tea.KeyEnter:
			if m.slashMenuOpen() {
				return m.acceptSlashSelection()
			}
			input := strings.TrimSpace(m.textarea.Value())
			if input == "" {
				return m, nil
			}
			if m.mode != modeIdle {
				// Only clear the composer after a successful enqueue so a full
				// queue does not wipe the draft the user just tried to send.
				return m.queueWhileBusy(input)
			}
			m.textarea.Reset()
			m.clearSlashMenu()
			m.syncComposerHeight()
			return m.submit(input)
		case tea.KeyHome:
			m.viewport.GotoTop()
			m.stickBottom = false
			m.loadOlderTranscript()
			return m, nil
		case tea.KeyEnd:
			m.viewport.GotoBottom()
			m.stickBottom = true
			return m, nil
		case tea.KeyUp:
			if m.slashMenuOpen() {
				if m.slashSel > 0 {
					m.slashSel--
				}
				return m, nil
			}
			if handled, cmd := m.tryHistoryUp(); handled {
				return m, cmd
			}
		case tea.KeyDown:
			if m.slashMenuOpen() {
				if m.slashSel < len(m.slashItems)-1 {
					m.slashSel++
				}
				return m, nil
			}
			if handled, cmd := m.tryHistoryDown(); handled {
				return m, cmd
			}
		}

	case spinner.TickMsg:
		if m.mode != modeIdle {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
		return m, nil

	case statusTickMsg:
		if m.mode != modeIdle {
			return m, tickStatus()
		}
		return m, nil

	case turnChunkMsg:
		if msg.turnID != m.turnID {
			return m, m.nextEventCmd()
		}
		m.appendAssistantChunk(msg.chunk)
		return m, m.nextEventCmd()

	case turnToolStartMsg:
		if msg.turnID != m.turnID {
			return m, m.nextEventCmd()
		}
		m.streamingAssistant = false
		m.currentTool = msg.tool
		m.appendLine(lineTool, formatToolCard(msg.tool, msg.input, "run"))
		key := toolCardKey(msg.callID, msg.tool)
		m.openToolCards[key] = len(m.lines) - 1
		m.openToolNames[key] = msg.tool
		return m, m.nextEventCmd()

	case turnToolEndMsg:
		if msg.turnID != m.turnID {
			return m, m.nextEventCmd()
		}
		m.streamingAssistant = false
		key := toolCardKey(msg.callID, msg.tool)
		card := formatToolCard(msg.tool, msg.output, "ok")
		if !m.updateOpenToolCard(key, card) {
			m.appendLine(lineTool, card)
		}
		delete(m.openToolCards, key)
		delete(m.openToolNames, key)
		m.currentTool = m.firstOpenToolName()
		return m, m.nextEventCmd()

	case turnToolErrorMsg:
		if msg.turnID != m.turnID {
			return m, m.nextEventCmd()
		}
		m.streamingAssistant = false
		key := toolCardKey(msg.callID, msg.tool)
		errText := "tool error"
		if msg.err != nil {
			errText = msg.err.Error()
		}
		card := formatToolCard(msg.tool, errText, "err")
		if !m.updateOpenToolCard(key, card) {
			m.appendLine(lineTool, card)
		}
		delete(m.openToolCards, key)
		delete(m.openToolNames, key)
		m.currentTool = m.firstOpenToolName()
		return m, m.nextEventCmd()

	case turnDoneMsg:
		if msg.turnID != m.turnID {
			return m, nil
		}
		if m.pendingTurnDone == nil {
			// waitTurnEvent may observe done before it has drained buffered stream
			// events. Keep completion pending until the closed event channel is empty.
			pending := msg
			m.pendingTurnDone = &pending
			return m, m.nextEventCmd()
		}
		done := *m.pendingTurnDone
		m.pendingTurnDone = nil
		cmd := m.finishTurn(done.err)
		return m, cmd

	case compactDoneMsg:
		if msg.compactID != m.compactID {
			return m, nil
		}
		return m, m.finishCompaction(msg)
	}

	// Transcript scroll (pgup/pgdn / mouse wheel) before the composer eats keys.
	if isViewportScrollMsg(msg, m.viewport.KeyMap) {
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		m.stickBottom = m.viewport.AtBottom()
		if m.mode == modeIdle && m.viewport.AtTop() {
			m.loadOlderTranscript()
		}
		return m, cmd
	}

	// Composer stays editable while busy so follow-ups can be drafted/queued.
	// Any non-history edit leaves browse mode (shell-style).
	if _, isKey := msg.(tea.KeyMsg); isKey && m.inputHist.browsing() {
		m.inputHist.exitBrowse()
	}
	var taCmd tea.Cmd
	m.textarea, taCmd = m.textarea.Update(msg)
	if taCmd != nil {
		cmds = append(cmds, taCmd)
	}
	heightChanged := m.syncComposerHeight()
	prevMenuH := m.slashMenuHeight()
	m.syncSlashMenu()
	menuChanged := prevMenuH != m.slashMenuHeight()
	if heightChanged || menuChanged {
		// Height change reflows the viewport; keep stickiness semantics.
		m.layout()
		m.refreshViewport()
	}

	return m, tea.Batch(cmds...)
}

func (m *model) View() string {
	if m.quitting {
		return ""
	}

	status := renderStatusBar(m.width, m.statusLabel())
	helpTextLine := "enter send · ↑↓ history · ctrl+j newline · pgup/pgdn scroll · esc interrupt · /help"
	if m.slashMenuOpen() {
		helpTextLine = "↑↓ select · tab complete · enter accept · esc dismiss · ctrl+j newline"
	}
	help := helpStyle.Render(helpTextLine)
	composer := renderComposer(m.width, m.textarea.View())

	// Transcript
	// ────────
	// status
	// [slash menu]
	// ╭ composer ╮
	// help
	parts := []string{m.viewport.View(), status}
	if m.slashMenuOpen() {
		parts = append(parts, renderSlashMenu(m.width, m.slashItems, m.slashSel))
	}
	parts = append(parts, composer, help)
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// statusLabel is the unstyled status text (spinner glyphs included when busy).
func (m *model) statusLabel() string {
	follow := !m.stickBottom && !m.viewport.AtBottom()

	if m.mode == modeIdle {
		parts := collectIdleStatus(m.deps.Session, m.deps.Status.Model, len(m.queue), follow)
		// Leave a little room for bar padding.
		return formatIdleStatus(max(20, m.width-4), parts)
	}

	queueSuffix := ""
	if n := len(m.queue); n > 0 {
		queueSuffix = fmt.Sprintf(" · queued:%d", n)
	}
	followSuffix := ""
	if follow {
		followSuffix = " · ↑ End to follow"
	}
	if m.mode == modeCompacting {
		elapsed := time.Since(m.compactStart).Round(time.Second)
		kind := "manual"
		if m.compactAutomatic {
			kind = "automatic"
		}
		return m.spinner.View() + " " +
			fmt.Sprintf("Compacting context · %s (%s · esc)%s%s", kind, elapsed, queueSuffix, followSuffix)
	}
	activity := "thinking"
	if m.currentTool != "" {
		activity = m.currentTool
	} else if m.streamingAssistant {
		activity = "streaming"
	}
	elapsed := time.Since(m.turnStart).Round(time.Second)
	return m.spinner.View() + " " +
		fmt.Sprintf("Working · %s (%s · esc)%s%s", activity, elapsed, queueSuffix, followSuffix)
}

// statusLine keeps a styled single-line form for tests and lightweight callers.
func (m *model) statusLine() string {
	return statusStyle.Render(m.statusLabel())
}

func (m *model) layout() {
	if m.width <= 0 {
		m.width = 80
	}
	if m.height <= 0 {
		m.height = 24
	}

	// Textarea width inside rounded border + horizontal padding.
	innerW := m.width - 6
	if innerW < 10 {
		innerW = 10
	}
	m.textarea.SetWidth(innerW)
	// viewport = total - composer border - status(rule+line) - help - slash menu
	// border +2; status rule+label +2; help +1; menu rows when open.
	composerHeight := m.textarea.Height() + 2
	reserved := composerHeight + 3 + m.slashMenuHeight()
	h := max(5, m.height-reserved)
	m.viewport.Width = m.width
	m.viewport.Height = h
}

// syncComposerHeight grows/shrinks the textarea with content (clamped).
// Returns true when the height changed (caller should re-layout / refresh).
func (m *model) syncComposerHeight() bool {
	lines := m.textarea.LineCount()
	if strings.TrimSpace(m.textarea.Value()) == "" {
		lines = composerMinHeight
	}
	h := max(composerMinHeight, min(lines, composerMaxHeight))
	if h == m.textarea.Height() {
		return false
	}
	m.textarea.SetHeight(h)
	m.layout()
	return true
}

func (m *model) queueWhileBusy(input string) (tea.Model, tea.Cmd) {
	if isImmediatelyExecutableWhileBusy(input) {
		m.textarea.Reset()
		m.syncComposerHeight()
		// `/queue clear` is intentionally handled here rather than put behind
		// the active operation; queued prompts must not run before it takes effect.
		return m.submit(input)
	}
	if !isQueueableInput(input) {
		m.appendLine(lineError, "cannot queue mutative command while busy (try after the turn, or /queue clear)")
		return m, nil
	}
	next, ok := enqueueFollowUp(m.queue, input)
	if !ok {
		m.appendLine(lineError, fmt.Sprintf("queue full (max %d); wait for the current turn", maxQueue))
		return m, nil
	}
	m.queue = next
	m.inputHist.push(input)
	m.textarea.Reset()
	m.syncComposerHeight()
	m.appendLine(lineSystem, queuedSystemLine(len(m.queue), input))
	return m, nil
}

func (m *model) submit(input string) (tea.Model, tea.Cmd) {
	// New user-visible activity re-anchors the transcript to the bottom.
	m.stickBottom = true
	m.inputHist.push(input)

	action, arg := parseSlash(input)
	switch action {
	case slashHelp:
		m.appendLine(lineSystem, helpText())
		m.appendLine(lineSep, "")
		return m, nil
	case slashExit:
		m.quitting = true
		return m, tea.Quit
	case slashClear:
		return m.cmdClear()
	case slashStatus:
		m.appendLine(lineSystem, m.statusReport())
		m.appendLine(lineSep, "")
		return m, nil
	case slashContext:
		return m.cmdContext(arg)
	case slashCompact:
		return m.cmdCompact(arg)
	case slashNew:
		return m.cmdNew(arg)
	case slashSessions:
		return m.cmdSessions()
	case slashResume:
		return m.cmdResume(arg)
	case slashTitle:
		return m.cmdTitle(arg)
	case slashDelete:
		return m.cmdDelete(arg)
	case slashQueue:
		return m.cmdQueue(arg)
	case slashUnknown:
		m.appendLine(lineError, "unknown command: "+input+"  (try /help)")
		return m, nil
	}

	m.appendLine(lineUser, input)
	m.streamingAssistant = false
	return m.startTurn(input)
}

func (m *model) cmdClear() (tea.Model, tea.Cmd) {
	if m.mode != modeIdle {
		m.appendLine(lineError, "busy: finish or interrupt the current turn first")
		return m, nil
	}
	if m.deps.Session == nil {
		m.appendLine(lineError, "session is unavailable")
		return m, nil
	}
	oldID := m.deps.Session.ID()
	session, err := m.createSession("")
	if err != nil {
		m.appendLine(lineError, "clear context: "+err.Error())
		return m, nil
	}
	m.deps.Session = session
	m.lines = nil
	m.streamingAssistant = false
	m.stickBottom = true
	m.queue = nil
	m.inputHist.clear()
	m.appendLine(lineSystem, "context cleared; new session "+session.ID())
	if oldID != "" {
		m.appendLine(lineSystem, "previous session retained: "+oldID)
	}
	m.appendLine(lineSep, "")
	return m, nil
}

func (m *model) cmdNew(title string) (tea.Model, tea.Cmd) {
	if m.mode != modeIdle {
		m.appendLine(lineError, "busy: finish or interrupt the current turn first")
		return m, nil
	}
	session, err := m.createSession(title)
	if err != nil {
		m.appendLine(lineError, "new session: "+err.Error())
		return m, nil
	}
	m.deps.Session = session
	m.lines = nil
	m.streamingAssistant = false
	m.stickBottom = true
	m.queue = nil
	m.inputHist.clear()
	msg := "new session " + session.ID()
	if session.Title() != "" {
		msg += " · " + session.Title()
	}
	m.appendLine(lineSystem, msg)
	m.appendLine(lineSep, "")
	return m, nil
}

// createSession always creates a distinct conversation. `/clear` uses this
// rather than rewriting the active thread, so raw turns stay resumable.
func (m *model) createSession(title string) (*chat.Session, error) {
	if m.deps.Session == nil {
		return nil, errors.New("session is unavailable")
	}
	model := m.deps.Session.Model()
	if model == nil {
		return nil, errors.New("chat model is unavailable")
	}
	system := m.deps.SystemPrompt
	if system == "" {
		system = m.deps.Session.SystemPrompt()
	}
	opts := m.deps.SessionOpts
	opts.Store = m.deps.Store
	opts.Title = title
	opts.ModelName = m.deps.Status.Model
	opts.ID = ""
	return chat.NewSession(model, system, opts)
}

func (m *model) cmdSessions() (tea.Model, tea.Cmd) {
	if m.deps.Store == nil {
		m.appendLine(lineError, "session store is not configured")
		return m, nil
	}
	list, err := m.deps.Store.ListThreads(m.processCtx())
	if err != nil {
		m.appendLine(lineError, "list sessions: "+err.Error())
		return m, nil
	}
	if len(list) == 0 {
		m.appendLine(lineSystem, "no saved sessions")
		m.appendLine(lineSep, "")
		return m, nil
	}
	var b strings.Builder
	b.WriteString("Sessions (most recent first):\n")
	current := ""
	if m.deps.Session != nil {
		current = m.deps.Session.ID()
	}
	// Cap list length for readability in the viewport.
	const maxList = 30
	n := min(len(list), maxList)
	for i := range n {
		meta := list[i]
		mark := "  "
		if meta.ID == current {
			mark = "* "
		}
		title := meta.Title
		if title == "" {
			title = "(untitled)"
		}
		fmt.Fprintf(&b, "%s%s  %s  msgs=%d  tokens=%s  cost=%s  %s\n",
			mark,
			meta.ID,
			title,
			meta.MessageCount,
			usage.FormatTokens(meta.TotalTokens),
			usage.FormatUSD(meta.CostUSD),
			meta.UpdatedAt.Local().Format("2006-01-02 15:04"),
		)
	}
	if len(list) > maxList {
		fmt.Fprintf(&b, "… and %d more\n", len(list)-maxList)
	}
	b.WriteString("Use /resume <id> or /delete <id>.")
	m.appendLine(lineSystem, strings.TrimRight(b.String(), "\n"))
	m.appendLine(lineSep, "")
	return m, nil
}

func (m *model) cmdResume(id string) (tea.Model, tea.Cmd) {
	if m.mode != modeIdle {
		m.appendLine(lineError, "busy: finish or interrupt the current turn first")
		return m, nil
	}
	id = strings.TrimSpace(id)
	if id == "" {
		m.appendLine(lineError, "usage: /resume <session-id>")
		return m, nil
	}
	if m.deps.Store == nil {
		m.appendLine(lineError, "session store is not configured")
		return m, nil
	}
	model := m.deps.Session.Model()
	if model == nil {
		m.appendLine(lineError, "chat model is unavailable")
		return m, nil
	}
	opts := m.deps.SessionOpts
	opts.Store = m.deps.Store
	opts.ModelName = m.deps.Status.Model
	session, err := chat.OpenSession(model, m.deps.Store, id, opts)
	if err != nil {
		m.appendLine(lineError, "resume: "+err.Error())
		return m, nil
	}
	m.deps.Session = session
	m.streamingAssistant = false
	m.stickBottom = true
	m.queue = nil
	transcript := session.Transcript()
	m.lines = seedLinesFromTranscript(transcript, resumeBanner(session.ID(), len(transcript)), session.Title())
	m.inputHist.seedFromMessages(transcript)
	m.refreshViewport()
	return m, nil
}

func (m *model) cmdTitle(title string) (tea.Model, tea.Cmd) {
	if m.mode != modeIdle {
		m.appendLine(lineError, "busy: finish or interrupt the current turn first")
		return m, nil
	}
	title = strings.TrimSpace(title)
	if title == "" {
		m.appendLine(lineError, "usage: /title <text>")
		return m, nil
	}
	if err := m.deps.Session.SetTitle(m.processCtx(), title); err != nil {
		m.appendLine(lineError, "title: "+err.Error())
		return m, nil
	}
	m.appendLine(lineSystem, "title set to "+title)
	m.appendLine(lineSep, "")
	return m, nil
}

func (m *model) cmdDelete(id string) (tea.Model, tea.Cmd) {
	if m.mode != modeIdle {
		m.appendLine(lineError, "busy: finish or interrupt the current turn first")
		return m, nil
	}
	id = strings.TrimSpace(id)
	if id == "" {
		m.appendLine(lineError, "usage: /delete <session-id>")
		return m, nil
	}
	if m.deps.Store == nil {
		m.appendLine(lineError, "session store is not configured")
		return m, nil
	}
	if m.deps.Session != nil && m.deps.Session.ID() == id {
		m.appendLine(lineError, "cannot delete the active session; /new or /resume another first")
		return m, nil
	}
	if err := m.deps.Store.DeleteThread(m.processCtx(), id); err != nil {
		m.appendLine(lineError, "delete: "+err.Error())
		return m, nil
	}
	m.appendLine(lineSystem, "deleted session "+id)
	m.appendLine(lineSep, "")
	return m, nil
}

func (m *model) cmdContext(arg string) (tea.Model, tea.Cmd) {
	if strings.TrimSpace(arg) != "" {
		m.appendLine(lineError, "usage: /context")
		return m, nil
	}
	if m.deps.Session == nil {
		m.appendLine(lineError, "session is unavailable")
		return m, nil
	}
	status := m.deps.Session.ContextStatus()
	cfg := m.deps.Session.ContextConfig()
	if status.BudgetTokens == 0 {
		status.BudgetTokens = cfg.UsableInputTokens()
		status.TriggerTokens = status.BudgetTokens * cfg.AutoCompactTriggerPercent / 100
		status.TargetTokens = status.BudgetTokens * cfg.PostCompactTargetPercent / 100
	}
	checkpoint := "none"
	if status.ActiveCheckpointID != "" {
		checkpoint = status.ActiveCheckpointID
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Context\n")
	fmt.Fprintf(&b, "budget=%s  output_reserve=%s  trigger=%s  target=%s\n",
		usage.FormatTokens(status.BudgetTokens),
		usage.FormatTokens(cfg.OutputReserveTokens),
		usage.FormatTokens(status.TriggerTokens),
		usage.FormatTokens(status.TargetTokens),
	)
	fmt.Fprintf(&b, "current=%s  source_estimate=%s  hot_groups=%d  omitted_groups=%d\n",
		usage.FormatTokens(status.CurrentTokens),
		usage.FormatTokens(status.OriginalTokens),
		status.HotTurnGroups,
		status.OmittedTurnGroups,
	)
	fmt.Fprintf(&b, "checkpoint=%s  summary_max=%s  auto_paused=%v  low_gain_streak=%d\n",
		checkpoint,
		usage.FormatTokens(cfg.SummaryMaxTokens),
		status.AutoCompactionPaused,
		status.LowGainStreak,
	)
	if len(status.LastFallbacks) > 0 {
		b.WriteString("fallbacks:")
		for _, fallback := range status.LastFallbacks {
			fmt.Fprintf(&b, "\n  %s: %s", fallback.Kind, fallback.Details)
		}
	}
	b.WriteString("\nraw turns and tool artifacts remain retained locally")
	m.appendLine(lineSystem, b.String())
	m.appendLine(lineSep, "")
	return m, nil
}

func (m *model) cmdCompact(focus string) (tea.Model, tea.Cmd) {
	if m.mode != modeIdle {
		m.appendLine(lineError, "busy: finish or interrupt the current operation first")
		return m, nil
	}
	if m.deps.Session == nil {
		m.appendLine(lineError, "session is unavailable")
		return m, nil
	}
	m.appendLine(lineSystem, "compacting context; raw turns are retained")
	return m.startCompaction(strings.TrimSpace(focus), false)
}

func (m *model) cmdQueue(arg string) (tea.Model, tea.Cmd) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		m.appendLine(lineSystem, formatQueueList(m.queue))
		m.appendLine(lineSep, "")
		return m, nil
	}
	switch strings.ToLower(arg) {
	case "clear":
		n := len(m.queue)
		m.queue = nil
		if n == 0 {
			m.appendLine(lineSystem, "queue empty")
		} else {
			m.appendLine(lineSystem, fmt.Sprintf("queue cleared (%d dropped)", n))
		}
		m.appendLine(lineSep, "")
		return m, nil
	default:
		m.appendLine(lineError, "usage: /queue | /queue clear")
		return m, nil
	}
}

func (m *model) startCompaction(focus string, automatic bool) (tea.Model, tea.Cmd) {
	m.compactID++
	compactID := m.compactID
	m.mode = modeCompacting
	m.compactStart = time.Now()
	m.compactAutomatic = automatic
	m.setBusyPlaceholder()
	ctx, cancel := context.WithCancel(m.processCtx())
	m.compactCancel = cancel
	session := m.deps.Session
	return m, tea.Batch(m.spinner.Tick, tickStatus(), func() tea.Msg {
		var (
			result chat.CompactionResult
			err    error
		)
		if automatic {
			result, err = session.CompactAutomatically(ctx)
		} else {
			result, err = session.Compact(ctx, focus)
		}
		return compactDoneMsg{compactID: compactID, automatic: automatic, result: result, err: err}
	})
}

func (m *model) interruptCompaction() {
	if m.compactCancel != nil {
		m.compactCancel()
	}
}

func (m *model) finishCompaction(msg compactDoneMsg) tea.Cmd {
	if m.compactCancel != nil {
		m.compactCancel()
		m.compactCancel = nil
	}
	m.mode = modeIdle
	m.compactAutomatic = false
	m.setIdlePlaceholder()
	m.textarea.Focus()

	if msg.err != nil {
		switch {
		case errors.Is(msg.err, context.Canceled), isCanceled(msg.err):
			m.appendLine(lineSystem, "context compaction interrupted; active checkpoint unchanged")
		case errors.Is(msg.err, chat.ErrNoCompactionCandidates) && msg.automatic:
			// A benign race with a just-finished turn should not look like an error.
			m.appendLine(lineSystem, "automatic context compaction not needed")
		default:
			m.appendLine(lineError, "context compaction: "+msg.err.Error())
		}
	} else {
		kind := "manual"
		if msg.automatic {
			kind = "automatic"
		}
		line := fmt.Sprintf("context compacted (%s): %s; %s -> %s; raw turns retained",
			kind,
			msg.result.CheckpointID,
			usage.FormatTokens(msg.result.BeforeTokens),
			usage.FormatTokens(msg.result.AfterTokens),
		)
		if msg.result.UsedFallback {
			line += "; deterministic fallback"
		}
		if msg.result.AutoPaused {
			line += "; automatic compact paused after low-gain attempts"
		}
		m.appendLine(lineSystem, line)
	}
	m.appendLine(lineSep, "")
	return m.drainQueue()
}

func (m *model) startTurn(input string) (tea.Model, tea.Cmd) {
	m.turnID++
	turnID := m.turnID
	m.mode = modeBusy
	m.turnStart = time.Now()
	m.err = nil
	m.currentTool = ""
	m.stickBottom = true
	m.setBusyPlaceholder()

	// Derive from process context so SIGTERM cancels in-flight turns.
	ctx, cancel := context.WithCancel(m.processCtx())
	m.turnCancel = cancel
	// Buffered enough for tool chatter; sends still block rather than drop.
	m.events = make(chan tea.Msg, 256)
	m.turnDone = make(chan turnDoneMsg, 1)
	m.pendingTurnDone = nil

	session := m.deps.Session
	events := m.events
	done := m.turnDone
	emit := emitFromTurnEvent(ctx, turnID, events)

	go func() {
		err := session.AskWithEvents(ctx, input, nil, emit)
		// Completion has its own slot so a saturated display-event queue cannot
		// strand the TUI in busy mode after cancellation.
		close(events)
		done <- turnDoneMsg{turnID: turnID, err: err}
	}()

	return m, tea.Batch(m.spinner.Tick, tickStatus(), m.nextEventCmd())
}

func (m *model) nextEventCmd() tea.Cmd {
	if m.events == nil && m.turnDone == nil {
		return nil
	}
	return waitTurnEvent(m.events, m.turnDone, m.pendingTurnDone)
}

func (m *model) interruptTurn(reason string) {
	if m.turnCancel != nil {
		m.turnCancel()
	}
	// finishTurn is driven by turnDoneMsg after Ask returns.
	// Queue is intentionally kept so follow-ups still auto-drain.
	_ = reason
}

func (m *model) finishTurn(err error) tea.Cmd {
	if m.turnCancel != nil {
		m.turnCancel()
		m.turnCancel = nil
	}
	m.mode = modeIdle
	m.events = nil
	m.turnDone = nil
	m.pendingTurnDone = nil
	m.streamingAssistant = false
	m.currentTool = ""
	m.openToolCards = make(map[string]int)
	m.openToolNames = make(map[string]string)
	m.setIdlePlaceholder()

	if err != nil {
		if isCanceled(err) {
			m.appendLine(lineSystem, "interrupted")
		} else {
			m.appendLine(lineError, err.Error())
		}
	} else if m.deps.Session != nil {
		if line := formatTurnUsageLine(m.deps.Session); line != "" {
			m.appendLine(lineSystem, line)
		}
	}
	m.appendLine(lineSep, "")
	m.textarea.Focus()
	if err == nil && m.deps.Session != nil && m.deps.Session.NeedsAutoCompaction() {
		m.appendLine(lineSystem, "context pressure reached; compacting before queued follow-ups")
		_, cmd := m.startCompaction("", true)
		return cmd
	}

	return m.drainQueue()
}

// drainQueue auto-sends queued follow-ups after a turn ends.
// Local slash commands are applied immediately; the first real turn returns its cmd.
func (m *model) drainQueue() tea.Cmd {
	for len(m.queue) > 0 {
		next := m.queue[0]
		m.queue = m.queue[1:]
		_, cmd := m.submit(next)
		if m.mode != modeIdle {
			return cmd
		}
		// Local slash consumed; continue draining.
		if m.quitting {
			return tea.Quit
		}
	}
	return nil
}

func (m *model) setBusyPlaceholder() {
	m.textarea.Placeholder = "Message… (Enter queues while working)"
}

func (m *model) setIdlePlaceholder() {
	m.textarea.Placeholder = "Message the assistant…  (/help)"
}

func formatTurnUsageLine(session *chat.Session) string {
	turn := session.LastTurnUsage()
	if turn.TotalTokens == 0 && turn.PromptTokens == 0 && turn.CompletionTokens == 0 {
		return ""
	}
	est := ""
	if turn.Estimated {
		est = " ~est"
	}
	ctxPart := ""
	status := session.ContextStatus()
	if status.BudgetTokens > 0 && status.CurrentTokens > 0 {
		pct := min(100, status.CurrentTokens*100/status.BudgetTokens)
		ctxPart = fmt.Sprintf("  ctx=%d%%", pct)
		if status.OmittedTurnGroups > 0 || len(status.LastFallbacks) > 0 {
			ctxPart += "*"
		}
		if status.CurrentTokens > status.BudgetTokens {
			ctxPart += "!"
		}
	}
	_, _, _, sessionCost, _ := session.UsageTotals()
	return fmt.Sprintf("tokens in=%s out=%s  cost=%s  session=%s%s%s",
		usage.FormatTokens(turn.PromptTokens),
		usage.FormatTokens(turn.CompletionTokens),
		usage.FormatUSD(turn.CostUSD),
		usage.FormatUSD(sessionCost),
		est,
		ctxPart,
	)
}

func (m *model) appendAssistantChunk(chunk string) {
	if !m.streamingAssistant {
		m.lines = append(m.lines, transcriptLine{kind: lineAssistant, text: chunk})
		m.streamingAssistant = true
	} else {
		last := len(m.lines) - 1
		m.lines[last].text += chunk
	}
	m.refreshViewport()
}

// updateOpenToolCard replaces one in-flight tool card in place.
// Returns false when no matching card was found (caller should append).
func (m *model) updateOpenToolCard(key, card string) bool {
	i, ok := m.openToolCards[key]
	if !ok {
		return false
	}
	if i < 0 || i >= len(m.lines) || m.lines[i].kind != lineTool {
		return false
	}
	m.lines[i].text = card
	m.refreshViewport()
	return true
}

func (m *model) firstOpenToolName() string {
	for _, name := range m.openToolNames {
		return name
	}
	return ""
}

func toolCardKey(callID, toolName string) string {
	if strings.TrimSpace(callID) != "" {
		return callID
	}
	// Providers should supply call IDs. The name fallback preserves display for
	// minimal models that only emit one same-named tool at a time.
	return "name:" + toolName
}

func (m *model) appendLine(kind lineKind, text string) {
	m.streamingAssistant = false
	m.lines = append(m.lines, transcriptLine{kind: kind, text: text})
	m.refreshViewport()
}

func (m *model) refreshViewport() {
	var b strings.Builder
	for i, line := range m.lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		switch line.kind {
		case lineUser:
			b.WriteString(renderUser(line.text))
		case lineAssistant:
			// Stream the open assistant line as plain text; completed lines may use markdown.
			streaming := m.streamingAssistant && i == len(m.lines)-1
			b.WriteString(renderAssistant(line.text, m.viewport.Width, streaming))
		case lineTool:
			b.WriteString(renderToolCard(line.text))
		case lineError:
			b.WriteString(renderError(line.text))
		case lineSystem:
			b.WriteString(renderSystem(line.text))
		case lineSep:
			b.WriteString(renderSeparator(m.viewport.Width))
		}
	}
	applyContent(&m.viewport, &m.stickBottom, b.String())
}

// loadOlderTranscript grows the visible replay only when the reader reaches
// its oldest loaded page. The session keeps this separate from model context,
// so scrolling cannot re-inflate the next prompt.
func (m *model) loadOlderTranscript() {
	if m.deps.Session == nil || m.mode != modeIdle {
		return
	}
	page, _, err := m.deps.Session.LoadOlderTranscript(m.processCtx(), 100)
	if err != nil {
		if !errors.Is(err, chat.ErrCompactionStale) {
			m.appendLine(lineError, "load older transcript: "+err.Error())
		}
		return
	}
	if len(page) == 0 {
		return
	}
	transcript := m.deps.Session.Transcript()
	m.lines = seedLinesFromTranscript(transcript, resumeBanner(m.deps.Session.ID(), len(transcript)), m.deps.Session.Title())
	m.inputHist.seedFromMessages(transcript)
	m.refreshViewport()
	m.viewport.GotoTop()
	m.stickBottom = false
}

func (m *model) statusReport() string {
	tools := "none"
	if len(m.deps.Status.Tools) > 0 {
		tools = strings.Join(m.deps.Status.Tools, ", ")
	}
	transcriptCount := 0
	sessionID := "(none)"
	title := ""
	usageLine := ""
	ctxLine := ""
	if m.deps.Session != nil {
		transcriptCount = len(m.deps.Session.Transcript())
		if id := m.deps.Session.ID(); id != "" {
			sessionID = id
		}
		title = m.deps.Session.Title()
		p, c, total, cost, est := m.deps.Session.UsageTotals()
		if total > 0 || p > 0 || c > 0 {
			usageLine = fmt.Sprintf("\ntokens prompt=%s completion=%s total=%s  cost=%s",
				usage.FormatTokens(p), usage.FormatTokens(c), usage.FormatTokens(total), usage.FormatUSD(cost))
			if est {
				usageLine += " (includes estimates)"
			}
		}
		cfg := m.deps.Session.ContextConfig()
		contextStatus := m.deps.Session.ContextStatus()
		if budget := contextStatus.BudgetTokens; budget > 0 {
			if contextStatus.OriginalTokens > 0 || contextStatus.CurrentTokens > 0 {
				pct := 0
				if contextStatus.CurrentTokens > 0 {
					pct = min(100, contextStatus.CurrentTokens*100/budget)
				}
				ctxLine = fmt.Sprintf("\ncontext view=%s/%s (%d%%) source_estimate=%s omitted_groups=%d fallbacks=%d",
					usage.FormatTokens(contextStatus.CurrentTokens),
					usage.FormatTokens(budget),
					pct,
					usage.FormatTokens(contextStatus.OriginalTokens),
					contextStatus.OmittedTurnGroups,
					len(contextStatus.LastFallbacks),
				)
			} else {
				ctxLine = fmt.Sprintf("\ncontext budget=%s keep_recent=%d summary_max=%s",
					usage.FormatTokens(budget),
					cfg.KeepRecentTurns,
					usage.FormatTokens(cfg.SummaryMaxTokens),
				)
			}
		} else if budget := cfg.UsableInputTokens(); budget > 0 {
			ctxLine = fmt.Sprintf("\ncontext budget=%s keep_recent=%d summary_max=%s",
				usage.FormatTokens(budget),
				cfg.KeepRecentTurns,
				usage.FormatTokens(cfg.SummaryMaxTokens),
			)
		}
	}
	modelName := m.deps.Status.Model
	if modelName == "" {
		modelName = "(unknown)"
	}
	report := fmt.Sprintf("model=%s  session=%s  transcript=%d  tools=%s  mode=%s",
		modelName, sessionID, transcriptCount, tools, modeName(m.mode))
	if title != "" {
		report += "  title=" + title
	}
	if m.deps.Status.MaxStep > 0 {
		report += fmt.Sprintf("  max_step=%d", m.deps.Status.MaxStep)
	}
	if n := len(m.queue); n > 0 {
		report += fmt.Sprintf("  queued=%d", n)
	}
	return report + usageLine + ctxLine
}

func modeName(m mode) string {
	switch m {
	case modeBusy:
		return "busy"
	case modeCompacting:
		return "compacting"
	default:
		return "idle"
	}
}

func tickStatus() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return statusTickMsg(t)
	})
}

func isCanceled(err error) bool {
	if err == nil {
		return false
	}
	if err == context.Canceled {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "context canceled")
}
