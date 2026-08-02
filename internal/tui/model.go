package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"eino-local-assistant/internal/chat"
	"eino-local-assistant/internal/memory"
	"eino-local-assistant/internal/runtimeguard"
	"eino-local-assistant/internal/store"
	"eino-local-assistant/internal/tools"
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
	// CmdPolicy is the status-bar fragment like "cmd=ask" or "cmd=auto".
	CmdPolicy string
	// Sandbox supplies compact worker-boundary state for the status bar.
	Sandbox SandboxInfo
	// Runtime supplies per-turn limits for /status.
	Runtime RuntimeInfo
}

// StatusFragment returns compact command-policy and sandbox state.
func (info StatusInfo) StatusFragment() string {
	fragments := make([]string, 0, 4)
	if fragment := strings.TrimSpace(info.CmdPolicy); fragment != "" {
		fragments = append(fragments, fragment)
	}
	fragments = append(fragments, info.Sandbox.statusFragments()...)
	return strings.Join(fragments, " · ")
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
	// ComposeSystemPrompt rebuilds persona+rules+memory when creating a new
	// session (/new, /clear). When nil, SystemPrompt is used as-is.
	// Mid-session /memory, /resume, and /compact do not recompose: the durable
	// thread system prompt is frozen for prefix-cache stability.
	ComposeSystemPrompt func() (string, error)
	// SessionOpts is reused for /new and /resume so pricing/context stay consistent.
	SessionOpts chat.SessionOptions
	Status      StatusInfo
	// TurnOptions enforce the configured total turn deadline and tool-call
	// budget for every interactive turn.
	TurnOptions runtimeguard.TurnOptions
	// HideTurnUsage skips the post-turn API usage footer when true.
	// Default (false) shows the footer. Wired from ui.show_turn_usage.
	HideTurnUsage bool
	// Approval is the TUI bridge for run_command ask decisions. Optional.
	Approval *ApprovalBridge
	// PolicyInfo drives /permissions and the cmd= status badge.
	PolicyInfo CommandPolicyInfo
	// Memory is the project-scoped semantic memory store (optional).
	Memory *memory.Store
	// NotifyActiveSession reports the current thread id to background workers
	// (e.g. memory consolidator). Optional.
	NotifyActiveSession func(sessionID string)
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
	// lineUsage is TUI chrome only (turn token/cost footer). It is never
	// written to the session ledger and never included in the model prompt.
	lineUsage
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
	turnUsage        usage.APIUsage
	turnUsageSeen    bool
	turnUsageCallIDs map[string]struct{}
	compactID        int
	compactCancel    context.CancelFunc
	compactStart     time.Time
	compactAutomatic bool

	streamingAssistant bool
	err                error
	quitting           bool

	// pendingApproval is set while run_command waits for a human decision.
	pendingApproval *approvalRequestMsg
	approvalFocus   int
	approvalScroll  int
	// composerReady is set by newModel once textarea/viewport are constructed.
	// Approval layout must not run against a zero-value model (unit tests).
	composerReady bool
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
		composerReady: true,
	}
	// CLI resume (and any Session with a loaded transcript) must show prior turns.
	// In-TUI /resume uses the same seed helper for a single source of truth.
	if deps.Session != nil {
		transcript := deps.Session.Transcript()
		if hasReplayableTranscript(transcript) {
			m.lines = seedLinesFromTranscript(transcript, resumeBanner(deps.Session.ID(), len(transcript)), deps.Session.Title())
		}
		m.appendLegacyCheckpointResetNotice(deps.Session)
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

	case approvalRequestMsg:
		m.handleApprovalRequest(msg)
		return m, nil

	case approvalCancelMsg:
		m.handleApprovalCancel(msg)
		return m, nil

	case tea.KeyMsg:
		if m.hasPendingApproval() {
			// Approval modal owns keys; Ctrl+C still cancels the whole turn.
			if msg.Type == tea.KeyCtrlC {
				m.clearPendingApproval(tools.ApprovalDeny)
				if m.mode == modeBusy {
					m.interruptTurn("interrupted")
				}
				return m, nil
			}
			return m.handleApprovalKey(msg)
		}
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

	case turnUsageMsg:
		if msg.turnID != m.turnID {
			return m, m.nextEventCmd()
		}
		m.addTurnUsage(msg.usage)
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
	if m.hasPendingApproval() {
		if approvalAllowsSession(m.pendingApproval.Request) {
			helpTextLine = "1 once · 2 session · 3 deny · enter confirm · esc deny"
		} else {
			helpTextLine = "1 once · 2 deny · pgup/pgdn review · enter confirm · esc deny"
		}
	} else if m.slashMenuOpen() {
		helpTextLine = "↑↓ select · tab complete · enter accept · esc dismiss · ctrl+j newline"
	}
	help := helpStyle.Render(helpTextLine)
	composer := renderComposer(m.width, m.textarea.View())

	// Transcript
	// ────────
	// status
	// [approval modal | slash menu]
	// ╭ composer ╮
	// help
	parts := []string{m.viewport.View(), status}
	if m.hasPendingApproval() {
		parts = append(parts, m.approvalModalPage().content)
	} else if m.slashMenuOpen() {
		parts = append(parts, renderSlashMenu(m.width, m.slashItems, m.slashSel))
	}
	parts = append(parts, composer, help)
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// statusLabel is the unstyled status text (spinner glyphs included when busy).
func (m *model) statusLabel() string {
	follow := !m.stickBottom && !m.viewport.AtBottom()
	cmdPolicy := m.statusPolicyFragment()
	extras := collectStatusExtras(m.deps.Session, len(m.queue), follow, cmdPolicy)

	if m.mode == modeIdle {
		parts := collectIdleStatus(m.deps.Session, m.deps.Status.Model, len(m.queue), follow, cmdPolicy)
		// Leave a little room for bar padding.
		return formatIdleStatus(max(20, m.width-4), parts)
	}

	suffix := joinStatusSuffix(extras)
	if m.hasPendingApproval() {
		elapsed := time.Since(m.turnStart).Round(time.Second)
		return m.spinner.View() + " " +
			fmt.Sprintf("Awaiting approval · tool (%s · esc deny)%s", elapsed, suffix)
	}
	if m.mode == modeCompacting {
		elapsed := time.Since(m.compactStart).Round(time.Second)
		kind := "manual"
		if m.compactAutomatic {
			kind = "automatic"
		}
		return m.spinner.View() + " " +
			fmt.Sprintf("Compacting context · %s (%s · esc)%s", kind, elapsed, suffix)
	}
	activity := "thinking"
	if m.currentTool != "" {
		activity = m.currentTool
	} else if m.streamingAssistant {
		activity = "streaming"
	}
	elapsed := time.Since(m.turnStart).Round(time.Second)
	return m.spinner.View() + " " +
		fmt.Sprintf("Working · %s (%s · esc)%s", activity, elapsed, suffix)
}

func (m *model) statusPolicyFragment() string {
	fragments := make([]string, 0, 4)
	command := strings.TrimSpace(m.deps.Status.CmdPolicy)
	if command == "" {
		command = m.deps.PolicyInfo.CmdPolicyFragment()
	}
	if command != "" {
		fragments = append(fragments, command)
	}

	// runTUI historically supplies only Status.CmdPolicy. Keep PolicyInfo as a
	// fallback source for the new sandbox posture without duplicating it when
	// callers pass the same state to both display DTOs.
	sandbox := m.deps.Status.Sandbox
	if !sandbox.Configured() {
		sandbox = m.deps.PolicyInfo.Sandbox
	}
	fragments = append(fragments, sandbox.statusFragments()...)
	return strings.Join(fragments, " · ")
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
	// viewport = total - composer border - status(rule+line) - help - slash/approval
	// border +2; status rule+label +2; help +1; menu rows when open.
	composerHeight := m.textarea.Height() + 2
	extra := m.slashMenuHeight()
	if m.hasPendingApproval() {
		extra = m.approvalModalHeight()
	}
	reserved := composerHeight + 3 + extra
	h := max(5, m.height-reserved)
	m.viewport.Width = m.width
	m.viewport.Height = h
}

func (m *model) approvalModalPage() approvalModalPage {
	if !m.hasPendingApproval() {
		return approvalModalPage{}
	}
	return renderApprovalModalPage(
		m.width,
		m.approvalDetailRows(),
		m.pendingApproval.Request,
		m.approvalFocus,
		m.approvalScroll,
	)
}

func (m *model) approvalModalHeight() int {
	page := m.approvalModalPage()
	if page.content == "" {
		return 0
	}
	return lipgloss.Height(page.content)
}

func (m *model) approvalDetailRows() int {
	if !m.hasPendingApproval() {
		return 0
	}
	width := m.width
	if width <= 0 {
		width = 80
	}
	height := m.height
	if height <= 0 {
		height = 24
	}
	composerHeight := m.textarea.Height() + 2
	if composerHeight <= 2 {
		composerHeight = composerMinHeight + 2
	}
	header, _ := approvalModalSections(width, m.pendingApproval.Request)
	// Keep a usable transcript viewport while reserving border, blank line,
	// choices, footer, and (when needed) the detail-page indicator.
	availableModalRows := max(8, height-composerHeight-3-5)
	fixedRows := len(header) + 6
	return max(1, availableModalRows-fixedRows)
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
	case slashUsage:
		return m.cmdUsage(arg)
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
	case slashPermissions:
		m.appendLine(lineSystem, m.deps.PolicyInfo.FormatPermissions())
		m.appendLine(lineSep, "")
		return m, nil
	case slashMemory:
		return m.cmdMemory(arg)
	case slashUnknown:
		m.appendLine(lineError, "unknown command: "+input+"  (try /help)")
		return m, nil
	}

	m.appendLine(lineUser, input)
	m.streamingAssistant = false
	return m.startTurn(input)
}

// cmdUsage toggles or sets the display-only per-turn API usage footer.
// This never affects the session ledger or model prompt.
func (m *model) cmdUsage(arg string) (tea.Model, tea.Cmd) {
	arg = strings.ToLower(strings.TrimSpace(arg))
	switch arg {
	case "", "toggle":
		m.deps.HideTurnUsage = !m.deps.HideTurnUsage
	case "on", "show", "enable":
		m.deps.HideTurnUsage = false
	case "off", "hide", "disable":
		m.deps.HideTurnUsage = true
	default:
		m.appendLine(lineError, "usage: /usage [on|off|toggle]")
		return m, nil
	}
	state := "on"
	if m.deps.HideTurnUsage {
		state = "off"
	}
	m.appendLine(lineSystem, "turn usage footer: "+state+"  (display only; not sent to the model)")
	m.appendLine(lineSep, "")
	return m, nil
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
	if m.deps.ComposeSystemPrompt != nil {
		rebuilt, err := m.deps.ComposeSystemPrompt()
		if err != nil {
			return nil, fmt.Errorf("compose system prompt: %w", err)
		}
		if strings.TrimSpace(rebuilt) == "" {
			return nil, errors.New("composed system prompt is empty")
		}
		system = rebuilt
		m.deps.SystemPrompt = rebuilt
	}
	if system == "" {
		system = m.deps.Session.SystemPrompt()
	}
	opts := m.deps.SessionOpts
	opts.Store = m.deps.Store
	opts.Title = title
	opts.ModelName = m.deps.Status.Model
	opts.ID = ""
	session, err := chat.NewSession(model, system, opts)
	if err != nil {
		return nil, err
	}
	if m.deps.NotifyActiveSession != nil {
		m.deps.NotifyActiveSession(session.ID())
	}
	return session, nil
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
		apiUsage := usage.APIUsageFromMeta(meta)
		fmt.Fprintf(&b, "%s%s  %s  msgs=%d  %s  %s  %s  %s\n",
			mark,
			meta.ID,
			title,
			meta.MessageCount,
			usage.FormatAPIUsage(apiUsage),
			usage.FormatContextSnapshot(meta.LastContext),
			usage.FormatCostEstimate(apiUsage.CostUSD, apiUsage.Status),
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

func (m *model) cmdResume(arg string) (tea.Model, tea.Cmd) {
	if m.mode != modeIdle {
		m.appendLine(lineError, "busy: finish or interrupt the current turn first")
		return m, nil
	}
	id, recoverInterrupted, ok := parseResumeArgs(arg)
	if !ok {
		m.appendLine(lineError, "usage: /resume <session-id> [--recover]")
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
	// Recovery is deliberately scoped to this command. In particular, a
	// startup --recover must not authorize later plain /resume commands.
	opts.RecoverInterrupted = recoverInterrupted
	session, err := chat.OpenSession(model, m.deps.Store, id, opts)
	if err != nil {
		m.appendLine(lineError, "resume: "+err.Error())
		return m, nil
	}
	m.deps.Session = session
	if m.deps.NotifyActiveSession != nil {
		m.deps.NotifyActiveSession(session.ID())
	}
	// Resume keeps the durable thread system prompt (create-time snapshot).
	m.streamingAssistant = false
	m.stickBottom = true
	m.queue = nil
	transcript := session.Transcript()
	m.lines = seedLinesFromTranscript(transcript, resumeBanner(session.ID(), len(transcript)), session.Title())
	m.appendLegacyCheckpointResetNotice(session)
	m.inputHist.seedFromMessages(transcript)
	m.refreshViewport()
	return m, nil
}

// parseResumeArgs accepts one thread ID and an optional exact trailing
// --recover acknowledgement. Thread IDs cannot contain whitespace.
func parseResumeArgs(arg string) (id string, recoverInterrupted, ok bool) {
	fields := strings.Fields(arg)
	switch len(fields) {
	case 1:
		if fields[0] == "--recover" {
			return "", false, false
		}
		return fields[0], false, true
	case 2:
		if fields[0] != "--recover" && fields[1] == "--recover" {
			return fields[0], true, true
		}
	}
	return "", false, false
}

func (m *model) appendLegacyCheckpointResetNotice(session *chat.Session) {
	if session == nil || !session.CheckpointResetDuringOpen() {
		return
	}
	m.appendLine(lineSystem, "legacy checkpoint reset; raw history retained and prompt rebuilt from source events")
	m.appendLine(lineSep, "")
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
	fmt.Fprintf(&b, "API snapshot: %s\n", usage.FormatContextSnapshot(sessionContextSnapshot(m.deps.Session)))
	b.WriteString("Planner estimate (local truncation/compaction only; not API usage)\n")
	fmt.Fprintf(&b, "budget=%s  max_output=%s  trigger=%s  target=%s\n",
		usage.FormatTokens(status.BudgetTokens),
		usage.FormatTokens(cfg.MaxOutputTokens),
		usage.FormatTokens(status.TriggerTokens),
		usage.FormatTokens(status.TargetTokens),
	)
	fmt.Fprintf(&b, "planned_view=%s  source_estimate=%s  hot_groups=%d  omitted_groups=%d\n",
		usage.FormatTokens(status.CurrentTokens),
		usage.FormatTokens(status.OriginalTokens),
		status.HotTurnGroups,
		status.OmittedTurnGroups,
	)
	fmt.Fprintf(&b, "checkpoint=%s  summary_max=%s  auto_paused=%v\n",
		checkpoint,
		usage.FormatTokens(cfg.SummaryMaxTokens),
		status.AutoCompactionPaused,
	)
	if status.LowGainStreak > 0 {
		fmt.Fprintf(&b, "legacy_low_gain_streak=%d\n", status.LowGainStreak)
	}
	if status.AutoCompactionPauseReason != "" {
		fmt.Fprintf(&b, "auto_pause_reason=%s\n", status.AutoCompactionPauseReason)
	}
	if status.LastCompaction != nil {
		outcome := status.LastCompaction
		fmt.Fprintf(&b, "last_compaction=%s  automatic=%v", outcome.Status, outcome.Automatic)
		if outcome.CheckpointID != "" {
			fmt.Fprintf(&b, "  checkpoint=%s", outcome.CheckpointID)
		}
		if outcome.OperationID != "" {
			fmt.Fprintf(&b, "  operation=%s", outcome.OperationID)
		}
		b.WriteByte('\n')
		if outcome.Reason != "" {
			fmt.Fprintf(&b, "last_compaction_reason=%s\n", outcome.Reason)
		}
	}
	if status.LastCompactionUsage != nil {
		compactionUsage := status.LastCompactionUsage
		fmt.Fprintf(&b, "last_compaction_provider_usage: calls=%d  input=%s  output=%s  total=%s  cache_read=%s  status=%s\n",
			compactionUsage.ModelCallCount,
			usage.FormatTokens(compactionUsage.PromptTokens),
			usage.FormatTokens(compactionUsage.CompletionTokens),
			usage.FormatTokens(compactionUsage.TotalTokens),
			usage.FormatTokens(compactionUsage.CachedTokens),
			compactionUsage.Status,
		)
		b.WriteString("cache_read is provider-reported cache-read input tokens; local planner estimates are not cache telemetry\n")
	}
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
	// Progress is status-bar only (mode=compacting). Do not emit a transcript
	// banner: no-candidate compact finishes as a silent no-op.
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
		case errors.Is(msg.err, chat.ErrNoCompactionCandidates):
			// Benign no-op: every completed turn is still in the hot window or
			// already covered. Manual and automatic paths stay silent so short
			// sessions and auto-compact races do not spam the transcript.
			return m.drainQueue()
		case msg.automatic && errors.Is(msg.err, chat.ErrCompactionStale):
			// A concurrent turn/checkpoint invalidated the frozen candidate. This
			// is not a user-visible failure. Do not immediately retry here: a
			// continuously changing ledger could otherwise create an unbounded,
			// charged provider loop. The next stable turn re-evaluates the signal.
			return m.drainQueue()
		case msg.automatic:
			status := m.deps.Session.ContextStatus()
			line := "automatic context compaction failed; active checkpoint unchanged"
			if errors.Is(msg.err, context.Canceled) || isCanceled(msg.err) {
				line = "automatic context compaction interrupted; active checkpoint unchanged"
				if status.AutoCompactionPaused {
					line += "; automatic compaction paused"
				}
			} else if status.AutoCompactionPaused {
				line += "; automatic compaction paused"
				if status.AutoCompactionPauseReason != "" {
					line += " (" + status.AutoCompactionPauseReason + ")"
				}
				line += "; use /compact [focus] to retry"
			}
			m.appendLine(lineSystem, line)
		case errors.Is(msg.err, context.Canceled), isCanceled(msg.err):
			m.appendLine(lineSystem, "context compaction interrupted; active checkpoint unchanged")
		default:
			m.appendLine(lineError, "context compaction failed; active checkpoint unchanged: "+msg.err.Error())
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

	// Derive from process context so SIGTERM cancels in-flight turns, then add
	// the configured turn deadline and tool-call budget shared by all tools.
	ctx, cancel, err := runtimeguard.WithTurnContext(m.processCtx(), m.deps.TurnOptions)
	if err != nil {
		m.mode = modeIdle
		m.setIdlePlaceholder()
		m.err = fmt.Errorf("configure runtime guard: %w", err)
		return m, nil
	}
	m.turnCancel = cancel
	// Buffered enough for tool chatter; sends still block rather than drop.
	m.events = make(chan tea.Msg, 256)
	m.turnDone = make(chan turnDoneMsg, 1)
	m.pendingTurnDone = nil
	m.turnUsage = usage.APIUsage{Status: store.UsageStatusExact}
	m.turnUsageSeen = false
	m.turnUsageCallIDs = make(map[string]struct{})

	session := m.deps.Session
	events := m.events
	done := m.turnDone
	emit := emitFromTurnEvent(ctx, turnID, events)

	go func() {
		err := session.AskWithEvents(ctx, input, nil, emit)
		if runtimeguard.IsTurnDeadlineExceeded(ctx) {
			err = runtimeguard.ErrTurnDeadlineExceeded
		}
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
	if m.hasPendingApproval() {
		m.clearPendingApproval(tools.ApprovalDeny)
	}
	if m.turnCancel != nil {
		m.turnCancel()
	}
	// finishTurn is driven by turnDoneMsg after Ask returns.
	// Queue is intentionally kept so follow-ups still auto-drain.
	_ = reason
}

func (m *model) finishTurn(err error) tea.Cmd {
	if m.hasPendingApproval() {
		m.clearPendingApproval(tools.ApprovalDeny)
	}
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
		if errors.Is(err, runtimeguard.ErrTurnDeadlineExceeded) {
			m.appendLine(lineSystem, runtimeguard.TurnTimeoutReason)
		} else if isCanceled(err) {
			m.appendLine(lineSystem, "interrupted")
		} else {
			m.appendLine(lineError, err.Error())
		}
	}
	// Turn token/cost footer is display-only chrome (lineUsage). It must never
	// enter the session ledger or model prompt. Context stays on the status bar.
	// Gated by ui.show_turn_usage.
	if !m.deps.HideTurnUsage {
		if line := formatTurnUsageLine(m.turnUsage, m.turnUsageSeen); line != "" {
			m.appendLine(lineUsage, line)
		}
	}
	m.appendLine(lineSep, "")
	m.textarea.Focus()
	if err == nil && m.deps.Session != nil && m.deps.Session.NeedsAutoCompaction() {
		// Status bar shows mode=compacting; only a successful install is logged.
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

func (m *model) addTurnUsage(event chat.ModelUsageEvent) {
	callID := strings.TrimSpace(event.CallID)
	if callID != "" {
		if m.turnUsageCallIDs == nil {
			m.turnUsageCallIDs = make(map[string]struct{})
		}
		if _, seen := m.turnUsageCallIDs[callID]; seen {
			return
		}
		m.turnUsageCallIDs[callID] = struct{}{}
	}
	m.turnUsageSeen = true
	m.turnUsage.CallCount++
	if !event.Available {
		m.turnUsage.Status = store.UsageStatusIncomplete
		return
	}
	m.turnUsage.PromptTokens += event.Usage.PromptTokens
	m.turnUsage.CompletionTokens += event.Usage.CompletionTokens
	m.turnUsage.CachedTokens += event.Usage.CachedTokens
	m.turnUsage.TotalTokens += event.Usage.TotalTokens
	m.turnUsage.CostUSD += event.Usage.CostUSD
}

// formatTurnUsageLine is the post-turn footer: API usage + turn cost.
// Display-only: callers must append as lineUsage and never persist this text
// into Session/store messages. Context belongs on the global status bar.
func formatTurnUsageLine(turn usage.APIUsage, seen bool) string {
	if !seen {
		return ""
	}
	return usage.FormatAPIUsage(turn) + "  turn " +
		usage.FormatCostEstimate(turn.CostUSD, turn.Status)
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
			b.WriteString(renderError(line.text, m.viewport.Width))
		case lineSystem, lineUsage:
			// lineUsage shares system styling but is never part of model context.
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
		apiUsage := sessionAPIUsage(m.deps.Session)
		usageLine = "\n" + usage.FormatAPIUsage(apiUsage) + "  " +
			usage.FormatCostEstimate(apiUsage.CostUSD, apiUsage.Status)
		ctxLine = "\n" + usage.FormatContextSnapshot(sessionContextSnapshot(m.deps.Session))
		cfg := m.deps.Session.ContextConfig()
		contextStatus := m.deps.Session.ContextStatus()
		if budget := contextStatus.BudgetTokens; budget > 0 {
			if contextStatus.OriginalTokens > 0 || contextStatus.CurrentTokens > 0 {
				pct := 0
				if contextStatus.CurrentTokens > 0 {
					pct = contextStatus.CurrentTokens * 100 / budget
				}
				ctxLine += fmt.Sprintf("\ncontext planner estimate: view=%s/%s (%d%%) source_estimate=%s omitted_groups=%d fallbacks=%d",
					usage.FormatTokens(contextStatus.CurrentTokens),
					usage.FormatTokens(budget),
					pct,
					usage.FormatTokens(contextStatus.OriginalTokens),
					contextStatus.OmittedTurnGroups,
					len(contextStatus.LastFallbacks),
				)
			} else {
				ctxLine += fmt.Sprintf("\ncontext planner estimate: budget=%s keep_recent=%d summary_max=%s",
					usage.FormatTokens(budget),
					cfg.KeepRecentTurns,
					usage.FormatTokens(cfg.SummaryMaxTokens),
				)
			}
		} else if budget := cfg.UsableInputTokens(); budget > 0 {
			ctxLine += fmt.Sprintf("\ncontext planner estimate: budget=%s keep_recent=%d summary_max=%s",
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
	turnUsage := "on"
	if m.deps.HideTurnUsage {
		turnUsage = "off"
	}
	report := fmt.Sprintf("model=%s  session=%s  transcript=%d  tools=%s  mode=%s  turn_usage=%s",
		modelName, sessionID, transcriptCount, tools, modeName(m.mode), turnUsage)
	if title != "" {
		report += "  title=" + title
	}
	if m.deps.Status.MaxStep > 0 {
		report += fmt.Sprintf("  max_step=%d", m.deps.Status.MaxStep)
	}
	if frag := m.statusPolicyFragment(); frag != "" {
		report += "  " + frag
	}
	runtime := m.deps.Status.Runtime
	if !runtime.Configured() {
		runtime = m.deps.PolicyInfo.Runtime
	}
	if runtime.MaxTurnSeconds > 0 {
		report += fmt.Sprintf("  max_turn_seconds=%d", runtime.MaxTurnSeconds)
	}
	if runtime.MaxReactSteps > 0 {
		report += fmt.Sprintf("  max_react_steps=%d", runtime.MaxReactSteps)
	}
	if runtime.MaxToolCalls > 0 {
		report += fmt.Sprintf("  max_tool_calls=%d", runtime.MaxToolCalls)
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
