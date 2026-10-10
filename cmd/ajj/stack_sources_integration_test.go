package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type realBookmarkSourceRepo struct {
	defaultPath string
	alphaPath   string
	featA       string // commit of local bookmark feat/a
	featB       string // commit of local bookmark feat/b (two commits above base)
	remoteOnly  string // commit of untracked remote-only@origin
	base        string // commit of main, an ancestor of default@
}

// setupRealBookmarkSourceRepo builds the "work synced in via branches" shape:
// default@ is an empty cursor on main; feat/a and feat/b are bookmark-only lines
// off main; remote-only@origin was fetched from a bare Git remote and never
// tracked; Workspace alpha exists with no unique work.
func setupRealBookmarkSourceRepo(t *testing.T) realBookmarkSourceRepo {
	t.Helper()
	for _, bin := range []string{"jj", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s binary not available for integration test", bin)
		}
	}
	tmp := t.TempDir()
	workspacesRoot := filepath.Join(tmp, "workspaces")
	defaultPath := filepath.Join(workspacesRoot, "proj", "default")
	alphaPath := filepath.Join(workspacesRoot, "proj", "alpha")
	originPath := filepath.Join(tmp, "origin.git")
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
	commitFile := func(parent, file, message string) string {
		t.Helper()
		runJJ(t, "-R", defaultPath, "new", parent, "-m", message)
		if err := os.WriteFile(filepath.Join(defaultPath, file), []byte(message+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runJJ(t, "-R", defaultPath, "file", "track", "root:"+file)
		return jjCommitID(t, defaultPath, "@")
	}
	if err := os.WriteFile(filepath.Join(defaultPath, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", defaultPath, "commit", "-m", "base")
	base := jjCommitID(t, defaultPath, "@-")
	runJJ(t, "-R", defaultPath, "bookmark", "create", "main", "-r", base)
	runJJ(t, "-R", defaultPath, "config", "set", "--repo", `revset-aliases."trunk()"`, "main")
	featA := commitFile(base, "a.txt", "feat: a")
	runJJ(t, "-R", defaultPath, "bookmark", "create", "feat/a", "-r", featA)
	commitFile(base, "b1.txt", "feat: b1")
	featB := commitFile("@", "b2.txt", "feat: b2")
	runJJ(t, "-R", defaultPath, "bookmark", "create", "feat/b", "-r", featB)
	remoteOnly := commitFile(base, "remote.txt", "feat: remote")
	runJJ(t, "-R", defaultPath, "new", base)
	if out, err := exec.Command("git", "init", "--bare", originPath).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", defaultPath, "push", originPath, remoteOnly+":refs/heads/remote-only").CombinedOutput(); err != nil {
		t.Fatalf("git push remote-only: %v\n%s", err, out)
	}
	runJJ(t, "-R", defaultPath, "git", "remote", "add", "origin", originPath)
	runJJ(t, "-R", defaultPath, "git", "fetch", "--remote", "origin")
	runJJ(t, "-R", defaultPath, "workspace", "add", "--revision", base, "--name", "alpha", alphaPath)
	return realBookmarkSourceRepo{defaultPath: defaultPath, alphaPath: alphaPath, featA: featA, featB: featB, remoteOnly: remoteOnly, base: base}
}

func bookmarkTargets(t *testing.T, repoPath string) map[string]string {
	t.Helper()
	refs, err := listBookmarkRefs(repoPath)
	if err != nil {
		t.Fatal(err)
	}
	targets := map[string]string{}
	for _, ref := range refs {
		targets[ref.label()] = ref.Commit
	}
	return targets
}

func TestDiscoverBookmarkSourcesRealRepoClassifiesLines(t *testing.T) {
	repo := setupRealBookmarkSourceRepo(t)
	sources, _, err := discoverBookmarkSources(repo.defaultPath, "default")
	if err != nil {
		t.Fatal(err)
	}
	items := mapSelectorItemsByHandle(selectorItemsForBookmarkSources(sources))
	for _, label := range []string{"main", "main@git", "feat/a@git"} {
		if _, ok := items[label]; ok {
			t.Fatalf("expected %s to be hidden, got rows %+v", label, items)
		}
	}
	if item := items["feat/a"]; item.Disabled || item.ExplicitOnly || item.Status != "unstacked" || item.Description != "feat: a" {
		t.Fatalf("unexpected feat/a row %+v", item)
	}
	if item := items["remote-only@origin"]; item.Disabled || !item.ExplicitOnly || item.Markers != "remote,untracked" {
		t.Fatalf("unexpected remote-only row %+v", item)
	}
	// Once the target is behind trunk, a line already in trunk is explicit-only.
	runJJ(t, "-R", repo.defaultPath, "bookmark", "set", "main", "-r", repo.featA)
	sources, _, err = discoverBookmarkSources(repo.defaultPath, "default")
	if err != nil {
		t.Fatal(err)
	}
	items = mapSelectorItemsByHandle(selectorItemsForBookmarkSources(sources))
	if item := items["feat/a"]; item.Status != "in-trunk" || !item.ExplicitOnly {
		t.Fatalf("expected feat/a in-trunk and explicit-only, got %+v", item)
	}
}

func TestRunStackRealRepoStacksBookmarksWithoutMovingThem(t *testing.T) {
	repo := setupRealBookmarkSourceRepo(t)
	before := bookmarkTargets(t, repo.defaultPath)
	_, errOut, err := captureOutput(func() error {
		return runStack([]string{"feat/b", "remote-only@origin", "--repo", repo.defaultPath, "--yes"})
	})
	if err != nil {
		t.Fatalf("expected bookmark Stack to succeed, got %v\nstderr:%s", err, errOut)
	}
	for _, commit := range []string{repo.featB, repo.remoteOnly} {
		if got := jjRevsetCount(t, repo.defaultPath, commit+" & ::default@"); got != 1 {
			t.Fatalf("expected default@ to include %s\nstderr:%s", commit, errOut)
		}
	}
	if got := jjRevsetCount(t, repo.defaultPath, repo.featA+" & ::default@"); got != 0 {
		t.Fatalf("unselected feat/a must stay out of the target\nstderr:%s", errOut)
	}
	after := bookmarkTargets(t, repo.defaultPath)
	if len(after) != len(before) {
		t.Fatalf("Stack must not create or delete bookmarks: before=%v after=%v", before, after)
	}
	for label, commit := range before {
		if after[label] != commit {
			t.Fatalf("Stack moved bookmark %s from %s to %s", label, commit, after[label])
		}
	}
	if got := jjRev(t, repo.alphaPath, "alpha@-"); got != jjRev(t, repo.defaultPath, repo.base) {
		t.Fatalf("unselected Workspace alpha must not advance, alpha@- is %s", got)
	}
	if err := runUndo([]string{"--repo", repo.defaultPath}); err != nil {
		t.Fatalf("expected bookmark Stack to be undoable: %v", err)
	}
	if got := jjRevsetCount(t, repo.defaultPath, repo.featB+" & ::default@"); got != 0 {
		t.Fatal("undo must restore the pre-Stack target")
	}
}

func TestRunStackRealRepoSingleBookmarkLandsLinearly(t *testing.T) {
	repo := setupRealBookmarkSourceRepo(t)
	_, errOut, err := captureOutput(func() error {
		return runStack([]string{"feat/a", "--repo", repo.defaultPath, "--yes"})
	})
	if err != nil {
		t.Fatalf("expected single bookmark Stack to succeed, got %v\nstderr:%s", err, errOut)
	}
	if strings.Contains(errOut, "Tidy Stack probe") {
		t.Fatalf("bookmark inputs must skip the payload-rewriting probe\nstderr:%s", errOut)
	}
	if got := jjCommitID(t, repo.defaultPath, "default@-"); got != repo.featA {
		t.Fatalf("expected default@ directly above feat/a %s, got %s\nstderr:%s", repo.featA, got, errOut)
	}
	if got := bookmarkTargets(t, repo.defaultPath)["feat/a"]; got != repo.featA {
		t.Fatalf("feat/a moved to %s", got)
	}
}

func TestRunStackRealRepoAllIncludesLocalBookmarksOnly(t *testing.T) {
	repo := setupRealBookmarkSourceRepo(t)
	_, errOut, err := captureOutput(func() error {
		return runStack([]string{"--all", "--repo", repo.defaultPath, "--yes"})
	})
	if err != nil {
		t.Fatalf("expected --all Stack to succeed, got %v\nstderr:%s", err, errOut)
	}
	for _, commit := range []string{repo.featA, repo.featB} {
		if got := jjRevsetCount(t, repo.defaultPath, commit+" & ::default@"); got != 1 {
			t.Fatalf("expected --all to include local bookmark commit %s\nstderr:%s", commit, errOut)
		}
	}
	if got := jjRevsetCount(t, repo.defaultPath, repo.remoteOnly+" & ::default@"); got != 0 {
		t.Fatalf("--all must not include remote-only bookmarks\nstderr:%s", errOut)
	}
}

func TestRunStackRealRepoExplainsUnstackableBookmarks(t *testing.T) {
	repo := setupRealBookmarkSourceRepo(t)
	for arg, want := range map[string]string{
		"main":         "already represented",
		"feat/a@git":   "colocated Git mirror",
		"missing/line": "no Workspace or bookmark",
	} {
		err := runStack([]string{arg, "--repo", repo.defaultPath, "--yes"})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("stack %s: expected error containing %q, got %v", arg, want, err)
		}
	}
	err := runStack([]string{"--line", "feat/a", "--repo", repo.defaultPath, "--yes"})
	if err == nil || !strings.Contains(err.Error(), "Line Stacking accepts Workspace Handles only") {
		t.Fatalf("expected Line Stack to reject bookmark inputs, got %v", err)
	}
}

func TestForcedCloseRealRepoKeepsBookmarkedWork(t *testing.T) {
	repo := setupRealBookmarkSourceRepo(t)
	if err := os.WriteFile(filepath.Join(repo.alphaPath, "alpha.txt"), []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", repo.alphaPath, "file", "track", "root:alpha.txt")
	runJJ(t, "-R", repo.alphaPath, "commit", "-m", "feat: alpha named")
	named := jjCommitID(t, repo.alphaPath, "@-")
	runJJ(t, "-R", repo.alphaPath, "bookmark", "create", "alpha-line", "-r", named)
	if err := os.WriteFile(filepath.Join(repo.alphaPath, "scratch.txt"), []byte("scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", repo.alphaPath, "file", "track", "root:scratch.txt")
	runJJ(t, "-R", repo.alphaPath, "commit", "-m", "wip: unnamed")
	unnamed := jjCommitID(t, repo.alphaPath, "@-")

	_, errOut, err := captureOutput(func() error {
		return runClose([]string{"alpha", "--repo", repo.defaultPath, "--force", "--yes"})
	})
	if err != nil {
		t.Fatalf("expected Forced Closing to succeed, got %v\nstderr:%s", err, errOut)
	}
	if got := bookmarkTargets(t, repo.defaultPath)["alpha-line"]; got != named {
		t.Fatalf("Forced Closing must keep bookmarked work, alpha-line=%q\nstderr:%s", got, errOut)
	}
	if got := jjRevsetCount(t, repo.defaultPath, unnamed+" & ::visible_heads()"); got != 0 {
		t.Fatalf("Forced Closing should still abandon unnamed unique work\nstderr:%s", errOut)
	}
	if !strings.Contains(errOut, "still named by bookmarks") {
		t.Fatalf("expected Forced Closing to report kept bookmark work\nstderr:%s", errOut)
	}
}

func TestEmptyCursorCleanupRealRepoKeepsBookmarkedEmptyCommit(t *testing.T) {
	repo := setupRealBookmarkSourceRepo(t)
	runJJ(t, "-R", repo.defaultPath, "bookmark", "create", "marker", "-r", "@")
	runJJ(t, "-R", repo.defaultPath, "new")
	marker := jjCommitID(t, repo.defaultPath, "@-")
	if err := abandonTopEmptyMutableAncestors(repo.defaultPath); err != nil {
		t.Fatal(err)
	}
	if got := bookmarkTargets(t, repo.defaultPath)["marker"]; got != marker {
		t.Fatalf("empty-ancestor cleanup must keep the bookmarked empty commit, marker=%q", got)
	}
}
