package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// The interactive graph Stack (ADR 0017) shows the user's own `jj log` and a
// live `jj log` of the projected result. The projection is not simulated: the
// real Stack pipeline runs as a chain of detached jj operations
// (`--at-op=<previous> --no-integrate-operation --ignore-working-copy`) that
// never touch the working copies, the Workspace directories, or the operation
// log until the user accepts, when exactly the previewed operation is
// integrated.

// stackGraphLogEntry is one commit of a `jj log` rendering: the lines jj
// printed for it, graph glyphs and colours intact, identified by the marker
// ajj asks jj to print at the start of its first line. Rows jj prints before
// the first commit (none in practice) form a leading entry without ids.
type stackGraphLogEntry struct {
	ChangeID string
	CommitID string
	Lines    []string
}

const stackGraphMarkerTemplate = `raw_escape_sequence("\e]777;ajj=" ++ change_id ++ ";" ++ commit_id ++ "\e\\")`

var stackGraphMarkerRE = regexp.MustCompile(`\x1b\]777;ajj=([a-z]{32});([0-9a-f]{40})\x1b\\`)

// stackGraphLogTemplate wraps the user's configured log template so every
// commit row starts with an invisible marker; the remaining bytes are what
// plain `jj log` prints.
func stackGraphLogTemplate(repoPath string) string {
	out, err := commandCaptureFn("jj", "-R", repoPath, "--ignore-working-copy", "--color=never", "--no-pager", "config", "get", "templates.log")
	template := strings.TrimSpace(out)
	if err != nil || template == "" {
		template = "builtin_log_compact"
	}
	return stackGraphMarkerTemplate + " ++ (" + template + ")"
}

// loadStackGraphLog renders the repository's default `jj log` (the user's
// revset, graph style, and colours) at the given operation.
func loadStackGraphLog(repoPath, operation string, color bool) ([]stackGraphLogEntry, error) {
	colorFlag := "--color=never"
	if color {
		colorFlag = "--color=always"
	}
	args := []string{"-R", repoPath, "--ignore-working-copy", "--no-pager", colorFlag}
	if operation != "" {
		args = append(args, "--at-op="+operation)
	}
	args = append(args, "log", "-T", stackGraphLogTemplate(repoPath))
	out, err := commandCaptureFn("jj", args...)
	if err != nil {
		return nil, err
	}
	return parseStackGraphLog(out), nil
}

func parseStackGraphLog(out string) []stackGraphLogEntry {
	if out == "" {
		return nil
	}
	entries := []stackGraphLogEntry{}
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if m := stackGraphMarkerRE.FindStringSubmatchIndex(line); m != nil {
			entry := stackGraphLogEntry{ChangeID: line[m[2]:m[3]], CommitID: line[m[4]:m[5]]}
			entry.Lines = []string{line[:m[0]] + line[m[1]:]}
			entries = append(entries, entry)
			continue
		}
		if len(entries) == 0 {
			entries = append(entries, stackGraphLogEntry{Lines: []string{line}})
			continue
		}
		entries[len(entries)-1].Lines = append(entries[len(entries)-1].Lines, line)
	}
	return entries
}

func stackGraphLogByChange(entries []stackGraphLogEntry) map[string]stackGraphLogEntry {
	byChange := make(map[string]stackGraphLogEntry, len(entries))
	for _, entry := range entries {
		if entry.ChangeID != "" {
			byChange[entry.ChangeID] = entry
		}
	}
	return byChange
}

// detachedStackChain runs the ordinary Stack pipeline against a chain of
// uncommitted jj operations. It is installed in place of the command seams
// while a preview runs: reads are pinned to the chain head, mutations extend
// it, and the pipeline's own undo/restore/integrate steps move along it.
type detachedStackChain struct {
	ops        []string
	read       func(string, ...string) (string, error)
	run        func(string, ...string) (string, error)
	transcript bytes.Buffer
}

func newDetachedStackChain(base string) *detachedStackChain {
	return &detachedStackChain{ops: []string{base}}
}

func (c *detachedStackChain) head() string { return c.ops[len(c.ops)-1] }

func (c *detachedStackChain) find(op string) int {
	for i := len(c.ops) - 1; i >= 0; i-- {
		if c.ops[i] == op || strings.HasPrefix(c.ops[i], op) || strings.HasPrefix(op, c.ops[i]) {
			return i
		}
	}
	return -1
}

// install swaps the command seams and silences stderr until restore runs.
// Previews are serialised by the view, so no two chains are installed at once.
func (c *detachedStackChain) install() (restore func()) {
	savedRead, savedCombined, savedToStderr, savedStderr := commandCaptureFn, commandCombinedCaptureFn, commandToStderrFn, stderrWriter
	c.read, c.run = savedRead, savedCombined
	commandCaptureFn = c.capture
	commandCombinedCaptureFn = c.combinedCapture
	commandToStderrFn = c.mutate
	stderrWriter = &c.transcript
	return func() {
		commandCaptureFn, commandCombinedCaptureFn, commandToStderrFn, stderrWriter = savedRead, savedCombined, savedToStderr, savedStderr
	}
}

// pinArgs adds --at-op for the chain head (replacing --at-op=@, keeping any
// other explicit operation such as the tidy probe's inspection) and the flags
// a detached command needs. Flags go right after `-R <path>` so the invoked
// subcommand and its arguments are untouched.
func (c *detachedStackChain) pinArgs(args []string, extra ...string) []string {
	kept := make([]string, 0, len(args))
	present := map[string]bool{}
	for _, arg := range args {
		if arg == "--at-op=@" {
			continue
		}
		if strings.HasPrefix(arg, "--at-op=") {
			present["--at-op"] = true
		}
		if strings.HasPrefix(arg, "--color=") {
			present["--color"] = true
		}
		present[arg] = true
		kept = append(kept, arg)
	}
	flags := []string{}
	if !present["--at-op"] {
		flags = append(flags, "--at-op="+c.head())
	}
	for _, flag := range append([]string{"--ignore-working-copy"}, extra...) {
		key := flag
		if strings.HasPrefix(flag, "--color=") {
			key = "--color"
		}
		if !present[key] {
			flags = append(flags, flag)
		}
	}
	insertAt := 0
	if len(kept) >= 2 && kept[0] == "-R" {
		insertAt = 2
	}
	out := make([]string, 0, len(kept)+len(flags))
	out = append(out, kept[:insertAt]...)
	out = append(out, flags...)
	return append(out, kept[insertAt:]...)
}

func (c *detachedStackChain) capture(name string, args ...string) (string, error) {
	if filepath.Base(name) != "jj" {
		return "", fmt.Errorf("stack preview cannot run %s", name)
	}
	return c.read(name, c.pinArgs(args)...)
}

// combinedCapture serves the tidy probe, which already asks for a detached
// operation and integrates it explicitly; the chain only pins its base.
func (c *detachedStackChain) combinedCapture(name string, args ...string) (string, error) {
	if filepath.Base(name) != "jj" {
		return "", fmt.Errorf("stack preview cannot run %s", name)
	}
	return c.run(name, c.pinArgs(args, "--no-integrate-operation", "--color=never", "--no-pager")...)
}

// jjSubcommand returns the verb (and its first word for `op`/`workspace`
// commands) of a jj invocation shaped like the pipeline's calls.
func jjSubcommand(args []string) (string, []string) {
	rest := args
	if len(rest) >= 2 && rest[0] == "-R" {
		rest = rest[2:]
	}
	for len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
		rest = rest[1:]
	}
	if len(rest) == 0 {
		return "", nil
	}
	verb := rest[0]
	rest = rest[1:]
	if (verb == "op" || verb == "workspace") && len(rest) > 0 {
		verb += " " + rest[0]
		rest = rest[1:]
	}
	return verb, rest
}

func (c *detachedStackChain) mutate(name string, args ...string) error {
	if filepath.Base(name) != "jj" {
		return fmt.Errorf("stack preview cannot run %s", name)
	}
	verb, rest := jjSubcommand(args)
	switch verb {
	case "workspace update-stale":
		// Working copies are not touched during a preview.
		return nil
	case "undo":
		if len(c.ops) < 2 {
			return errors.New("stack preview: nothing to undo on the preview chain")
		}
		c.ops = c.ops[:len(c.ops)-1]
		return nil
	case "op restore":
		if len(rest) == 0 {
			return errors.New("stack preview: op restore without an operation")
		}
		idx := c.find(rest[0])
		if idx < 0 {
			return fmt.Errorf("stack preview: operation %s is not on the preview chain", rest[0])
		}
		c.ops = c.ops[:idx+1]
		return nil
	case "op integrate":
		if len(rest) == 0 {
			return errors.New("stack preview: op integrate without an operation")
		}
		c.ops = append(c.ops, rest[0])
		return nil
	}
	out, err := c.run(name, c.pinArgs(args, "--no-integrate-operation", "--color=never", "--no-pager")...)
	if err != nil {
		return err
	}
	op, err := detachedOperationID(out)
	if err != nil {
		if jjReportedNothingChanged(out) {
			// jj wrote no operation, so the chain head stands.
			return nil
		}
		return fmt.Errorf("stack preview: %s produced no detached operation: %w", verb, err)
	}
	c.ops = append(c.ops, op)
	return nil
}

func jjReportedNothingChanged(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "Nothing changed." {
			return true
		}
	}
	return false
}

// quietStackCommands routes the pipeline's progress output and jj's own
// output into a transcript while the interactive view owns the terminal.
func quietStackCommands() (*bytes.Buffer, func()) {
	savedToStderr, savedStderr := commandToStderrFn, stderrWriter
	combined := commandCombinedCaptureFn
	transcript := &bytes.Buffer{}
	commandToStderrFn = func(name string, args ...string) error {
		out, err := combined(name, append([]string{}, args...)...)
		transcript.WriteString(out)
		return err
	}
	stderrWriter = transcript
	return transcript, func() { commandToStderrFn, stderrWriter = savedToStderr, savedStderr }
}

// stackGraphLine is one line of work the view can stack: a Workspace
// (`name@`) or a Bookmark Stack Source (`name` or `name@remote`).
type stackGraphLine struct {
	Key        string
	Name       string
	Kind       string // "ws" or "bm"
	Status     string
	Selectable bool
	// InAll marks lines the `a` key selects (the old All row / --all rule).
	InAll      bool
	Role       string
	HeadChange string
	HeadCommit string
	Info       workspaceInfo
	Source     bookmarkSource
}

const (
	stackLineWorkspace = "ws"
	stackLineBookmark  = "bm"
)

func (l stackGraphLine) isTarget() bool {
	return strings.HasPrefix(l.Status, "target")
}

// stackGraphState is everything the view knows about the repository at its
// base operation. It is rebuilt after a retarget, an apply, or an undo.
type stackGraphState struct {
	repoRoot     string
	cfg          config
	project      string
	color        bool
	target       stackTargetResolution
	targetInfo   workspaceInfo
	infos        []workspaceInfo
	byHandle     map[string]workspaceInfo
	current      string
	sources      []bookmarkSource
	bookmarkRefs []bookmarkRef
	base         string
	log          []stackGraphLogEntry
	heads        map[string]string
	baseConflict map[string]bool
	trunkCommit  string
	// payloads holds, per line key, the change ids the line owns at the base
	// operation: its mutable history that is not in the target.
	payloads map[string]map[string]bool
	// parents maps each shown commit to its parent commits.
	parents map[string][]string
	// unavailable explains Workspaces whose snapshot failed; they are shown
	// but cannot be stacked.
	unavailable map[string]string
}

func (st *stackGraphState) targetPath() string { return st.target.Path }

// cutPayload is the part of a line of work at or below commit: what
// stacking the line "up to here" brings into the target.
func (st *stackGraphState) cutPayload(key, commit string) map[string]bool {
	changeOf := map[string]string{}
	for _, entry := range st.log {
		changeOf[entry.CommitID] = entry.ChangeID
	}
	line := st.payloads[key]
	set := map[string]bool{}
	seen := map[string]bool{}
	queue := []string{commit}
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		if seen[next] {
			continue
		}
		seen[next] = true
		if change := changeOf[next]; line[change] {
			set[change] = true
		}
		queue = append(queue, st.parents[next]...)
	}
	return set
}

// payloadsWithCuts narrows the lines stacked "up to here" to their cut.
func (st *stackGraphState) payloadsWithCuts(cuts map[string]string) map[string]map[string]bool {
	if len(cuts) == 0 {
		return st.payloads
	}
	out := make(map[string]map[string]bool, len(st.payloads))
	for key, ids := range st.payloads {
		out[key] = ids
	}
	for key, commit := range cuts {
		out[key] = st.cutPayload(key, commit)
	}
	return out
}

func fullChangeIDs(repoPath, operation, revset string) ([]string, error) {
	return logIDsAtOperation(repoPath, operation, revset, "change_id")
}

func fullCommitIDs(repoPath, operation, revset string) ([]string, error) {
	return logIDsAtOperation(repoPath, operation, revset, "commit_id")
}

func logIDsAtOperation(repoPath, operation, revset, field string) ([]string, error) {
	args := []string{"-R", repoPath, "--ignore-working-copy", "--color=never", "--no-pager"}
	if operation != "" {
		args = append(args, "--at-op="+operation)
	}
	args = append(args, "log", "-r", revset, "--no-graph", "-T", field+" ++ \"\\n\"")
	out, err := commandCaptureFn("jj", args...)
	if err != nil {
		return nil, err
	}
	return uniqueNonEmptyStrings(strings.Split(out, "\n")), nil
}

func workspaceHeadsAtOperation(repoPath, operation string) (map[string]string, map[string]string, error) {
	args := []string{"-R", repoPath, "--ignore-working-copy", "--color=never", "--no-pager"}
	if operation != "" {
		args = append(args, "--at-op="+operation)
	}
	args = append(args, "workspace", "list", "-T", "name ++ \"\\t\" ++ target.change_id() ++ \"\\t\" ++ target.commit_id() ++ \"\\n\"")
	out, err := commandCaptureFn("jj", args...)
	if err != nil {
		return nil, nil, err
	}
	changes, commits := map[string]string{}, map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) != 3 || fields[0] == "" {
			continue
		}
		changes[fields[0]], commits[fields[0]] = fields[1], fields[2]
	}
	return changes, commits, nil
}

// snapshotStackGraphWorkspaces records on-disk edits of every present
// Workspace before the base operation is read, so the preview sees the same
// payloads the user sees and the accepted operation leaves nothing to
// snapshot into a divergent commit. A Workspace whose snapshot fails (for
// example a stale working copy) is reported and excluded from stacking.
func snapshotStackGraphWorkspaces(repoRoot string, cfg config, project string) (map[string]string, error) {
	refs, err := listWorkspaceRefs(repoRoot)
	if err != nil {
		return nil, err
	}
	current := ""
	if detected, err := currentWorkspaceHandle(repoRoot, refs); err == nil {
		current = detected
	}
	unavailable := map[string]string{}
	for _, ref := range refs {
		path := workspacePathForRef(repoRoot, cfg.WorkspacesRoot, project, ref, current)
		if path == "" {
			continue
		}
		if st, err := os.Stat(path); err != nil || !st.IsDir() {
			continue
		}
		if _, err := os.Lstat(filepath.Join(path, ".jj")); err != nil {
			continue
		}
		if _, err := commandCaptureFn("jj", "-R", path, "--config=snapshot.auto-update-stale=false", "--color=never", "--no-pager", "status"); err != nil {
			reason := "snapshot failed"
			if isStaleWorkingCopyError(err) {
				reason = "stale working copy (jj workspace update-stale)"
			}
			unavailable[ref.Handle] = reason
		}
	}
	return unavailable, nil
}

// loadStackGraphState resolves the Stack target, snapshots the Workspaces,
// and reads everything the view shows at one base operation.
func loadStackGraphState(repoRoot string, cfg config, project string, workspaceOverride string, color bool) (*stackGraphState, error) {
	unavailable, err := snapshotStackGraphWorkspaces(repoRoot, cfg, project)
	if err != nil {
		return nil, err
	}
	cfg, infos, byHandle, target, err := resolveStackTargetWorkspace(repoRoot, cfg, project, workspaceOverride)
	if err != nil {
		return nil, err
	}
	if reason, bad := unavailable[target.Handle]; bad {
		return nil, fmt.Errorf("target Workspace %q: %s", target.Handle, reason)
	}
	st := &stackGraphState{repoRoot: repoRoot, cfg: cfg, project: project, color: color, target: target, targetInfo: byHandle[target.Handle], infos: infos, byHandle: byHandle, unavailable: unavailable, payloads: map[string]map[string]bool{}}
	for _, info := range infos {
		if info.Current {
			st.current = info.Ref.Handle
		}
	}
	path := target.Path
	st.base, err = currentOperationID(path)
	if err != nil {
		return nil, fmt.Errorf("record Stack base operation: %w", err)
	}
	st.sources, st.bookmarkRefs, err = discoverBookmarkSources(path, target.Handle)
	if err != nil {
		return nil, err
	}
	st.heads, _, err = workspaceHeadsAtOperation(path, st.base)
	if err != nil {
		return nil, err
	}
	st.log, err = loadStackGraphLog(path, st.base, color)
	if err != nil {
		return nil, fmt.Errorf("read jj log: %w", err)
	}
	conflicts, err := fullChangeIDs(path, st.base, "conflicts() & mutable()")
	if err != nil {
		return nil, err
	}
	st.baseConflict = stringSet(conflicts)
	if trunk, err := commandCaptureFn("jj", "-R", path, "--ignore-working-copy", "--color=never", "--no-pager", "--at-op="+st.base, "log", "-r", "trunk()", "--no-graph", "-T", "commit_id ++ \"\\n\""); err == nil {
		if ids := uniqueNonEmptyStrings(strings.Split(trunk, "\n")); len(ids) == 1 {
			st.trunkCommit = ids[0]
		}
	}
	// A line of work is everything shown from its head down to the target's
	// history, immutable commits included; the target's own line runs down
	// to trunk. Only shown commits matter, so the query is bounded by them,
	// named by commit id because a divergent change id names several.
	shown := []string{}
	for _, entry := range st.log {
		if entry.CommitID != "" {
			shown = append(shown, entry.CommitID)
		}
	}
	shownRevset := revsetUnion(shown)
	if len(shown) > 0 {
		out, err := commandCaptureFn("jj", "-R", path, "--ignore-working-copy", "--color=never", "--no-pager", "--at-op="+st.base, "log", "-r", shownRevset, "--no-graph", "-T", `commit_id ++ "\t" ++ parents.map(|c| c.commit_id()).join(" ") ++ "\n"`)
		if err != nil {
			return nil, err
		}
		st.parents = map[string][]string{}
		for _, line := range strings.Split(out, "\n") {
			commit, parents, ok := strings.Cut(strings.TrimSpace(line), "\t")
			if ok {
				st.parents[commit] = strings.Fields(parents)
			}
		}
	}
	lineOf := func(head string) (map[string]bool, error) {
		ids, err := fullChangeIDs(path, st.base, "(::"+head+" & ~::"+target.Handle+"@) & "+shownRevset)
		return stringSet(ids), err
	}
	for _, info := range infos {
		if info.Missing {
			continue
		}
		if info.Ref.Handle != target.Handle {
			ids, err := lineOf(info.Ref.Handle + "@")
			if err != nil {
				return nil, err
			}
			st.payloads[info.Ref.Handle+"@"] = ids
			continue
		}
		ids, err := fullChangeIDs(path, st.base, "(::"+target.Handle+"@ & ~::trunk()) & "+shownRevset)
		if err != nil {
			ids, err = fullChangeIDs(path, st.base, "::"+target.Handle+"@ & "+shownRevset)
		}
		if err != nil {
			return nil, err
		}
		st.payloads[info.Ref.Handle+"@"] = stringSet(ids)
	}
	for _, source := range st.sources {
		if source.Ref.Commit == "" {
			continue
		}
		ids, err := lineOf(source.Ref.Commit)
		if err != nil {
			return nil, err
		}
		st.payloads[source.Ref.label()] = ids
	}
	return st, nil
}

// trunkBookmark names the local bookmark that stands for trunk: one at
// trunk() when jj resolves it, else main, master, or trunk by name.
func (st *stackGraphState) trunkBookmark() string {
	rank := func(name string) int {
		switch name {
		case "main":
			return 0
		case "master":
			return 1
		case "trunk":
			return 2
		}
		return 3
	}
	best, bestRank := "", 4
	for _, ref := range st.bookmarkRefs {
		if !ref.Present || ref.Remote != "" {
			continue
		}
		r := rank(ref.Name)
		if ref.Commit == st.trunkCommit && st.trunkCommit != "" {
			r -= 4
		}
		if r < bestRank {
			best, bestRank = ref.Name, r
		}
	}
	if bestRank >= 3 {
		return ""
	}
	return best
}

func stringSet(items []string) map[string]bool {
	set := make(map[string]bool, len(items))
	for _, item := range items {
		set[item] = true
	}
	return set
}

// lines classifies every line of work for the given shape: what it is
// relative to the target and whether Space can make it a Stack Input.
func (st *stackGraphState) lines(shape string) []stackGraphLine {
	lines := []stackGraphLine{}
	line := shape == "line"
	for _, info := range st.infos {
		l := stackGraphLine{Key: info.Ref.Handle + "@", Name: info.Ref.Handle, Kind: stackLineWorkspace, Info: info, HeadChange: st.heads[info.Ref.Handle]}
		l.Role = lineStackRoleForInfo(info)
		switch {
		case info.Ref.Handle == st.target.Handle:
			l.Status = "target"
			if line {
				l.Status = "target: not a Line Stack Input"
			}
		case info.Missing:
			l.Status = "missing"
		case st.unavailable[info.Ref.Handle] != "":
			l.Status = st.unavailable[info.Ref.Handle]
		case info.Conflict && !line:
			l.Status, l.Selectable = "conflict", true
			l.InAll = !stackInputProtectedByTarget(info, st.target)
		case line && l.Role == selectorRoleFollow:
			l.Status, l.Selectable = "follow-only", true
		case line:
			l.Status, l.Selectable, l.InAll = "ready", true, true
		case isStackRelevant(info):
			l.Status, l.Selectable = "ready", true
			l.InAll = !stackInputProtectedByTarget(info, st.target)
		case isMovableToMain(info):
			l.Status, l.Selectable = "behind target", true
		default:
			l.Status = "already in " + st.target.Handle + "@"
		}
		lines = append(lines, l)
	}
	for _, source := range st.sources {
		l := stackGraphLine{Key: source.Ref.label(), Name: source.Ref.label(), Kind: stackLineBookmark, Source: source, HeadCommit: source.Ref.Commit}
		switch {
		case line:
			l.Status = "bookmarks cannot be Line Stack Inputs"
		case source.Ref.Conflict:
			l.Status = "ref-conflict"
		case !source.stackable():
			l.Status = "already in " + st.target.Handle + "@"
		case source.Conflict:
			l.Status, l.Selectable = "conflict", true
			l.InAll = source.inAll()
		case source.InTrunk:
			l.Status, l.Selectable = "in trunk", true
		case source.Ref.Remote != "":
			l.Status, l.Selectable = "remote", true
		default:
			l.Status, l.Selectable, l.InAll = "ready", true, true
		}
		lines = append(lines, l)
	}
	byChange := stackGraphLogByChange(st.log)
	for i := range lines {
		if lines[i].HeadChange != "" {
			if entry, ok := byChange[lines[i].HeadChange]; ok {
				lines[i].HeadCommit = entry.CommitID
			}
			continue
		}
		for _, entry := range st.log {
			if entry.CommitID == lines[i].HeadCommit {
				lines[i].HeadChange = entry.ChangeID
				break
			}
		}
	}
	return lines
}

func stackGraphLineByKey(lines []stackGraphLine, key string) (stackGraphLine, bool) {
	for _, l := range lines {
		if l.Key == key {
			return l, true
		}
	}
	return stackGraphLine{}, false
}

// stackPreviewSpec is what the user has molded: the order of the selected
// lines is the input order.
type stackPreviewSpec struct {
	id       uint64
	shape    string
	selected []string
	// cuts stacks a selected line only up to a commit of it, by line key.
	cuts          map[string]string
	moveBookmarks []string
	tidy          bool
}

type stackBookmarkMove struct {
	Name string
	From string
	To   string
	Err  string
}

type stackPreviewResult struct {
	id   uint64
	spec stackPreviewSpec
	// err reports a pipeline failure; the preview cannot be applied.
	err error
	// stackOp is the detached operation an accept integrates.
	stackOp       string
	log           []stackGraphLogEntry
	heads         map[string]string
	created       map[string]bool
	rewritten     map[string]bool
	conflicts     []string
	conflictFiles []string
	lineConflict  string
	bookmarkMoves []stackBookmarkMove
	tidyCloses    []string
	tidyKept      []string
	notes         []string
	top           string
}

func (r *stackPreviewResult) hasConflicts() bool {
	return r != nil && (len(r.conflicts) > 0 || r.lineConflict != "")
}

// refusedBookmarkMove names the first marked bookmark jj would not move,
// with jj's reason; an accept must not silently skip it.
func (r *stackPreviewResult) refusedBookmarkMove() string {
	if r == nil {
		return ""
	}
	for _, move := range r.bookmarkMoves {
		if move.Err != "" {
			return move.Name + ": " + move.Err
		}
	}
	return ""
}

var stackHeadingRE = regexp.MustCompile(`== (.*) ==`)

func stackTranscriptNotes(transcript string) []string {
	notes := []string{}
	for _, match := range stackHeadingRE.FindAllStringSubmatch(stripANSI(transcript), -1) {
		text := match[1]
		switch {
		case strings.HasPrefix(text, "Stack shape:"), strings.HasPrefix(text, "Rebase mode:"), strings.HasPrefix(text, "Tidy Stack"), strings.HasPrefix(text, "Conflict fallback"), strings.HasPrefix(text, "Both strategies"):
			notes = append(notes, text)
		}
	}
	return notes
}

// preview runs the real Stack pipeline detached from the repository and
// reads the result. It must run off the UI goroutine, one at a time.
func (st *stackGraphState) preview(spec stackPreviewSpec) stackPreviewResult {
	res := stackPreviewResult{id: spec.id, spec: spec, created: map[string]bool{}, rewritten: map[string]bool{}}
	lines := st.lines(spec.shape)
	chain := newDetachedStackChain(st.base)
	restore := chain.install()
	defer restore()
	path := st.targetPath()
	var inputs []stackInput
	var lineInputs []lineStackInput
	for _, key := range spec.selected {
		l, ok := stackGraphLineByKey(lines, key)
		if !ok || !l.Selectable {
			res.err = fmt.Errorf("%s cannot be stacked here", key)
			return res
		}
		if cut, ok := spec.cuts[key]; ok {
			if spec.shape == "line" {
				res.err = fmt.Errorf("Line Stack takes whole Workspaces: %s is selected up to a change", key)
				return res
			}
			inputs = append(inputs, revisionStackInput(shortChangeID(changeIDForCommit(path, st.base, cut, st.log)), cut))
			continue
		}
		if l.Kind == stackLineBookmark {
			inputs = append(inputs, bookmarkStackInput(l.Source))
		} else {
			inputs = append(inputs, workspaceStackInput(l.Name))
			lineInputs = append(lineInputs, lineStackInput{Handle: l.Name})
		}
	}
	if len(inputs) == 0 {
		res.err = errors.New("select at least one line of work first")
		return res
	}
	if spec.shape == "line" {
		plan, err := buildLineStackPlan(st.infos, lineInputs)
		if err == nil {
			plan, err = resolveLineStackPlan(path, plan)
		}
		if err != nil {
			res.err = err
			return res
		}
		err = executeLineStackPlan(path, plan)
		var conflict lineStackConflictError
		if errors.As(err, &conflict) {
			res.lineConflict = conflict.Error()
		} else if err != nil {
			res.err = err
			return res
		}
		if tips, err := fullCommitIDs(path, chain.head(), plan.FinalTip); err == nil && len(tips) > 0 {
			res.top = tips[0]
		}
	} else {
		stack := st.cfg.Stack
		stack.Shape = spec.shape
		if err := validateLinearSelection(path, inputs, stack.Shape); err != nil {
			res.err = err
			return res
		}
		if err := executeStack(st.targetInfo, st.target.Handle, inputs, st.byHandle, stack); err != nil {
			res.err = err
			return res
		}
		parents, err := fullCommitIDs(path, chain.head(), st.target.Handle+"@-")
		if err != nil {
			res.err = err
			return res
		}
		if len(parents) == 1 {
			res.top = parents[0]
		} else if heads, err := fullCommitIDs(path, chain.head(), st.target.Handle+"@"); err == nil && len(heads) == 1 {
			res.top = heads[0]
		}
	}
	res.notes = stackTranscriptNotes(chain.transcript.String())
	byLabel := map[string]bookmarkRef{}
	for _, ref := range st.bookmarkRefs {
		byLabel[ref.label()] = ref
	}
	topChange := ""
	if res.top != "" {
		topChange = shortChangeID(changeIDForCommit(path, chain.head(), res.top, nil))
	}
	for _, name := range spec.moveBookmarks {
		move := stackBookmarkMove{Name: name, To: topChange}
		ref, ok := byLabel[name]
		if !ok || !ref.Present || ref.Remote != "" {
			move.Err = name + " is not a local bookmark"
		} else if res.top == "" {
			move.Err = "the stack has no top to move " + name + " to"
		} else {
			move.From = shortChangeID(changeIDForCommit(path, chain.head(), ref.Commit, st.log))
			if err := chain.mutate("jj", "-R", path, "bookmark", "set", name, "-r", res.top); err != nil {
				move.Err = jjErrorMessage(err)
			}
		}
		res.bookmarkMoves = append(res.bookmarkMoves, move)
	}
	res.stackOp = chain.head()
	conflicted, err := fullChangeIDs(path, chain.head(), "conflicts() & mutable()")
	if err != nil {
		res.err = err
		return res
	}
	for _, id := range conflicted {
		if !st.baseConflict[id] {
			res.conflicts = append(res.conflicts, id)
		}
	}
	res.conflictFiles = conflictedFiles(path, chain.head(), res.conflicts)
	workspaceInputs := []string{}
	for _, input := range inputs {
		if input.Handle != "" {
			workspaceInputs = append(workspaceInputs, input.Handle)
		}
	}
	if spec.tidy {
		// Tidy is not simulated on the chain: `jj workspace forget` writes
		// the repository-wide workspace store, which no operation scopes.
		// The after pane marks the cursors Tidy would close instead.
		res.tidyCloses, res.tidyKept = st.tidyPlan(path, workspaceInputs, res.hasConflicts())
	}
	res.log, err = loadStackGraphLog(path, res.stackOp, st.color)
	if err != nil {
		res.err = fmt.Errorf("read projected jj log: %w", err)
		return res
	}
	res.heads, _, err = workspaceHeadsAtOperation(path, res.stackOp)
	if err != nil {
		res.err = err
		return res
	}
	res.created, res.rewritten = stackGraphChanges(st.log, res.log)
	return res
}

// stackGraphChanges compares two renderings by change id: a change that was
// absent is new; one whose commit id differs was rewritten.
func stackGraphChanges(before, after []stackGraphLogEntry) (created, rewritten map[string]bool) {
	created, rewritten = map[string]bool{}, map[string]bool{}
	base := stackGraphLogByChange(before)
	for _, entry := range after {
		if entry.ChangeID == "" {
			continue
		}
		old, ok := base[entry.ChangeID]
		switch {
		case !ok:
			created[entry.ChangeID] = true
		case old.CommitID != entry.CommitID:
			rewritten[entry.ChangeID] = true
		}
	}
	return created, rewritten
}

// tidyPlan decides which stacked input Workspaces Tidy would Close at the
// current operation: never the target, the Current Workspace, or a protected
// configured Main; never one with an in-progress head; nothing after a
// conflicted stack; and only normally Closable ones.
func (st *stackGraphState) tidyPlan(path string, workspaceInputs []string, conflicted bool) ([]string, []string) {
	closes, kept := []string{}, []string{}
	candidates := []workspaceInfo{}
	for _, handle := range uniqueNonEmptyStrings(workspaceInputs) {
		info, ok := st.byHandle[handle]
		if !ok || handle == st.target.Handle || info.Missing {
			continue
		}
		if handle == st.current {
			kept = append(kept, handle+"@ kept: the Current Workspace")
			continue
		}
		if stackInputProtectedByTarget(info, st.target) {
			kept = append(kept, handle+"@ kept: configured main_workspace")
			continue
		}
		if info.External {
			// Deleting a directory outside the Project layout needs its own
			// consent, which this view does not collect.
			kept = append(kept, handle+"@ kept: outside the Project layout ("+info.Path+"); close it with ajj close")
			continue
		}
		if conflicted {
			kept = append(kept, handle+"@ kept: the stack conflicts")
			continue
		}
		inProgress, err := revisionMatches(path, lineStackInProgressHeadRevset(handle))
		if err != nil {
			kept = append(kept, handle+"@ kept: "+err.Error())
			continue
		}
		if inProgress {
			kept = append(kept, handle+"@ kept: in-progress changes")
			continue
		}
		candidates = append(candidates, info)
	}
	if len(candidates) == 0 {
		return closes, kept
	}
	protection, err := newCloseProtectionContext(path, candidates)
	if err != nil {
		for _, info := range candidates {
			kept = append(kept, info.Ref.Handle+"@ kept: "+err.Error())
		}
		return closes, kept
	}
	unsafe, err := normallyUnclosableTargetsWithProtection(path, candidates, protection)
	if err != nil {
		for _, info := range candidates {
			kept = append(kept, info.Ref.Handle+"@ kept: "+err.Error())
		}
		return closes, kept
	}
	unsafeSet := map[string]bool{}
	for _, info := range unsafe {
		unsafeSet[info.Ref.Handle] = true
	}
	for _, info := range candidates {
		if unsafeSet[info.Ref.Handle] {
			kept = append(kept, info.Ref.Handle+"@ kept: not normally Closable (its work is not represented in a surviving Workspace)")
			continue
		}
		closes = append(closes, info.Ref.Handle)
	}
	return closes, kept
}

func conflictedFiles(path, operation string, changes []string) []string {
	files := []string{}
	seen := map[string]bool{}
	for i, id := range changes {
		if i >= 3 {
			files = append(files, "…")
			break
		}
		out, err := commandCaptureFn("jj", "-R", path, "--ignore-working-copy", "--color=never", "--no-pager", "--at-op="+operation, "resolve", "--list", "-r", id)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(out, "\n") {
			fields := regexp.MustCompile(`\s{2,}`).Split(strings.TrimSpace(line), 2)
			if fields[0] == "" || seen[fields[0]] {
				continue
			}
			seen[fields[0]] = true
			files = append(files, sanitizeTidyPreview(fields[0]))
		}
	}
	return files
}

// fetchStackGraphRemotes runs jj git fetch; changed reports whether it
// recorded a new operation, that is, whether anything was imported.
func fetchStackGraphRemotes(repoRoot string) (string, bool, error) {
	before, err := currentOperationID(repoRoot)
	if err != nil {
		return "", false, err
	}
	if _, err := commandCaptureFn("jj", "-R", repoRoot, "--color=never", "--no-pager", "git", "fetch"); err != nil {
		return "", false, err
	}
	after, err := currentOperationID(repoRoot)
	if err != nil {
		return "", false, err
	}
	if before == after {
		return "fetched: nothing new", false, nil
	}
	return "fetched", true, nil
}

func changeIDForCommit(path, operation, commit string, log []stackGraphLogEntry) string {
	for _, entry := range log {
		if entry.CommitID == commit {
			return entry.ChangeID
		}
	}
	ids, err := fullChangeIDs(path, operation, commit)
	if err != nil || len(ids) != 1 {
		return commit
	}
	return ids[0]
}

func shortChangeID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// jjErrorMessage extracts jj's own `Error:` line from a failed command.
func jjErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	for _, line := range strings.Split(err.Error(), "\n") {
		if _, message, ok := strings.Cut(line, "Error: "); ok {
			return sanitizeTidyPreview(message)
		}
	}
	return sanitizeTidyPreview(err.Error())
}

var errStackPreviewStale = errors.New("the repository changed since the preview; nothing was applied")

type stackApplyResult struct {
	err   error
	stale bool
	// undoRecorded reports that `ajj undo` can restore the base operation.
	undoRecorded bool
	log          []stackGraphLogEntry
	heads        map[string]string
	closedPaths  []string
	lines        []string
}

// apply integrates exactly the previewed Stack operation, then updates the
// touched Workspaces, Tidies through the normal Close safety path, and
// records the undo state. It refuses when the repository moved on, including
// edits made in a touched Workspace while the view was open.
func (st *stackGraphState) apply(res stackPreviewResult, spec stackPreviewSpec) stackApplyResult {
	out := stackApplyResult{}
	_, restore := quietStackCommands()
	defer restore()
	path := st.targetPath()
	lines := st.lines(spec.shape)
	touched := []string{st.target.Handle}
	for _, key := range spec.selected {
		if l, ok := stackGraphLineByKey(lines, key); ok && l.Kind == stackLineWorkspace {
			touched = append(touched, l.Name)
		}
	}
	touched = uniqueNonEmptyStrings(touched)
	// Snapshot first: an edit made since the view opened would otherwise be
	// snapshotted by update-stale into a divergent commit after the rewrite.
	for _, handle := range touched {
		info, ok := st.byHandle[handle]
		if !ok || info.Missing {
			continue
		}
		if _, err := commandCaptureFn("jj", "-R", info.Path, "--config=snapshot.auto-update-stale=false", "--color=never", "--no-pager", "status"); err != nil {
			out.err = fmt.Errorf("snapshot Workspace %q before applying: %w", handle, err)
			return out
		}
	}
	current, err := currentOperationID(path)
	if err != nil {
		out.err = err
		return out
	}
	if current != st.base {
		out.err, out.stale = errStackPreviewStale, true
		return out
	}
	if err := commandToStderrFn("jj", "-R", path, "op", "integrate", res.stackOp); err != nil {
		out.err = fmt.Errorf("integrate the previewed operation: %w", err)
		return out
	}
	// jj reconciles an operation that landed between the check and the
	// integration; only an exclusively owned result is the previewed one.
	current, err = currentOperationID(path)
	if err != nil {
		out.err = err
		return out
	}
	reconciled := !strings.HasPrefix(current, res.stackOp)
	// Working copies must follow the integrated view either way; jj refuses
	// to work in a stale Workspace otherwise.
	for _, handle := range touched {
		info, ok := st.byHandle[handle]
		if !ok || info.Missing {
			continue
		}
		if err := commandToStderrFn("jj", "-R", info.Path, "workspace", "update-stale"); err != nil {
			out.lines = append(out.lines, "✗ "+handle+"@ update-stale: "+jjErrorMessage(err))
		}
	}
	if reconciled {
		out.err = fmt.Errorf("another operation landed while applying: the previewed Stack was integrated alongside it as %s, not as the previewed result (expected %s); Workspaces follow that reconciled state, Tidy was skipped, and no undo record was written. Inspect `jj op log`; `jj op restore %s` returns to the state before the Stack", current, res.stackOp, st.base)
		out.stale = true
		return out
	}
	for _, move := range res.bookmarkMoves {
		if move.Err != "" {
			out.lines = append(out.lines, "✗ "+move.Name+": "+move.Err)
		} else {
			out.lines = append(out.lines, move.Name+" moved "+move.From+" → "+move.To+" (not pushed)")
		}
	}
	if spec.tidy {
		out.lines = append(out.lines, st.tidyClose(path, res, &out)...)
	}
	out.undoRecorded, err = recordUndoableOperation(st.repoRoot, path, "stack", st.base)
	if err != nil {
		out.lines = append(out.lines, "✗ undo not recorded: "+err.Error()+"; jj op restore "+st.base+" returns to the state before the Stack")
	}
	afterOp, err := currentOperationID(path)
	if err != nil {
		out.err = err
		return out
	}
	out.log, err = loadStackGraphLog(path, afterOp, st.color)
	if err != nil {
		out.err = err
		return out
	}
	out.heads, _, err = workspaceHeadsAtOperation(path, afterOp)
	if err != nil {
		out.err = err
	}
	return out
}

// tidyClose Closes the previewed Tidy set through the ordinary normal-close
// path (snapshot, review, revalidation; never forced). Failures are reported
// and never undo the Stack.
func (st *stackGraphState) tidyClose(path string, res stackPreviewResult, out *stackApplyResult) []string {
	lines := append([]string{}, res.tidyKept...)
	for i := range lines {
		lines[i] = "tidy: " + lines[i]
	}
	if len(res.tidyCloses) == 0 {
		if len(lines) == 0 {
			lines = append(lines, "tidy: no stacked Workspaces to close")
		}
		return lines
	}
	targets := []workspaceInfo{}
	for _, handle := range res.tidyCloses {
		if info, ok := st.byHandle[handle]; ok {
			targets = append(targets, info)
		}
	}
	protection, err := prepareCloseReview(path, targets)
	if err != nil {
		return append(lines, "✗ tidy: "+err.Error())
	}
	unsafe, err := normallyUnclosableTargetsWithProtection(path, targets, protection)
	if err != nil {
		return append(lines, "✗ tidy: "+err.Error())
	}
	if len(unsafe) > 0 {
		unsafeSet := map[string]bool{}
		for _, info := range unsafe {
			unsafeSet[info.Ref.Handle] = true
			lines = append(lines, "tidy: "+info.Ref.Handle+"@ kept: not normally Closable after the Stack")
		}
		kept := targets[:0]
		for _, info := range targets {
			if !unsafeSet[info.Ref.Handle] {
				kept = append(kept, info)
			}
		}
		targets = kept
		if len(targets) == 0 {
			return lines
		}
		if protection, err = prepareCloseReview(path, targets); err != nil {
			return append(lines, "✗ tidy: "+err.Error())
		}
	}
	closed, err := closeWorkspacesWithProtection(path, targets, false, true, false, protection)
	out.closedPaths = append(out.closedPaths, closed...)
	closedHandles := []string{}
	for _, info := range targets {
		for _, p := range closed {
			if p == info.Path {
				closedHandles = append(closedHandles, info.Ref.Handle+"@")
			}
		}
	}
	if len(closedHandles) > 0 {
		lines = append(lines, "tidy closed "+strings.Join(closedHandles, ", ")+" (work kept in "+st.target.Handle+"@, directories removed)")
	}
	if err != nil {
		lines = append(lines, "✗ tidy: "+err.Error())
	}
	return lines
}

// undo restores the operation recorded by the last accepted Stack, with the
// same guards as `ajj undo`, then refreshes the touched working copies.
func (st *stackGraphState) undo() error {
	_, restore := quietStackCommands()
	defer restore()
	if _, err := undoRecordedOperation(st.repoRoot); err != nil {
		return err
	}
	for _, info := range st.infos {
		if info.Missing {
			continue
		}
		if _, err := os.Stat(info.Path); err != nil {
			continue
		}
		_ = commandToStderrFn("jj", "-R", info.Path, "workspace", "update-stale")
	}
	return nil
}

// stackGraphOwner maps every change the view knows to the line of work it
// belongs to: the target first, then selected inputs in order, then any other
// line whose history contains it.
func stackGraphOwners(lines []stackGraphLine, payloads map[string]map[string]bool, selected []string) map[string]string {
	owners := map[string]string{}
	ordered := []string{}
	for _, l := range lines {
		if l.isTarget() {
			ordered = append(ordered, l.Key)
		}
	}
	ordered = append(ordered, selected...)
	for _, l := range lines {
		ordered = append(ordered, l.Key)
	}
	for _, key := range ordered {
		for id := range payloads[key] {
			if _, taken := owners[id]; !taken {
				owners[id] = key
			}
		}
	}
	return owners
}
