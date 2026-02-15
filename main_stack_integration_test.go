package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMainStackAutoUsesLinearWhenWorkspaceHeadsCollapse(t *testing.T) {
	requireJJ(t)

	repoRoot, worktreesRoot := initIntegrationRepo(t)
	app := "nixfiles"
	kiloPath, limaPath := createWorkspaces(t, repoRoot, worktreesRoot, app)

	writeFile(t, filepath.Join(kiloPath, "kilo.txt"), "kilo\n")
	runJJ(t, kiloPath, "-R", kiloPath, "describe", "-m", "kilo change")
	runJJ(t, kiloPath, "-R", kiloPath, "new")

	runJJ(t, limaPath, "-R", limaPath, "rebase", "-r", "@", "-d", "kilo@")
	writeFile(t, filepath.Join(limaPath, "lima.txt"), "lima\n")
	runJJ(t, limaPath, "-R", limaPath, "describe", "-m", "lima change")
	runJJ(t, limaPath, "-R", limaPath, "new")

	if err := runMainStack([]string{"--repo", repoRoot, "--app", app, "--worktrees-root", worktreesRoot, "--workspace", "default", "--stack-shape", "auto", "--rebase-mode", "branch"}); err != nil {
		t.Fatalf("runMainStack failed: %v", err)
	}

	parents := nonEmptyLines(runJJ(t, repoRoot, "-R", repoRoot, "log", "-r", "parents(default@)", "--no-graph", "-T", "change_id.short() ++ \"\\n\""))
	if len(parents) != 1 {
		t.Fatalf("expected linear stack with one parent, got %d parents: %#v", len(parents), parents)
	}
	limaHead := workspaceHeadByName(t, repoRoot, "lima")
	if parents[0] != limaHead {
		t.Fatalf("expected default parent to be lima head %q, got %q", limaHead, parents[0])
	}
}

func TestMainStackAutoUsesMergeWhenWorkspaceHeadsDiverge(t *testing.T) {
	requireJJ(t)

	repoRoot, worktreesRoot := initIntegrationRepo(t)
	app := "nixfiles"
	kiloPath, limaPath := createWorkspaces(t, repoRoot, worktreesRoot, app)

	writeFile(t, filepath.Join(kiloPath, "kilo.txt"), "kilo\n")
	runJJ(t, kiloPath, "-R", kiloPath, "describe", "-m", "kilo change")
	runJJ(t, kiloPath, "-R", kiloPath, "new")

	writeFile(t, filepath.Join(limaPath, "lima.txt"), "lima\n")
	runJJ(t, limaPath, "-R", limaPath, "describe", "-m", "lima change")
	runJJ(t, limaPath, "-R", limaPath, "new")

	if err := runMainStack([]string{"--repo", repoRoot, "--app", app, "--worktrees-root", worktreesRoot, "--workspace", "default", "--stack-shape", "auto", "--rebase-mode", "branch"}); err != nil {
		t.Fatalf("runMainStack failed: %v", err)
	}

	parents := nonEmptyLines(runJJ(t, repoRoot, "-R", repoRoot, "log", "-r", "parents(default@)", "--no-graph", "-T", "change_id.short() ++ \"\\n\""))
	if len(parents) < 2 {
		t.Fatalf("expected merge stack with at least two parents, got %d: %#v", len(parents), parents)
	}

	kiloHead := workspaceHeadByName(t, repoRoot, "kilo")
	limaHead := workspaceHeadByName(t, repoRoot, "lima")
	parentSet := map[string]struct{}{}
	for _, p := range parents {
		parentSet[p] = struct{}{}
	}
	if _, ok := parentSet[kiloHead]; !ok {
		t.Fatalf("expected kilo head %q in parents %#v", kiloHead, parents)
	}
	if _, ok := parentSet[limaHead]; !ok {
		t.Fatalf("expected lima head %q in parents %#v", limaHead, parents)
	}
}

func TestMainStackLinearErrorsWhenWorkspaceHeadsDiverge(t *testing.T) {
	requireJJ(t)

	repoRoot, worktreesRoot := initIntegrationRepo(t)
	app := "nixfiles"
	kiloPath, limaPath := createWorkspaces(t, repoRoot, worktreesRoot, app)

	writeFile(t, filepath.Join(kiloPath, "kilo.txt"), "kilo\n")
	runJJ(t, kiloPath, "-R", kiloPath, "describe", "-m", "kilo change")
	runJJ(t, kiloPath, "-R", kiloPath, "new")

	writeFile(t, filepath.Join(limaPath, "lima.txt"), "lima\n")
	runJJ(t, limaPath, "-R", limaPath, "describe", "-m", "lima change")
	runJJ(t, limaPath, "-R", limaPath, "new")

	err := runMainStack([]string{"--repo", repoRoot, "--app", app, "--worktrees-root", worktreesRoot, "--workspace", "default", "--stack-shape", "linear", "--rebase-mode", "branch"})
	if err == nil || !strings.Contains(err.Error(), "requires a single frontier head") {
		t.Fatalf("expected strict linear error, got %v", err)
	}
}

func requireJJ(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not found in PATH")
	}
}

func initIntegrationRepo(t *testing.T) (string, string) {
	t.Helper()
	repoRoot := filepath.Join(t.TempDir(), "repo")
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")

	runJJ(t, filepath.Dir(repoRoot), "git", "init", repoRoot)
	writeFile(t, filepath.Join(repoRoot, "README.md"), "base\n")
	runJJ(t, repoRoot, "-R", repoRoot, "describe", "-m", "base")
	runJJ(t, repoRoot, "-R", repoRoot, "bookmark", "set", "main", "-r", "@")
	runJJ(t, repoRoot, "-R", repoRoot, "new")

	return repoRoot, worktreesRoot
}

func createWorkspaces(t *testing.T, repoRoot string, worktreesRoot string, app string) (string, string) {
	t.Helper()
	kiloPath := filepath.Join(worktreesRoot, app, "kilo")
	limaPath := filepath.Join(worktreesRoot, app, "lima")
	if err := os.MkdirAll(filepath.Dir(kiloPath), 0o755); err != nil {
		t.Fatalf("mkdir worktrees parent: %v", err)
	}
	if err := os.MkdirAll(kiloPath, 0o755); err != nil {
		t.Fatalf("mkdir kilo path: %v", err)
	}
	if err := os.MkdirAll(limaPath, 0o755); err != nil {
		t.Fatalf("mkdir lima path: %v", err)
	}
	runJJ(t, repoRoot, "-R", repoRoot, "workspace", "add", "--name", "kilo", kiloPath)
	runJJ(t, repoRoot, "-R", repoRoot, "workspace", "add", "--name", "lima", limaPath)
	return kiloPath, limaPath
}

func workspaceHeadByName(t *testing.T, repoRoot string, name string) string {
	t.Helper()
	out := runJJ(t, repoRoot, "-R", repoRoot, "workspace", "list", "-T", "name ++ \"\\t\" ++ target.change_id().short() ++ \"\\n\"")
	for _, line := range nonEmptyLines(out) {
		parts := strings.Split(line, "\t")
		if len(parts) != 2 {
			continue
		}
		if strings.TrimSpace(parts[0]) == name {
			return strings.TrimSpace(parts[1])
		}
	}
	t.Fatalf("workspace %q not found in workspace list output %q", name, out)
	return ""
}

func nonEmptyLines(out string) []string {
	lines := strings.Split(out, "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		result = append(result, line)
	}
	return result
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func runJJ(t *testing.T, workdir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("jj", args...)
	if strings.TrimSpace(workdir) != "" {
		cmd.Dir = workdir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("jj %s failed: %v\n%s", strings.Join(args, " "), err, string(out))
	}
	return string(out)
}

func TestMainStackInvalidStackShape(t *testing.T) {
	repoRoot := t.TempDir()
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	app := "nixfiles"
	if err := os.MkdirAll(filepath.Join(worktreesRoot, app, "main"), 0o755); err != nil {
		t.Fatalf("mkdir main workspace: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(worktreesRoot, app, "feat"), 0o755); err != nil {
		t.Fatalf("mkdir feat workspace: %v", err)
	}

	origCapture := commandCaptureFn
	origRun := commandToStderrFn
	defer func() {
		commandCaptureFn = origCapture
		commandToStderrFn = origRun
	}()

	commandCaptureFn = func(name string, args ...string) (string, error) {
		if len(args) >= 3 && args[2] == "workspace" {
			return "feat\tabc111\nmain\tabc333\n", nil
		}
		if len(args) >= 4 && args[2] == "log" && args[3] == "-r" {
			return "abc333\n", nil
		}
		return "", nil
	}
	commandToStderrFn = func(name string, args ...string) error { return nil }

	err := runMainStack([]string{"--repo", repoRoot, "--app", app, "--worktrees-root", worktreesRoot, "--workspace", "main", "--stack-shape", "wat"})
	if err == nil || !strings.Contains(err.Error(), "invalid --stack-shape") {
		t.Fatalf("expected invalid stack shape error, got %v", err)
	}
}

func TestMainStackLinearRequiresSingleFrontierHead(t *testing.T) {
	repoRoot := t.TempDir()
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	app := "nixfiles"
	for _, name := range []string{"main", "feat-a", "feat-b"} {
		if err := os.MkdirAll(filepath.Join(worktreesRoot, app, name), 0o755); err != nil {
			t.Fatalf("mkdir workspace %s: %v", name, err)
		}
	}

	origCapture := commandCaptureFn
	origRun := commandToStderrFn
	defer func() {
		commandCaptureFn = origCapture
		commandToStderrFn = origRun
	}()

	commandCaptureFn = func(name string, args ...string) (string, error) {
		if len(args) >= 3 && args[2] == "workspace" {
			return "feat-a\tabc111\nfeat-b\tabc222\nmain\tabc333\n", nil
		}
		if len(args) >= 5 && args[2] == "log" && args[3] == "-r" && strings.HasPrefix(args[4], "heads(") {
			return "abc111\nabc222\n", nil
		}
		if len(args) >= 4 && args[2] == "log" && args[3] == "-r" {
			return "abc333\n", nil
		}
		return "", nil
	}
	commandToStderrFn = func(name string, args ...string) error { return nil }

	err := runMainStack([]string{"--repo", repoRoot, "--app", app, "--worktrees-root", worktreesRoot, "--workspace", "main", "--stack-shape", "linear"})
	if err == nil || !strings.Contains(err.Error(), "requires a single frontier head") {
		t.Fatalf("expected single frontier head error, got %v", err)
	}
}
