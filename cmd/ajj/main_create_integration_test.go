package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// setupRealCreateRepo builds a colocated Main (default) Workspace with an ajj
// config, ready for `ajj create` exercises against a real jj binary.
func setupRealCreateRepo(t *testing.T) (workspacesRoot, defaultPath string) {
	t.Helper()
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj binary not available for integration test")
	}
	workspacesRoot = filepath.Join(t.TempDir(), "workspaces")
	defaultPath = filepath.Join(workspacesRoot, "proj", "default")
	if err := os.MkdirAll(filepath.Dir(defaultPath), 0o755); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "git", "init", "--colocate", defaultPath)
	writeConfig(t, defaultPath, strings.Join([]string{
		"workspaces_root: " + workspacesRoot,
		"project: proj",
		"main_workspace: default",
		"",
	}, "\n"))
	if err := os.WriteFile(filepath.Join(defaultPath, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", defaultPath, "commit", "-m", "base")
	return workspacesRoot, defaultPath
}

// jjCommitID returns the full (40-hex) commit id of revset in repoPath without
// snapshotting the working copy — used for read-back assertions that must not
// mutate the Workspace under test.
func jjCommitID(t *testing.T, repoPath, revset string) string {
	t.Helper()
	cmd := exec.Command("jj", "-R", repoPath, "--ignore-working-copy", "log", "-r", revset, "--no-graph", "-T", "commit_id ++ \"\\n\"")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("jj commit_id -r %s failed: %v\n%s", revset, err, out)
	}
	return strings.TrimSpace(string(out))
}

// jjCaptureCommitID snapshots the working copy (no --ignore-working-copy) and
// returns the resulting commit id. This mirrors a caller capturing the source
// Workspace's exact current commit, folding any dirty content into it.
func jjCaptureCommitID(t *testing.T, repoPath, revset string) string {
	t.Helper()
	cmd := exec.Command("jj", "-R", repoPath, "log", "-r", revset, "--no-graph", "-T", "commit_id ++ \"\\n\"")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("jj capture commit_id -r %s failed: %v\n%s", revset, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestCreateWithRevisionInheritsCapturedDirtyCommit is the core behavior: when a
// caller first captures the source's exact current commit id (which snapshots
// its dirty working-copy content), `ajj create --revision <id>` bases the new
// Workspace on exactly that commit, so the dirty content is inherited, the new
// working-copy parent is exactly the requested commit, and the source Workspace
// is not retargeted.
func TestCreateWithRevisionInheritsCapturedDirtyCommit(t *testing.T) {
	workspacesRoot, defaultPath := setupRealCreateRepo(t)

	// Dirty, uncommitted content in the source (Main) Workspace.
	if err := os.WriteFile(filepath.Join(defaultPath, "dirty.txt"), []byte("dirty inherited content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The caller captures the source's exact current commit id, snapshotting the
	// dirty content into default@.
	captured := jjCaptureCommitID(t, defaultPath, "@")
	if captured == "" {
		t.Fatal("expected a captured commit id")
	}

	if _, _, err := captureOutput(func() error {
		return runCreate([]string{"feature", "--repo", defaultPath, "--revision", captured})
	}); err != nil {
		t.Fatalf("expected create --revision to succeed, got %v", err)
	}

	featurePath := filepath.Join(workspacesRoot, "proj", "feature")

	// The new Workspace working-copy change has exactly the captured commit as parent.
	if parent := jjCommitID(t, featurePath, "@-"); parent != captured {
		t.Fatalf("expected new Workspace parent %s, got %s", captured, parent)
	}
	// The dirty content is inherited into the new Workspace.
	got, err := os.ReadFile(filepath.Join(featurePath, "dirty.txt"))
	if err != nil || string(got) != "dirty inherited content\n" {
		t.Fatalf("expected inherited dirty content, got %q err=%v", string(got), err)
	}
	// The source Workspace is not retargeted: default@ still points at the captured commit.
	if now := jjCommitID(t, defaultPath, "@"); now != captured {
		t.Fatalf("source Workspace was retargeted: default@ %s != captured %s", now, captured)
	}
}

// TestCreateWithUnknownRevisionFailsBeforeCreation proves a well-formed but
// nonexistent commit id is rejected before any creation side effect: no new
// Workspace directory, and jj still lists only the Main Workspace.
func TestCreateWithUnknownRevisionFailsBeforeCreation(t *testing.T) {
	workspacesRoot, defaultPath := setupRealCreateRepo(t)
	unknown := strings.Repeat("a", 40) // valid shape, absent from the repo

	err := runCreate([]string{"feature", "--repo", defaultPath, "--revision", unknown})
	// jj rejects an absent full commit id; ajj surfaces that as a resolution
	// failure. Either way the revision must not resolve and must fail before creation.
	if err == nil || !strings.Contains(err.Error(), unknown) {
		t.Fatalf("expected unknown-revision failure, got %v", err)
	}
	if exists(filepath.Join(workspacesRoot, "proj", "feature")) {
		t.Fatal("no Workspace directory should exist for an unknown revision")
	}
	if names := jjWorkspaceNames(t, defaultPath); names != "default" {
		t.Fatalf("expected only the Main Workspace registered, got %q", names)
	}
}

// TestCreateWithMalformedRevisionFailsBeforeCreation proves a malformed revision
// is rejected before creation, end-to-end against real jj.
func TestCreateWithMalformedRevisionFailsBeforeCreation(t *testing.T) {
	workspacesRoot, defaultPath := setupRealCreateRepo(t)

	err := runCreate([]string{"feature", "--repo", defaultPath, "--revision", "@-"})
	if err == nil || !strings.Contains(err.Error(), "full 40-character") {
		t.Fatalf("expected malformed-revision failure, got %v", err)
	}
	if exists(filepath.Join(workspacesRoot, "proj", "feature")) {
		t.Fatal("no Workspace directory should exist for a malformed revision")
	}
	if names := jjWorkspaceNames(t, defaultPath); names != "default" {
		t.Fatalf("expected only the Main Workspace registered, got %q", names)
	}
}

// TestCreateWithoutRevisionUnchanged proves the default create path is unchanged
// end-to-end: create with no --revision succeeds and registers the Workspace.
func TestCreateWithoutRevisionUnchanged(t *testing.T) {
	workspacesRoot, defaultPath := setupRealCreateRepo(t)

	if _, _, err := captureOutput(func() error {
		return runCreate([]string{"feature", "--repo", defaultPath})
	}); err != nil {
		t.Fatalf("expected default create to succeed, got %v", err)
	}
	if !exists(filepath.Join(workspacesRoot, "proj", "feature")) {
		t.Fatal("expected default create to produce a Workspace directory")
	}
	if names := jjWorkspaceNames(t, defaultPath); !strings.Contains(names, "feature") {
		t.Fatalf("expected the new Workspace to be registered, got %q", names)
	}
}

func TestCreateFromWorkspaceInheritsCurrentContent(t *testing.T) {
	for _, source := range []string{"default", "external"} {
		t.Run(source, func(t *testing.T) {
			workspacesRoot, defaultPath := setupRealCreateRepo(t)
			sourcePath := defaultPath
			if source == "external" {
				// Resolve registered roots, not a guessed path under workspaces_root.
				sourcePath = filepath.Join(t.TempDir(), "source")
				runJJ(t, "-R", defaultPath, "workspace", "add", "--name", source, sourcePath)
			}
			mainBefore := jjCommitID(t, defaultPath, "@")
			sourceChange := lifecycleJJOutput(t, sourcePath, "log", "-r", "@", "--no-graph", "-T", "change_id")
			if err := os.WriteFile(filepath.Join(sourcePath, "dirty.txt"), []byte("source edits\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			// Do not snapshot source before create: create must capture these edits itself.
			out, _, err := captureOutput(func() error {
				return runCreate([]string{source, "--repo", defaultPath, "child", "--envrc"})
			})
			if err != nil {
				t.Fatal(err)
			}
			childPath := filepath.Join(workspacesRoot, "proj", "child")
			if strings.TrimSpace(out) != childPath {
				t.Fatalf("expected only child navigation path, got %q", out)
			}
			if got, err := os.ReadFile(filepath.Join(childPath, "dirty.txt")); err != nil || string(got) != "source edits\n" {
				t.Fatalf("expected inherited source edits, got %q err=%v", got, err)
			}
			if parent, head := jjCommitID(t, childPath, "@-"), jjCommitID(t, sourcePath, "@"); parent != head {
				t.Fatalf("child parent %s != source head %s", parent, head)
			}
			if now := lifecycleJJOutput(t, sourcePath, "log", "-r", "@", "--no-graph", "-T", "change_id"); now != sourceChange {
				t.Fatal("source Workspace was retargeted")
			}
			if source != "default" && jjCommitID(t, defaultPath, "@") != mainBefore {
				t.Fatal("Current/Main Workspace was changed")
			}
			if !exists(filepath.Join(childPath, ".envrc")) {
				t.Fatal("expected create setup flags to apply to child")
			}
		})
	}
}

func TestCreateFromWorkspaceRejectsInvalidRequests(t *testing.T) {
	workspacesRoot, defaultPath := setupRealCreateRepo(t)
	missingPath := filepath.Join(workspacesRoot, "proj", "missing")
	runJJ(t, "-R", defaultPath, "workspace", "add", "--name", "missing", missingPath)
	if err := os.RemoveAll(missingPath); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"unknown", "child"}, "not found"},
		{[]string{"missing", "child"}, "path not found"},
		{[]string{"../default", "child"}, "invalid"},
		{[]string{"default", "../child"}, "invalid"},
		{[]string{"default", "child", "extra"}, "at most two"},
		{[]string{"default", "child", "--revision", jjCommitID(t, defaultPath, "@")}, "cannot be combined"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			err := runCreate(append(tc.args, "--repo", defaultPath))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
			if exists(filepath.Join(workspacesRoot, "proj", "child")) {
				t.Fatal("invalid request created a directory")
			}
			if names := jjWorkspaceNames(t, defaultPath); names != "default,missing" {
				t.Fatalf("invalid request changed registrations: %s", names)
			}
		})
	}
}

// jjWorkspaceNames returns the sorted, comma-joined workspace names registered
// in repoPath.
func jjWorkspaceNames(t *testing.T, repoPath string) string {
	t.Helper()
	cmd := exec.Command("jj", "-R", repoPath, "--ignore-working-copy", "workspace", "list", "-T", "name ++ \"\\n\"")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("jj workspace list failed: %v\n%s", err, out)
	}
	return strings.Join(uniqueNonEmptyStrings(strings.Split(string(out), "\n")), ",")
}
