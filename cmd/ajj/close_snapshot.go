package main

import "fmt"

// Snapshot only lifecycle candidates, not every Workspace inspected by list or
// Stack. Graph queries intentionally ignore working copies; without this step,
// a represented cursor can conceal unrecorded files that removal would destroy.
func snapshotCloseCandidates(repoPath string, targets []workspaceInfo) error {
	protection, err := newCloseProtectionContext(repoPath, targets)
	if err != nil {
		return err
	}
	// Validate every candidate before running any snapshot. Containment is a
	// deletion restriction, not a snapshot restriction: an unselected parent
	// may contain the safe child that the user actually wants to close.
	for _, target := range targets {
		if !target.Missing {
			if err := validateWorkspaceSnapshotTarget(repoPath, target, protection.workspaceRoots); err != nil {
				return err
			}
		}
	}
	for _, target := range targets {
		if target.Missing {
			continue
		}
		if _, err := commandCaptureFn("jj", "-R", target.Path, "--config=snapshot.auto-update-stale=false", "--color=never", "--no-pager", "status"); err != nil {
			return fmt.Errorf("snapshot Workspace %q before Closing: %w; inspect the Workspace and retry (no automatic stale recovery)", target.Ref.Handle, err)
		}
	}
	return nil
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
