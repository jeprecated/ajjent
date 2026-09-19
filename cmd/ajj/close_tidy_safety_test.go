package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCloseRepairsReadOnlyCacheWithoutChangingOutsideLinks(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "workspace")
	repoPath := t.TempDir()
	createJJWorkspaceLink(t, repoPath, workspace)
	cache := filepath.Join(workspace, ".devenv", "go", "pkg", "mod", "cache")
	if err := os.MkdirAll(cache, 0755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	externalFile := filepath.Join(outside, "shared")
	if err := os.WriteFile(externalFile, []byte("preserve"), 0444); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(externalFile, filepath.Join(cache, "hardlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(cache, "outside")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cache, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cache, 0755) })
	before, _ := os.Stat(outside)
	forgot := false
	withCommandCapture(t, func(string, ...string) (string, error) { return "", nil })
	withCommandToStderr(t, func(_ string, args ...string) error {
		forgot = strings.Contains(strings.Join(args, " "), "workspace forget cache")
		return nil
	})
	withCloseReviewEvidence(t, repoPath, []workspaceInfo{{Ref: workspaceRef{Handle: "cache"}, Path: workspace}})
	_, err := closeWorkspacesWithProtection(repoPath, []workspaceInfo{{Ref: workspaceRef{Handle: "cache"}, Path: workspace}}, true, true, false, closeProtectionContext{})
	if err != nil {
		t.Fatal(err)
	}
	if !forgot || exists(workspace) {
		t.Fatal("workspace was not closed")
	}
	after, _ := os.Stat(outside)
	file, err := os.Stat(externalFile)
	if err != nil || before.Mode() != after.Mode() || file.Mode().Perm() != 0444 {
		t.Fatalf("outside permissions changed: %v", err)
	}
	content, _ := os.ReadFile(externalFile)
	if string(content) != "preserve" {
		t.Fatal("outside content changed")
	}
}

func TestTidyExternalCancellationPrecedesAllAbandonment(t *testing.T) {
	mainPath, workspace := t.TempDir(), t.TempDir()
	createJJWorkspaceLink(t, mainPath, workspace)
	infos := []workspaceInfo{{Policy: policyDisposable, Ref: workspaceRef{Handle: "default"}, Path: mainPath, Main: true}, {Policy: policyDisposable, Ref: workspaceRef{Handle: "alpha"}, Path: workspace, External: true, RepresentedElsewhere: true}}
	withCommandCapture(t, func(_ string, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "op log") {
			return "reviewed-operation\n", nil
		}
		query := strings.Join(args, " ")
		if strings.Contains(query, "workspace list") {
			return "default\tmain\t" + mainPath + "\nalpha\talpha\t" + workspace + "\n", nil
		}
		if strings.Contains(query, "empty() & description(\"\") & mutable() & (alpha@)") {
			return "cursor\n", nil
		}
		return "", nil
	})
	calls := []string{}
	withCommandToStderr(t, func(_ string, args ...string) error { calls = append(calls, strings.Join(args, " ")); return nil })
	oldIn, oldErr := stdinReader, stderrWriter
	stdinReader = &bytewiseReader{Reader: strings.NewReader("y\nn\n")}
	var output bytes.Buffer
	stderrWriter = &output
	t.Cleanup(func() { stdinReader, stderrWriter = oldIn, oldErr })
	if err := setWorkspacePolicies(mainPath, "proj", infos[1:], policyDisposable); err != nil {
		t.Fatal(err)
	}
	if err := tidyWorkspaces(mainPath, config{MainWorkspace: "default"}, "proj", infos, false, false); err != nil {
		t.Fatal(err)
	}
	if len(calls) > 0 {
		t.Fatalf("external cancellation mutated repository: %v", calls)
	}
	if !exists(workspace) {
		t.Fatal("external cancellation deleted workspace")
	}
}

type bytewiseReader struct{ *strings.Reader }

func (r *bytewiseReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.Reader.Read(p)
}

func TestTidyShowsSafetySeparatelyFromMainRelativeStatus(t *testing.T) {
	items := selectorItemsForTidy([]workspaceInfo{
		{Ref: workspaceRef{Handle: "safe"}, Ahead: 1, RepresentedElsewhere: true},
		{Ref: workspaceRef{Handle: "unique"}, Ahead: 1},
	}, false)
	line := formatSelectorItemLine("", "", items[0], selectorColumnWidthsForItems(items, []int{0, 1}))
	if items[0].Status != "unstacked" || !strings.Contains(line, "safe-to-close") {
		t.Fatalf("missing independent safety: %q", line)
	}
	line = formatSelectorItemLine("", "", items[1], selectorColumnWidthsForItems(items, []int{0, 1}))
	if items[1].Status != "unstacked" || !strings.Contains(line, "requires-force") {
		t.Fatalf("missing force requirement: %q", line)
	}
	if !strings.Contains(selectorLegend(selectorOptions{Tidy: true}), "closing set") {
		t.Fatal("legend must qualify individual safety against complete closing set")
	}
}

func TestClosePartialFailureReportsCompletedAndUnattemptedTargets(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission test requires non-root user")
	}
	parent := t.TempDir()
	blocked := filepath.Join(parent, "blocked")
	if err := os.Mkdir(blocked, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0755) })
	targets := []workspaceInfo{{Ref: workspaceRef{Handle: "done"}, Path: t.TempDir()}, {Ref: workspaceRef{Handle: "blocked"}, Path: blocked}, {Ref: workspaceRef{Handle: "later"}, Path: t.TempDir()}}
	repoPath := t.TempDir()
	for _, target := range targets {
		createJJWorkspaceLink(t, repoPath, target.Path)
	}
	withCommandCapture(t, func(string, ...string) (string, error) { return "unique\n", nil })
	forgot := []string{}
	withCommandToStderr(t, func(_ string, args ...string) error {
		q := strings.Join(args, " ")
		if strings.Contains(q, "workspace forget") {
			forgot = append(forgot, args[len(args)-1])
		}
		return nil
	})
	withCloseReviewEvidence(t, repoPath, targets)
	_, err := closeWorkspacesWithProtection(repoPath, targets, true, true, false, closeProtectionContext{})
	if err == nil || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("expected retained permission failure: %v", err)
	}
	for _, want := range []string{"closed: done", "failed: blocked", "not attempted: later", "abandoned unique mutable changes: done, blocked", "remains registered"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q: %v", want, err)
		}
	}
	if strings.Join(forgot, ",") != "done" || !exists(targets[2].Path) {
		t.Fatalf("unexpected later effects: %v", forgot)
	}
}

func TestRemovalUnlinksSymlinkWorkspaceWithoutTouchingTarget(t *testing.T) {
	outside := t.TempDir()
	file := filepath.Join(outside, "keep")
	if err := os.WriteFile(file, []byte("keep"), 0444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(outside, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(outside, 0755) })
	link := filepath.Join(t.TempDir(), "workspace")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := removeWorkspaceDirectory(link); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("link not removed: %v", err)
	}
	info, err := os.Stat(outside)
	if err != nil || info.Mode().Perm() != 0555 {
		t.Fatalf("outside directory changed: %v", err)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "keep" {
		t.Fatalf("outside file changed: %v", err)
	}
}

func TestRemovalStaysAnchoredWhenAncestorPathBecomesSymlink(t *testing.T) {
	container := t.TempDir()
	parentPath := filepath.Join(container, "parent")
	selected := filepath.Join(parentPath, "workspace")
	if err := os.MkdirAll(selected, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(selected, "cache"), []byte("selected"), 0444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(selected, 0555); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(parentPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	parent, err := root.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	outside := t.TempDir()
	outsideWorkspace := filepath.Join(outside, "workspace")
	if err := os.Mkdir(outsideWorkspace, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(outsideWorkspace, 0755) })
	relocated := filepath.Join(container, "relocated")
	if err := os.Rename(parentPath, relocated); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, parentPath); err != nil {
		t.Fatal(err)
	}
	if err := removeWorkspaceEntry(parent, "workspace"); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(relocated, "workspace")) {
		t.Fatal("selected directory survived")
	}
	info, err := os.Stat(outsideWorkspace)
	if err != nil || info.Mode().Perm() != 0555 {
		t.Fatalf("replacement ancestor redirected removal/repair: %v", err)
	}
}

func TestNormalCloseExplainsEmptyHeadWithProtectedUnstackedHistory(t *testing.T) {
	mainPath, alphaPath, _ := setupMutuallyRepresentedCloseRepo(t)
	if got := jjRevsetCount(t, mainPath, "alpha@ & empty()"); got != 1 {
		t.Fatal("fixture needs empty head")
	}
	oldIn := stdinReader
	stdinReader = strings.NewReader("n\n")
	t.Cleanup(func() { stdinReader = oldIn })
	before := currentOperationIDFullForTest(t, mainPath)
	_, output, err := captureOutput(func() error { return runClose([]string{"alpha", "--repo", mainPath}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"alpha (unstacked)", "represented in surviving Workspaces", "complete closing set", "no unique work will be lost"} {
		if !strings.Contains(output, want) {
			t.Errorf("missing %q: %s", want, output)
		}
	}
	if !exists(alphaPath) || before != currentOperationIDFullForTest(t, mainPath) {
		t.Fatal("declined close changed repository")
	}
}

func TestTidyPartialFailureReportsPriorEmptyCursorCleanup(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission test requires non-root user")
	}
	mainPath, parent := t.TempDir(), t.TempDir()
	workspace := filepath.Join(parent, "blocked")
	if err := os.Mkdir(workspace, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0755) })
	createJJWorkspaceLink(t, mainPath, workspace)
	infos := []workspaceInfo{{Policy: policyDisposable, Ref: workspaceRef{Handle: "default"}, Path: mainPath, Main: true}, {Policy: policyDisposable, Ref: workspaceRef{Handle: "blocked"}, Path: workspace, RepresentedElsewhere: true}}
	withCommandCapture(t, func(_ string, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "op log") {
			return "reviewed-operation\n", nil
		}
		query := strings.Join(args, " ")
		if strings.Contains(query, "workspace list") {
			return "default\tmain\t" + mainPath + "\nblocked\tblocked\t" + workspace + "\n", nil
		}
		if strings.Contains(query, "empty() & description(\"\") & mutable() & (blocked@)") {
			return "cursor\n", nil
		}
		return "", nil
	})
	withCommandToStderr(t, func(_ string, args ...string) error {
		if strings.Contains(strings.Join(args, " "), "workspace forget") {
			t.Fatal("failed target was forgotten")
		}
		return nil
	})
	if err := setWorkspacePolicies(mainPath, "proj", infos[1:], policyDisposable); err != nil {
		t.Fatal(err)
	}
	err := tidyWorkspaces(mainPath, config{MainWorkspace: "default"}, "proj", infos, false, true)
	if err == nil || !strings.Contains(err.Error(), "heads were already abandoned") || !strings.Contains(err.Error(), "failed: blocked") {
		t.Fatalf("missing earlier mutation report: %v", err)
	}
}

// Lower-level filesystem tests mock JJ rather than create a repository. Supply
// explicit stable review evidence without changing their graph-query responses.
func withCloseReviewEvidence(t *testing.T, repoPath string, targets []workspaceInfo) {
	t.Helper()
	refs := ""
	hasDefault := false
	for _, target := range targets {
		refs += target.Ref.Handle + "\thead\t" + target.Path + "\n"
		hasDefault = hasDefault || target.Ref.Handle == "default"
	}
	if !hasDefault {
		refs = "default\tmain\t" + repoPath + "\n" + refs
	}
	original := commandCaptureFn
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		query := strings.Join(args, " ")
		if name == "jj" && strings.Contains(query, "workspace list") {
			return refs, nil
		}
		if name == "jj" && strings.Contains(query, "op log") {
			return "reviewed-operation\n", nil
		}
		return original(name, args...)
	})
}
