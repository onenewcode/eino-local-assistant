package tui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
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
	// RulesReport returns the active session's captured instruction metadata.
	// It must be read-only: /rules never asks the callback to reload files.
	RulesReport func() string
	// InvalidateRulesSnapshot marks provenance unavailable after /resume.
	InvalidateRulesSnapshot func()
	// SideQuestion answers a temporary side question without writing the main
	// session ledger or changing the active turn.
	SideQuestion func(context.Context, *chat.Session, string) (string, error)
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
	// folded is set when a lineReasoning block was collapsed to a one-line summary.
	// Display-only; never enters the session ledger.
	folded bool
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
	// lineReasoning is ephemeral model reasoning summary for this process only.
	lineReasoning
	// lineSide is display-only output from /btw and /side.
	lineSide
	lineSep
)

const (
	// Start as a single-line Claude/Codex-style input; grows with content.
	composerMinHeight         = 1
	composerMaxHeight         = 8
	interruptRequestedMessage = "interrupt requested; waiting for turn cleanup"
)

type model struct {
	deps Deps

	width  int
	height int

	viewport viewport.Model
	textarea textarea.Model
	spinner  spinner.Model

	lines []transcriptLine
	// sideLines stay separate from the main transcript and turn stream.
	sideLines []transcriptLine

	// stickBottom auto-follows new transcript content while the user is at the bottom.
	stickBottom bool
	// currentTool is the in-flight tool name for the status bar (cleared after tool ends).
	currentTool string
	// openToolCards maps stable tool call IDs to in-flight transcript cards.
	openToolCards map[string]int
	openToolNames map[string]string
	// queue holds follow-ups submitted while a turn is running (FIFO).
	queue []string
	// queuePaused blocks automatic FIFO promotion after a non-cancel turn error.
	// It is process-local; /queue resume explicitly clears it.
	queuePaused bool
	// inputHist is shell-style Up/Down composer history for this TUI process.
	inputHist inputHistory
	// slashItems is the live prefix-filtered command menu (empty => closed).
	slashItems []slashCommand
	slashSel   int
	// taskPaneOpen exposes a compact, read-only task projection without making
	// the controller's internal graph part of the command surface.
	taskPaneOpen bool

	// backtrackState is a pending source-preserving fork selection. It is kept
	// separate from the durable session until the user confirms a prompt.
	backtrackState      backtrackState
	backtrackGeneration uint64
	// sideQuestions is kept as a cheap count for UI gating. The set gives each
	// callback an identity so a late result can only reconcile its own request.
	sideQuestions       int
	sideQuestionPending map[uint64]struct{}
	sideQuestionNextID  uint64

	mode              mode
	sessionMu         sync.RWMutex
	sessionGeneration uint64
	turnID            int
	// steerSession is a TUI-local compatibility seam for the optional core
	// steering API. Production uses the active *chat.Session assertion below;
	// tests can exercise the TUI without modifying or faking internal/chat.
	steerSession interface {
		ActiveTurnID() (string, bool)
		Steer(context.Context, string, string) error
	}
	turnCancel             context.CancelFunc
	interruptFeedbackShown bool
	turnStart              time.Time
	events                 chan tea.Msg
	turnDone               chan turnDoneMsg
	pendingTurnDone        *turnDoneMsg
	turnUsage              usage.APIUsage
	turnUsageSeen          bool
	turnUsageCallIDs       map[string]struct{}
	// turnSteerAdmitted counts successful /steer admissions for the active turn.
	// turnSteerAdmissions retains only receipts from the additive API so an
	// interrupted turn can restore unconfirmed text without guessing by content.
	turnSteerAdmissions map[uint64]string
	// consumed sequences are reported only after the model reaches a safe call
	// boundary, so late inputs can be settled separately at turn completion.
	turnSteerAdmitted int
	turnSteerConsumed map[uint64]struct{}
	compactID         int
	compactCancel     context.CancelFunc
	compactStart      time.Time
	compactAutomatic  bool

	streamingAssistant bool
	// openReasoning is the index of the in-flight reasoning line, or noOpenReasoning.
	openReasoning int
	err           error
	quitting      bool

	// pendingApproval is set while run_command waits for a human decision.
	pendingApproval *approvalRequestMsg
	approvalFocus   int
	approvalScroll  int
	// composerReady is set by newModel once textarea/viewport are constructed.
	// Approval layout must not run against a zero-value model (unit tests).
	composerReady bool
}

func (m *model) clearBacktrack() {
	m.backtrackState = backtrackState{mode: backtrackInactive}
	m.backtrackGeneration = 0
}

func (m *model) refreshBacktrackChrome() {
	m.layout()
	m.refreshViewport()
}

func (m *model) backtrackStatus() string {
	switch m.backtrackState.mode {
	case backtrackArmed:
		return "backtrack: armed"
	case backtrackSelecting:
		return "backtrack: selecting"
	default:
		return ""
	}
}

func (m *model) rejectBacktrack(message string) {
	m.clearBacktrack()
	m.refreshBacktrackChrome()
	m.appendLine(lineError, message)
}

func (m *model) armBacktrack() {
	m.clearBacktrack()
	m.backtrackState.mode = backtrackArmed
	m.refreshBacktrackChrome()
}

func (m *model) openBacktrack() {
	if m.mode != modeIdle {
		m.rejectBacktrack("busy: finish or interrupt the current operation first")
		return
	}
	if m.hasPendingApproval() {
		m.rejectBacktrack("busy: resolve the pending approval first")
		return
	}
	if m.sideQuestions > 0 {
		m.rejectBacktrack("busy: wait for the side question to finish first")
		return
	}
	source, generation := m.activeSessionSnapshot()
	if source == nil {
		m.rejectBacktrack("backtrack: session is unavailable")
		return
	}
	repository := m.deps.Store
	if repository == nil {
		repository = source.Store()
	}
	if repository == nil {
		m.rejectBacktrack("backtrack: session store is unavailable")
		return
	}
	groups, err := repository.LoadTurnGroups(m.processCtx(), source.ID())
	if err != nil {
		m.rejectBacktrack("backtrack: load turn groups: " + err.Error())
		return
	}
	prompts := buildBacktrackPrompts(groups)
	if len(prompts) == 0 {
		m.rejectBacktrack("backtrack: no earlier committed prompt is available")
		return
	}
	state := newBacktrackState(prompts)
	state.mode = backtrackSelecting
	m.backtrackState = state
	m.backtrackGeneration = generation
	// The backtrack selector owns the rows above the composer.
	m.clearSlashMenu()
	m.refreshBacktrackChrome()
}

func (m *model) handleBacktrackKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.backtrackState.mode != backtrackSelecting {
		return m, nil
	}
	switch {
	case msg.Type == tea.KeyEsc:
		m.clearBacktrack()
		m.refreshBacktrackChrome()
		return m, nil
	case msg.Type == tea.KeyUp || backtrackKeyRune(msg, 'k'):
		m.backtrackState = moveBacktrackSelection(m.backtrackState, -1)
		m.refreshBacktrackChrome()
		return m, nil
	case msg.Type == tea.KeyDown || backtrackKeyRune(msg, 'j'):
		m.backtrackState = moveBacktrackSelection(m.backtrackState, 1)
		m.refreshBacktrackChrome()
		return m, nil
	case msg.Type == tea.KeyEnter:
		return m.submitBacktrack()
	default:
		return m, nil
	}
}

func backtrackKeyRune(msg tea.KeyMsg, want rune) bool {
	return msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == want
}

func (m *model) submitBacktrack() (tea.Model, tea.Cmd) {
	selected, ok := selectedBacktrackPrompt(m.backtrackState)
	if !ok {
		m.rejectBacktrack("backtrack: no prompt is selected")
		return m, nil
	}
	// Keep the prompt in the composer when the durable fork or child open fails.
	setSelectedPrompt := func() {
		m.textarea.SetValue(selected.Text)
		m.textarea.CursorEnd()
		m.clearSlashMenu()
		m.syncComposerHeight()
		m.refreshBacktrackChrome()
	}
	if m.mode != modeIdle {
		setSelectedPrompt()
		m.rejectBacktrack("busy: finish or interrupt the current operation first")
		return m, nil
	}
	if m.hasPendingApproval() {
		setSelectedPrompt()
		m.rejectBacktrack("busy: resolve the pending approval first")
		return m, nil
	}
	if m.sideQuestions > 0 {
		setSelectedPrompt()
		m.rejectBacktrack("busy: wait for the side question to finish first")
		return m, nil
	}
	source, generation := m.activeSessionSnapshot()
	if source == nil {
		setSelectedPrompt()
		m.rejectBacktrack("backtrack: session is unavailable")
		return m, nil
	}
	if generation != m.backtrackGeneration {
		setSelectedPrompt()
		m.rejectBacktrack("backtrack: active session changed; choose a prompt again")
		return m, nil
	}
	var child *chat.Session
	var result store.ForkResult
	var err error
	if selected.BeforeFirst {
		child, result, err = source.ForkBeforeFirstTurn(m.processCtx(), "")
	} else {
		child, result, err = source.Fork(m.processCtx(), "", selected.BoundaryTurnID)
	}
	if err != nil {
		setSelectedPrompt()
		m.rejectBacktrack("backtrack: " + err.Error())
		return m, nil
	}
	if child == nil {
		setSelectedPrompt()
		m.rejectBacktrack("backtrack: child session is unavailable")
		return m, nil
	}
	childID := child.ID()
	if childID == "" {
		childID = result.ChildID
	}
	if childID == "" {
		setSelectedPrompt()
		m.rejectBacktrack("backtrack: child session has no ID")
		return m, nil
	}
	m.activateForkChild(source, child, result)
	setSelectedPrompt()
	return m, nil
}

func (m *model) beginSideQuestion() uint64 {
	m.sideQuestionNextID++
	if m.sideQuestionNextID == 0 {
		// Keep zero reserved for hand-built legacy messages.
		m.sideQuestionNextID++
	}
	if m.sideQuestionPending == nil {
		m.sideQuestionPending = make(map[uint64]struct{})
	}
	requestID := m.sideQuestionNextID
	m.sideQuestionPending[requestID] = struct{}{}
	m.sideQuestions++
	return requestID
}

// discardSideQuestion reconciles one callback result at most once. Runtime
// messages always carry a request ID; the zero-ID fallback keeps hand-built
// test messages compatible with the old counter-only representation.
func (m *model) discardSideQuestion(requestID uint64) bool {
	if requestID == 0 {
		if m.sideQuestions == 0 {
			return false
		}
		m.sideQuestions--
		return true
	}
	if _, ok := m.sideQuestionPending[requestID]; !ok {
		return false
	}
	delete(m.sideQuestionPending, requestID)
	if m.sideQuestions > 0 {
		m.sideQuestions--
	}
	return true
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

func (m *model) activeSessionSnapshot() (*chat.Session, uint64) {
	m.sessionMu.RLock()
	defer m.sessionMu.RUnlock()
	return m.deps.Session, m.sessionGeneration
}

func (m *model) activeSession() *chat.Session {
	session, _ := m.activeSessionSnapshot()
	return session
}

func (m *model) replaceSession(session *chat.Session) {
	m.sessionMu.Lock()
	m.deps.Session = session
	m.sessionGeneration++
	m.sessionMu.Unlock()
	m.interruptFeedbackShown = false
	m.clearBacktrack()
}

// resetSessionTransientState clears process-local state that cannot cross a
// durable session boundary. The caller reseeds lines and input history after
// switching to the new session.
func (m *model) resetSessionTransientState() {
	if m.turnCancel != nil {
		m.turnCancel()
	}
	m.turnCancel = nil
	if m.compactCancel != nil {
		m.compactCancel()
	}
	m.compactCancel = nil
	m.compactID++
	m.compactAutomatic = false
	m.mode = modeIdle
	m.events = nil
	m.turnDone = nil
	m.pendingTurnDone = nil
	m.err = nil
	m.streamingAssistant = false
	m.currentTool = ""
	m.openReasoning = noOpenReasoning
	m.openToolCards = make(map[string]int)
	m.openToolNames = make(map[string]string)
	m.turnUsage = usage.APIUsage{}
	m.turnUsageSeen = false
	m.turnUsageCallIDs = nil
	m.turnSteerAdmitted = 0
	m.turnSteerAdmissions = nil
	m.turnSteerConsumed = nil
	m.interruptFeedbackShown = false
	m.sideQuestions = 0
	m.sideQuestionPending = nil
	m.sideLines = nil
	m.queue = nil
	m.queuePaused = false
	m.clearBacktrack()
	m.clearSlashMenu()
	m.closeTaskPane()
	m.stickBottom = true
	m.setIdlePlaceholder()
	m.textarea.Focus()
}

func (m *model) activateForkChild(source, child *chat.Session, result store.ForkResult) {
	childID := child.ID()
	if childID == "" {
		childID = result.ChildID
	}
	sourceID := ""
	if source != nil {
		sourceID = source.ID()
	}
	transcript := child.Transcript()
	m.replaceSession(child)
	// Invalidate the old turn identity before painting the child so late stream
	// messages cannot populate the new transcript.
	m.turnID++
	m.resetSessionTransientState()
	m.lines = seedLinesFromTranscript(transcript, forkBanner(childID, sourceID, result.LastTurnID, len(transcript)), child.Title())
	m.inputHist.seedFromMessages(transcript)
	m.refreshViewport()
	if m.deps.NotifyActiveSession != nil {
		m.deps.NotifyActiveSession(childID)
	}
}

func (m *model) acceptsTurn(turnID int, sessionGeneration uint64) bool {
	// Zero generation is retained for hand-built legacy test messages. Runtime
	// turn messages always carry the non-zero generation captured at start.
	return turnID == m.turnID && (sessionGeneration == 0 || sessionGeneration == m.sessionGeneration)
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
		deps:              deps,
		viewport:          vp,
		textarea:          ta,
		spinner:           sp,
		stickBottom:       true,
		inputHist:         newInputHistory(),
		openToolCards:     make(map[string]int),
		openToolNames:     make(map[string]string),
		openReasoning:     noOpenReasoning,
		sessionGeneration: 1,
		composerReady:     true,
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
		if m.backtrackState.mode != backtrackInactive {
			m.clearBacktrack()
			m.refreshBacktrackChrome()
		}
		m.handleApprovalRequest(msg)
		return m, nil

	case approvalCancelMsg:
		m.handleApprovalCancel(msg)
		return m, nil

	case tea.KeyMsg:
		if m.hasPendingApproval() {
			m.clearBacktrack()
			// Approval modal owns keys; Ctrl+C still cancels the whole turn.
			if msg.Type == tea.KeyCtrlC {
				m.clearPendingApproval(tools.ApprovalDeny)
				if m.mode == modeBusy {
					m.interruptTurn("interrupted")
					m.cancelActiveTask("user interrupted the active turn")
					m.showInterruptRequested()
				}
				return m, nil
			}
			return m.handleApprovalKey(msg)
		}
		// Busy/compacting Esc keeps its existing interrupt contract even if a
		// stale backtrack state was left by an embedding caller.
		if msg.Type == tea.KeyEsc && m.mode == modeBusy {
			m.interruptTurn("interrupted")
			m.cancelActiveTask("user interrupted the active turn")
			m.showInterruptRequested()
			return m, nil
		}
		if msg.Type == tea.KeyEsc && m.mode == modeCompacting {
			m.interruptCompaction()
			return m, nil
		}
		if m.backtrackState.mode == backtrackSelecting {
			return m.handleBacktrackKey(msg)
		}
		if m.backtrackState.mode == backtrackArmed && msg.Type != tea.KeyEsc {
			m.clearBacktrack()
			m.refreshBacktrackChrome()
		}
		switch msg.Type {
		case tea.KeyCtrlC:
			if m.mode == modeBusy {
				m.interruptTurn("interrupted")
				m.cancelActiveTask("user interrupted the active turn")
				m.showInterruptRequested()
				return m, nil
			}
			if m.mode == modeCompacting {
				m.interruptCompaction()
				return m, nil
			}
			m.quitting = true
			return m, tea.Quit
		case tea.KeyEsc:
			if m.slashMenuOpen() {
				m.clearSlashMenu()
				return m, nil
			}
			if m.sideQuestions > 0 {
				m.rejectBacktrack("busy: wait for the side question to finish first")
				return m, nil
			}
			if m.textarea.Value() != "" {
				return m, nil
			}
			if m.backtrackState.mode == backtrackArmed {
				m.openBacktrack()
				return m, nil
			}
			m.armBacktrack()
			return m, nil
		case tea.KeyCtrlD:
			if m.mode == modeIdle && strings.TrimSpace(m.textarea.Value()) == "" {
				m.quitting = true
				return m, tea.Quit
			}
		case tea.KeyCtrlT:
			if m.toggleTaskPane() {
				m.layout()
				m.refreshViewport()
			}
			return m, nil
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
		if !m.acceptsTurn(msg.turnID, msg.sessionGeneration) {
			return m, m.nextEventCmd()
		}
		// Do not fold reasoning here: content and reasoning for the final
		// model call can arrive concurrently from different goroutines.
		m.appendAssistantChunk(msg.chunk)
		return m, m.nextEventCmd()

	case turnReasoningMsg:
		if !m.acceptsTurn(msg.turnID, msg.sessionGeneration) {
			return m, m.nextEventCmd()
		}
		m.appendReasoningChunk(msg.chunk)
		return m, m.nextEventCmd()

	case turnSteerConsumedMsg:
		if !m.acceptsTurn(msg.turnID, msg.sessionGeneration) {
			return m, m.nextEventCmd()
		}
		if m.turnSteerConsumed == nil {
			m.turnSteerConsumed = make(map[uint64]struct{})
		}
		if _, seen := m.turnSteerConsumed[msg.sequence]; seen {
			return m, m.nextEventCmd()
		}
		m.turnSteerConsumed[msg.sequence] = struct{}{}
		m.appendLine(lineSystem, fmt.Sprintf("steer consumed at model boundary (#%d): %s", msg.sequence, msg.content))
		return m, m.nextEventCmd()

	case turnToolStartMsg:
		if !m.acceptsTurn(msg.turnID, msg.sessionGeneration) {
			return m, m.nextEventCmd()
		}
		// appendLine folds any open reasoning before the tool card.
		m.currentTool = msg.tool
		m.appendLine(lineTool, formatToolCard(msg.tool, msg.input, "run"))
		key := toolCardKey(msg.callID, msg.tool)
		m.openToolCards[key] = len(m.lines) - 1
		m.openToolNames[key] = msg.tool
		return m, m.nextEventCmd()

	case turnToolEndMsg:
		if !m.acceptsTurn(msg.turnID, msg.sessionGeneration) {
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
		if !m.acceptsTurn(msg.turnID, msg.sessionGeneration) {
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
		if !m.acceptsTurn(msg.turnID, msg.sessionGeneration) {
			return m, m.nextEventCmd()
		}
		m.addTurnUsage(msg.usage)
		return m, m.nextEventCmd()

	case turnTaskGateMsg:
		if !m.acceptsTurn(msg.turnID, msg.sessionGeneration) {
			return m, m.nextEventCmd()
		}
		summary := strings.TrimSpace(msg.gate.Summary)
		if summary == "" {
			summary = "controller rejected completion"
		}
		m.appendLine(lineSystem, "task completion check: "+summary+"; continuing")
		return m, m.nextEventCmd()

	case turnDoneMsg:
		if !m.acceptsTurn(msg.turnID, msg.sessionGeneration) {
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
		m.foldOpenReasoning()
		cmd := m.finishTurn(done.err)
		return m, cmd

	case sideQuestionDoneMsg:
		if !m.discardSideQuestion(msg.requestID) {
			return m, nil
		}
		current, generation := m.activeSessionSnapshot()
		if msg.sessionGeneration != 0 && msg.sessionGeneration != generation {
			return m, nil
		}
		if msg.sessionID != "" && (current == nil || msg.sessionID != current.ID()) {
			return m, nil
		}
		m.finishSideQuestion(msg)
		return m, nil

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
	helpTextLine := "enter send · ↑↓ history · ctrl+t tasks · esc interrupt · /help"
	if m.hasPendingApproval() {
		if approvalAllowsSession(m.pendingApproval.Request) {
			helpTextLine = "1 once · 2 session · 3 deny · enter confirm · esc deny"
		} else {
			helpTextLine = "1 once · 2 deny · pgup/pgdn review · enter confirm · esc deny"
		}
	} else if m.slashMenuOpen() {
		helpTextLine = "↑↓ select · tab complete · enter accept · esc dismiss · ctrl+j newline"
	} else if m.backtrackState.mode == backtrackSelecting {
		helpTextLine = "↑↓/jk select prompt · enter fork before prompt · esc cancel"
	} else if m.backtrackState.mode == backtrackArmed {
		helpTextLine = "esc backtrack history · edit or submit to cancel · /help"
	} else if m.taskPaneOpen {
		helpTextLine = "ctrl+t hide task progress · esc interrupt · /help"
	}
	help := helpStyle.Render(helpTextLine)
	composer := renderComposer(m.width, m.textarea.View())

	// Transcript
	// ────────
	// status
	// [task progress]
	// [approval modal | slash menu]
	// ╭ composer ╮
	// help
	parts := []string{m.viewport.View(), status}
	if m.taskPaneOpen {
		parts = append(parts, m.taskPaneView())
	}
	if m.hasPendingApproval() {
		parts = append(parts, m.approvalModalPage().content)
	} else if m.slashMenuOpen() {
		parts = append(parts, renderSlashMenu(m.width, m.slashItems, m.slashSel))
	} else if m.backtrackState.mode == backtrackSelecting {
		parts = append(parts, m.backtrackOverlayView())
	}
	parts = append(parts, composer, help)
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// statusLabel is the unstyled status text (spinner glyphs included when busy).
func (m *model) statusLabel() string {
	follow := !m.stickBottom && !m.viewport.AtBottom()
	cmdPolicy := m.statusPolicyFragment()
	session := m.activeSession()
	extras := collectStatusExtras(session, len(m.queue), follow, cmdPolicy)
	if m.queuePaused {
		extras.paused = "queue:paused"
	}

	if m.mode == modeIdle {
		parts := collectIdleStatus(session, m.deps.Status.Model, len(m.queue), follow, cmdPolicy)
		if m.queuePaused {
			parts.paused = "queue:paused"
		}
		// Leave a little room for bar padding.
		label := formatIdleStatus(max(20, m.width-4), parts)
		if backtrack := m.backtrackStatus(); backtrack != "" {
			label += " · " + backtrack
		}
		return label
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
	if m.mode == modeBusy && m.interruptFeedbackShown {
		elapsed := time.Since(m.turnStart).Round(time.Second)
		return m.spinner.View() + " " +
			fmt.Sprintf("Stopping · waiting for turn cleanup (%s · esc)%s", elapsed, suffix)
	}
	activity := "thinking"
	if m.currentTool != "" {
		activity = m.currentTool
	} else if m.streamingAssistant {
		activity = "streaming"
	}
	// Open reasoning keeps the default "thinking" activity label.
	elapsed := time.Since(m.turnStart).Round(time.Second)
	return m.spinner.View() + " " +
		fmt.Sprintf("Working · %s (%s · esc)%s", activity, elapsed, suffix)
}

func (m *model) statusPolicyFragment() string {
	fragments := make([]string, 0, 4)
	command := ""
	if m.deps.PolicyInfo.ApprovalState != nil {
		command = m.deps.PolicyInfo.CmdPolicyFragment()
	} else {
		command = strings.TrimSpace(m.deps.Status.CmdPolicy)
	}
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
	// viewport = total - composer border - status(rule+line) - help - optional
	// task pane - slash/approval/backtrack. Border +2; status rule+label +2; help +1.
	composerHeight := m.textarea.Height() + 2
	extra := m.taskPaneHeight() + m.slashMenuHeight() + m.backtrackOverlayHeight()
	if m.hasPendingApproval() {
		extra = m.taskPaneHeight() + m.approvalModalHeight()
	}
	reserved := composerHeight + 3 + extra
	h := max(5, m.height-reserved)
	m.viewport.Width = m.width
	m.viewport.Height = h
}

func (m *model) backtrackOverlayHeight() int {
	if m.backtrackState.mode != backtrackSelecting {
		return 0
	}
	return backtrackOverlayHeight(m.backtrackState)
}

func (m *model) backtrackOverlayView() string {
	if m.backtrackState.mode != backtrackSelecting {
		return ""
	}
	width := m.width
	if width <= 0 {
		width = 80
	}
	return renderBacktrackOverlay(width, m.backtrackState)
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
	if action, arg := parseSlash(input); action == slashSteer {
		m.textarea.Reset()
		m.syncComposerHeight()
		return m.cmdSteer(arg)
	}
	if isImmediatelyExecutableWhileBusy(input) {
		m.textarea.Reset()
		m.syncComposerHeight()
		// Queue control is intentionally handled here rather than put behind the
		// active operation; queued prompts must not run before it takes effect.
		return m.submit(input)
	}
	if action, _ := parseSlash(input); action == slashPermissions {
		m.appendLine(lineError, "permission mode changes are unavailable while busy; retry when idle")
		return m, nil
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
	case slashRules:
		return m.cmdRules(arg)
	case slashSide:
		return m.cmdSideQuestion(sideQuestionLabel(input), arg)
	case slashSteer:
		return m.cmdSteer(arg)
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
	case slashFork:
		return m.cmdFork(arg)
	case slashTitle:
		return m.cmdTitle(arg)
	case slashDelete:
		return m.cmdDelete(arg)
	case slashQueue:
		return m.cmdQueue(arg)
	case slashPermissions:
		return m.cmdPermissions(arg)
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

// cmdPermissions keeps the no-argument report read-only and limits changes to
// the two process-local interactive modes supported by the TUI.
func (m *model) cmdPermissions(arg string) (tea.Model, tea.Cmd) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		m.appendLine(lineSystem, m.deps.PolicyInfo.FormatPermissions())
		m.appendLine(lineSep, "")
		return m, nil
	}
	if m.mode != modeIdle {
		m.appendLine(lineError, "permission mode changes are unavailable while busy; retry when idle")
		return m, nil
	}
	state := m.deps.PolicyInfo.ApprovalState
	if state == nil {
		m.appendLine(lineError, "permission mode switching is unavailable in this TUI")
		return m, nil
	}
	if err := state.SetInteractiveMode(arg); err != nil {
		m.appendLine(lineError, "usage: /permissions [ask|auto]")
		return m, nil
	}
	m.appendLine(lineSystem, "permission mode: "+state.InteractiveMode())
	return m, nil
}

func sideQuestionLabel(input string) string {
	fields := strings.Fields(input)
	if len(fields) > 0 && strings.EqualFold(fields[0], "/side") {
		return "side"
	}
	return "btw"
}

// cmdSteer admits input to the currently running regular turn. The core API is
// deliberately discovered through a local interface so this TUI remains
// buildable while older chat.Session implementations are in use.
func (m *model) cmdSteer(input string) (tea.Model, tea.Cmd) {
	input = strings.TrimSpace(input)
	if input == "" {
		m.appendLine(lineError, "usage: /steer <text>")
		return m, nil
	}
	if m.mode != modeBusy {
		m.appendLine(lineError, "steer unavailable: a regular turn is not running")
		return m, nil
	}

	session, generation := m.activeSessionSnapshot()
	if session == nil {
		m.appendLine(lineError, "steer failed: session is unavailable")
		return m, nil
	}
	// A session switch is synchronous in Bubble Tea, but retain the generation
	// guard at the admission boundary so an old UI action cannot target a new
	// session if the embedding caller changes it concurrently.
	current, currentGeneration := m.activeSessionSnapshot()
	if current != session || currentGeneration != generation {
		m.appendLine(lineError, "steer failed: active session changed")
		return m, nil
	}

	api := m.steerSession
	if api == nil {
		candidate, ok := interface{}(session).(interface {
			ActiveTurnID() (string, bool)
			Steer(context.Context, string, string) error
		})
		if !ok {
			m.appendLine(lineError, "steer failed: core steering is unsupported")
			return m, nil
		}
		api = candidate
	}
	expectedTurnID, ok := api.ActiveTurnID()
	if !ok || strings.TrimSpace(expectedTurnID) == "" {
		m.appendLine(lineError, "steer failed: no active steerable turn")
		return m, nil
	}
	receipt, err := m.admitSteer(api, expectedTurnID, input)
	if err != nil {
		m.appendLine(lineError, "steer failed: "+err.Error())
		return m, nil
	}
	m.turnSteerAdmitted++
	if receipt.Sequence != 0 && strings.TrimSpace(receipt.Content) != "" {
		if m.turnSteerAdmissions == nil {
			m.turnSteerAdmissions = make(map[uint64]string)
		}
		m.turnSteerAdmissions[receipt.Sequence] = receipt.Content
	}
	m.appendLine(lineSystem, "steer admitted; awaiting next model call: "+input)
	return m, nil
}

func (m *model) admitSteer(api interface {
	Steer(context.Context, string, string) error
}, expectedTurnID, input string) (chat.TurnSteerReceipt, error) {
	if receiptAPI, ok := api.(interface {
		SteerWithReceipt(context.Context, string, string) (chat.TurnSteerReceipt, error)
	}); ok {
		return receiptAPI.SteerWithReceipt(m.processCtx(), expectedTurnID, input)
	}
	return chat.TurnSteerReceipt{}, api.Steer(m.processCtx(), expectedTurnID, input)
}

func (m *model) cmdSideQuestion(label, question string) (tea.Model, tea.Cmd) {
	label = strings.ToLower(strings.TrimSpace(label))
	if label != "side" {
		label = "btw"
	}
	question = strings.TrimSpace(question)
	if question == "" {
		m.appendLine(lineError, "usage: /btw <question>")
		return m, nil
	}

	m.appendSideLine(fmt.Sprintf("[%s] question: %s", label, question))
	callback := m.deps.SideQuestion
	if callback == nil {
		m.appendSideLine(fmt.Sprintf("[%s] side unavailable: callback is not configured", label))
		return m, nil
	}
	if session, _ := m.activeSessionSnapshot(); session == nil {
		m.appendSideLine(fmt.Sprintf("[%s] side unavailable: session is unavailable", label))
		return m, nil
	}
	requestID := m.beginSideQuestion()
	ctx := m.processCtx()
	return m, func() tea.Msg {
		// Resolve the session when the command starts and carry its generation so
		// results cannot cross a later /new or /resume boundary.
		session, generation := m.activeSessionSnapshot()
		if session == nil {
			return sideQuestionDoneMsg{
				requestID:         requestID,
				label:             label,
				sessionGeneration: generation,
				unavailable:       true,
			}
		}
		answer, err := callback(ctx, session, question)
		return sideQuestionDoneMsg{
			requestID:         requestID,
			label:             label,
			sessionID:         session.ID(),
			sessionGeneration: generation,
			answer:            answer,
			err:               err,
		}
	}
}

func (m *model) finishSideQuestion(msg sideQuestionDoneMsg) {
	label := strings.ToLower(strings.TrimSpace(msg.label))
	if label != "side" {
		label = "btw"
	}
	if msg.unavailable {
		m.appendSideLine(fmt.Sprintf("[%s] side unavailable: session is unavailable", label))
		return
	}
	if msg.err != nil {
		m.appendSideLine(fmt.Sprintf("[%s] side error: %s", label, msg.err.Error()))
		return
	}
	answer := strings.TrimSpace(msg.answer)
	if answer == "" {
		m.appendSideLine(fmt.Sprintf("[%s] side error: empty answer", label))
		return
	}
	m.appendSideLine(fmt.Sprintf("[%s] answer: %s", label, answer))
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

func (m *model) cmdRules(arg string) (tea.Model, tea.Cmd) {
	if strings.TrimSpace(arg) != "" {
		m.appendLine(lineError, "usage: /rules")
		return m, nil
	}
	if m.deps.RulesReport == nil {
		m.appendLine(lineSystem, "Rules\nsource metadata unavailable (runtime callback is not configured)")
	} else {
		m.appendLine(lineSystem, m.deps.RulesReport())
	}
	m.appendLine(lineSep, "")
	return m, nil
}

func (m *model) cmdClear() (tea.Model, tea.Cmd) {
	if m.mode != modeIdle {
		m.appendLine(lineError, "busy: finish or interrupt the current turn first")
		return m, nil
	}
	current := m.activeSession()
	if current == nil {
		m.appendLine(lineError, "session is unavailable")
		return m, nil
	}
	oldID := current.ID()
	session, err := m.createSession("")
	if err != nil {
		m.appendLine(lineError, "clear context: "+err.Error())
		return m, nil
	}
	m.replaceSession(session)
	m.resetSessionTransientState()
	m.lines = nil
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
	m.replaceSession(session)
	m.resetSessionTransientState()
	m.lines = nil
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
	current := m.activeSession()
	if current == nil {
		return nil, errors.New("session is unavailable")
	}
	model := current.Model()
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
		system = current.SystemPrompt()
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
	if session := m.activeSession(); session != nil {
		current = session.ID()
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
	current := m.activeSession()
	if current == nil {
		m.appendLine(lineError, "session is unavailable")
		return m, nil
	}
	model := current.Model()
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
	m.replaceSession(session)
	m.resetSessionTransientState()
	if m.deps.InvalidateRulesSnapshot != nil {
		m.deps.InvalidateRulesSnapshot()
	}
	if m.deps.NotifyActiveSession != nil {
		m.deps.NotifyActiveSession(session.ID())
	}
	// Resume keeps the durable thread system prompt (create-time snapshot).
	transcript := session.Transcript()
	m.lines = seedLinesFromTranscript(transcript, resumeBanner(session.ID(), len(transcript)), session.Title())
	m.appendLegacyCheckpointResetNotice(session)
	m.inputHist.seedFromMessages(transcript)
	m.refreshViewport()
	return m, nil
}

func (m *model) cmdFork(arg string) (tea.Model, tea.Cmd) {
	if m.hasPendingApproval() {
		m.appendLine(lineError, "busy: resolve the pending approval first")
		return m, nil
	}
	if m.mode != modeIdle {
		m.appendLine(lineError, "busy: finish or interrupt the current turn first")
		return m, nil
	}
	if strings.TrimSpace(arg) != "" {
		m.appendLine(lineError, "usage: /fork")
		return m, nil
	}

	source := m.activeSession()
	if source == nil {
		m.appendLine(lineError, "fork: session is unavailable")
		return m, nil
	}

	// Empty arguments ask the durable fork primitive to select the latest
	// complete turn and generate the child ID. The source title is carried by
	// the child ledger; no TUI-only title mutation is needed here.
	child, result, err := source.Fork(m.processCtx(), "", "")
	if err != nil {
		m.appendLine(lineError, "fork: "+err.Error())
		return m, nil
	}
	if child == nil {
		m.appendLine(lineError, "fork: child session is unavailable")
		return m, nil
	}

	childID := child.ID()
	if childID == "" {
		childID = result.ChildID
	}
	if childID == "" {
		m.appendLine(lineError, "fork: child session has no ID")
		return m, nil
	}

	m.activateForkChild(source, child, result)
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
	session := m.activeSession()
	if session == nil {
		m.appendLine(lineError, "title: session is unavailable")
		return m, nil
	}
	if err := session.SetTitle(m.processCtx(), title); err != nil {
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
	if session := m.activeSession(); session != nil && session.ID() == id {
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
	session := m.activeSession()
	if session == nil {
		m.appendLine(lineError, "session is unavailable")
		return m, nil
	}
	status := session.ContextStatus()
	cfg := session.ContextConfig()
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
	fmt.Fprintf(&b, "API snapshot: %s\n", usage.FormatContextSnapshot(sessionContextSnapshot(session)))
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
	if m.activeSession() == nil {
		m.appendLine(lineError, "session is unavailable")
		return m, nil
	}
	// Progress is status-bar only (mode=compacting). Do not emit a transcript
	// banner: no-candidate compact finishes as a silent no-op.
	return m.startCompaction(strings.TrimSpace(focus), false)
}

func (m *model) cmdQueue(arg string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		m.appendLine(lineSystem, formatQueueList(m.queue, m.queuePaused))
		m.appendLine(lineSep, "")
		return m, nil
	}
	switch strings.ToLower(fields[0]) {
	case "clear":
		if len(fields) != 1 {
			m.appendLine(lineError, queueCommandUsage)
			m.appendLine(lineSep, "")
			return m, nil
		}
		n := len(m.queue)
		m.queue = nil
		m.queuePaused = false
		if n == 0 {
			m.appendLine(lineSystem, queueEmptyMessage)
		} else {
			m.appendLine(lineSystem, fmt.Sprintf("queue cleared (%d dropped)", n))
		}
		m.appendLine(lineSep, "")
		return m, nil
	case "drop":
		index, parseError := parseQueueDropIndex(fields)
		if parseError != "" {
			m.appendLine(lineError, parseError)
			m.appendLine(lineSep, "")
			return m, nil
		}
		if len(m.queue) == 0 {
			m.appendLine(lineError, queueEmptyMessage)
			m.appendLine(lineSep, "")
			return m, nil
		}
		if index > len(m.queue) {
			m.appendLine(lineError, queueDropRangeError)
			m.appendLine(lineSep, "")
			return m, nil
		}
		var dropped string
		m.queue, dropped, _ = dropQueuedFollowUp(m.queue, index)
		if len(m.queue) == 0 {
			m.queuePaused = false
		}
		m.appendLine(lineSystem, fmt.Sprintf("queue dropped (%d): %s", index, queuePreview(dropped)))
		m.appendLine(lineSep, "")
		return m, nil
	case "resume":
		if len(fields) != 1 {
			m.appendLine(lineError, queueCommandUsage)
			m.appendLine(lineSep, "")
			return m, nil
		}
		if m.mode != modeIdle {
			m.appendLine(lineSystem, queueResumeBusyMessage)
			m.appendLine(lineSep, "")
			return m, nil
		}
		m.queuePaused = false
		if len(m.queue) == 0 {
			m.appendLine(lineSystem, queueEmptyMessage)
			m.appendLine(lineSep, "")
			return m, nil
		}
		m.appendLine(lineSystem, "queue resumed; continuing queued follow-ups")
		m.appendLine(lineSep, "")
		return m, m.drainQueue()
	case "edit":
		index, newText, parseError := parseQueueEdit(arg)
		if parseError != "" {
			m.appendLine(lineError, parseError)
			m.appendLine(lineSep, "")
			return m, nil
		}
		if len(m.queue) == 0 {
			m.appendLine(lineError, queueEmptyMessage)
			m.appendLine(lineSep, "")
			return m, nil
		}
		if index > len(m.queue) {
			m.appendLine(lineError, queueEditRangeError)
			m.appendLine(lineSep, "")
			return m, nil
		}
		if !isQueueableInput(newText) {
			m.appendLine(lineError, queueEditAdmissionError)
			m.appendLine(lineSep, "")
			return m, nil
		}
		m.queue, _ = editQueuedFollowUp(m.queue, index, newText)
		m.appendLine(lineSystem, fmt.Sprintf("queue edited (%d): %s", index, queuePreview(newText)))
		m.appendLine(lineSep, "")
		return m, nil
	default:
		m.appendLine(lineError, queueCommandUsage)
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
	session := m.activeSession()
	if session == nil {
		m.mode = modeIdle
		m.setIdlePlaceholder()
		m.appendLine(lineError, "compaction: session is unavailable")
		return m, nil
	}
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
			status := chat.ContextStatus{}
			if session := m.activeSession(); session != nil {
				status = session.ContextStatus()
			}
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
	session, sessionGeneration := m.activeSessionSnapshot()
	if session == nil {
		m.err = errors.New("session is unavailable")
		m.appendLine(lineError, m.err.Error())
		return m, nil
	}
	// Validate the per-turn runtime guard before publishing local busy state.
	// drainQueue can then detect this preflight failure without losing its head.
	m.err = nil
	ctx, cancel, err := runtimeguard.WithTurnContext(m.processCtx(), m.deps.TurnOptions)
	if err != nil {
		m.err = fmt.Errorf("configure runtime guard: %w", err)
		m.appendLine(lineError, m.err.Error())
		return m, nil
	}
	m.turnID++
	turnID := m.turnID
	m.mode = modeBusy
	m.interruptFeedbackShown = false
	m.turnStart = time.Now()
	m.err = nil
	m.currentTool = ""
	m.openReasoning = noOpenReasoning
	m.streamingAssistant = false
	m.stickBottom = true
	m.setBusyPlaceholder()

	m.turnCancel = cancel
	// Buffered enough for tool chatter; sends still block rather than drop.
	m.events = make(chan tea.Msg, 256)
	m.turnDone = make(chan turnDoneMsg, 1)
	m.pendingTurnDone = nil
	m.turnUsage = usage.APIUsage{Status: store.UsageStatusExact}
	m.turnUsageSeen = false
	m.turnUsageCallIDs = make(map[string]struct{})
	m.turnSteerAdmitted = 0
	m.turnSteerAdmissions = make(map[uint64]string)
	m.turnSteerConsumed = make(map[uint64]struct{})

	events := m.events
	done := m.turnDone
	emit := emitFromTurnEvent(ctx, turnID, sessionGeneration, events)

	go func() {
		err := session.AskWithEvents(ctx, input, nil, emit)
		if runtimeguard.IsTurnDeadlineExceeded(ctx) {
			err = runtimeguard.ErrTurnDeadlineExceeded
		}
		// Completion has its own slot so a saturated display-event queue cannot
		// strand the TUI in busy mode after cancellation.
		close(events)
		done <- turnDoneMsg{turnID: turnID, sessionGeneration: sessionGeneration, err: err}
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

// cancelActiveTask mirrors the terminal interrupt into the deterministic task
// runtime after cancellation has been requested. It uses the process context
// so the durable interruption can outlive the cancelled turn context.
func (m *model) cancelActiveTask(reason string) {
	session := m.activeSession()
	if session == nil {
		return
	}
	receipt := session.InterruptTask(m.processCtx(), reason)
	if receipt.Applied {
		m.appendLine(lineSystem, receipt.Summary)
	}
}

func (m *model) showInterruptRequested() {
	if m.interruptFeedbackShown {
		return
	}
	m.interruptFeedbackShown = true
	m.appendLine(lineSystem, interruptRequestedMessage)
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
	m.openReasoning = noOpenReasoning
	m.currentTool = ""
	m.openToolCards = make(map[string]int)
	m.openToolNames = make(map[string]string)
	m.interruptFeedbackShown = false
	m.setIdlePlaceholder()

	if err != nil {
		if errors.Is(err, runtimeguard.ErrTurnDeadlineExceeded) {
			m.appendLine(lineSystem, runtimeguard.TurnTimeoutReason)
		} else if isCanceled(err) {
			m.appendLine(lineSystem, "interrupted")
		} else {
			m.appendLine(lineError, err.Error())
		}
		// Cancellation leaves an existing pause intact. An explicit resume already
		// clears it; otherwise a later user turn cancelled by Esc/Ctrl+C must not
		// bypass the earlier turn error's queue decision.
		if !isCanceled(err) && len(m.queue) > 0 {
			m.queuePaused = true
			m.appendLine(lineSystem, queuePausedLine(len(m.queue)))
		}
	}
	if isCanceled(err) {
		m.restoreInterruptedSteers()
	}
	m.settleSteerFeedback(err)
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
	if session := m.activeSession(); err == nil && session != nil && session.NeedsAutoCompaction() {
		// Status bar shows mode=compacting; only a successful install is logged.
		_, cmd := m.startCompaction("", true)
		return cmd
	}

	return m.drainQueue()
}

func (m *model) settleSteerFeedback(err error) {
	admitted := m.turnSteerAdmitted
	consumed := len(m.turnSteerConsumed)
	if admitted == 0 && consumed == 0 {
		m.turnSteerAdmissions = nil
		m.turnSteerConsumed = nil
		return
	}
	if err != nil {
		m.appendLine(lineSystem, fmt.Sprintf("steer discarded: %d admitted input(s); turn not committed", admitted))
	} else {
		m.appendLine(lineSystem, fmt.Sprintf("steer committed: %d consumed input(s)", consumed))
		if pending := admitted - consumed; pending > 0 {
			m.appendLine(lineSystem, fmt.Sprintf("steer discarded: %d admitted input(s) were not consumed before turn completion", pending))
		}
	}
	m.turnSteerAdmitted = 0
	m.turnSteerAdmissions = nil
	m.turnSteerConsumed = nil
}

// restoreInterruptedSteers returns receipt-tracked admissions that never
// reached an observed model boundary to the composer. The core deliberately
// discards these inputs with the cancelled turn, so this is a display-only
// recovery path rather than a second admission or a ledger write.
func (m *model) restoreInterruptedSteers() {
	if len(m.turnSteerAdmissions) == 0 {
		return
	}
	sequences := make([]uint64, 0, len(m.turnSteerAdmissions))
	for sequence, content := range m.turnSteerAdmissions {
		if _, consumed := m.turnSteerConsumed[sequence]; consumed || strings.TrimSpace(content) == "" {
			continue
		}
		sequences = append(sequences, sequence)
	}
	if len(sequences) == 0 {
		return
	}
	sort.Slice(sequences, func(i, j int) bool { return sequences[i] < sequences[j] })
	restored := make([]string, 0, len(sequences))
	for _, sequence := range sequences {
		restored = append(restored, m.turnSteerAdmissions[sequence])
	}

	draft := m.textarea.Value()
	if draft != "" && !strings.HasSuffix(draft, "\n") {
		draft += "\n"
	}
	draft += strings.Join(restored, "\n")
	m.textarea.SetValue(draft)
	m.textarea.CursorEnd()
	m.clearSlashMenu()
	m.syncComposerHeight()
	m.layout()
	m.refreshViewport()
	m.appendLine(lineSystem, fmt.Sprintf("steer restored to composer: %d uncommitted input(s)", len(restored)))
}

// drainQueue auto-sends queued follow-ups after a turn ends.
// Local slash commands are applied immediately; the first real turn returns its cmd.
func (m *model) drainQueue() tea.Cmd {
	if m.queuePaused {
		return nil
	}
	for len(m.queue) > 0 {
		next := m.queue[0]
		m.queue = m.queue[1:]
		m.err = nil
		_, cmd := m.submit(next)
		if m.mode != modeIdle {
			return cmd
		}
		if m.err != nil {
			// startTurn has already validated its guard, but keep the FIFO head if
			// that preflight ever fails while a queued item is being promoted.
			m.queue = append([]string{next}, m.queue...)
			m.queuePaused = true
			m.appendLine(lineSystem, queuePausedLine(len(m.queue)))
			return nil
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
	// Single place that ends open reasoning before a discrete transcript line.
	m.foldOpenReasoning()
	m.streamingAssistant = false
	m.lines = append(m.lines, transcriptLine{kind: kind, text: text})
	m.refreshViewport()
}

func (m *model) appendSideLine(text string) {
	m.sideLines = append(m.sideLines, transcriptLine{kind: lineSide, text: text})
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
		case lineReasoning:
			// text is body while open, or one-line summary after fold.
			b.WriteString(renderReasoning(line.text, line.folded, m.reasoningIsStreaming(i)))
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
	for _, line := range m.sideLines {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(renderSystem(line.text))
	}
	applyContent(&m.viewport, &m.stickBottom, b.String())
}

// loadOlderTranscript grows the visible replay only when the reader reaches
// its oldest loaded page. The session keeps this separate from model context,
// so scrolling cannot re-inflate the next prompt.
func (m *model) loadOlderTranscript() {
	session := m.activeSession()
	if session == nil || m.mode != modeIdle {
		return
	}
	page, _, err := session.LoadOlderTranscript(m.processCtx(), 100)
	if err != nil {
		if !errors.Is(err, chat.ErrCompactionStale) {
			m.appendLine(lineError, "load older transcript: "+err.Error())
		}
		return
	}
	if len(page) == 0 {
		return
	}
	transcript := session.Transcript()
	m.lines = seedLinesFromTranscript(transcript, resumeBanner(session.ID(), len(transcript)), session.Title())
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
	if session := m.activeSession(); session != nil {
		transcriptCount = len(session.Transcript())
		if id := session.ID(); id != "" {
			sessionID = id
		}
		title = session.Title()
		apiUsage := sessionAPIUsage(session)
		usageLine = "\n" + usage.FormatAPIUsage(apiUsage) + "  " +
			usage.FormatCostEstimate(apiUsage.CostUSD, apiUsage.Status)
		ctxLine = "\n" + usage.FormatContextSnapshot(sessionContextSnapshot(session))
		cfg := session.ContextConfig()
		contextStatus := session.ContextStatus()
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
	if m.queuePaused {
		report += fmt.Sprintf("  queue_paused=true  queued=%d  queue_resume=/queue resume", len(m.queue))
	} else if n := len(m.queue); n > 0 {
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
