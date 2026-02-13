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

func TestRunSelectWithFakeFzf(t *testing.T) {
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

	deleted := strings.TrimSpace(output)
	if deleted != defunctA {
		t.Fatalf("expected %q to be deleted, got %q", defunctA, deleted)
	}
	if _, err := os.Stat(defunctA); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be removed", defunctA)
	}
	if _, err := os.Stat(defunctB); err != nil {
		t.Fatalf("expected %s to remain: %v", defunctB, err)
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

func TestRunCdWithNameCreatesWorkspacePath(t *testing.T) {
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
		return runCd([]string{"--repo", repoRoot, "--app", app, "--worktrees-root", worktreesRoot, "feature-2", "--no-direnv-allow"})
	})
	if err != nil {
		t.Fatalf("runCd failed: %v", err)
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

func TestRunCdWithNameFlagCreatesWorkspacePath(t *testing.T) {
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
		return runCd([]string{"--repo", repoRoot, "--app", app, "--worktrees-root", worktreesRoot, "--name", "feature-3", "--no-direnv-allow"})
	})
	if err != nil {
		t.Fatalf("runCd failed: %v", err)
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

	err := runMainStack([]string{"--repo", repoRoot, "--app", app, "--worktrees-root", worktreesRoot})
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
		if len(args) >= 4 && args[2] == "log" && args[3] == "-r" {
			return "abc333\n", nil
		}
		return "", nil
	}
	commandToStderrFn = func(name string, args ...string) error { return nil }

	err := runMainStack([]string{"--repo", repoRoot, "--app", app, "--worktrees-root", worktreesRoot})
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

	err := runMainStack([]string{"--repo", repoRoot, "--app", app, "--worktrees-root", worktreesRoot, "--main", "default"})
	if err != nil {
		t.Fatalf("runMainStack failed: %v", err)
	}
	if !sawDefaultUpdate {
		t.Fatalf("expected default workspace update-stale to run at repo root")
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

func TestSelectOneInjectedFzfNoSelection(t *testing.T) {
	origLookPath := lookPathFn
	origRunFzf := runFzfFn
	defer func() {
		lookPathFn = origLookPath
		runFzfFn = origRunFzf
	}()

	lookPathFn = func(file string) (string, error) {
		if file == "fzf" {
			return "/fake/fzf", nil
		}
		return "", os.ErrNotExist
	}
	runFzfFn = func(items []string, multi bool) (string, error) {
		return "", nil
	}

	_, err := selectOne([]string{"/tmp/a", "/tmp/b"})
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
