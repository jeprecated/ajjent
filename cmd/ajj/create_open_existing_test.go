package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const testAgentBookmark = "bot/agents-update"

// setupBookmarkCreateRepo is a real repo whose base commit carries the
// bookmark a long-lived agent Workspace follows.
func setupBookmarkCreateRepo(t *testing.T) (workspacesRoot, defaultPath, bookmarkCommit string) {
	t.Helper()
	workspacesRoot, defaultPath = setupRealCreateRepo(t)
	runJJ(t, "-R", defaultPath, "bookmark", "create", testAgentBookmark, "-r", "@-")
	return workspacesRoot, defaultPath, jjCommitID(t, defaultPath, testAgentBookmark)
}

func ensureAgentWorkspace(defaultPath string, extra ...string) (string, string, error) {
	args := append([]string{"agents-update", "--repo", defaultPath, "--bookmark", testAgentBookmark, "--open-existing"}, extra...)
	return captureOutput(func() error { return runCreate(args) })
}

func jjVisibleCommits(t *testing.T, repoPath string) string {
	t.Helper()
	cmd := exec.Command("jj", "-R", repoPath, "--ignore-working-copy", "log", "-r", "all()", "--no-graph", "-T", "commit_id ++ \"\\n\"")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("jj log all() failed: %v\n%s", err, out)
	}
	return string(out)
}

func TestCreateBookmarkBasesWorkspaceOnBookmarkCommit(t *testing.T) {
	workspacesRoot, defaultPath, bookmarkCommit := setupBookmarkCreateRepo(t)
	// Move default past the bookmark so jj's default base would differ.
	runJJ(t, "-R", defaultPath, "commit", "-m", "later")

	stdout, _, err := captureOutput(func() error {
		return runCreate([]string{"agent", "--repo", defaultPath, "--bookmark", testAgentBookmark})
	})
	if err != nil {
		t.Fatalf("create --bookmark: %v", err)
	}
	agentPath := filepath.Join(workspacesRoot, "proj", "agent")
	if strings.TrimSpace(stdout) != agentPath {
		t.Fatalf("stdout = %q, want %s", stdout, agentPath)
	}
	if parent := jjCommitID(t, agentPath, "@-"); parent != bookmarkCommit {
		t.Fatalf("parent = %s, want bookmark commit %s", parent, bookmarkCommit)
	}
}

func TestCreateBookmarkRejectsMissingConflictingAndCombinedInputs(t *testing.T) {
	workspacesRoot, defaultPath, bookmarkCommit := setupBookmarkCreateRepo(t)
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"missing bookmark", []string{"agent", "--bookmark", "bot/nope"}, `local bookmark "bot/nope" not found; create it first`},
		{"with revision", []string{"agent", "--bookmark", testAgentBookmark, "--revision", bookmarkCommit}, "cannot be combined with --revision"},
		{"with source", []string{"default", "agent", "--bookmark", testAgentBookmark}, "cannot be combined with --bookmark"},
		{"open-existing without handle", []string{"--open-existing"}, "requires exactly one Workspace Handle"},
		{"update-stale without open-existing", []string{"agent", "--update-stale"}, "requires --open-existing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, _, err := captureOutput(func() error { return runCreate(append(tc.args, "--repo", defaultPath)) })
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want empty", stdout)
			}
		})
	}
	if names := jjWorkspaceNames(t, defaultPath); names != "default" {
		t.Fatalf("workspaces = %s, want only default", names)
	}
	if exists(filepath.Join(workspacesRoot, "proj", "agent")) {
		t.Fatal("rejected create left a Workspace directory")
	}
}

func TestResolveLocalBookmarkCommitRefusesUnusableRecords(t *testing.T) {
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		return strings.Join([]string{
			"split\t\t1\t1\t0\t\t",
			"gone\t\t0\t0\t0\t\t",
			"gone\torigin\t1\t0\t1\t" + testCommitA + "\tremote",
			"remote-only\torigin\t1\t0\t0\t" + testCommitB + "\tremote",
			"ok\t\t1\t0\t0\t" + testCommitC + "\tlocal",
		}, "\n") + "\n", nil
	})
	for label, want := range map[string]string{
		"split":       `bookmark "split" is conflicted`,
		"gone":        `bookmark "gone" is deleted locally`,
		"remote-only": `local bookmark "remote-only" not found`,
	} {
		if _, err := resolveLocalBookmarkCommit("/repo", label); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: err = %v, want %q", label, err, want)
		}
	}
	if commit, err := resolveLocalBookmarkCommit("/repo", "ok"); err != nil || commit != testCommitC {
		t.Fatalf("ok: commit %q err %v", commit, err)
	}
}

func TestCreateOpenExistingIsIdempotent(t *testing.T) {
	workspacesRoot, defaultPath, bookmarkCommit := setupBookmarkCreateRepo(t)
	agentPath := filepath.Join(workspacesRoot, "proj", "agents-update")

	first, _, err := ensureAgentWorkspace(defaultPath)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if strings.TrimSpace(first) != agentPath {
		t.Fatalf("first stdout = %q, want %s", first, agentPath)
	}
	if parent := jjCommitID(t, agentPath, "@-"); parent != bookmarkCommit {
		t.Fatalf("parent = %s, want %s", parent, bookmarkCommit)
	}
	commits := jjVisibleCommits(t, defaultPath)

	second, stderr, err := ensureAgentWorkspace(defaultPath)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second != first {
		t.Fatalf("second stdout = %q, want %q", second, first)
	}
	if !strings.Contains(stderr, `Opened existing Workspace "agents-update"`) {
		t.Fatalf("stderr = %q", stderr)
	}
	if names := jjWorkspaceNames(t, defaultPath); names != "agents-update,default" {
		t.Fatalf("workspaces = %s", names)
	}
	if after := jjVisibleCommits(t, defaultPath); after != commits {
		t.Fatalf("second run changed visible commits:\nbefore %s\nafter %s", commits, after)
	}

	// An in-progress head on the bookmark is still on it.
	if err := os.WriteFile(filepath.Join(agentPath, "wip.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if third, _, err := ensureAgentWorkspace(defaultPath); err != nil || third != first {
		t.Fatalf("in-progress run: stdout %q err %v", third, err)
	}
	// So is the bookmark commit itself as @.
	runJJ(t, "-R", agentPath, "edit", testAgentBookmark)
	if fourth, _, err := ensureAgentWorkspace(defaultPath); err != nil || fourth != first {
		t.Fatalf("@ on bookmark run: stdout %q err %v", fourth, err)
	}
}

func TestCreateOpenExistingRefusesWorkspaceOffBookmark(t *testing.T) {
	workspacesRoot, defaultPath, _ := setupBookmarkCreateRepo(t)
	if _, _, err := ensureAgentWorkspace(defaultPath); err != nil {
		t.Fatal(err)
	}
	agentPath := filepath.Join(workspacesRoot, "proj", "agents-update")
	// The agent committed past the bookmark without moving it.
	if err := os.WriteFile(filepath.Join(agentPath, "work.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", agentPath, "commit", "-m", "work")
	head := jjCommitID(t, agentPath, "@")

	stdout, _, err := ensureAgentWorkspace(defaultPath)
	if err == nil || !strings.Contains(err.Error(), `Workspace "agents-update" is not on bookmark "bot/agents-update"`) {
		t.Fatalf("err = %v", err)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if now := jjCommitID(t, agentPath, "@"); now != head {
		t.Fatalf("Workspace was moved: @ %s, was %s", now, head)
	}
}

func TestCreateOpenExistingUpdatesStaleWorkspaceOnlyWhenAsked(t *testing.T) {
	workspacesRoot, defaultPath, _ := setupBookmarkCreateRepo(t)
	if _, _, err := ensureAgentWorkspace(defaultPath); err != nil {
		t.Fatal(err)
	}
	agentPath := filepath.Join(workspacesRoot, "proj", "agents-update")
	// Changing the bookmark commit's content from Main rebases the agent's
	// cursor onto a new tree and leaves its working copy stale.
	if err := os.WriteFile(filepath.Join(defaultPath, "fix.txt"), []byte("fix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", defaultPath, "squash", "--into", testAgentBookmark)
	if !workspaceIsStaleForTest(agentPath) {
		t.Fatal("fixture Workspace must be stale")
	}
	rewritten := jjCommitID(t, defaultPath, testAgentBookmark)

	stdout, _, err := ensureAgentWorkspace(defaultPath)
	if err == nil || !strings.Contains(err.Error(), "stale working copy; rerun with --update-stale") {
		t.Fatalf("err = %v", err)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}

	stdout, _, err = ensureAgentWorkspace(defaultPath, "--update-stale")
	if err != nil {
		t.Fatalf("--update-stale: %v", err)
	}
	if strings.TrimSpace(stdout) != agentPath {
		t.Fatalf("stdout = %q, want %s", stdout, agentPath)
	}
	if parent := jjCommitID(t, agentPath, "@-"); parent != rewritten {
		t.Fatalf("parent = %s, want rewritten bookmark %s", parent, rewritten)
	}
	if out, err := exec.Command("jj", "-R", agentPath, "--config=snapshot.auto-update-stale=false", "status").CombinedOutput(); err != nil {
		t.Fatalf("Workspace still stale: %v\n%s", err, out)
	}
}

func TestCreateOpenExistingRecreatesDeletedEmptyCursor(t *testing.T) {
	workspacesRoot, defaultPath, bookmarkCommit := setupBookmarkCreateRepo(t)
	first, _, err := ensureAgentWorkspace(defaultPath)
	if err != nil {
		t.Fatal(err)
	}
	agentPath := filepath.Join(workspacesRoot, "proj", "agents-update")
	oldCursor := jjCommitID(t, agentPath, "@")
	if err := os.RemoveAll(agentPath); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := ensureAgentWorkspace(defaultPath)
	if err != nil {
		t.Fatalf("recreate: %v", err)
	}
	if stdout != first {
		t.Fatalf("stdout = %q, want %q", stdout, first)
	}
	if names := jjWorkspaceNames(t, defaultPath); names != "agents-update,default" {
		t.Fatalf("workspaces = %s", names)
	}
	if parent := jjCommitID(t, agentPath, "@-"); parent != bookmarkCommit {
		t.Fatalf("parent = %s, want %s", parent, bookmarkCommit)
	}
	if strings.Contains(jjVisibleCommits(t, defaultPath), oldCursor) {
		t.Fatalf("old empty cursor %s is still visible", oldCursor)
	}
}

func TestCreateOpenExistingRefusesDeletedWorkspaceWithWork(t *testing.T) {
	workspacesRoot, defaultPath, _ := setupBookmarkCreateRepo(t)
	if _, _, err := ensureAgentWorkspace(defaultPath); err != nil {
		t.Fatal(err)
	}
	agentPath := filepath.Join(workspacesRoot, "proj", "agents-update")
	if err := os.WriteFile(filepath.Join(agentPath, "wip.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", agentPath, "status")
	head := jjCommitID(t, agentPath, "@")
	if err := os.RemoveAll(agentPath); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := ensureAgentWorkspace(defaultPath)
	if err == nil || !strings.Contains(err.Error(), "its head is not an empty cursor on bookmark") {
		t.Fatalf("err = %v", err)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if names := jjWorkspaceNames(t, defaultPath); names != "agents-update,default" {
		t.Fatalf("registration was forgotten: workspaces = %s", names)
	}
	if !strings.Contains(jjVisibleCommits(t, defaultPath), head) {
		t.Fatalf("head %s with work is no longer visible", head)
	}
	if exists(agentPath) {
		t.Fatal("refused run recreated the Workspace directory")
	}
}
