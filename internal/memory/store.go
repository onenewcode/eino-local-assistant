package memory

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"eino-local-assistant/internal/usage"

	"github.com/gofrs/flock"
)

const (
	dirName       = ".eino"
	memoryDirName = "memory"
	metaFile      = "meta.json"
	entriesFile   = "entries.jsonl"
	summaryFile   = "summary.md"
	lockFile      = "write.lock"
)

// Store is a project-scoped memory store under <workspace>/.eino/memory/.
type Store struct {
	root   string // .eino/memory absolute path
	wsRoot string
	mu     sync.Mutex // serializes flock acquisition in this process
	maxSum int
	useOn  bool
	genOn  bool
	now    func() time.Time
}

// Options configures a Store.
type Options struct {
	WorkspaceRoot    string
	MaxSummaryTokens int
	UseEnabled       bool
	GenerateEnabled  bool
	Now              func() time.Time
}

// Open bootstraps and opens a memory store for the workspace.
func Open(opts Options) (*Store, error) {
	ws, err := filepath.Abs(strings.TrimSpace(opts.WorkspaceRoot))
	if err != nil {
		return nil, fmt.Errorf("memory workspace: %w", err)
	}
	info, err := os.Stat(ws)
	if err != nil {
		return nil, fmt.Errorf("memory workspace: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("memory workspace %q is not a directory", ws)
	}
	maxSum := opts.MaxSummaryTokens
	if maxSum <= 0 {
		maxSum = 2500
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	root := filepath.Join(ws, dirName, memoryDirName)
	s := &Store{
		root:   root,
		wsRoot: ws,
		maxSum: maxSum,
		useOn:  opts.UseEnabled,
		genOn:  opts.GenerateEnabled,
		now:    now,
	}
	if err := s.bootstrap(); err != nil {
		return nil, err
	}
	return s, nil
}

// Root returns the absolute memory directory.
func (s *Store) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

// WorkspaceRoot returns the workspace absolute path.
func (s *Store) WorkspaceRoot() string {
	if s == nil {
		return ""
	}
	return s.wsRoot
}

// UseEnabled reports whether injection/tools are allowed.
func (s *Store) UseEnabled() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.useOn
}

// GenerateEnabled reports whether auto extraction is allowed.
func (s *Store) GenerateEnabled() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.genOn
}

// SetUseEnabled updates the runtime use flag and meta.
func (s *Store) SetUseEnabled(on bool) error {
	return s.withFileLock(func() error {
		s.useOn = on
		meta, err := s.readMetaUnlocked()
		if err != nil {
			return err
		}
		meta.UseEnabled = on
		return s.writeMetaUnlocked(meta)
	})
}

// SetGenerateEnabled updates the runtime generate flag and meta.
func (s *Store) SetGenerateEnabled(on bool) error {
	return s.withFileLock(func() error {
		s.genOn = on
		meta, err := s.readMetaUnlocked()
		if err != nil {
			return err
		}
		meta.GenerateEnabled = on
		return s.writeMetaUnlocked(meta)
	})
}

func (s *Store) bootstrap() error {
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return fmt.Errorf("create memory dir: %w", err)
	}
	gitignore := filepath.Join(s.wsRoot, dirName, ".gitignore")
	if _, err := os.Stat(gitignore); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(gitignore, []byte("memory/\n"), 0o644); err != nil {
			return fmt.Errorf("write .eino/.gitignore: %w", err)
		}
	} else if err != nil {
		return err
	}
	return s.withFileLock(func() error {
		metaPath := filepath.Join(s.root, metaFile)
		if _, err := os.Stat(metaPath); errors.Is(err, os.ErrNotExist) {
			meta := Meta{
				SchemaVersion:   SchemaVersion,
				WorkspaceRoot:   s.wsRoot,
				UseEnabled:      s.useOn,
				GenerateEnabled: s.genOn,
			}
			if err := s.writeMetaUnlocked(meta); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			meta, err := s.readMetaUnlocked()
			if err != nil {
				return err
			}
			// Runtime flags from Open options win on process start.
			meta.UseEnabled = s.useOn
			meta.GenerateEnabled = s.genOn
			meta.WorkspaceRoot = s.wsRoot
			if err := s.writeMetaUnlocked(meta); err != nil {
				return err
			}
		}
		entriesPath := filepath.Join(s.root, entriesFile)
		if _, err := os.Stat(entriesPath); errors.Is(err, os.ErrNotExist) {
			if err := os.WriteFile(entriesPath, nil, 0o644); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		return s.rebuildSummaryUnlocked()
	})
}

// AddUser writes a user-trusted memory (LWW among user keys; supersedes candidates).
func (s *Store) AddUser(key, claim string) (Entry, error) {
	return s.add(key, claim, TrustUser, "", nil)
}

// AddCandidate writes an auto-extracted candidate.
// It never supersedes an active user-trusted entry for the same key.
func (s *Store) AddCandidate(key, claim, threadID string, sourceEventIDs []string) (Entry, error) {
	return s.add(key, claim, TrustCandidate, threadID, sourceEventIDs)
}

func (s *Store) add(key, claim string, trust Trust, threadID string, sourceEventIDs []string) (Entry, error) {
	claim = strings.TrimSpace(claim)
	if claim == "" {
		return Entry{}, errors.New("memory claim is required")
	}
	key = normalizeKey(key, claim)
	var out Entry
	err := s.withFileLock(func() error {
		entries, err := s.loadEntriesUnlocked()
		if err != nil {
			return err
		}
		now := s.now().UTC()
		version := 1
		var supersedes string

		if trust == TrustCandidate {
			// Do not overwrite user-confirmed facts for the same key.
			for _, e := range entries {
				if e.Key == key && e.Status == StatusActive && e.Trust == TrustUser {
					return fmt.Errorf("candidate refused: key %q has active user memory", key)
				}
			}
		}

		for i := range entries {
			e := &entries[i]
			if e.Key != key || e.Status != StatusActive {
				continue
			}
			// User write supersedes any active (user or candidate).
			// Candidate write supersedes only other candidates (user blocked above).
			if trust == TrustUser || e.Trust == TrustCandidate {
				e.Status = StatusSuperseded
				e.UpdatedAt = now
				if e.Version >= version {
					version = e.Version + 1
				}
				supersedes = e.ID
			}
		}
		out = Entry{
			ID:             newID(),
			Key:            key,
			Claim:          claim,
			Trust:          trust,
			Status:         StatusActive,
			Version:        version,
			SourceEventIDs: append([]string(nil), sourceEventIDs...),
			SourceThreadID: threadID,
			CreatedAt:      now,
			UpdatedAt:      now,
			Supersedes:     supersedes,
		}
		entries = append(entries, out)
		if err := s.writeEntriesUnlocked(entries); err != nil {
			return err
		}
		return s.rebuildSummaryUnlocked()
	})
	return out, err
}

// Delete marks an active entry deleted by id or key.
func (s *Store) Delete(idOrKey string) (Entry, error) {
	idOrKey = strings.TrimSpace(idOrKey)
	if idOrKey == "" {
		return Entry{}, errors.New("id or key is required")
	}
	var out Entry
	err := s.withFileLock(func() error {
		entries, err := s.loadEntriesUnlocked()
		if err != nil {
			return err
		}
		now := s.now().UTC()
		found := false
		for i := range entries {
			e := &entries[i]
			if e.Status != StatusActive {
				continue
			}
			if e.ID == idOrKey || e.Key == idOrKey {
				e.Status = StatusDeleted
				e.UpdatedAt = now
				out = *e
				found = true
			}
		}
		if !found {
			return fmt.Errorf("memory not found: %s", idOrKey)
		}
		if err := s.writeEntriesUnlocked(entries); err != nil {
			return err
		}
		return s.rebuildSummaryUnlocked()
	})
	return out, err
}

// Accept promotes a candidate to user trust.
func (s *Store) Accept(id string) (Entry, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Entry{}, errors.New("id is required")
	}
	var out Entry
	err := s.withFileLock(func() error {
		entries, err := s.loadEntriesUnlocked()
		if err != nil {
			return err
		}
		var cand *Entry
		for i := range entries {
			if entries[i].ID == id && entries[i].Status == StatusActive {
				cand = &entries[i]
				break
			}
		}
		if cand == nil {
			return fmt.Errorf("active memory not found: %s", id)
		}
		if cand.Trust == TrustUser {
			out = *cand
			return nil
		}
		// Supersede any other active candidate with the same key; refuse if a
		// different user entry already owns the key (should not happen).
		now := s.now().UTC()
		for i := range entries {
			e := &entries[i]
			if e.ID == cand.ID || e.Status != StatusActive || e.Key != cand.Key {
				continue
			}
			if e.Trust == TrustUser {
				return fmt.Errorf("cannot accept: key %q already has user memory %s", cand.Key, e.ID)
			}
			e.Status = StatusSuperseded
			e.UpdatedAt = now
		}
		cand.Trust = TrustUser
		cand.UpdatedAt = now
		out = *cand
		if err := s.writeEntriesUnlocked(entries); err != nil {
			return err
		}
		return s.rebuildSummaryUnlocked()
	})
	return out, err
}

// ListActive returns active entries, users first.
func (s *Store) ListActive() ([]Entry, error) {
	var out []Entry
	err := s.withFileLock(func() error {
		entries, err := s.loadEntriesUnlocked()
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.Status == StatusActive {
				out = append(out, e)
			}
		}
		return nil
	})
	sortEntries(out)
	return out, err
}

// Get returns an active entry by id or key.
func (s *Store) Get(idOrKey string) (Entry, error) {
	idOrKey = strings.TrimSpace(idOrKey)
	entries, err := s.ListActive()
	if err != nil {
		return Entry{}, err
	}
	for _, e := range entries {
		if e.ID == idOrKey || e.Key == idOrKey {
			return e, nil
		}
	}
	return Entry{}, fmt.Errorf("memory not found: %s", idOrKey)
}

// Search returns active entries whose key or claim contains query (case-insensitive).
func (s *Store) Search(query string) ([]Entry, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return s.ListActive()
	}
	all, err := s.ListActive()
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0)
	for _, e := range all {
		if strings.Contains(strings.ToLower(e.Key), query) ||
			strings.Contains(strings.ToLower(e.Claim), query) {
			out = append(out, e)
		}
	}
	return out, nil
}

// Summary returns the bounded injection text (rebuilds if missing).
func (s *Store) Summary() (SummaryBundle, error) {
	var bundle SummaryBundle
	err := s.withFileLock(func() error {
		path := filepath.Join(s.root, summaryFile)
		data, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if len(data) == 0 {
			if err := s.rebuildSummaryUnlocked(); err != nil {
				return err
			}
			data, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		text := string(data)
		entries, err := s.loadEntriesUnlocked()
		if err != nil {
			return err
		}
		users, cands := 0, 0
		for _, e := range entries {
			if e.Status != StatusActive {
				continue
			}
			if e.Trust == TrustUser {
				users++
			} else {
				cands++
			}
		}
		tokens := usage.EstimateText(text)
		bundle = SummaryBundle{
			Text:      text,
			Tokens:    tokens,
			Truncated: tokens >= s.maxSum,
			UserCount: users,
			CandCount: cands,
		}
		return nil
	})
	return bundle, err
}

// RebuildSummary forces summary.md regeneration.
func (s *Store) RebuildSummary() error {
	return s.withFileLock(s.rebuildSummaryUnlocked)
}

// Report returns status for /memory status.
func (s *Store) Report() (StatusReport, error) {
	var rep StatusReport
	err := s.withFileLock(func() error {
		meta, err := s.readMetaUnlocked()
		if err != nil {
			return err
		}
		entries, err := s.loadEntriesUnlocked()
		if err != nil {
			return err
		}
		users, cands := 0, 0
		for _, e := range entries {
			if e.Status != StatusActive {
				continue
			}
			if e.Trust == TrustUser {
				users++
			} else {
				cands++
			}
		}
		rep = StatusReport{
			Root:            s.root,
			UseEnabled:      s.useOn,
			GenerateEnabled: s.genOn,
			UserActive:      users,
			CandidateActive: cands,
			LastConsolidate: meta.LastConsolidate,
			LastError:       meta.LastError,
		}
		return nil
	})
	return rep, err
}

// MarkExtracted records a successful extraction so the thread is not rescanned.
func (s *Store) MarkExtracted(threadID string) error {
	return s.withFileLock(func() error {
		meta, err := s.readMetaUnlocked()
		if err != nil {
			return err
		}
		now := s.now().UTC()
		meta.LastConsolidate = &now
		meta.LastError = ""
		if threadID != "" && !containsString(meta.ProcessedThreads, threadID) {
			meta.ProcessedThreads = append(meta.ProcessedThreads, threadID)
			if len(meta.ProcessedThreads) > 500 {
				meta.ProcessedThreads = meta.ProcessedThreads[len(meta.ProcessedThreads)-500:]
			}
		}
		return s.writeMetaUnlocked(meta)
	})
}

// RecordExtractError stores the last failure without marking the thread processed.
func (s *Store) RecordExtractError(extractErr error) error {
	return s.withFileLock(func() error {
		meta, err := s.readMetaUnlocked()
		if err != nil {
			return err
		}
		now := s.now().UTC()
		meta.LastConsolidate = &now
		if extractErr != nil {
			meta.LastError = extractErr.Error()
		}
		return s.writeMetaUnlocked(meta)
	})
}

// IsProcessed reports whether a thread was successfully extracted.
func (s *Store) IsProcessed(threadID string) (bool, error) {
	var ok bool
	err := s.withFileLock(func() error {
		meta, err := s.readMetaUnlocked()
		if err != nil {
			return err
		}
		ok = containsString(meta.ProcessedThreads, threadID)
		return nil
	})
	return ok, err
}

func (s *Store) rebuildSummaryUnlocked() error {
	entries, err := s.loadEntriesUnlocked()
	if err != nil {
		return err
	}
	active := make([]Entry, 0)
	for _, e := range entries {
		if e.Status == StatusActive {
			active = append(active, e)
		}
	}
	sortEntries(active)
	var b strings.Builder
	b.WriteString("# Persistent memory (project-scoped)\n\n")
	users := make([]Entry, 0)
	cands := make([]Entry, 0)
	for _, e := range active {
		if e.Trust == TrustUser {
			users = append(users, e)
		} else {
			cands = append(cands, e)
		}
	}
	if len(users) > 0 {
		b.WriteString("## Confirmed\n\n")
		for _, e := range users {
			fmt.Fprintf(&b, "- **%s**: %s\n", e.Key, e.Claim)
		}
		b.WriteString("\n")
	}
	if len(cands) > 0 {
		b.WriteString("## Candidates (unverified — do not treat as ground truth)\n\n")
		for _, e := range cands {
			fmt.Fprintf(&b, "- **%s**: %s _(unverified)_\n", e.Key, e.Claim)
		}
		b.WriteString("\n")
	}
	if len(users) == 0 && len(cands) == 0 {
		b.WriteString("_No memories stored yet._\n")
	}
	text := b.String()
	const notice = "\n\n…(truncated)\n"
	if usage.EstimateText(text) > s.maxSum {
		runes := []rune(text)
		lo, hi := 0, len(runes)
		for lo < hi {
			mid := (lo + hi + 1) / 2
			candidate := string(runes[:mid]) + notice
			if usage.EstimateText(candidate) <= s.maxSum {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		if lo < 1 {
			lo = 1
		}
		text = string(runes[:lo]) + notice
	}
	return os.WriteFile(filepath.Join(s.root, summaryFile), []byte(text), 0o644)
}

func (s *Store) loadEntriesUnlocked() ([]Entry, error) {
	path := filepath.Join(s.root, entriesFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	out := make([]Entry, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("parse entries.jsonl: %w", err)
		}
		out = append(out, e)
	}
	return out, nil
}

func (s *Store) writeEntriesUnlocked(entries []Entry) error {
	var b strings.Builder
	for _, e := range entries {
		raw, err := json.Marshal(e)
		if err != nil {
			return err
		}
		b.Write(raw)
		b.WriteByte('\n')
	}
	tmp := filepath.Join(s.root, entriesFile+".tmp")
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.root, entriesFile))
}

func (s *Store) readMetaUnlocked() (Meta, error) {
	data, err := os.ReadFile(filepath.Join(s.root, metaFile))
	if err != nil {
		return Meta{}, err
	}
	var meta Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		return Meta{}, err
	}
	return meta, nil
}

func (s *Store) writeMetaUnlocked(meta Meta) error {
	meta.SchemaVersion = SchemaVersion
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp := filepath.Join(s.root, metaFile+".tmp")
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.root, metaFile))
}

// withFileLock acquires a process-local mutex and an OS file lock for cross-process safety.
func (s *Store) withFileLock(fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	lockPath := filepath.Join(s.root, lockFile)
	fileLock := flock.New(lockPath)
	if err := fileLock.Lock(); err != nil {
		return fmt.Errorf("memory lock: %w", err)
	}
	defer func() {
		_ = fileLock.Unlock()
		_ = fileLock.Close()
	}()
	return fn()
}

func normalizeKey(key, claim string) string {
	key = strings.TrimSpace(key)
	if key != "" {
		return slugify(key)
	}
	return slugify(claim)
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
		if b.Len() >= 48 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "note"
	}
	if len(out) > 48 {
		out = out[:48]
	}
	return out
}

func newID() string {
	var buf [8]byte
	_, _ = rand.Read(buf[:])
	return "mem_" + hex.EncodeToString(buf[:])
}

func sortEntries(entries []Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return lessEntry(entries[i], entries[j])
	})
}

func lessEntry(a, b Entry) bool {
	if a.Trust != b.Trust {
		return a.Trust == TrustUser
	}
	if a.Key != b.Key {
		return a.Key < b.Key
	}
	return a.UpdatedAt.After(b.UpdatedAt)
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
