package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigRejectsLegacyUnknownKeys(t *testing.T) {
	repoRoot := t.TempDir()
	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "jjw"), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := []byte("worktrees_root: /tmp/worktrees\nname_list:\n  - alpha\n")
	if err := os.WriteFile(filepath.Join(xdg, "jjw", "config.yaml"), legacy, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)
	_, err := loadConfig(repoRoot)
	if err == nil || !strings.Contains(err.Error(), "field worktrees_root not found") {
		t.Fatalf("expected unknown legacy key error, got %v", err)
	}
}

func TestLoadConfigUsesRedesignedVocabulary(t *testing.T) {
	repoRoot := t.TempDir()
	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "jjw"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgText := []byte(strings.Join([]string{
		"workspaces_root: /tmp/workspaces",
		"project: my-project",
		"handle_strategy: next-unused",
		"workspace_handles:",
		"  - kilo",
		"main_workspace: default",
		"assimilated_folders:",
		"  - scratch",
		"stack:",
		"  rebase_mode: revision",
		"  shape: merge",
		"  conflict_strategy: off",
		"create:",
		"  envrc: true",
		"  direnv_allow: true",
		"projects:",
		"  my-project:",
		"    assimilated_folders:",
		"      - .local-notes",
		"",
	}, "\n"))
	if err := os.WriteFile(filepath.Join(xdg, "jjw", "config.yaml"), cfgText, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)
	cfg, err := loadConfig(repoRoot)
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	if cfg.WorkspacesRoot != "/tmp/workspaces" || cfg.Project != "my-project" || cfg.HandleStrategy != strategyNextUnused || cfg.WorkspaceHandles[0] != "kilo" || cfg.MainWorkspace != "default" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.Stack.RebaseMode != "revision" || cfg.Stack.Shape != "merge" || cfg.Stack.ConflictStrategy != "off" {
		t.Fatalf("unexpected stack config: %+v", cfg.Stack)
	}
	if !cfg.Create.Envrc || !cfg.Create.DirenvAllow {
		t.Fatalf("expected create setup config, got %+v", cfg.Create)
	}
	folders := effectiveAssimilatedFolders(cfg, "my-project")
	if strings.Join(folders, ",") != "scratch,.local-notes" {
		t.Fatalf("unexpected assimilated folders: %v", folders)
	}
}

func TestLoadConfigMergesGlobalAndProjectAssimilatedFolders(t *testing.T) {
	repoRoot := t.TempDir()
	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "jjw"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgText := []byte(strings.Join([]string{
		"workspaces_root: /tmp/workspaces",
		"assimilated_folders:",
		"  - scratch",
		"  - logs",
		"projects:",
		"  proj:",
		"    assimilated_folders:",
		"      - logs",
		"      - .local-tools",
		"",
	}, "\n"))
	if err := os.WriteFile(filepath.Join(xdg, "jjw", "config.yaml"), cfgText, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)
	cfg, err := loadConfig(repoRoot)
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	folders := effectiveAssimilatedFolders(cfg, "proj")
	if strings.Join(folders, ",") != "scratch,logs,.local-tools" {
		t.Fatalf("unexpected merged assimilated folders: %v", folders)
	}
}

func TestLoadConfigRejectsUnsafeAssimilatedFolders(t *testing.T) {
	repoRoot := t.TempDir()
	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "jjw"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgText := []byte("workspaces_root: /tmp/workspaces\nassimilated_folders:\n  - ../scratch\n")
	if err := os.WriteFile(filepath.Join(xdg, "jjw", "config.yaml"), cfgText, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)
	_, err := loadConfig(repoRoot)
	if err == nil || !strings.Contains(err.Error(), "assimilated_folders") {
		t.Fatalf("expected unsafe assimilated_folders error, got %v", err)
	}
}

func TestChooseAutoHandleFirstUnused(t *testing.T) {
	cfg := config{HandleStrategy: strategyFirstUnused, WorkspaceHandles: []string{"alpha", "bravo"}}
	got, err := chooseAutoHandle(cfg, t.TempDir(), map[string]struct{}{"alpha": {}})
	if err != nil {
		t.Fatal(err)
	}
	if got != "bravo" {
		t.Fatalf("expected bravo, got %q", got)
	}
}

func TestChooseAutoHandleNextUnusedWritesProjectState(t *testing.T) {
	repoRoot := t.TempDir()
	cfg := config{HandleStrategy: strategyNextUnused, WorkspaceHandles: []string{"alpha", "bravo"}}
	got, err := chooseAutoHandle(cfg, repoRoot, map[string]struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "alpha" {
		t.Fatalf("expected alpha, got %q", got)
	}
	got, err = chooseAutoHandle(cfg, repoRoot, map[string]struct{}{"alpha": {}})
	if err != nil {
		t.Fatal(err)
	}
	if got != "bravo" {
		t.Fatalf("expected bravo, got %q", got)
	}
}

func TestValidateSlugsRejectTraversal(t *testing.T) {
	bad := []string{"", "../x", "x/y", "..", " space"}
	for _, value := range bad {
		if err := validateWorkspaceHandle(value); err == nil {
			t.Fatalf("expected %q to be invalid", value)
		}
	}
	if err := validateWorkspaceHandle("fix.auth-2"); err != nil {
		t.Fatalf("expected slug valid: %v", err)
	}
}

func TestRunRejectsLegacyCommands(t *testing.T) {
	if err := run([]string{"cd"}); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("expected cd unknown, got %v", err)
	}
	if err := run([]string{"select"}); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("expected select unknown, got %v", err)
	}
}

func TestRunOpenRejectsMultipleHandles(t *testing.T) {
	err := runOpen([]string{"alpha", "bravo"})
	if err == nil || !strings.Contains(err.Error(), "at most one") {
		t.Fatalf("expected multiple handle error, got %v", err)
	}
}

func TestListIncludesCurrentAndMainMarkers(t *testing.T) {
	repoRoot := t.TempDir()
	workspacesRoot := filepath.Join(t.TempDir(), "workspaces")
	mainPath := filepath.Join(workspacesRoot, "proj", "default")
	alphaPath := filepath.Join(workspacesRoot, "proj", "alpha")
	for _, path := range []string{mainPath, alphaPath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(t, repoRoot, "workspaces_root: "+workspacesRoot+"\nproject: proj\nmain_workspace: default\n")
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "workspace list") {
			return "default\tmain111\t" + mainPath + "\nalpha\talpha111\t" + alphaPath + "\n", nil
		}
		if strings.Contains(joined, "log -r @") {
			return "alpha111\n", nil
		}
		return "", nil
	})
	out, err := captureStdout(func() error { return runList([]string{"--repo", repoRoot}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "default\tmain") || !strings.Contains(out, "alpha\tcurrent") {
		t.Fatalf("expected main and current markers, got %q", out)
	}
}

func TestRunInitRefusesExistingConfigUnlessForce(t *testing.T) {
	xdg := t.TempDir()
	cfgDir := filepath.Join(xdg, "jjw")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(cfgDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("workspaces_root: /tmp/a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if err := runInit([]string{"--workspaces-root", "/tmp/b"}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected existing config error, got %v", err)
	}
	if _, err := captureStdout(func() error { return runInit([]string{"--force", "--workspaces-root", "/tmp/b"}) }); err != nil {
		t.Fatalf("force init failed: %v", err)
	}
}

func writeConfig(t *testing.T, repoRoot string, content string) {
	t.Helper()
	dir := filepath.Join(repoRoot, ".jjw")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

func withCommandCapture(t *testing.T, fn func(string, ...string) (string, error)) {
	t.Helper()
	orig := commandCaptureFn
	commandCaptureFn = fn
	t.Cleanup(func() { commandCaptureFn = orig })
}

func withCommandToStderr(t *testing.T, fn func(string, ...string) error) {
	t.Helper()
	orig := commandToStderrFn
	commandToStderrFn = fn
	t.Cleanup(func() { commandToStderrFn = orig })
}

func captureStdout(fn func() error) (string, error) {
	origOut := stdoutWriter
	origErr := stderrWriter
	defer func() {
		stdoutWriter = origOut
		stderrWriter = origErr
	}()
	var out bytes.Buffer
	stdoutWriter = &out
	stderrWriter = io.Discard
	err := fn()
	return out.String(), err
}

func TestCommandContextRequiresWorkspacesRoot(t *testing.T) {
	repoRoot := t.TempDir()
	writeConfig(t, repoRoot, "project: proj\n")
	_, _, _, err := commandContext(repoRoot, "", "")
	if err == nil || !strings.Contains(err.Error(), "workspaces_root is required") {
		t.Fatalf("expected workspaces_root required error, got %v", err)
	}
}

func TestMaterializeAssimilatedFoldersSymlinksExistingSources(t *testing.T) {
	mainPath := t.TempDir()
	workspacePath := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(filepath.Join(mainPath, "scratch"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mainPath, "scratch", "note.md"), []byte("one-off"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config{AssimilatedFolders: []string{"scratch", "missing"}}
	if err := materializeAssimilatedFolders(mainPath, workspacePath, cfg, "proj"); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(workspacePath, "scratch"))
	if err != nil {
		t.Fatalf("expected scratch symlink: %v", err)
	}
	if target != filepath.Join(mainPath, "scratch") {
		t.Fatalf("expected symlink to main scratch, got %q", target)
	}
	if exists(filepath.Join(workspacePath, "missing")) {
		t.Fatal("missing source should be skipped, not created")
	}
}

func TestMaterializeAssimilatedFoldersRefusesToReplaceWorkspaceContent(t *testing.T) {
	mainPath := t.TempDir()
	workspacePath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(mainPath, "scratch"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspacePath, "scratch"), []byte("workspace-local"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config{AssimilatedFolders: []string{"scratch"}}
	if err := materializeAssimilatedFolders(mainPath, workspacePath, cfg, "proj"); err == nil || !strings.Contains(err.Error(), "refusing to replace") {
		t.Fatalf("expected refusal to replace existing content, got %v", err)
	}
}

func TestRunCreateMaterializesAssimilatedFolders(t *testing.T) {
	mainPath := t.TempDir()
	workspacesRoot := filepath.Join(t.TempDir(), "workspaces")
	if err := os.MkdirAll(filepath.Join(mainPath, "scratch"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, mainPath, strings.Join([]string{
		"workspaces_root: " + workspacesRoot,
		"project: proj",
		"assimilated_folders:",
		"  - scratch",
		"",
	}, "\n"))
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "workspace list") {
			return "default\tmain111\t" + mainPath + "\n", nil
		}
		return "", nil
	})
	withCommandToStderr(t, func(name string, args ...string) error {
		if name == "jj" && len(args) > 0 && strings.Contains(strings.Join(args, " "), "workspace add") {
			return os.MkdirAll(args[len(args)-1], 0o755)
		}
		return nil
	})
	out, err := captureStdout(func() error { return runCreate([]string{"alpha", "--repo", mainPath}) })
	if err != nil {
		t.Fatal(err)
	}
	workspacePath := filepath.Join(workspacesRoot, "proj", "alpha")
	if strings.TrimSpace(out) != workspacePath {
		t.Fatalf("expected create to print workspace path, got %q", out)
	}
	if target, err := os.Readlink(filepath.Join(workspacePath, "scratch")); err != nil || target != filepath.Join(mainPath, "scratch") {
		t.Fatalf("expected create to materialize scratch symlink to main, got target=%q err=%v", target, err)
	}
}

func TestRunOpenMaterializesAssimilatedFoldersBeforePrintingPath(t *testing.T) {
	mainPath := t.TempDir()
	workspacePath := filepath.Join(t.TempDir(), "alpha")
	if err := os.MkdirAll(filepath.Join(mainPath, "scratch"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, mainPath, strings.Join([]string{
		"workspaces_root: /tmp/workspaces",
		"project: proj",
		"assimilated_folders:",
		"  - scratch",
		"",
	}, "\n"))
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "workspace list") {
			return "default\tmain111\t" + mainPath + "\nalpha\talpha111\t" + workspacePath + "\n", nil
		}
		return "", nil
	})
	out, err := captureStdout(func() error { return runOpen([]string{"alpha", "--repo", mainPath}) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != workspacePath {
		t.Fatalf("expected open to print workspace path, got %q", out)
	}
	if target, err := os.Readlink(filepath.Join(workspacePath, "scratch")); err != nil || target != filepath.Join(mainPath, "scratch") {
		t.Fatalf("expected open to materialize scratch symlink to main, got target=%q err=%v", target, err)
	}
}

func TestRunOpenNonTTYWithoutHandleFails(t *testing.T) {
	origIn, origErr := stdinReader, stderrWriter
	stdinReader = strings.NewReader("")
	stderrWriter = io.Discard
	defer func() { stdinReader, stderrWriter = origIn, origErr }()
	repoRoot := t.TempDir()
	writeConfig(t, repoRoot, "workspaces_root: /tmp/workspaces\nproject: proj\n")
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "workspace list") {
			return "default\tabc\t/tmp/workspaces/proj/default\n", nil
		}
		return "", nil
	})
	if err := runOpen([]string{"--repo", repoRoot}); err == nil || !strings.Contains(err.Error(), "requires a Workspace Handle") {
		t.Fatalf("expected non-tty handle error, got %v", err)
	}
}

func TestCloseAllSelectsOnlyClosableWithoutForce(t *testing.T) {
	infos := []workspaceInfo{
		{Ref: workspaceRef{Handle: "default"}, Main: true},
		{Ref: workspaceRef{Handle: "empty"}, Empty: true},
		{Ref: workspaceRef{Handle: "stacked"}, Stacked: true},
		{Ref: workspaceRef{Handle: "conflict"}, Conflict: true},
		{Ref: workspaceRef{Handle: "unstacked"}},
	}
	closable := []string{}
	for _, info := range infos {
		if !info.Main && isClosable(info) {
			closable = append(closable, info.Ref.Handle)
		}
	}
	if strings.Join(closable, ",") != "empty,stacked" {
		t.Fatalf("unexpected closable set: %v", closable)
	}
}

func TestRunStackAllUsesStackRelevantOnly(t *testing.T) {
	infos := []workspaceInfo{
		{Ref: workspaceRef{Handle: "default"}, Main: true},
		{Ref: workspaceRef{Handle: "empty"}, Empty: true},
		{Ref: workspaceRef{Handle: "stacked"}, Stacked: true},
		{Ref: workspaceRef{Handle: "conflict"}, Conflict: true},
		{Ref: workspaceRef{Handle: "unstacked"}},
		{Ref: workspaceRef{Handle: "missing"}, Missing: true},
	}
	relevant := []string{}
	for _, info := range infos {
		if isStackRelevant(info) {
			relevant = append(relevant, info.Ref.Handle)
		}
	}
	if strings.Join(relevant, ",") != "conflict,unstacked" {
		t.Fatalf("unexpected relevant set: %v", relevant)
	}
}

func TestCommandCaptureErrorsCanBeRestored(t *testing.T) {
	withCommandCapture(t, func(string, ...string) (string, error) { return "", errors.New("boom") })
	_, err := resolveRepoRoot("")
	if err == nil {
		t.Fatal("expected error")
	}
}
