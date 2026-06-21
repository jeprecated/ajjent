package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStackInputPayloadRevsetUsesWorkspaceParent(t *testing.T) {
	got := stackInputPayloadRevset("bravo")
	want := "bravo@-"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestResolveStackShapeMergeUsesWorkspacePayloadParents(t *testing.T) {
	orig := commandCaptureFn
	defer func() { commandCaptureFn = orig }()

	commandCaptureFn = func(name string, args ...string) (string, error) {
		return "one\ntwo\n", nil
	}
	shape, _, dests, err := resolveStackShape("/repo", []string{"alpha", "bravo"}, "merge")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{stackInputPayloadRevset("alpha"), stackInputPayloadRevset("bravo")}
	if shape != "merge" || strings.Join(dests, ",") != strings.Join(want, ",") {
		t.Fatalf("expected merge destinations %v, got shape=%s dests=%v", want, shape, dests)
	}
}

func TestResolveStackShapeAutoLinearAndMerge(t *testing.T) {
	orig := commandCaptureFn
	defer func() { commandCaptureFn = orig }()

	commandCaptureFn = func(name string, args ...string) (string, error) {
		return "one\n", nil
	}
	shape, reason, dests, err := resolveStackShape("/repo", []string{"alpha", "bravo"}, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if shape != "linear" || reason != "single frontier head" || len(dests) != 1 || dests[0] != "one" {
		t.Fatalf("unexpected linear resolution: %s %s %v", shape, reason, dests)
	}

	commandCaptureFn = func(name string, args ...string) (string, error) {
		return "one\ntwo\n", nil
	}
	shape, reason, dests, err = resolveStackShape("/repo", []string{"alpha", "bravo"}, "auto")
	if err != nil {
		t.Fatal(err)
	}
	wantDests := []string{stackInputPayloadRevset("alpha"), stackInputPayloadRevset("bravo")}
	if shape != "merge" || reason != "2 frontier heads" || strings.Join(dests, ",") != strings.Join(wantDests, ",") {
		t.Fatalf("unexpected merge resolution: %s %s %v", shape, reason, dests)
	}
}

func TestResolveStackShapeLinearRejectsDivergence(t *testing.T) {
	orig := commandCaptureFn
	defer func() { commandCaptureFn = orig }()
	commandCaptureFn = func(name string, args ...string) (string, error) {
		return "one\ntwo\n", nil
	}
	_, _, _, err := resolveStackShape("/repo", []string{"alpha", "bravo"}, "linear")
	if err == nil {
		t.Fatal("expected linear divergence error")
	}
}

func TestRunStackRebaseMergeUsesWorkspacePayloadParents(t *testing.T) {
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "conflicts() & @") {
			return "", nil
		}
		return "delta-parent\nbravo-parent\n", nil
	})
	rebaseArgs := []string{}
	withCommandToStderr(t, func(name string, args ...string) error {
		if strings.Contains(strings.Join(args, " "), " rebase ") {
			rebaseArgs = append([]string(nil), args...)
		}
		return nil
	})
	conflicted, err := runStackRebase("/repo", []string{"delta", "bravo"}, stackConfig{RebaseMode: "branch", Shape: "merge", ConflictStrategy: "off"})
	if err != nil || conflicted {
		t.Fatalf("expected clean rebase, conflicted=%v err=%v", conflicted, err)
	}
	dests := rebaseDestinations(rebaseArgs)
	want := []string{stackInputPayloadRevset("delta"), stackInputPayloadRevset("bravo")}
	if strings.Join(dests, ",") != strings.Join(want, ",") {
		t.Fatalf("expected Workspace payload destinations %v, got args=%v dests=%v", want, rebaseArgs, dests)
	}
}

func TestRunStackRebaseAutoLinearUsesResolvedWorkspacePayloadFrontier(t *testing.T) {
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "conflicts() & @") {
			return "", nil
		}
		return "delta-parent\n", nil
	})
	rebaseArgs := []string{}
	withCommandToStderr(t, func(name string, args ...string) error {
		if strings.Contains(strings.Join(args, " "), " rebase ") {
			rebaseArgs = append([]string(nil), args...)
		}
		return nil
	})
	conflicted, err := runStackRebase("/repo", []string{"delta", "bravo"}, stackConfig{RebaseMode: "branch", Shape: "auto", ConflictStrategy: "off"})
	if err != nil || conflicted {
		t.Fatalf("expected clean rebase, conflicted=%v err=%v", conflicted, err)
	}
	dests := rebaseDestinations(rebaseArgs)
	if strings.Join(dests, ",") != "delta-parent" {
		t.Fatalf("expected resolved Workspace payload frontier destination, got args=%v dests=%v", rebaseArgs, dests)
	}
}

func rebaseDestinations(args []string) []string {
	dests := []string{}
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-d" {
			dests = append(dests, args[i+1])
		}
	}
	return dests
}

func TestResolveStackConflictStrategyDefaultsPreferClean(t *testing.T) {
	got, err := resolveStackConflictStrategy("")
	if err != nil {
		t.Fatal(err)
	}
	if got != "prefer-clean" {
		t.Fatalf("expected prefer-clean, got %q", got)
	}
}

func TestRunStackRealRepoTargetsCurrentNonDefaultWorkspace(t *testing.T) {
	paths := setupRealStackRepo(t)
	defaultBefore := jjRev(t, paths.defaultPath, "default@")
	childPayload := jjRev(t, paths.defaultPath, "agm-speed-transition@-")
	_, errOut, err := captureOutput(func() error {
		return runStack([]string{"agm-speed-transition", "--repo", paths.speedPath, "--yes", "--rebase-mode", "branch", "--conflict-strategy", "off"})
	})
	if err != nil {
		t.Fatalf("expected stack from speed workspace to succeed, got %v\nstderr:%s", err, errOut)
	}
	if got := jjRev(t, paths.defaultPath, "default@"); got != defaultBefore {
		t.Fatalf("default@ moved unexpectedly: before=%s after=%s", defaultBefore, got)
	}
	if got := strings.TrimSpace(jjLog(t, paths.defaultPath, childPayload+" & ::speed@")); got != childPayload {
		t.Fatalf("expected speed@ to include child payload %s, got %q\nstderr:%s", childPayload, got, errOut)
	}
	if !strings.Contains(errOut, "Stack target workspace: speed ("+paths.speedPath+")") {
		t.Fatalf("expected resolved speed target in stderr, got %q", errOut)
	}
}

func TestRunStackRealRepoUsesCurrentDirectoryWorkspaceWhenRepoOmitted(t *testing.T) {
	paths := setupRealStackRepo(t)
	defaultBefore := jjRev(t, paths.defaultPath, "default@")
	childPayload := jjRev(t, paths.defaultPath, "agm-speed-transition@-")
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(paths.speedPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
	_, errOut, err := captureOutput(func() error {
		return runStack([]string{"agm-speed-transition", "--yes", "--rebase-mode", "branch", "--conflict-strategy", "off"})
	})
	if err != nil {
		t.Fatalf("expected stack from speed cwd to succeed, got %v\nstderr:%s", err, errOut)
	}
	if got := jjRev(t, paths.defaultPath, "default@"); got != defaultBefore {
		t.Fatalf("default@ moved unexpectedly: before=%s after=%s", defaultBefore, got)
	}
	if got := strings.TrimSpace(jjLog(t, paths.defaultPath, childPayload+" & ::speed@")); got != childPayload {
		t.Fatalf("expected speed@ to include child payload %s, got %q\nstderr:%s", childPayload, got, errOut)
	}
	if !strings.Contains(errOut, "Stack target workspace: speed ("+paths.speedPath+")") {
		t.Fatalf("expected resolved speed target in stderr, got %q", errOut)
	}
}

func TestRunStackRealRepoExplicitWorkspaceOverrideTargetsDefault(t *testing.T) {
	paths := setupRealStackRepo(t)
	childPayload := jjRev(t, paths.defaultPath, "agm-speed-transition@-")
	_, errOut, err := captureOutput(func() error {
		return runStack([]string{"agm-speed-transition", "--repo", paths.speedPath, "--workspace", "default", "--yes", "--rebase-mode", "branch", "--conflict-strategy", "off"})
	})
	if err != nil {
		t.Fatalf("expected explicit --workspace default stack to succeed, got %v\nstderr:%s", err, errOut)
	}
	if got := strings.TrimSpace(jjLog(t, paths.defaultPath, childPayload+" & ::default@")); got != childPayload {
		t.Fatalf("expected default@ to include child payload %s, got %q\nstderr:%s", childPayload, got, errOut)
	}
}

func TestRunStackRealRepoSelfTargetGuard(t *testing.T) {
	paths := setupRealStackRepo(t)
	err := runStack([]string{"agm-speed-transition", "--repo", paths.childPath, "--yes", "--rebase-mode", "branch", "--conflict-strategy", "off"})
	if err == nil || !strings.Contains(err.Error(), "target workspace") || !strings.Contains(err.Error(), "--workspace") {
		t.Fatalf("expected self-target guard with --workspace guidance, got %v", err)
	}
}

type realStackRepoPaths struct {
	defaultPath string
	speedPath   string
	childPath   string
}

func setupRealStackRepo(t *testing.T) realStackRepoPaths {
	t.Helper()
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj binary not available for integration test")
	}
	workspacesRoot := filepath.Join(t.TempDir(), "workspaces")
	defaultPath := filepath.Join(workspacesRoot, "proj", "default")
	speedPath := filepath.Join(workspacesRoot, "proj", "speed")
	childPath := filepath.Join(workspacesRoot, "proj", "agm-speed-transition")
	if err := os.MkdirAll(filepath.Dir(defaultPath), 0o755); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "git", "init", "--colocate", defaultPath)
	writeConfig(t, defaultPath, strings.Join([]string{
		"workspaces_root: " + workspacesRoot,
		"project: proj",
		"main_workspace: default",
		"stack:",
		"  rebase_mode: branch",
		"  shape: auto",
		"  conflict_strategy: off",
		"",
	}, "\n"))
	runJJ(t, "-R", defaultPath, "workspace", "add", "--name", "speed", speedPath)
	runJJ(t, "-R", speedPath, "workspace", "add", "--name", "agm-speed-transition", childPath)
	if err := os.WriteFile(filepath.Join(childPath, "payload.txt"), []byte("child payload\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = exec.Command("jj", "-R", childPath, "file", "track", "payload.txt").Run()
	runJJ(t, "-R", childPath, "commit", "-m", "child payload")
	return realStackRepoPaths{defaultPath: defaultPath, speedPath: speedPath, childPath: childPath}
}

func runJJ(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("jj", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("jj %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func jjRev(t *testing.T, repoPath string, revset string) string {
	t.Helper()
	return strings.TrimSpace(jjLog(t, repoPath, revset))
}

func jjLog(t *testing.T, repoPath string, revset string) string {
	t.Helper()
	cmd := exec.Command("jj", "-R", repoPath, "log", "-r", revset, "--no-graph", "-T", "change_id.short() ++ \"\\n\"")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("jj log -r %s failed: %v\n%s", revset, err, out)
	}
	return string(out)
}
