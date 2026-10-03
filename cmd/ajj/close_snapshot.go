package main

import (
	"fmt"
	"strings"
)

// Snapshot only lifecycle candidates, not every Workspace inspected by list or
// Stack. Graph queries intentionally ignore working copies; without this step,
// a represented cursor can conceal unrecorded files that removal would destroy.
func snapshotCloseCandidates(repoPath string, targets []workspaceInfo) error {
	_, err := snapshotLifecycleCandidates(repoPath, targets, false)
	return err
}

// snapshotTidyCandidates scopes the stale fail-closed contract per Workspace:
// a candidate whose snapshot fails because its working copy is stale is
// reported (and must then be excluded from every Tidy selection) instead of
// aborting the whole Tidy. Nothing is recovered automatically: only the
// user's explicit `u` key or --update-stale runs update-stale, followed by a
// complete new review. Any other snapshot failure still aborts.
func snapshotTidyCandidates(repoPath string, targets []workspaceInfo) (map[string]bool, error) {
	return snapshotLifecycleCandidates(repoPath, targets, true)
}

// staleUpdateReport is what jj said about one `workspace update-stale` run.
type staleUpdateReport struct {
	// output is jj's complete report (stdout and stderr) of a successful run.
	output string
	err    error
}

// updateStaleWorkspaces is the user-requested recovery the stale hint names,
// `jj -R <path> workspace update-stale`, for each given stale Workspace. It is
// reached only from Tidy's `u` key or --update-stale, never automatically, and
// the caller must rebuild the whole review afterwards. Each path is validated
// against its registration and the shared repository first, as for a
// snapshot. Both callers run it outside a live selector. jj's whole report
// is kept, success included, because it is the only place jj says that it
// first snapshotted on-disk edits of the stale copy into a divergent commit;
// the caller must show that and keep such a Workspace out of automatic
// selection. A failure is recorded for that Workspace only; the others still
// update.
func updateStaleWorkspaces(repoPath string, targets []workspaceInfo) (map[string]staleUpdateReport, error) {
	reports := map[string]staleUpdateReport{}
	if len(targets) == 0 {
		return reports, nil
	}
	refs, err := listWorkspaceRefs(repoPath)
	if err != nil {
		return nil, err
	}
	for _, target := range targets {
		if !target.Stale || target.Main || target.Current || target.Missing {
			reports[target.Ref.Handle] = staleUpdateReport{err: fmt.Errorf("not a stale Tidy candidate")}
			continue
		}
		if err := validateWorkspaceSnapshotTarget(repoPath, target, refs); err != nil {
			reports[target.Ref.Handle] = staleUpdateReport{err: err}
			continue
		}
		output, err := commandCombinedCaptureFn("jj", "-R", target.Path, "--color=never", "--no-pager", "workspace", "update-stale")
		reports[target.Ref.Handle] = staleUpdateReport{output: output, err: err}
	}
	return reports, nil
}

func isStaleWorkingCopyError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "working copy is stale")
}

func staleWorkspaceInfos(infos []workspaceInfo) []workspaceInfo {
	stale := []workspaceInfo{}
	for _, info := range infos {
		if info.Stale {
			stale = append(stale, info)
		}
	}
	return stale
}

func staleWorkspaceHint(info workspaceInfo) string {
	return "stale — run: jj -R " + info.Path + " workspace update-stale"
}

// staleWorkspaceSkipHint explains a stale Workspace skipped by non-interactive
// Tidy. --update-stale only updates Disposable Workspaces, so it is suggested
// for those alone, and not again once it already ran.
func staleWorkspaceSkipHint(info workspaceInfo, updateStaleRan bool) string {
	switch {
	case info.Policy != policyDisposable:
		return staleWorkspaceHint(info)
	case updateStaleRan:
		return "still stale after --update-stale; inspect it, then run: jj -R " + info.Path + " workspace update-stale"
	default:
		return staleWorkspaceHint(info) + ", or rerun ajj tidy with --update-stale"
	}
}

// tidyStaleRowDetail is the Tidy detail line for a highlighted stale row.
func tidyStaleRowDetail(item selectorItem) string {
	detail := item.Handle + ": stale working copy — press u to update it (jj workspace update-stale), then it can be tidied"
	if item.Path != "" {
		detail += " · " + item.Path
	}
	return detail
}

func snapshotLifecycleCandidates(repoPath string, targets []workspaceInfo, tolerateStale bool) (map[string]bool, error) {
	stale := map[string]bool{}
	protection, err := newCloseProtectionContext(repoPath, targets)
	if err != nil {
		return nil, err
	}
	// Validate every candidate before running any snapshot. Containment is a
	// deletion restriction, not a snapshot restriction: an unselected parent
	// may contain the safe child that the user actually wants to close.
	for _, target := range targets {
		if !target.Missing {
			if err := validateWorkspaceSnapshotTarget(repoPath, target, protection.workspaceRoots); err != nil {
				return nil, err
			}
		}
	}
	for _, target := range targets {
		if target.Missing {
			continue
		}
		if _, err := commandCaptureFn("jj", "-R", target.Path, "--config=snapshot.auto-update-stale=false", "--color=never", "--no-pager", "status"); err != nil {
			if tolerateStale && isStaleWorkingCopyError(err) {
				stale[target.Ref.Handle] = true
				continue
			}
			return nil, fmt.Errorf("snapshot Workspace %q before Closing: %w; inspect the Workspace and retry (no automatic stale recovery)", target.Ref.Handle, err)
		}
	}
	return stale, nil
}

func validateWorkspaceSnapshotTarget(repoPath string, target workspaceInfo, refs []workspaceRef) error {
	candidate, err := canonicalExistingDirectory(target.Path)
	if err != nil {
		return fmt.Errorf("resolve Workspace %q snapshot path: %w", target.Ref.Handle, err)
	}
	matched := false
	for _, ref := range refs {
		if ref.Handle != target.Ref.Handle {
			continue
		}
		root := cleanWorkspaceRoot(ref.Root)
		if root == "" {
			return fmt.Errorf("cannot verify registered path for Workspace %q", target.Ref.Handle)
		}
		registered, err := canonicalExistingDirectory(root)
		if err != nil || registered != candidate {
			return fmt.Errorf("Workspace %q snapshot path does not match its registered path", target.Ref.Handle)
		}
		matched = true
	}
	if !matched {
		return fmt.Errorf("cannot verify registered path for Workspace %q", target.Ref.Handle)
	}
	return validateWorkspaceRepositoryIdentity(repoPath, target)
}

// The reviewed operation binds both the selected heads and all their graph
// protectors. Never extend a force confirmation to edits made during a prompt.
// This is a preflight guard, not a lock against concurrent filesystem writers.
func revalidateCloseReview(repoPath string, targets []workspaceInfo, reviewedOperation string) (closeProtectionContext, error) {
	unchanged := func() error {
		current, err := currentOperationID(repoPath)
		if err != nil {
			return err
		}
		if current != reviewedOperation {
			return fmt.Errorf("Workspace graph changed after review; rerun Close/Tidy and review the new state")
		}
		return nil
	}
	if err := unchanged(); err != nil {
		return closeProtectionContext{}, err
	}
	if err := snapshotCloseCandidates(repoPath, targets); err != nil {
		return closeProtectionContext{}, err
	}
	if err := unchanged(); err != nil {
		return closeProtectionContext{}, err
	}
	return newCloseProtectionContext(repoPath, targets)
}

// prepareCloseReview is for already-selected closing targets. It does not run
// during Stack computation or generic listing. Validate deletion restrictions
// before snapshotting, then bind every later confirmation to the resulting view.
func prepareCloseReview(repoPath string, targets []workspaceInfo) (closeProtectionContext, error) {
	protection, err := newCloseProtectionContext(repoPath, targets)
	if err != nil {
		return closeProtectionContext{}, err
	}
	if err := validateWorkspaceRemovalTargets(repoPath, targets, protection); err != nil {
		return closeProtectionContext{}, err
	}
	if err := snapshotCloseCandidates(repoPath, targets); err != nil {
		return closeProtectionContext{}, err
	}
	protection, err = newCloseProtectionContext(repoPath, targets)
	if err != nil {
		return closeProtectionContext{}, err
	}
	protection.reviewedOperation, err = currentOperationID(repoPath)
	return protection, err
}
