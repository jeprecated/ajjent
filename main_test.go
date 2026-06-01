package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeCreateArgs(t *testing.T) {
	t.Parallel()

	got, err := normalizeCreateArgs([]string{"name", "--repo", "/r", "--app", "app"})
	if err != nil {
		t.Fatalf("normalizeCreateArgs failed: %v", err)
	}
	want := []string{"--repo", "/r", "--app", "app", "name"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected normalized args\nwant: %#v\ngot:  %#v", want, got)
	}
}

func TestNormalizeCreateArgsTooManyPositionals(t *testing.T) {
	t.Parallel()

	_, err := normalizeCreateArgs([]string{"one", "two"})
	if err == nil {
		t.Fatalf("expected error for too many positionals")
	}
}

func TestChooseAutoNameFirstUnused(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	cfg := config{
		NameStrategy: strategyFirstUnused,
		NameList:     []string{"alpha", "bravo", "charlie"},
	}
	inUse := map[string]struct{}{"alpha": {}}

	got, err := chooseAutoName(cfg, tmp, inUse)
	if err != nil {
		t.Fatalf("chooseAutoName failed: %v", err)
	}
	if got != "bravo" {
		t.Fatalf("expected bravo, got %q", got)
	}
}

func TestChooseAutoNameStatefulWritesState(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	cfg := config{
		NameStrategy: strategyStateful,
		NameList:     []string{"alpha", "bravo", "charlie"},
	}

	first, err := chooseAutoName(cfg, tmp, map[string]struct{}{})
	if err != nil {
		t.Fatalf("first chooseAutoName failed: %v", err)
	}
	if first != "alpha" {
		t.Fatalf("expected alpha, got %q", first)
	}

	second, err := chooseAutoName(cfg, tmp, map[string]struct{}{})
	if err != nil {
		t.Fatalf("second chooseAutoName failed: %v", err)
	}
	if second != "bravo" {
		t.Fatalf("expected bravo, got %q", second)
	}
}

func TestLoadConfigGlobalAndLocalOverride(t *testing.T) {
	repoRoot := t.TempDir()
	xdg := t.TempDir()

	if err := os.MkdirAll(filepath.Join(xdg, "jjw"), 0o755); err != nil {
		t.Fatalf("mkdir global config dir: %v", err)
	}
	globalConfig := []byte("dev_root: /tmp/dev\nworktrees_root: /tmp/work\nname_strategy: first-unused\nname_list:\n  - alpha\n")
	if err := os.WriteFile(filepath.Join(xdg, "jjw", "config.yaml"), globalConfig, 0o644); err != nil {
		t.Fatalf("write global config: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(repoRoot, ".jjw"), 0o755); err != nil {
		t.Fatalf("mkdir local config dir: %v", err)
	}
	localConfig := []byte("worktrees_root: /tmp/local-work\nname_list:\n  - bravo\n  - charlie\n")
	if err := os.WriteFile(filepath.Join(repoRoot, ".jjw", "config.yaml"), localConfig, 0o644); err != nil {
		t.Fatalf("write local config: %v", err)
	}

	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("DEV_ROOT", "")

	cfg, err := loadConfig(repoRoot)
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}

	if cfg.WorktreesRoot != "/tmp/local-work" {
		t.Fatalf("expected local worktrees root override, got %q", cfg.WorktreesRoot)
	}
	if cfg.NameStrategy != strategyFirstUnused {
		t.Fatalf("expected first-unused strategy, got %q", cfg.NameStrategy)
	}
	if !reflect.DeepEqual(cfg.NameList, []string{"bravo", "charlie"}) {
		t.Fatalf("unexpected name_list: %#v", cfg.NameList)
	}
	if cfg.MainStack.DefaultWorkspace != "default" || cfg.MainStack.RebaseMode != "auto" || cfg.MainStack.StackShape != "auto" || cfg.MainStack.ConflictStrategy != "prefer-clean" {
		t.Fatalf("unexpected main_stack defaults: %#v", cfg.MainStack)
	}
}

func TestLoadConfigMainStackOverride(t *testing.T) {
	repoRoot := t.TempDir()
	xdg := t.TempDir()

	if err := os.MkdirAll(filepath.Join(xdg, "jjw"), 0o755); err != nil {
		t.Fatalf("mkdir global config dir: %v", err)
	}
	globalConfig := []byte("main_stack:\n  default_workspace: main\n  rebase_mode: revision\n")
	if err := os.WriteFile(filepath.Join(xdg, "jjw", "config.yaml"), globalConfig, 0o644); err != nil {
		t.Fatalf("write global config: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(repoRoot, ".jjw"), 0o755); err != nil {
		t.Fatalf("mkdir local config dir: %v", err)
	}
	localConfig := []byte("main_stack:\n  stack_shape: merge\n  conflict_strategy: prefer-clean\n")
	if err := os.WriteFile(filepath.Join(repoRoot, ".jjw", "config.yaml"), localConfig, 0o644); err != nil {
		t.Fatalf("write local config: %v", err)
	}

	t.Setenv("XDG_CONFIG_HOME", xdg)

	cfg, err := loadConfig(repoRoot)
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	if cfg.MainStack.DefaultWorkspace != "main" {
		t.Fatalf("expected main_stack.default_workspace override, got %q", cfg.MainStack.DefaultWorkspace)
	}
	if cfg.MainStack.RebaseMode != "revision" {
		t.Fatalf("expected main_stack.rebase_mode override, got %q", cfg.MainStack.RebaseMode)
	}
	if cfg.MainStack.StackShape != "merge" {
		t.Fatalf("expected main_stack.stack_shape override, got %q", cfg.MainStack.StackShape)
	}
	if cfg.MainStack.ConflictStrategy != "prefer-clean" {
		t.Fatalf("expected main_stack.conflict_strategy override, got %q", cfg.MainStack.ConflictStrategy)
	}
}

func TestLoadConfigRejectsInvalidMainStackDefaults(t *testing.T) {
	repoRoot := t.TempDir()
	xdg := t.TempDir()

	if err := os.MkdirAll(filepath.Join(xdg, "jjw"), 0o755); err != nil {
		t.Fatalf("mkdir global config dir: %v", err)
	}
	badConfig := []byte("main_stack:\n  rebase_mode: wat\n")
	if err := os.WriteFile(filepath.Join(xdg, "jjw", "config.yaml"), badConfig, 0o644); err != nil {
		t.Fatalf("write global config: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)

	_, err := loadConfig(repoRoot)
	if err == nil || !strings.Contains(err.Error(), "invalid rebase_mode") {
		t.Fatalf("expected invalid rebase_mode error, got %v", err)
	}
}

func TestLoadConfigUsesDefaultNATONameList(t *testing.T) {
	repoRoot := t.TempDir()
	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "jjw"), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)

	cfg, err := loadConfig(repoRoot)
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	if len(cfg.NameList) != 26 {
		t.Fatalf("expected 26 default names, got %d", len(cfg.NameList))
	}
	if cfg.NameList[0] != "alpha" || cfg.NameList[len(cfg.NameList)-1] != "zulu" {
		t.Fatalf("unexpected default NATO name list: %#v", cfg.NameList)
	}
}

func TestRunCreateRequiresConfiguredWorktreesRoot(t *testing.T) {
	repoRoot := t.TempDir()
	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "jjw"), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)

	err := runCreate([]string{"--repo", repoRoot, "alpha"})
	if err == nil || !strings.Contains(err.Error(), "worktrees_root is required") {
		t.Fatalf("expected worktrees_root required error, got %v", err)
	}
}

func TestRunCreateWithFakeJJ(t *testing.T) {
	repoRoot := t.TempDir()
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	xdg := t.TempDir()
	bin := filepath.Join(t.TempDir(), "bin")

	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	jjScript := `#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == "-R" ]]; then
  shift 2
fi
if [[ "$1" == "workspace" && "$2" == "list" ]]; then
  printf 'default\n'
  exit 0
fi
if [[ "$1" == "workspace" && "$2" == "add" ]]; then
  target="${@: -1}"
  mkdir -p "$target"
  exit 0
fi
echo "unexpected args: $*" >&2
exit 2
`
	if err := os.WriteFile(filepath.Join(bin, "jj"), []byte(jjScript), 0o755); err != nil {
		t.Fatalf("write fake jj: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(xdg, "jjw"), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	cfg := []byte("name_strategy: first-unused\nname_list:\n  - alpha\n")
	if err := os.WriteFile(filepath.Join(xdg, "jjw", "config.yaml"), cfg, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("DEV_ROOT", filepath.Dir(worktreesRoot))

	stdout := stdoutWriter
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	stdoutWriter = w

	runErr := runCreate([]string{"--repo", repoRoot, "--app", "appx", "--worktrees-root", worktreesRoot, "feature-1", "--no-direnv-allow"})

	_ = w.Close()
	stdoutWriter = stdout

	if runErr != nil {
		t.Fatalf("runCreate failed: %v", runErr)
	}

	var out bytes.Buffer
	if _, err := out.ReadFrom(r); err != nil {
		t.Fatalf("read stdout: %v", err)
	}

	createdPath := strings.TrimSpace(out.String())
	if createdPath == "" {
		t.Fatalf("expected runCreate to print created path")
	}
	if _, err := os.Stat(filepath.Join(worktreesRoot, "appx", "feature-1")); err != nil {
		t.Fatalf("expected workspace directory to exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(createdPath, ".envrc")); err != nil {
		t.Fatalf("expected .envrc to be created: %v", err)
	}
}

func TestChooseAutoNameExhaustedFirstUnused(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	cfg := config{NameStrategy: strategyFirstUnused, NameList: []string{"alpha"}}
	_, err := chooseAutoName(cfg, tmp, map[string]struct{}{"alpha": {}})
	if err == nil {
		t.Fatalf("expected exhausted name list error")
	}
}

func TestChooseAutoNameEmptyList(t *testing.T) {
	t.Parallel()

	_, err := chooseAutoName(config{NameStrategy: strategyFirstUnused}, t.TempDir(), map[string]struct{}{})
	if err == nil {
		t.Fatalf("expected error for empty name list")
	}
}

func TestLoadStateInvalidJSON(t *testing.T) {
	t.Parallel()

	repoRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoRoot, ".jjw"), 0o755); err != nil {
		t.Fatalf("mkdir .jjw: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".jjw", "state.json"), []byte("{"), 0o644); err != nil {
		t.Fatalf("write state: %v", err)
	}

	_, err := loadState(repoRoot)
	if err == nil {
		t.Fatalf("expected invalid json error")
	}
}

func TestLoadStateClampsNegativeIndex(t *testing.T) {
	t.Parallel()

	repoRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoRoot, ".jjw"), 0o755); err != nil {
		t.Fatalf("mkdir .jjw: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".jjw", "state.json"), []byte("{\"next_index\":-7}\n"), 0o644); err != nil {
		t.Fatalf("write state: %v", err)
	}

	st, err := loadState(repoRoot)
	if err != nil {
		t.Fatalf("loadState failed: %v", err)
	}
	if st.NextIndex != 0 {
		t.Fatalf("expected clamped index 0, got %d", st.NextIndex)
	}
}

func TestSaveLoadStateRoundTrip(t *testing.T) {
	t.Parallel()

	repoRoot := t.TempDir()
	if err := saveState(repoRoot, state{NextIndex: 3}); err != nil {
		t.Fatalf("saveState failed: %v", err)
	}

	st, err := loadState(repoRoot)
	if err != nil {
		t.Fatalf("loadState failed: %v", err)
	}
	if st.NextIndex != 3 {
		t.Fatalf("expected next_index 3, got %d", st.NextIndex)
	}
}

func TestEnsureEnvrcIdempotent(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	if err := ensureEnvrc(workspace); err != nil {
		t.Fatalf("ensureEnvrc failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".envrc"), []byte("custom\n"), 0o644); err != nil {
		t.Fatalf("write custom envrc: %v", err)
	}
	if err := ensureEnvrc(workspace); err != nil {
		t.Fatalf("ensureEnvrc second call failed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(workspace, ".envrc"))
	if err != nil {
		t.Fatalf("read envrc: %v", err)
	}
	if string(data) != "custom\n" {
		t.Fatalf("expected existing .envrc preserved, got %q", string(data))
	}
}

func TestSplitNonEmptyLines(t *testing.T) {
	t.Parallel()

	got := splitNonEmptyLines("a\n\n b \n")
	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected split result: %#v", got)
	}
}

func TestIsLiteralEmptyDir(t *testing.T) {
	t.Parallel()

	emptyDir := t.TempDir()
	empty, err := isLiteralEmptyDir(emptyDir)
	if err != nil {
		t.Fatalf("isLiteralEmptyDir failed: %v", err)
	}
	if !empty {
		t.Fatalf("expected empty dir to be empty")
	}

	if err := os.WriteFile(filepath.Join(emptyDir, "file"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	empty, err = isLiteralEmptyDir(emptyDir)
	if err != nil {
		t.Fatalf("isLiteralEmptyDir failed: %v", err)
	}
	if empty {
		t.Fatalf("expected non-empty dir to be non-empty")
	}
}

func TestDeriveApp(t *testing.T) {
	t.Parallel()

	if got := deriveApp("/tmp/repo-x", ""); got != "repo-x" {
		t.Fatalf("unexpected derived app: %q", got)
	}
	if got := deriveApp("/tmp/repo-x", "override"); got != "override" {
		t.Fatalf("unexpected overridden app: %q", got)
	}
}

func TestDeriveAppFromLinkedWorkspaceUsesDefaultRootName(t *testing.T) {
	tmp := t.TempDir()
	defaultRoot := filepath.Join(tmp, "agent-tick")
	linkedRoot := filepath.Join(tmp, "worktrees", "agent-tick", "bravo")
	if err := os.MkdirAll(filepath.Join(defaultRoot, ".jj"), 0o755); err != nil {
		t.Fatalf("mkdir default .jj: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(linkedRoot, ".jj"), 0o755); err != nil {
		t.Fatalf("mkdir linked .jj: %v", err)
	}
	if err := os.WriteFile(filepath.Join(linkedRoot, ".jj", "repo"), []byte(filepath.Join(defaultRoot, ".jj", "repo")+"\n"), 0o644); err != nil {
		t.Fatalf("write linked .jj/repo: %v", err)
	}

	if got := deriveApp(linkedRoot, ""); got != "agent-tick" {
		t.Fatalf("expected app from default root, got %q", got)
	}
}

func TestResolveRepoRootOverride(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	got, err := resolveRepoRoot(tmp)
	if err != nil {
		t.Fatalf("resolveRepoRoot failed: %v", err)
	}
	if got != tmp {
		t.Fatalf("expected %q got %q", tmp, got)
	}
}

func TestResolveRepoRootFallsBackToGit(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	if err := writeExecutable(filepath.Join(bin, "jj"), "#!/usr/bin/env bash\nexit 1\n"); err != nil {
		t.Fatalf("write fake jj: %v", err)
	}
	if err := writeExecutable(filepath.Join(bin, "git"), "#!/usr/bin/env bash\necho /tmp/git-root\n"); err != nil {
		t.Fatalf("write fake git: %v", err)
	}

	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	got, err := resolveRepoRoot("")
	if err != nil {
		t.Fatalf("resolveRepoRoot failed: %v", err)
	}
	if got != "/tmp/git-root" {
		t.Fatalf("expected git fallback root, got %q", got)
	}
}

func TestListWorkspaceNamesSortAndDedup(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	jj := `#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == "-R" ]]; then
  shift 2
fi
if [[ "$1" == "workspace" && "$2" == "list" ]]; then
  printf 'bravo\nalpha\nalpha\n'
  exit 0
fi
exit 2
`
	if err := writeExecutable(filepath.Join(bin, "jj"), jj); err != nil {
		t.Fatalf("write fake jj: %v", err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	names, err := listWorkspaceNames("/tmp/repo")
	if err != nil {
		t.Fatalf("listWorkspaceNames failed: %v", err)
	}
	want := []string{"alpha", "bravo"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("unexpected names\nwant: %#v\ngot:  %#v", want, names)
	}
}

func TestRunListWithFakeJJ(t *testing.T) {
	repoRoot := t.TempDir()
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	xdg := t.TempDir()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	jj := `#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == "-R" ]]; then
  shift 2
fi
if [[ "$1" == "workspace" && "$2" == "list" ]]; then
  printf 'default\nmeta\n'
  exit 0
fi
exit 2
`
	if err := writeExecutable(filepath.Join(bin, "jj"), jj); err != nil {
		t.Fatalf("write fake jj: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(xdg, "jjw"), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "jjw", "config.yaml"), []byte("worktrees_root: "+worktreesRoot+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("XDG_CONFIG_HOME", xdg)

	output, err := captureStdout(func() error {
		return runList([]string{"--repo", repoRoot, "--app", "nixfiles"})
	})
	if err != nil {
		t.Fatalf("runList failed: %v", err)
	}

	lines := splitNonEmptyLines(output)
	want := []string{
		filepath.Join(worktreesRoot, "nixfiles", "default"),
		filepath.Join(worktreesRoot, "nixfiles", "meta"),
	}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("unexpected list output\nwant: %#v\ngot:  %#v", want, lines)
	}
}

func TestRunListOmitsCurrentWhenOnlyCurrentExists(t *testing.T) {
	repoRoot := t.TempDir()
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	xdg := t.TempDir()

	if err := os.MkdirAll(filepath.Join(xdg, "jjw"), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "jjw", "config.yaml"), []byte("worktrees_root: "+worktreesRoot+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	origCapture := commandCaptureFn
	defer func() { commandCaptureFn = origCapture }()
	commandCaptureFn = func(name string, args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "-R" && args[2] == "workspace" {
			return "default\tabc111\t" + repoRoot + "\n", nil
		}
		return "", nil
	}

	t.Setenv("XDG_CONFIG_HOME", xdg)

	output, err := captureStdout(func() error {
		return runList([]string{"--repo", repoRoot, "--app", "appx"})
	})
	if err != nil {
		t.Fatalf("runList failed: %v", err)
	}
	if strings.TrimSpace(output) != "" {
		t.Fatalf("expected no listed workspaces, got %q", strings.TrimSpace(output))
	}
}

func TestRunListSkipsCurrentWorkspaceByDefault(t *testing.T) {
	repoRoot := t.TempDir()
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	xdg := t.TempDir()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	jj := `#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == "-R" ]]; then
  shift 2
fi
if [[ "$1" == "workspace" && "$2" == "list" ]]; then
  printf 'default\nmeta\n'
  exit 0
fi
if [[ "$1" == "log" ]]; then
  printf 'abc111\n'
  exit 0
fi
exit 2
`
	if err := writeExecutable(filepath.Join(bin, "jj"), jj); err != nil {
		t.Fatalf("write fake jj: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(xdg, "jjw"), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "jjw", "config.yaml"), []byte("worktrees_root: "+worktreesRoot+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	origCapture := commandCaptureFn
	defer func() { commandCaptureFn = origCapture }()
	commandCaptureFn = func(name string, args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "-R" && args[2] == "workspace" {
			return "default\tabc111\nmeta\tdef222\n", nil
		}
		if len(args) >= 4 && args[2] == "log" {
			return "abc111\n", nil
		}
		return "", nil
	}

	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("XDG_CONFIG_HOME", xdg)

	output, err := captureStdout(func() error {
		return runList([]string{"--repo", repoRoot, "--app", "nixfiles"})
	})
	if err != nil {
		t.Fatalf("runList failed: %v", err)
	}

	lines := splitNonEmptyLines(output)
	want := []string{filepath.Join(worktreesRoot, "nixfiles", "meta")}
	if !reflect.DeepEqual(lines, want) {
		t.Fatalf("unexpected list output\nwant: %#v\ngot:  %#v", want, lines)
	}
}

func TestRunSelectInteractivePrompt(t *testing.T) {
	repoRoot := t.TempDir()
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	app := "myapp"
	if err := os.MkdirAll(filepath.Join(worktreesRoot, app, "one"), 0o755); err != nil {
		t.Fatalf("mkdir workspace one: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(worktreesRoot, app, "two"), 0o755); err != nil {
		t.Fatalf("mkdir workspace two: %v", err)
	}

	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "jjw"), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "jjw", "config.yaml"), []byte("worktrees_root: "+worktreesRoot+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	jj := `#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == "-R" ]]; then
  shift 2
fi
if [[ "$1" == "workspace" && "$2" == "list" ]]; then
  printf 'one\ntwo\n'
  exit 0
fi
exit 2
`
	if err := writeExecutable(filepath.Join(bin, "jj"), jj); err != nil {
		t.Fatalf("write fake jj: %v", err)
	}

	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("XDG_CONFIG_HOME", xdg)
	origStdin := stdinReader
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	stdinReader = r
	defer func() {
		stdinReader = origStdin
	}()
	if _, err := w.Write([]byte("1\n")); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	_ = w.Close()

	output, err := captureStdout(func() error {
		return runSelect([]string{"--repo", repoRoot, "--app", app})
	})
	if err != nil {
		t.Fatalf("runSelect failed: %v", err)
	}

	selected := strings.TrimSpace(output)
	want := filepath.Join(worktreesRoot, app, "one")
	if selected != want {
		t.Fatalf("expected %q, got %q", want, selected)
	}
}

func TestRunTidyDeletesDefunctEmptySelection(t *testing.T) {
	repoRoot := t.TempDir()
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	app := "nixfiles"
	appRoot := filepath.Join(worktreesRoot, app)
	active := filepath.Join(appRoot, "default")
	defunctA := filepath.Join(appRoot, "defunct-a")
	defunctB := filepath.Join(appRoot, "defunct-b")
	defunctNonEmpty := filepath.Join(appRoot, "defunct-nonempty")

	for _, dir := range []string{active, defunctA, defunctB, defunctNonEmpty} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(defunctNonEmpty, "file"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write non-empty marker: %v", err)
	}

	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "jjw"), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "jjw", "config.yaml"), []byte("worktrees_root: "+worktreesRoot+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	jj := `#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == "-R" ]]; then
  shift 2
fi
if [[ "$1" == "workspace" && "$2" == "list" ]]; then
  printf 'default\n'
  exit 0
fi
exit 2
`
	fzf := "#!/usr/bin/env bash\nset -euo pipefail\nIFS= read -r line || true\nprintf '%s\\n' \"$line\"\n"
	if err := writeExecutable(filepath.Join(bin, "jj"), jj); err != nil {
		t.Fatalf("write fake jj: %v", err)
	}
	if err := writeExecutable(filepath.Join(bin, "fzf"), fzf); err != nil {
		t.Fatalf("write fake fzf: %v", err)
	}

	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("XDG_CONFIG_HOME", xdg)

	output, err := captureStdout(func() error {
		return runTidy([]string{"--repo", repoRoot, "--app", app, "--yes"})
	})
	if err != nil {
		t.Fatalf("runTidy failed: %v", err)
	}

	deleted := splitNonEmptyLines(output)
	wantDeleted := []string{defunctA, defunctB}
	if !reflect.DeepEqual(deleted, wantDeleted) {
		t.Fatalf("unexpected deleted paths\nwant: %#v\ngot:  %#v", wantDeleted, deleted)
	}
	if _, err := os.Stat(defunctA); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be removed", defunctA)
	}
	if _, err := os.Stat(defunctB); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be removed", defunctB)
	}
	if _, err := os.Stat(defunctNonEmpty); err != nil {
		t.Fatalf("expected non-empty %s to remain: %v", defunctNonEmpty, err)
	}
}

func TestRunTidyNoCandidates(t *testing.T) {
	repoRoot := t.TempDir()
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	app := "nixfiles"
	if err := os.MkdirAll(filepath.Join(worktreesRoot, app, "default"), 0o755); err != nil {
		t.Fatalf("mkdir active workspace: %v", err)
	}

	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "jjw"), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "jjw", "config.yaml"), []byte("worktrees_root: "+worktreesRoot+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	jj := "#!/usr/bin/env bash\nset -euo pipefail\nif [[ \"$1\" == \"-R\" ]]; then shift 2; fi\nif [[ \"$1\" == \"workspace\" && \"$2\" == \"list\" ]]; then printf 'default\\n'; exit 0; fi\nexit 2\n"
	if err := writeExecutable(filepath.Join(bin, "jj"), jj); err != nil {
		t.Fatalf("write fake jj: %v", err)
	}

	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("XDG_CONFIG_HOME", xdg)

	output, err := captureStdout(func() error {
		return runTidy([]string{"--repo", repoRoot, "--app", app, "--yes"})
	})
	if err != nil {
		t.Fatalf("runTidy failed: %v", err)
	}
	if strings.TrimSpace(output) != "" {
		t.Fatalf("expected no output for no candidates, got %q", output)
	}
}

func TestRunTidyOffersActiveNonDefaultForDeletion(t *testing.T) {
	repoRoot := t.TempDir()
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	app := "nixfiles"
	alphaPath := filepath.Join(worktreesRoot, app, "alpha")
	betaPath := filepath.Join(worktreesRoot, app, "beta")
	for _, p := range []string{alphaPath, betaPath} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		if err := os.WriteFile(filepath.Join(p, "file"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write marker file: %v", err)
		}
	}

	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "jjw"), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "jjw", "config.yaml"), []byte("worktrees_root: "+worktreesRoot+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)

	origCapture := commandCaptureFn
	origRun := commandToStderrFn
	origStdin := stdinReader
	defer func() {
		commandCaptureFn = origCapture
		commandToStderrFn = origRun
		stdinReader = origStdin
	}()

	commandCaptureFn = func(name string, args ...string) (string, error) {
		if len(args) >= 3 && args[2] == "workspace" {
			return "default\tabc111\nalpha\tdef222\nbeta\tghi333\n", nil
		}
		if len(args) >= 3 && args[2] == "log" {
			if len(args) >= 5 && strings.Contains(args[4], "empty()") {
				return "eempty1\n", nil
			}
			return "abc111\n", nil
		}
		return "", nil
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	stdinReader = r
	if _, err := w.Write([]byte("1\n")); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	_ = w.Close()

	var forgetCalls []string
	var sawAbandon bool
	commandToStderrFn = func(name string, args ...string) error {
		if len(args) >= 5 && args[2] == "workspace" && args[3] == "forget" {
			forgetCalls = append(forgetCalls, args[4])
		}
		if len(args) >= 4 && args[2] == "abandon" {
			sawAbandon = true
		}
		return nil
	}

	output, err := captureStdout(func() error {
		return runTidy([]string{"--repo", repoRoot, "--app", app, "--worktrees-root", worktreesRoot, "--yes"})
	})
	if err != nil {
		t.Fatalf("runTidy failed: %v", err)
	}

	if got := strings.TrimSpace(output); got != alphaPath {
		t.Fatalf("expected deleted output %q, got %q", alphaPath, got)
	}
	if !reflect.DeepEqual(forgetCalls, []string{"alpha"}) {
		t.Fatalf("unexpected forget calls: %#v", forgetCalls)
	}
	if !sawAbandon {
		t.Fatalf("expected tidy to abandon top empty mutable commits")
	}
	if _, err := os.Stat(alphaPath); !os.IsNotExist(err) {
		t.Fatalf("expected %s removed", alphaPath)
	}
	if _, err := os.Stat(betaPath); err != nil {
		t.Fatalf("expected %s to remain: %v", betaPath, err)
	}
}

func TestRunTidyFromLinkedWorkspaceUsesDefaultAppForActiveWorkspaces(t *testing.T) {
	tmp := t.TempDir()
	defaultRoot := filepath.Join(tmp, "agent-tick")
	worktreesRoot := filepath.Join(tmp, "worktrees")
	app := "agent-tick"
	bravoPath := filepath.Join(worktreesRoot, app, "bravo")
	alphaPath := filepath.Join(worktreesRoot, app, "alpha")
	for _, dir := range []string{filepath.Join(defaultRoot, ".jj"), filepath.Join(bravoPath, ".jj"), alphaPath} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(alphaPath, "file"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write alpha marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bravoPath, ".jj", "repo"), []byte(filepath.Join(defaultRoot, ".jj", "repo")+"\n"), 0o644); err != nil {
		t.Fatalf("write linked .jj/repo: %v", err)
	}

	origCapture := commandCaptureFn
	origRun := commandToStderrFn
	origStdin := stdinReader
	defer func() {
		commandCaptureFn = origCapture
		commandToStderrFn = origRun
		stdinReader = origStdin
	}()

	commandCaptureFn = func(name string, args ...string) (string, error) {
		if len(args) >= 3 && args[2] == "workspace" {
			return "default\tbase111\nalpha\talpha222\nbravo\tbravo333\n", nil
		}
		if len(args) >= 5 && args[2] == "log" && args[3] == "-r" {
			if strings.Contains(args[4], "empty()") {
				return "\n", nil
			}
			return "bravo333\n", nil
		}
		return "", nil
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	stdinReader = r
	if _, err := w.Write([]byte("1\n")); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	_ = w.Close()

	var forgetCalls []string
	commandToStderrFn = func(name string, args ...string) error {
		if len(args) >= 5 && args[2] == "workspace" && args[3] == "forget" {
			forgetCalls = append(forgetCalls, args[4])
		}
		return nil
	}

	output, err := captureStdout(func() error {
		return runTidy([]string{"--repo", bravoPath, "--worktrees-root", worktreesRoot, "--yes"})
	})
	if err != nil {
		t.Fatalf("runTidy failed: %v", err)
	}
	if got := strings.TrimSpace(output); got != alphaPath {
		t.Fatalf("expected deleted output %q, got %q", alphaPath, got)
	}
	if !reflect.DeepEqual(forgetCalls, []string{"alpha"}) {
		t.Fatalf("unexpected forget calls: %#v", forgetCalls)
	}
	if _, err := os.Stat(alphaPath); !os.IsNotExist(err) {
		t.Fatalf("expected %s removed", alphaPath)
	}
}

func TestRunTidySkipsAbandonWhenNoTopEmptyCommits(t *testing.T) {
	repoRoot := t.TempDir()
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	app := "nixfiles"
	alphaPath := filepath.Join(worktreesRoot, app, "alpha")
	betaPath := filepath.Join(worktreesRoot, app, "beta")
	for _, p := range []string{alphaPath, betaPath} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		if err := os.WriteFile(filepath.Join(p, "file"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write marker file: %v", err)
		}
	}

	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "jjw"), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "jjw", "config.yaml"), []byte("worktrees_root: "+worktreesRoot+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)

	origCapture := commandCaptureFn
	origRun := commandToStderrFn
	origStdin := stdinReader
	defer func() {
		commandCaptureFn = origCapture
		commandToStderrFn = origRun
		stdinReader = origStdin
	}()

	commandCaptureFn = func(name string, args ...string) (string, error) {
		if len(args) >= 3 && args[2] == "workspace" {
			return "default\tabc111\nalpha\tdef222\nbeta\tghi333\n", nil
		}
		if len(args) >= 3 && args[2] == "log" {
			if len(args) >= 5 && strings.Contains(args[4], "empty()") {
				return "\n", nil
			}
			return "abc111\n", nil
		}
		return "", nil
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	stdinReader = r
	if _, err := w.Write([]byte("1\n")); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	_ = w.Close()

	var sawAbandon bool
	commandToStderrFn = func(name string, args ...string) error {
		if len(args) >= 4 && args[2] == "abandon" {
			sawAbandon = true
		}
		return nil
	}

	if err := runTidy([]string{"--repo", repoRoot, "--app", app, "--worktrees-root", worktreesRoot, "--yes"}); err != nil {
		t.Fatalf("runTidy failed: %v", err)
	}
	if sawAbandon {
		t.Fatalf("did not expect abandon call when there are no top empty commits")
	}
}

func TestRunCreateErrorsOnExistingPath(t *testing.T) {
	repoRoot := t.TempDir()
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	if err := os.MkdirAll(filepath.Join(worktreesRoot, "appx", "feature-1"), 0o755); err != nil {
		t.Fatalf("mkdir existing path: %v", err)
	}

	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	jj := "#!/usr/bin/env bash\nset -euo pipefail\nif [[ \"$1\" == \"-R\" ]]; then shift 2; fi\nif [[ \"$1\" == \"workspace\" && \"$2\" == \"list\" ]]; then printf 'default\\n'; exit 0; fi\nexit 2\n"
	if err := writeExecutable(filepath.Join(bin, "jj"), jj); err != nil {
		t.Fatalf("write fake jj: %v", err)
	}

	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "jjw"), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "jjw", "config.yaml"), []byte("name_list:\n  - alpha\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("XDG_CONFIG_HOME", xdg)

	err := runCreate([]string{"--repo", repoRoot, "--app", "appx", "--worktrees-root", worktreesRoot, "feature-1", "--no-direnv-allow"})
	if err == nil {
		t.Fatalf("expected existing path error")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected already exists error, got %v", err)
	}
}

func TestRunCdDelegatesToSelect(t *testing.T) {
	repoRoot := t.TempDir()
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	app := "myapp"
	if err := os.MkdirAll(filepath.Join(worktreesRoot, app, "one"), 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}

	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "jjw"), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "jjw", "config.yaml"), []byte("worktrees_root: "+worktreesRoot+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	jj := "#!/usr/bin/env bash\nset -euo pipefail\nif [[ \"$1\" == \"-R\" ]]; then shift 2; fi\nif [[ \"$1\" == \"workspace\" && \"$2\" == \"list\" ]]; then printf 'one\\n'; exit 0; fi\nexit 2\n"
	if err := writeExecutable(filepath.Join(bin, "jj"), jj); err != nil {
		t.Fatalf("write fake jj: %v", err)
	}

	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("XDG_CONFIG_HOME", xdg)

	out, err := captureStdout(func() error {
		return runCd([]string{"--repo", repoRoot, "--app", app})
	})
	if err != nil {
		t.Fatalf("runCd failed: %v", err)
	}
	if strings.TrimSpace(out) != filepath.Join(worktreesRoot, app, "one") {
		t.Fatalf("unexpected runCd output: %q", strings.TrimSpace(out))
	}
}

func TestRunCdWithoutExistingWorkspaceDoesNotCreate(t *testing.T) {
	repoRoot := t.TempDir()
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	app := "myapp"
	xdg := t.TempDir()

	if err := os.MkdirAll(filepath.Join(xdg, "jjw"), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	cfg := []byte("worktrees_root: " + worktreesRoot + "\nname_strategy: first-unused\nname_list:\n  - alpha\n")
	if err := os.WriteFile(filepath.Join(xdg, "jjw", "config.yaml"), cfg, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	origCapture := commandCaptureFn
	defer func() { commandCaptureFn = origCapture }()
	commandCaptureFn = func(name string, args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "-R" && args[2] == "workspace" {
			return "default\tabc111\t" + repoRoot + "\n", nil
		}
		return "", nil
	}

	t.Setenv("XDG_CONFIG_HOME", xdg)

	_, err := captureStdout(func() error {
		return runCd([]string{"--repo", repoRoot, "--app", app})
	})
	if err == nil || !strings.Contains(err.Error(), "no workspace directories found") {
		t.Fatalf("expected no workspace directories error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(worktreesRoot, app, "alpha")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected alpha not to be created, stat err: %v", err)
	}
}

func TestRunCdWithExistingNamePrintsWorkspacePath(t *testing.T) {
	repoRoot := t.TempDir()
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	app := "myapp"
	alphaPath := filepath.Join(worktreesRoot, app, "alpha")
	if err := os.MkdirAll(alphaPath, 0o755); err != nil {
		t.Fatalf("mkdir alpha workspace: %v", err)
	}

	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "jjw"), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "jjw", "config.yaml"), []byte("worktrees_root: "+worktreesRoot+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	origCapture := commandCaptureFn
	origRun := commandToStderrFn
	defer func() {
		commandCaptureFn = origCapture
		commandToStderrFn = origRun
	}()
	commandCaptureFn = func(name string, args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "-R" && args[2] == "workspace" {
			return "default\tabc111\t" + repoRoot + "\nalpha\tdef222\t" + alphaPath + "\n", nil
		}
		return "", nil
	}
	commandToStderrFn = func(name string, args ...string) error {
		return errors.New("unexpected create")
	}

	t.Setenv("XDG_CONFIG_HOME", xdg)

	out, err := captureStdout(func() error {
		return runCd([]string{"--repo", repoRoot, "--app", app, "alpha"})
	})
	if err != nil {
		t.Fatalf("runCd failed: %v", err)
	}
	if strings.TrimSpace(out) != alphaPath {
		t.Fatalf("expected existing path %q, got %q", alphaPath, strings.TrimSpace(out))
	}
}

func TestRunCdWithMissingNameDoesNotCreateWorkspace(t *testing.T) {
	repoRoot := t.TempDir()
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	app := "myapp"
	xdg := t.TempDir()

	if err := os.MkdirAll(filepath.Join(xdg, "jjw"), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "jjw", "config.yaml"), []byte("worktrees_root: "+worktreesRoot+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	origCapture := commandCaptureFn
	origRun := commandToStderrFn
	defer func() {
		commandCaptureFn = origCapture
		commandToStderrFn = origRun
	}()
	commandCaptureFn = func(name string, args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "-R" && args[2] == "workspace" {
			return "default\tabc111\t" + repoRoot + "\n", nil
		}
		return "", nil
	}
	commandToStderrFn = func(name string, args ...string) error {
		return errors.New("unexpected create")
	}

	t.Setenv("XDG_CONFIG_HOME", xdg)

	_, err := captureStdout(func() error {
		return runCd([]string{"--repo", repoRoot, "--app", app, "alpha"})
	})
	if err == nil || !strings.Contains(err.Error(), "workspace \"alpha\" not found") {
		t.Fatalf("expected missing workspace error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(worktreesRoot, app, "alpha")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected alpha not to be created, stat err: %v", err)
	}
}

func TestRunCreateWithTrailingNameCreatesWorkspacePath(t *testing.T) {
	repoRoot := t.TempDir()
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	app := "nixfiles"
	xdg := t.TempDir()
	bin := filepath.Join(t.TempDir(), "bin")

	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	jj := `#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == "-R" ]]; then
  shift 2
fi
if [[ "$1" == "workspace" && "$2" == "list" ]]; then
  printf 'default\n'
  exit 0
fi
if [[ "$1" == "workspace" && "$2" == "add" ]]; then
  target="${@: -1}"
  mkdir -p "$target"
  exit 0
fi
exit 2
`
	if err := writeExecutable(filepath.Join(bin, "jj"), jj); err != nil {
		t.Fatalf("write fake jj: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(xdg, "jjw"), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "jjw", "config.yaml"), []byte("name_list:\n  - alpha\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("XDG_CONFIG_HOME", xdg)

	out, err := captureStdout(func() error {
		return runCreate([]string{"--repo", repoRoot, "--app", app, "--worktrees-root", worktreesRoot, "feature-2", "--no-direnv-allow"})
	})
	if err != nil {
		t.Fatalf("runCreate failed: %v", err)
	}

	created := strings.TrimSpace(out)
	want := filepath.Join(worktreesRoot, app, "feature-2")
	if created != want {
		t.Fatalf("expected created path %q, got %q", want, created)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected workspace directory to exist: %v", err)
	}
}

func TestRunCreateWithNameFlagCreatesWorkspacePath(t *testing.T) {
	repoRoot := t.TempDir()
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	app := "nixfiles"
	xdg := t.TempDir()
	bin := filepath.Join(t.TempDir(), "bin")

	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}

	jj := `#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == "-R" ]]; then
  shift 2
fi
if [[ "$1" == "workspace" && "$2" == "list" ]]; then
  printf 'default\n'
  exit 0
fi
if [[ "$1" == "workspace" && "$2" == "add" ]]; then
  target="${@: -1}"
  mkdir -p "$target"
  exit 0
fi
exit 2
`
	if err := writeExecutable(filepath.Join(bin, "jj"), jj); err != nil {
		t.Fatalf("write fake jj: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(xdg, "jjw"), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "jjw", "config.yaml"), []byte("name_list:\n  - alpha\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("XDG_CONFIG_HOME", xdg)

	out, err := captureStdout(func() error {
		return runCreate([]string{"--repo", repoRoot, "--app", app, "--worktrees-root", worktreesRoot, "--name", "feature-3", "--no-direnv-allow"})
	})
	if err != nil {
		t.Fatalf("runCreate failed: %v", err)
	}

	created := strings.TrimSpace(out)
	want := filepath.Join(worktreesRoot, app, "feature-3")
	if created != want {
		t.Fatalf("expected created path %q, got %q", want, created)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected workspace directory to exist: %v", err)
	}
}

func writeExecutable(path string, content string) error {
	return os.WriteFile(path, []byte(content), 0o755)
}

func captureStdout(run func() error) (string, error) {
	orig := stdoutWriter
	r, w, err := os.Pipe()
	if err != nil {
		return "", err
	}
	stdoutWriter = w

	runErr := run()

	_ = w.Close()
	stdoutWriter = orig

	var out bytes.Buffer
	if _, err := out.ReadFrom(r); err != nil {
		return "", err
	}
	return out.String(), runErr
}

func TestFakeToolsOnPathSanity(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	if err := writeExecutable(filepath.Join(bin, "jj"), "#!/usr/bin/env bash\necho ok\n"); err != nil {
		t.Fatalf("write fake jj: %v", err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	p, err := exec.LookPath("jj")
	if err != nil {
		t.Fatalf("lookpath failed: %v", err)
	}
	if !strings.HasPrefix(p, bin) {
		t.Fatalf("expected fake tool on PATH, got %s", p)
	}
}

func TestLoadConfigInvalidStrategy(t *testing.T) {
	repoRoot := t.TempDir()
	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "jjw"), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	bad := []byte("name_strategy: nope\n")
	if err := os.WriteFile(filepath.Join(xdg, "jjw", "config.yaml"), bad, 0o644); err != nil {
		t.Fatalf("write bad config: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)

	_, err := loadConfig(repoRoot)
	if err == nil {
		t.Fatalf("expected invalid strategy error")
	}
}

func TestMergeConfigFileInvalidYAML(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("name_list: [\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var cfg config
	err := mergeConfigFile(&cfg, path)
	if err == nil {
		t.Fatalf("expected yaml parse error")
	}
}

func TestGlobalConfigPathXDG(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	got, ok := globalConfigPath()
	if !ok {
		t.Fatalf("expected config path")
	}
	want := filepath.Join(xdg, "jjw", "config.yaml")
	if got != want {
		t.Fatalf("expected %q got %q", want, got)
	}
}

func TestSelectOneSingleItem(t *testing.T) {
	t.Parallel()

	got, err := selectOne([]string{"/tmp/only"})
	if err != nil {
		t.Fatalf("selectOne failed: %v", err)
	}
	if got != "/tmp/only" {
		t.Fatalf("expected only item, got %q", got)
	}
}

func TestSelectManyWithoutFzfPrompt(t *testing.T) {
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", "")

	origStdin := stdinReader
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	stdinReader = r
	defer func() {
		stdinReader = origStdin
		t.Setenv("PATH", origPath)
	}()

	if _, err := w.Write([]byte("1,2\n")); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	_ = w.Close()

	got, err := selectMany([]string{"/tmp/a", "/tmp/b"})
	if err != nil {
		t.Fatalf("selectMany failed: %v", err)
	}
	want := []string{"/tmp/a", "/tmp/b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected selections\nwant: %#v\ngot:  %#v", want, got)
	}
}

func TestBuildMultiSelectLinesWrapsWithHangingIndent(t *testing.T) {
	t.Parallel()

	selected := map[int]bool{0: true}
	lines := buildMultiSelectLines([]string{"abcdefghijklmnop"}, 0, selected, 20)

	if len(lines) < 2 {
		t.Fatalf("unexpected line count: got %d\nlines: %#v", len(lines), lines)
	}
	itemStart := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "> [x] ") {
			itemStart = i
			break
		}
	}
	if itemStart == -1 {
		t.Fatalf("did not find selected item line in %#v", lines)
	}
	if lines[itemStart] != "> [x] abcdefghijklm" {
		t.Fatalf("unexpected first item line: %q", lines[itemStart])
	}
	if lines[itemStart+1] != "      nop" {
		t.Fatalf("unexpected wrapped continuation line: %q", lines[itemStart+1])
	}

	foundSubmit := false
	for _, line := range lines {
		if line == "  [ continue ]" || line == "> [ continue ]" {
			foundSubmit = true
			break
		}
	}
	if !foundSubmit {
		t.Fatalf("did not find submit row in %#v", lines)
	}
}

func TestBuildMultiSelectLinesKeepsFullPath(t *testing.T) {
	t.Parallel()

	path := "/home/jmo/Development/worktrees/nixfiles/charlie"
	lines := buildMultiSelectLines([]string{path}, 0, map[int]bool{}, 18)

	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "> [ ] ") {
			start = i
			break
		}
	}
	if start == -1 {
		t.Fatalf("did not find item start line in %#v", lines)
	}

	var rebuilt strings.Builder
	for _, line := range lines[start:] {
		if strings.HasSuffix(line, "[ continue ]") {
			break
		}
		if strings.HasPrefix(line, "> [ ] ") {
			rebuilt.WriteString(strings.TrimPrefix(line, "> [ ] "))
			continue
		}
		rebuilt.WriteString(strings.TrimPrefix(line, "      "))
	}

	if rebuilt.String() != path {
		t.Fatalf("path was truncated or altered\nwant: %q\ngot:  %q", path, rebuilt.String())
	}
}

func TestBuildMultiSelectLinesWrapsHeaderText(t *testing.T) {
	t.Parallel()

	lines := buildMultiSelectLines([]string{"/tmp/a"}, 0, map[int]bool{}, 24)

	if len(lines) < 8 {
		t.Fatalf("expected wrapped header lines, got %d lines: %#v", len(lines), lines)
	}
	if lines[0] != "Select workspaces to de" {
		t.Fatalf("unexpected first wrapped header line: %q", lines[0])
	}
	if lines[1] != "lete" {
		t.Fatalf("unexpected second wrapped header line: %q", lines[1])
	}
}

func TestBuildMultiSelectLinesCursorCanSelectSubmit(t *testing.T) {
	t.Parallel()

	lines := buildMultiSelectLines([]string{"/tmp/a", "/tmp/b"}, 2, map[int]bool{}, 80)
	if lines[len(lines)-1] != "> [ continue ]" {
		t.Fatalf("expected submit row selected, got %q", lines[len(lines)-1])
	}
}

func TestApplyEnterTogglesAndAdvancesThenSubmits(t *testing.T) {
	t.Parallel()

	selected := map[int]bool{}
	cursor, done := applyEnter(0, 2, selected)
	if done {
		t.Fatalf("should not be done on first item")
	}
	if cursor != 1 {
		t.Fatalf("expected cursor 1, got %d", cursor)
	}
	if !selected[0] {
		t.Fatalf("expected item 0 selected")
	}

	cursor, done = applyEnter(cursor, 2, selected)
	if done {
		t.Fatalf("should not be done on second item")
	}
	if cursor != 2 {
		t.Fatalf("expected cursor on submit row (2), got %d", cursor)
	}

	cursor, done = applyEnter(cursor, 2, selected)
	if !done {
		t.Fatalf("expected done on submit row")
	}
}

func TestInvertSelections(t *testing.T) {
	t.Parallel()

	selected := map[int]bool{1: true}
	invertSelections(selected, 3)
	if !selected[0] || selected[1] || !selected[2] {
		t.Fatalf("unexpected selection after invert: %#v", selected)
	}
}

func TestInterpretConfirmByte(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		b         byte
		wantDone  bool
		wantValue bool
	}{
		{name: "yes lower", b: 'y', wantDone: true, wantValue: true},
		{name: "yes upper", b: 'Y', wantDone: true, wantValue: true},
		{name: "no", b: 'n', wantDone: true, wantValue: false},
		{name: "enter", b: '\n', wantDone: true, wantValue: false},
		{name: "escape", b: 0x1b, wantDone: true, wantValue: false},
		{name: "other key", b: 'x', wantDone: false, wantValue: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			done, value := interpretConfirmByte(tc.b)
			if done != tc.wantDone || value != tc.wantValue {
				t.Fatalf("interpretConfirmByte(%q) = (%v, %v), want (%v, %v)", tc.b, done, value, tc.wantDone, tc.wantValue)
			}
		})
	}
}

func TestRunFzfCancelReturnsNoSelection(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	if err := writeExecutable(filepath.Join(bin, "fzf"), "#!/usr/bin/env bash\nexit 1\n"); err != nil {
		t.Fatalf("write fake fzf: %v", err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	out, err := runFzf([]string{"a", "b"}, false)
	if err != nil {
		t.Fatalf("runFzf failed: %v", err)
	}
	if out != "" {
		t.Fatalf("expected empty output on cancel, got %q", out)
	}
}

func TestRunCommandCaptureSuccessAndFailure(t *testing.T) {
	t.Parallel()

	out, err := runCommandCapture("sh", "-c", "printf ok")
	if err != nil {
		t.Fatalf("runCommandCapture success failed: %v", err)
	}
	if out != "ok" {
		t.Fatalf("expected ok, got %q", out)
	}

	_, err = runCommandCapture("sh", "-c", "echo boom >&2; exit 2")
	if err == nil {
		t.Fatalf("expected failure")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected stderr in error, got %v", err)
	}
}

func TestRunCommandToStderrSuccess(t *testing.T) {
	t.Parallel()

	if err := runCommandToStderr("sh", "-c", "echo ok >/dev/null"); err != nil {
		t.Fatalf("runCommandToStderr failed: %v", err)
	}
}

func TestRunHelpAndUnknown(t *testing.T) {
	out, err := captureStdout(func() error {
		return run([]string{"help"})
	})
	if err != nil {
		t.Fatalf("run help failed: %v", err)
	}
	if !strings.Contains(out, "Usage: jjw") {
		t.Fatalf("expected usage output, got %q", out)
	}

	err = run([]string{"wat"})
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("expected unknown command error, got %v", err)
	}
}

func TestRunSubcommandHelp(t *testing.T) {
	for _, helpFlag := range []string{"-h", "--help"} {
		helpFlag := helpFlag
		t.Run(helpFlag, func(t *testing.T) {
			out, err := captureStdout(func() error {
				return run([]string{"tidy", helpFlag})
			})
			if err != nil {
				t.Fatalf("run tidy %s failed: %v", helpFlag, err)
			}
			if !strings.Contains(out, "Usage: jjw tidy") || !strings.Contains(out, "--worktrees-root") || !strings.Contains(out, "--yes") {
				t.Fatalf("expected tidy usage with options, got %q", out)
			}
			if strings.Contains(out, "flag: help requested") {
				t.Fatalf("help should not expose flag parser error: %q", out)
			}
		})
	}
}

func TestRunMainStackAbandonsEmptyAncestorsAfterCleanRebase(t *testing.T) {
	repoRoot := t.TempDir()
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	app := "nixfiles"
	for _, name := range []string{"main", "feat"} {
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
			return "feat\tabc111\nmain\tabc333\n", nil
		}
		if len(args) >= 5 && args[2] == "log" && args[3] == "-r" {
			switch {
			case args[4] == "conflicts() & @":
				return "", nil
			case strings.HasPrefix(args[4], "heads("):
				return "abc111\n", nil
			case args[4] == "immutable() & ::@ & ~@":
				return "", nil
			case args[4] == "empty() & mutable() & ::@ & ~@":
				return "empty123\n", nil
			}
		}
		if len(args) >= 4 && args[2] == "log" && args[3] == "-r" {
			return "abc333\n", nil
		}
		return "", nil
	}

	var calls [][]string
	commandToStderrFn = func(name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		return nil
	}

	if err := runMainStack([]string{"--repo", repoRoot, "--app", app, "--worktrees-root", worktreesRoot, "--workspace", "main"}); err != nil {
		t.Fatalf("runMainStack failed: %v", err)
	}

	abandonIndex := -1
	finalUpdateIndex := -1
	for i, call := range calls {
		if len(call) >= 4 && call[3] == "abandon" {
			abandonIndex = i
		}
		if len(call) >= 5 && call[3] == "workspace" && call[4] == "update-stale" {
			finalUpdateIndex = i
		}
	}
	if abandonIndex == -1 {
		t.Fatalf("expected stack to abandon empty ancestors; calls: %#v", calls)
	}
	if finalUpdateIndex == -1 || abandonIndex > finalUpdateIndex {
		t.Fatalf("expected final update-stale after abandon; calls: %#v", calls)
	}
}

func TestRunMainStackRunsStatusAndRebase(t *testing.T) {
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
	origStderr := stderrWriter
	defer func() {
		commandCaptureFn = origCapture
		commandToStderrFn = origRun
		stderrWriter = origStderr
	}()

	commandCaptureFn = func(name string, args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "-R" && args[2] == "workspace" {
			return "feat-a\tabc111\nfeat-b\tabc222\nmain\tabc333\n", nil
		}
		if len(args) >= 5 && args[2] == "log" && args[3] == "-r" && args[4] == "conflicts() & @" {
			return "", nil
		}
		if len(args) >= 5 && args[2] == "log" && args[3] == "-r" && strings.HasPrefix(args[4], "heads(") {
			return "abc111\nabc222\n", nil
		}
		if len(args) >= 8 && args[2] == "log" && args[3] == "-r" && args[4] == "immutable() & ::@ & ~@" {
			return "", nil
		}
		if len(args) >= 5 && args[2] == "log" && args[3] == "-r" && args[4] == "empty() & mutable() & ::@ & ~@" {
			return "", nil
		}
		if len(args) >= 4 && args[2] == "log" && args[3] == "-r" {
			return "abc333\n", nil
		}
		return "", nil
	}

	var calls [][]string
	commandToStderrFn = func(name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		return nil
	}

	var errOut bytes.Buffer
	stderrWriter = &errOut

	err := runMainStack([]string{"--repo", repoRoot, "--app", app, "--worktrees-root", worktreesRoot, "--workspace", "main"})
	if err != nil {
		t.Fatalf("runMainStack failed: %v", err)
	}

	if len(calls) != 8 {
		t.Fatalf("expected 8 jj calls (3 update-stale + 3 st + rebase + final update-stale), got %d", len(calls))
	}
	if len(calls[6]) < 4 || calls[6][3] != "rebase" {
		t.Fatalf("expected rebase call at index 6, got %#v", calls[6])
	}
	joined := strings.Join(calls[6], " ")
	if !strings.Contains(joined, " rebase -b @") {
		t.Fatalf("expected branch rebase mode, got %q", joined)
	}
	if !strings.Contains(joined, "-d feat-a@") || !strings.Contains(joined, "-d feat-b@") {
		t.Fatalf("expected rebase destinations for feature workspaces, got %q", joined)
	}
	if calls[7][3] != "workspace" || calls[7][4] != "update-stale" {
		t.Fatalf("expected final update-stale call, got %#v", calls[7])
	}
}

func TestRunMainPrintsDefaultWorkspacePath(t *testing.T) {
	repoRoot := t.TempDir()
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	app := "nixfiles"
	defaultPath := filepath.Join(worktreesRoot, app, "default")
	if err := os.MkdirAll(defaultPath, 0o755); err != nil {
		t.Fatalf("mkdir default workspace: %v", err)
	}

	origCapture := commandCaptureFn
	defer func() { commandCaptureFn = origCapture }()
	commandCaptureFn = func(name string, args ...string) (string, error) {
		if len(args) >= 3 && args[2] == "workspace" {
			return "default\tabc111\nalpha\tdef222\n", nil
		}
		if len(args) >= 3 && args[2] == "log" {
			return "def222\n", nil
		}
		return "", nil
	}

	out, err := captureStdout(func() error {
		return runMain([]string{"--repo", repoRoot, "--app", app, "--worktrees-root", worktreesRoot})
	})
	if err != nil {
		t.Fatalf("runMain failed: %v", err)
	}
	if strings.TrimSpace(out) != defaultPath {
		t.Fatalf("expected %q got %q", defaultPath, strings.TrimSpace(out))
	}
}

func TestRunMainPrintsRepoRootWhenAlreadyOnDefault(t *testing.T) {
	repoRoot := t.TempDir()
	origCapture := commandCaptureFn
	defer func() { commandCaptureFn = origCapture }()
	commandCaptureFn = func(name string, args ...string) (string, error) {
		if len(args) >= 3 && args[2] == "workspace" {
			return "default\tabc111\n", nil
		}
		if len(args) >= 3 && args[2] == "log" {
			return "abc111\n", nil
		}
		return "", nil
	}

	out, err := captureStdout(func() error {
		return runMain([]string{"--repo", repoRoot, "--app", "nixfiles", "--worktrees-root", filepath.Join(t.TempDir(), "worktrees")})
	})
	if err != nil {
		t.Fatalf("runMain failed: %v", err)
	}
	if strings.TrimSpace(out) != repoRoot {
		t.Fatalf("expected repoRoot %q got %q", repoRoot, strings.TrimSpace(out))
	}
}

func TestResolveDefaultWorkspaceRootFromJjRepoLink(t *testing.T) {
	repoRoot := t.TempDir()
	defaultRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoRoot, ".jj"), 0o755); err != nil {
		t.Fatalf("mkdir .jj: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(defaultRoot, ".jj"), 0o755); err != nil {
		t.Fatalf("mkdir default .jj: %v", err)
	}
	linkTarget := filepath.Join(defaultRoot, ".jj", "repo")
	if err := os.WriteFile(filepath.Join(repoRoot, ".jj", "repo"), []byte(linkTarget+"\n"), 0o644); err != nil {
		t.Fatalf("write .jj/repo link file: %v", err)
	}

	got, ok := resolveDefaultWorkspaceRoot(repoRoot)
	if !ok {
		t.Fatalf("expected default workspace root to resolve")
	}
	if got != defaultRoot {
		t.Fatalf("expected %q got %q", defaultRoot, got)
	}
}

func TestRunMainStackNeedsOtherWorkspaces(t *testing.T) {
	repoRoot := t.TempDir()
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	app := "nixfiles"
	if err := os.MkdirAll(filepath.Join(worktreesRoot, app, "main"), 0o755); err != nil {
		t.Fatalf("mkdir main workspace: %v", err)
	}

	origCapture := commandCaptureFn
	origRun := commandToStderrFn
	defer func() {
		commandCaptureFn = origCapture
		commandToStderrFn = origRun
	}()

	commandCaptureFn = func(name string, args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "-R" && args[2] == "workspace" {
			return "main\tabc333\n", nil
		}
		if len(args) >= 8 && args[2] == "log" && args[3] == "-r" && args[4] == "immutable() & ::@ & ~@" {
			return "", nil
		}
		if len(args) >= 4 && args[2] == "log" && args[3] == "-r" {
			return "abc333\n", nil
		}
		return "", nil
	}
	commandToStderrFn = func(name string, args ...string) error { return nil }

	err := runMainStack([]string{"--repo", repoRoot, "--app", app, "--worktrees-root", worktreesRoot, "--workspace", "main"})
	if err == nil || !strings.Contains(err.Error(), "no other workspaces") {
		t.Fatalf("expected no other workspaces error, got %v", err)
	}
}

func TestCurrentWorkspaceNameNotDetected(t *testing.T) {
	t.Parallel()

	origCapture := commandCaptureFn
	defer func() { commandCaptureFn = origCapture }()

	commandCaptureFn = func(name string, args ...string) (string, error) {
		return "zzz999\n", nil
	}

	_, err := currentWorkspaceName("/tmp/repo", []workspaceRef{{Name: "main", TargetChange: "abc111"}})
	if err == nil {
		t.Fatalf("expected workspace detection error")
	}
}

func TestRunMainStackUsesRepoRootForDefaultWorkspace(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoRoot, ".jj"), 0o755); err != nil {
		t.Fatalf("mkdir .jj: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".jj", "repo"), []byte(filepath.Join(repoRoot, ".jj", "repo")+"\n"), 0o644); err != nil {
		t.Fatalf("write .jj/repo: %v", err)
	}
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	app := "nixfiles"
	if err := os.MkdirAll(filepath.Join(worktreesRoot, app, "alpha"), 0o755); err != nil {
		t.Fatalf("mkdir alpha workspace: %v", err)
	}

	origCapture := commandCaptureFn
	origRun := commandToStderrFn
	defer func() {
		commandCaptureFn = origCapture
		commandToStderrFn = origRun
	}()

	commandCaptureFn = func(name string, args ...string) (string, error) {
		if len(args) >= 3 && args[2] == "workspace" {
			return "default\tabc111\nalpha\tdef222\n", nil
		}
		if len(args) >= 5 && args[2] == "log" && args[3] == "-r" && args[4] == "conflicts() & @" {
			return "", nil
		}
		if len(args) >= 5 && args[2] == "log" && args[3] == "-r" && strings.HasPrefix(args[4], "heads(") {
			return "def222\n", nil
		}
		if len(args) >= 8 && args[2] == "log" && args[3] == "-r" && args[4] == "immutable() & ::@ & ~@" {
			return "", nil
		}
		if len(args) >= 3 && args[2] == "log" {
			return "abc111\n", nil
		}
		return "", nil
	}

	var sawDefaultUpdate bool
	commandToStderrFn = func(name string, args ...string) error {
		if len(args) >= 4 && args[2] == "workspace" && args[3] == "update-stale" && args[1] == repoRoot {
			sawDefaultUpdate = true
		}
		return nil
	}

	err := runMainStack([]string{"--repo", repoRoot, "--app", app, "--worktrees-root", worktreesRoot, "--workspace", "default"})
	if err != nil {
		t.Fatalf("runMainStack failed: %v", err)
	}
	if !sawDefaultUpdate {
		t.Fatalf("expected default workspace update-stale to run at repo root")
	}
}

func TestRunMainStackAutoUsesRevisionModeWhenImmutableAncestorsFound(t *testing.T) {
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
		if len(args) >= 3 && args[0] == "-R" && args[2] == "workspace" {
			return "feat-a\tabc111\nfeat-b\tabc222\nmain\tabc333\n", nil
		}
		if len(args) >= 5 && args[2] == "log" && args[3] == "-r" && args[4] == "conflicts() & @" {
			return "", nil
		}
		if len(args) >= 5 && args[2] == "log" && args[3] == "-r" && strings.HasPrefix(args[4], "heads(") {
			return "abc111\nabc222\n", nil
		}
		if len(args) >= 8 && args[2] == "log" && args[3] == "-r" && args[4] == "parents(@)" {
			return "main999\n", nil
		}
		if len(args) >= 8 && args[2] == "log" && args[3] == "-r" && args[4] == "immutable() & ::@ & ~@" {
			return "imm123\n", nil
		}
		if len(args) >= 5 && args[2] == "log" && args[3] == "-r" && strings.HasPrefix(args[4], "main999::(") {
			return "", nil
		}
		if len(args) >= 4 && args[2] == "log" && args[3] == "-r" {
			return "abc333\n", nil
		}
		return "", nil
	}

	var rebaseCall []string
	commandToStderrFn = func(name string, args ...string) error {
		if len(args) >= 4 && args[2] == "rebase" {
			rebaseCall = append([]string{name}, args...)
		}
		return nil
	}

	err := runMainStack([]string{"--repo", repoRoot, "--app", app, "--worktrees-root", worktreesRoot, "--workspace", "main"})
	if err != nil {
		t.Fatalf("runMainStack failed: %v", err)
	}

	joined := strings.Join(rebaseCall, " ")
	if !strings.Contains(joined, " rebase -r @") {
		t.Fatalf("expected revision rebase mode, got %q", joined)
	}
	if !strings.Contains(joined, "-d main999") {
		t.Fatalf("expected revision rebase to preserve existing parent, got %q", joined)
	}
}

func TestRunMainStackRejectsInvalidRebaseMode(t *testing.T) {
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
		if len(args) >= 5 && args[2] == "log" && args[3] == "-r" && args[4] == "conflicts() & @" {
			return "", nil
		}
		if len(args) >= 4 && args[2] == "log" && args[3] == "-r" {
			return "abc333\n", nil
		}
		return "", nil
	}
	commandToStderrFn = func(name string, args ...string) error { return nil }

	err := runMainStack([]string{"--repo", repoRoot, "--app", app, "--worktrees-root", worktreesRoot, "--workspace", "main", "--rebase-mode", "wat"})
	if err == nil || !strings.Contains(err.Error(), "invalid --rebase-mode") {
		t.Fatalf("expected invalid rebase mode error, got %v", err)
	}
}

func TestRunMainStackUsesConfiguredMainStackDefaults(t *testing.T) {
	repoRoot := t.TempDir()
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	app := "nixfiles"
	for _, name := range []string{"main", "feat-a", "feat-b"} {
		if err := os.MkdirAll(filepath.Join(worktreesRoot, app, name), 0o755); err != nil {
			t.Fatalf("mkdir workspace %s: %v", name, err)
		}
	}

	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "jjw"), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	configData := []byte("main_stack:\n  default_workspace: main\n  rebase_mode: revision\n  stack_shape: merge\n  conflict_strategy: off\n")
	if err := os.WriteFile(filepath.Join(xdg, "jjw", "config.yaml"), configData, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)

	origCapture := commandCaptureFn
	origRun := commandToStderrFn
	defer func() {
		commandCaptureFn = origCapture
		commandToStderrFn = origRun
	}()

	commandCaptureFn = func(name string, args ...string) (string, error) {
		if len(args) >= 3 && args[2] == "workspace" {
			return "feat-a\taaa111\nfeat-b\tbbb222\nmain\tmmm333\n", nil
		}
		if len(args) >= 5 && args[2] == "log" && args[3] == "-r" && args[4] == "parents(@)" {
			return "main999\n", nil
		}
		if len(args) >= 5 && args[2] == "log" && args[3] == "-r" && strings.HasPrefix(args[4], "main999::(") {
			return "", nil
		}
		if len(args) >= 5 && args[2] == "log" && args[3] == "-r" && args[4] == "conflicts() & @" {
			return "", nil
		}
		if len(args) >= 4 && args[2] == "log" && args[3] == "-r" {
			return "mmm333\n", nil
		}
		return "", nil
	}

	var rebaseCall []string
	commandToStderrFn = func(name string, args ...string) error {
		if len(args) >= 4 && args[2] == "rebase" {
			rebaseCall = append([]string{name}, args...)
		}
		return nil
	}

	err := runMainStack([]string{"--repo", repoRoot, "--app", app, "--worktrees-root", worktreesRoot})
	if err != nil {
		t.Fatalf("runMainStack failed: %v", err)
	}

	joined := strings.Join(rebaseCall, " ")
	if !strings.Contains(joined, " rebase -r @") {
		t.Fatalf("expected revision mode from config, got %q", joined)
	}
	if !strings.Contains(joined, "-d feat-a@") || !strings.Contains(joined, "-d feat-b@") {
		t.Fatalf("expected merge destinations from config, got %q", joined)
	}
}

func TestRunMainStackRejectsInvalidConflictStrategy(t *testing.T) {
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

	err := runMainStack([]string{"--repo", repoRoot, "--app", app, "--worktrees-root", worktreesRoot, "--workspace", "main", "--conflict-strategy", "wat"})
	if err == nil || !strings.Contains(err.Error(), "invalid --conflict-strategy") {
		t.Fatalf("expected invalid conflict strategy error, got %v", err)
	}
}

func TestRunMainStackPreferCleanFallsBackToMerge(t *testing.T) {
	repoRoot := t.TempDir()
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	app := "nixfiles"
	for _, name := range []string{"main", "alpha", "beta"} {
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

	attempt := ""
	commandCaptureFn = func(name string, args ...string) (string, error) {
		if len(args) >= 3 && args[2] == "workspace" {
			return "alpha\taaa111\nbeta\tbbb222\nmain\tmmm333\n", nil
		}
		if len(args) >= 5 && args[2] == "log" && args[3] == "-r" && strings.HasPrefix(args[4], "heads(") {
			return "bbb222\n", nil
		}
		if len(args) >= 5 && args[2] == "log" && args[3] == "-r" && args[4] == "conflicts() & @" {
			if attempt == "linear" {
				return "conf123\n", nil
			}
			return "", nil
		}
		if len(args) >= 8 && args[2] == "log" && args[3] == "-r" && args[4] == "immutable() & ::@ & ~@" {
			return "", nil
		}
		if len(args) >= 4 && args[2] == "log" && args[3] == "-r" {
			return "mmm333\n", nil
		}
		return "", nil
	}

	var rebaseCalls []string
	undoCount := 0
	commandToStderrFn = func(name string, args ...string) error {
		if len(args) >= 4 && args[2] == "rebase" {
			joined := strings.Join(append([]string{name}, args...), " ")
			rebaseCalls = append(rebaseCalls, joined)
			if strings.Contains(joined, "-d bbb222") {
				attempt = "linear"
			} else {
				attempt = "merge"
			}
		}
		if len(args) >= 3 && args[2] == "undo" {
			undoCount++
		}
		return nil
	}

	err := runMainStack([]string{"--repo", repoRoot, "--app", app, "--worktrees-root", worktreesRoot, "--workspace", "main", "--conflict-strategy", "prefer-clean"})
	if err != nil {
		t.Fatalf("runMainStack failed: %v", err)
	}

	if len(rebaseCalls) != 2 {
		t.Fatalf("expected two rebase attempts, got %d", len(rebaseCalls))
	}
	if undoCount != 1 {
		t.Fatalf("expected one undo between attempts, got %d", undoCount)
	}
	if !strings.Contains(rebaseCalls[0], "-d bbb222") {
		t.Fatalf("expected first attempt to be linear, got %q", rebaseCalls[0])
	}
	if !strings.Contains(rebaseCalls[1], "-d alpha@") || !strings.Contains(rebaseCalls[1], "-d beta@") {
		t.Fatalf("expected second attempt to be merge, got %q", rebaseCalls[1])
	}
}

func TestRunMainStackPreferCleanKeepsMergeWhenBothConflict(t *testing.T) {
	repoRoot := t.TempDir()
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	app := "nixfiles"
	for _, name := range []string{"main", "alpha", "beta"} {
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

	attempt := ""
	commandCaptureFn = func(name string, args ...string) (string, error) {
		if len(args) >= 3 && args[2] == "workspace" {
			return "alpha\taaa111\nbeta\tbbb222\nmain\tmmm333\n", nil
		}
		if len(args) >= 5 && args[2] == "log" && args[3] == "-r" && strings.HasPrefix(args[4], "heads(") {
			return "bbb222\n", nil
		}
		if len(args) >= 5 && args[2] == "log" && args[3] == "-r" && args[4] == "conflicts() & @" {
			if attempt == "linear" || attempt == "merge" {
				return "conf123\n", nil
			}
			return "", nil
		}
		if len(args) >= 8 && args[2] == "log" && args[3] == "-r" && args[4] == "immutable() & ::@ & ~@" {
			return "", nil
		}
		if len(args) >= 4 && args[2] == "log" && args[3] == "-r" {
			return "mmm333\n", nil
		}
		return "", nil
	}

	var rebaseCalls []string
	undoCount := 0
	commandToStderrFn = func(name string, args ...string) error {
		if len(args) >= 4 && args[2] == "rebase" {
			joined := strings.Join(append([]string{name}, args...), " ")
			rebaseCalls = append(rebaseCalls, joined)
			if strings.Contains(joined, "-d alpha@") {
				attempt = "merge"
			} else {
				attempt = "linear"
			}
		}
		if len(args) >= 3 && args[2] == "undo" {
			undoCount++
		}
		return nil
	}

	err := runMainStack([]string{"--repo", repoRoot, "--app", app, "--worktrees-root", worktreesRoot, "--workspace", "main", "--conflict-strategy", "prefer-clean", "--stack-shape", "auto"})
	if err != nil {
		t.Fatalf("runMainStack failed: %v", err)
	}

	if len(rebaseCalls) != 2 {
		t.Fatalf("expected two rebase attempts, got %d", len(rebaseCalls))
	}
	if undoCount != 1 {
		t.Fatalf("expected one undo between attempts, got %d", undoCount)
	}
	last := rebaseCalls[len(rebaseCalls)-1]
	if !strings.Contains(last, "-d alpha@") || !strings.Contains(last, "-d beta@") {
		t.Fatalf("expected final result to keep merge shape, got %q", last)
	}
}

func TestExistsAndExpandPath(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	if !exists(tmp) {
		t.Fatalf("expected temp dir to exist")
	}
	if exists(filepath.Join(tmp, "missing")) {
		t.Fatalf("expected missing path to not exist")
	}

	home, _ := os.UserHomeDir()
	if got := expandPath("~/x"); got != filepath.Join(home, "x") {
		t.Fatalf("unexpected expanded path: %q", got)
	}
}

func TestRunCreateRejectsNameConflictInputs(t *testing.T) {
	t.Parallel()

	err := runCreate([]string{"foo", "--name", "bar"})
	if err == nil {
		t.Fatalf("expected conflict error")
	}
}

func TestRunSelectNoWorkspaceDirs(t *testing.T) {
	repoRoot := t.TempDir()
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	app := "myapp"
	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "jjw"), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "jjw", "config.yaml"), []byte("worktrees_root: "+worktreesRoot+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	jj := "#!/usr/bin/env bash\nset -euo pipefail\nif [[ \"$1\" == \"-R\" ]]; then shift 2; fi\nif [[ \"$1\" == \"workspace\" && \"$2\" == \"list\" ]]; then printf 'one\\n'; exit 0; fi\nexit 2\n"
	if err := writeExecutable(filepath.Join(bin, "jj"), jj); err != nil {
		t.Fatalf("write fake jj: %v", err)
	}

	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("XDG_CONFIG_HOME", xdg)

	err := runSelect([]string{"--repo", repoRoot, "--app", app})
	if err == nil || !strings.Contains(err.Error(), "no workspace directories") {
		t.Fatalf("expected no workspace directories error, got %v", err)
	}
}

func TestResolveRepoRootUsesInjectedCommandCapture(t *testing.T) {
	origCapture := commandCaptureFn
	defer func() { commandCaptureFn = origCapture }()

	commandCaptureFn = func(name string, args ...string) (string, error) {
		if name == "jj" {
			return "/tmp/injected-root\n", nil
		}
		return "", os.ErrNotExist
	}

	root, err := resolveRepoRoot("")
	if err != nil {
		t.Fatalf("resolveRepoRoot failed: %v", err)
	}
	if root != "/tmp/injected-root" {
		t.Fatalf("expected injected root, got %q", root)
	}
}

func TestRunCreateDirenvAllowFailureIgnored(t *testing.T) {
	repoRoot := t.TempDir()
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")

	origCapture := commandCaptureFn
	origRun := commandToStderrFn
	origLookPath := lookPathFn
	origStdout := stdoutWriter
	defer func() {
		commandCaptureFn = origCapture
		commandToStderrFn = origRun
		lookPathFn = origLookPath
		stdoutWriter = origStdout
	}()

	commandCaptureFn = func(name string, args ...string) (string, error) {
		if len(args) >= 3 && args[0] == "-R" && args[2] == "workspace" {
			return "default\n", nil
		}
		return "", nil
	}

	var calls [][]string
	commandToStderrFn = func(name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		if name == "jj" {
			target := args[len(args)-1]
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			return nil
		}
		if name == "direnv" {
			return errors.New("direnv failed")
		}
		return nil
	}

	lookPathFn = func(file string) (string, error) {
		if file == "direnv" {
			return "/fake/direnv", nil
		}
		return "", os.ErrNotExist
	}

	out, err := captureStdout(func() error {
		return runCreate([]string{"--repo", repoRoot, "--app", "app", "--worktrees-root", worktreesRoot, "alpha"})
	})
	if err != nil {
		t.Fatalf("runCreate failed: %v", err)
	}
	if strings.TrimSpace(out) != filepath.Join(worktreesRoot, "app", "alpha") {
		t.Fatalf("unexpected create output: %q", strings.TrimSpace(out))
	}

	if len(calls) < 2 {
		t.Fatalf("expected jj add + direnv allow calls, got %d", len(calls))
	}
}

func TestSelectOneNoSelectionOnQuit(t *testing.T) {
	origStdin := stdinReader
	defer func() {
		stdinReader = origStdin
	}()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	stdinReader = r
	if _, err := w.Write([]byte("q\n")); err != nil {
		t.Fatalf("write stdin: %v", err)
	}
	_ = w.Close()

	_, err = selectOne([]string{"/tmp/a", "/tmp/b"})
	if err == nil || !strings.Contains(err.Error(), "no selection") {
		t.Fatalf("expected no selection error, got %v", err)
	}
}

func TestRunTidyMissingAppRootIsNoop(t *testing.T) {
	repoRoot := t.TempDir()
	worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "jjw"), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "jjw", "config.yaml"), []byte("worktrees_root: "+worktreesRoot+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	jj := "#!/usr/bin/env bash\nset -euo pipefail\nif [[ \"$1\" == \"-R\" ]]; then shift 2; fi\nif [[ \"$1\" == \"workspace\" && \"$2\" == \"list\" ]]; then printf 'default\\n'; exit 0; fi\nexit 2\n"
	if err := writeExecutable(filepath.Join(bin, "jj"), jj); err != nil {
		t.Fatalf("write fake jj: %v", err)
	}

	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("XDG_CONFIG_HOME", xdg)

	if err := runTidy([]string{"--repo", repoRoot, "--app", "missing", "--yes"}); err != nil {
		t.Fatalf("expected no-op tidy, got %v", err)
	}
}
