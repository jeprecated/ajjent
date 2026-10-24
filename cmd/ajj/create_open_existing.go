package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// resolveLocalBookmarkCommit is the exact commit `ajj create --bookmark`
// bases a new Workspace on, and the one an existing Workspace must sit on.
// Only a present, unconflicted local bookmark qualifies: Ajj never creates,
// moves, or tracks it.
func resolveLocalBookmarkCommit(repoRoot, name string) (string, error) {
	refs, err := listBookmarkRefs(repoRoot)
	if err != nil {
		return "", err
	}
	for _, ref := range refs {
		if ref.Name != name || ref.Remote != "" {
			continue
		}
		switch {
		case ref.Conflict:
			return "", fmt.Errorf("bookmark %q is conflicted; resolve it with `jj bookmark set` first", name)
		case !ref.Present || ref.Commit == "":
			return "", fmt.Errorf("bookmark %q is deleted locally; restore it with `jj bookmark set` or `jj bookmark track` first", name)
		}
		return ref.Commit, nil
	}
	return "", fmt.Errorf("local bookmark %q not found; create it first, e.g. `jj bookmark create %s -r main`", name, name)
}

// openExistingForCreate is `ajj create <handle> --open-existing`: it Opens the
// registered Workspace instead of Creating one. With a bookmark, the
// Workspace must already sit on the bookmark's commit (as @ or a parent of
// @); it is never moved. A stale working copy is updated only with
// updateStale. A registration whose directory is gone is forgotten only when
// its head is an empty, undescribed cursor on the bookmark, which loses
// nothing; it then returns recreate so the caller Creates the Workspace again.
func openExistingForCreate(repoRoot string, cfg config, project, handle, bookmark, commit string, updateStale bool) (recreate bool, err error) {
	infos, _, err := loadWorkspaceInfos(repoRoot, cfg, project)
	if err != nil {
		return false, err
	}
	var info workspaceInfo
	found := false
	for _, candidate := range infos {
		if candidate.Ref.Handle == handle {
			info, found = candidate, true
			break
		}
	}
	if !found {
		return false, workspaceNotFoundError(handle)
	}
	if info.Missing {
		if err := forgetMissingCursorForCreate(repoRoot, cfg, project, info, bookmark, commit); err != nil {
			return false, err
		}
		return true, nil
	}
	if err := refreshExistingWorkspace(repoRoot, info, updateStale); err != nil {
		return false, err
	}
	if commit != "" {
		head, err := workspaceHeadAt(info.Path)
		if err != nil {
			return false, fmt.Errorf("read Workspace %q head: %w", handle, err)
		}
		if !head.on(commit) {
			return false, fmt.Errorf("Workspace %q is not on bookmark %q (its parent is %s, the bookmark is at %s); move it with `jj -R %s new %s`, or point the bookmark at its work with `jj bookmark set`", handle, bookmark, shortCommits(head.Parents), shortCommit(commit), info.Path, bookmark)
		}
	}
	materializeAssimilatedFoldersForOpen(mainWorkspaceRoot(repoRoot), info.Path, cfg, project)
	fmt.Fprintf(stderrWriter, "Opened existing Workspace %q\n", handle)
	printNavigationPath(info.Path, "create")
	return false, nil
}

// refreshExistingWorkspace snapshots the Workspace, as any jj command in it
// would, without jj's automatic stale recovery. A stale working copy is
// refused unless the caller asked for `jj workspace update-stale`; an update
// that kept on-disk edits in a divergent commit, or left the change
// divergent, is refused for review rather than Opened.
func refreshExistingWorkspace(repoRoot string, info workspaceInfo, updateStale bool) error {
	refs, err := listWorkspaceRefs(repoRoot)
	if err != nil {
		return err
	}
	if err := validateWorkspaceSnapshotTarget(repoRoot, info, refs); err != nil {
		return err
	}
	handle := info.Ref.Handle
	err = snapshotWorkspaceNoStaleUpdate(info.Path)
	if err == nil {
		return nil
	}
	if !isStaleWorkingCopyError(err) {
		return fmt.Errorf("snapshot Workspace %q: %w", handle, err)
	}
	if !updateStale {
		return fmt.Errorf("Workspace %q has a stale working copy; rerun with --update-stale, or run `jj -R %s workspace update-stale`", handle, info.Path)
	}
	if info.Main || info.Current {
		return fmt.Errorf("Workspace %q has a stale working copy; --update-stale does not update the Main or Current Workspace, run `jj -R %s workspace update-stale`", handle, info.Path)
	}
	info.Stale = true
	reports, err := updateStaleWorkspaces(repoRoot, []workspaceInfo{info})
	if err != nil {
		return err
	}
	report := reports[handle]
	if strings.TrimSpace(report.output) != "" {
		fmt.Fprintln(stderrWriter, strings.TrimRight(sanitizeTidyPreview(report.output), "\n"))
	}
	if report.err != nil {
		return fmt.Errorf("update stale Workspace %q: %w", handle, report.err)
	}
	if staleUpdateKeptEdits(report.output) {
		return fmt.Errorf("updated stale Workspace %q, but jj kept its on-disk edits in a divergent commit (jj log -r 'divergent()'); review them before Opening it", handle)
	}
	if err := snapshotWorkspaceNoStaleUpdate(info.Path); err != nil {
		return fmt.Errorf("Workspace %q after update-stale: %w", handle, err)
	}
	out, err := commandCaptureFn("jj", "-R", info.Path, "--ignore-working-copy", "--color=never", "--no-pager", "log", "--no-graph", "-r", "@ & divergent()", "-T", `commit_id ++ "\n"`)
	if err != nil {
		return fmt.Errorf("check Workspace %q for divergence: %w", handle, err)
	}
	if strings.TrimSpace(out) != "" {
		return fmt.Errorf("updated stale Workspace %q, but its working-copy change is divergent (jj log -r 'divergent()'); review it before Opening it", handle)
	}
	return nil
}

func snapshotWorkspaceNoStaleUpdate(path string) error {
	_, err := commandCaptureFn("jj", "-R", path, "--config=snapshot.auto-update-stale=false", "--color=never", "--no-pager", "status")
	return err
}

// forgetMissingCursorForCreate forgets a registration whose directory or
// `.jj` metadata is gone, but only when its head is the conventional empty,
// undescribed, unbookmarked cursor whose sole parent is the bookmark's
// commit: jj abandons that cursor on forget and nothing else is touched.
// Anything else stays registered for the user to inspect.
func forgetMissingCursorForCreate(repoRoot string, cfg config, project string, info workspaceInfo, bookmark, commit string) error {
	handle := info.Ref.Handle
	refuse := func(reason string) error {
		return fmt.Errorf("Workspace %q is registered but its directory %s is gone, and %s; inspect it with `jj log -r 'present(%s@)'`, then `jj workspace forget %s`", handle, emptyDefault(info.Path, "(unknown)"), reason, handle, handle)
	}
	if commit == "" {
		return refuse("without --bookmark there is no commit to check its head against")
	}
	head, err := registeredWorkspaceHead(repoRoot, handle)
	if err != nil {
		return fmt.Errorf("read Workspace %q head: %w", handle, err)
	}
	if !head.Discardable || len(head.Parents) != 1 || head.Parents[0] != commit {
		return refuse(fmt.Sprintf("its head is not an empty cursor on bookmark %q", bookmark))
	}
	target := filepath.Join(cfg.WorkspacesRoot, project, handle)
	if exists(target) {
		return fmt.Errorf("Workspace %q is registered but missing, and its path already exists without Workspace metadata: %s; move it aside, then retry", handle, target)
	}
	fmt.Fprintf(stderrWriter, "Forgetting Workspace %q, whose directory is gone; its head was an empty cursor on bookmark %q\n", handle, bookmark)
	return commandToStderrFn("jj", "-R", repoRoot, "workspace", "forget", jjWorkspaceName(handle))
}

type workspaceHead struct {
	Commit  string
	Parents []string
	// Discardable is an empty, undescribed, mutable, unbookmarked cursor.
	Discardable bool
}

// on reports whether commit is the head itself or one of its parents.
func (h workspaceHead) on(commit string) bool {
	if h.Commit == commit {
		return true
	}
	for _, parent := range h.Parents {
		if parent == commit {
			return true
		}
	}
	return false
}

// workspaceHeadTemplate and registeredWorkspaceHeadTemplate print the same
// fields, for a commit in `jj log` and for each `jj workspace list` target.
const (
	workspaceHeadTemplate           = `commit_id ++ "\t" ++ parents.map(|c| c.commit_id()).join(",") ++ "\t" ++ if(empty && description == "" && !immutable && !bookmarks, "1", "0") ++ "\n"`
	registeredWorkspaceHeadTemplate = `name ++ "\t" ++ target.commit_id() ++ "\t" ++ target.parents().map(|c| c.commit_id()).join(",") ++ "\t" ++ if(target.empty() && target.description() == "" && !target.immutable() && !target.bookmarks(), "1", "0") ++ "\n"`
)

// workspaceHeadAt reads the head of the Workspace rooted at path through its
// own @, without snapshotting again.
func workspaceHeadAt(path string) (workspaceHead, error) {
	out, err := commandCaptureFn("jj", "-R", path, "--ignore-working-copy", "--color=never", "--no-pager", "log", "--no-graph", "-r", "@", "-T", workspaceHeadTemplate)
	if err != nil {
		return workspaceHead{}, err
	}
	return parseWorkspaceHead(strings.TrimSpace(out))
}

// registeredWorkspaceHead reads a Workspace's recorded head from the
// workspace list, which needs no directory and no revset naming the Handle.
func registeredWorkspaceHead(repoRoot, handle string) (workspaceHead, error) {
	out, err := commandCaptureFn("jj", "-R", repoRoot, "--ignore-working-copy", "--color=never", "--no-pager", "workspace", "list", "-T", registeredWorkspaceHeadTemplate)
	if err != nil {
		return workspaceHead{}, err
	}
	for _, line := range strings.Split(out, "\n") {
		name, rest, ok := strings.Cut(line, "\t")
		if ok && name == jjWorkspaceName(handle) {
			return parseWorkspaceHead(rest)
		}
	}
	return workspaceHead{}, fmt.Errorf("Workspace %q is not registered", handle)
}

func parseWorkspaceHead(line string) (workspaceHead, error) {
	fields := strings.Split(line, "\t")
	if len(fields) != 3 || !revisionCommitIDRE.MatchString(fields[0]) {
		return workspaceHead{}, fmt.Errorf("unexpected Workspace head %q", sanitizeTidyPreview(line))
	}
	head := workspaceHead{Commit: fields[0], Discardable: fields[2] == "1"}
	for _, parent := range strings.Split(fields[1], ",") {
		if parent == "" {
			continue
		}
		if !revisionCommitIDRE.MatchString(parent) {
			return workspaceHead{}, fmt.Errorf("unexpected Workspace head parent %q", sanitizeTidyPreview(parent))
		}
		head.Parents = append(head.Parents, parent)
	}
	return head, nil
}

func shortCommit(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}

func shortCommits(commits []string) string {
	short := make([]string, 0, len(commits))
	for _, commit := range commits {
		short = append(short, shortCommit(commit))
	}
	return emptyDefault(strings.Join(short, ", "), "none")
}
