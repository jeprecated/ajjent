package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jeprecated/ajjent/internal/buildcfg"
)

// withAgentTickPrefix sets the Agent Tick prefix for one test.
func withAgentTickPrefix(t *testing.T, prefix string) {
	t.Helper()
	orig := agentTickPrefix
	agentTickPrefix = prefix
	t.Cleanup(func() { agentTickPrefix = orig })
}

// fakeJJRepo lays out just enough of a jj repo for detection: a main
// workspace with the given op store type, and a secondary workspace whose
// `.jj/repo` points at it.
func fakeJJRepo(t *testing.T, opStoreType string) (mainRoot, secondaryRoot string) {
	t.Helper()
	base := t.TempDir()
	mainRoot = filepath.Join(base, "main")
	opStore := filepath.Join(mainRoot, ".jj", "repo", "op_store")
	if err := os.MkdirAll(opStore, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(opStore, "type"), []byte(opStoreType), 0o644); err != nil {
		t.Fatal(err)
	}
	secondaryRoot = filepath.Join(base, "workspaces", "alpha")
	if err := os.MkdirAll(filepath.Join(secondaryRoot, ".jj"), 0o755); err != nil {
		t.Fatal(err)
	}
	pointer, err := filepath.Rel(filepath.Join(secondaryRoot, ".jj"), filepath.Join(mainRoot, ".jj", "repo"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secondaryRoot, ".jj", "repo"), []byte(pointer), 0o644); err != nil {
		t.Fatal(err)
	}
	return mainRoot, secondaryRoot
}

func TestIsAgentTickRepoFollowsSecondaryWorkspacePointer(t *testing.T) {
	mainRoot, secondaryRoot := fakeJJRepo(t, agentTickOpStoreType)
	if !isAgentTickRepo(mainRoot) || !isAgentTickRepo(secondaryRoot) {
		t.Fatal("expected both workspaces of an Agent Tick repo to be detected")
	}
	if !isAgentTickRepo(nearestJJWorkspace(filepath.Join(secondaryRoot))) {
		t.Fatal("expected detection from the nearest workspace")
	}
	plainMain, plainSecondary := fakeJJRepo(t, "simple_op_store")
	if isAgentTickRepo(plainMain) || isAgentTickRepo(plainSecondary) {
		t.Fatal("an ordinary jj repo is not an Agent Tick repo")
	}
	if isAgentTickRepo(t.TempDir()) || isAgentTickRepo("") {
		t.Fatal("a directory without jj metadata is not an Agent Tick repo")
	}
}

func TestAgentTickHostMatchesTickRule(t *testing.T) {
	t.Setenv("TICK_HOST", "Quartz.tail6e682f.ts.net")
	if host, err := agentTickHost(); err != nil || host != "quartz" {
		t.Fatalf("agentTickHost() = %q, %v; want quartz", host, err)
	}
	t.Setenv("TICK_HOST", "bad/host")
	if _, err := agentTickHost(); err == nil {
		t.Fatal("expected a host name with a slash to be refused")
	}
}

func TestWorkspaceNamesCarryTheHostPrefixOnlyInAgentTickRepos(t *testing.T) {
	withAgentTickPrefix(t, "")
	withHostPrefix(t, "")
	if got := jjWorkspaceName("alpha"); got != "alpha" {
		t.Fatalf("ordinary repo name = %q", got)
	}
	if handle, ok := handleFromJJWorkspaceName("quartz/alpha"); !ok || handle != "quartz/alpha" {
		t.Fatalf("ordinary repo names pass through unchanged, got %q %v", handle, ok)
	}
	if program, args := jjProgram("jj", []string{"status"}); program != "jj" || !reflect.DeepEqual(args, []string{"status"}) {
		t.Fatalf("ordinary repo program = %s %v", program, args)
	}

	withAgentTickPrefix(t, "lattice/")
	if got := jjWorkspaceName("alpha"); got != "lattice/alpha" {
		t.Fatalf("Agent Tick name = %q", got)
	}
	for name, want := range map[string]struct {
		handle string
		ours   bool
	}{
		"lattice/alpha":  {"alpha", true},
		"lattice/":       {"", false},
		"quartz/alpha":   {"", false},
		"alpha":          {"", false},
		"latticex/alpha": {"", false},
	} {
		handle, ours := handleFromJJWorkspaceName(name)
		if ours != want.ours || (ours && handle != want.handle) {
			t.Errorf("handleFromJJWorkspaceName(%q) = %q, %v; want %q, %v", name, handle, ours, want.handle, want.ours)
		}
	}
	if program, args := jjProgram("jj", []string{"-R", "/r", "status"}); program != "tick" || !reflect.DeepEqual(args, []string{"jj", "-R", "/r", "status"}) {
		t.Fatalf("Agent Tick program = %s %v", program, args)
	}
	if program, _ := jjProgram("git", []string{"status"}); program != "git" {
		t.Fatalf("only jj is rerouted, got %s", program)
	}
}

func TestListWorkspaceRefsHidesOtherHostsWorkspaces(t *testing.T) {
	withAgentTickPrefix(t, "lattice/")
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		return strings.Join([]string{
			"lattice/default\tabc\t/repo",
			"lattice/alpha\tdef\t/ws/alpha",
			"quartz/alpha\tghi\t",
			"legacy\tjkl\t/old",
		}, "\n") + "\n", nil
	})
	refs, err := listWorkspaceRefs("/repo")
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, ref := range refs {
		got = append(got, ref.Handle+"="+ref.Root)
	}
	if want := []string{"alpha=/ws/alpha", "default=/repo"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("refs = %v, want %v", got, want)
	}
}

func TestAdoptAgentTickMainWorkspaceUsesTheRepoMainDirectory(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoRoot, ".jj", "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	withAgentTickPrefix(t, "quartz/")
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		return "quartz/nix\tabc\t" + repoRoot + "\nquartz/alpha\tdef\t/ws/alpha\nlattice/default\tghi\t\n", nil
	})
	cfg := config{MainWorkspace: "default"}
	adoptAgentTickMainWorkspace(repoRoot, &cfg)
	if cfg.MainWorkspace != "nix" {
		t.Fatalf("MainWorkspace = %q, want nix", cfg.MainWorkspace)
	}
	cfg = config{MainWorkspace: "alpha"}
	adoptAgentTickMainWorkspace(repoRoot, &cfg)
	if cfg.MainWorkspace != "alpha" {
		t.Fatalf("a configured Main Workspace on this host is kept, got %q", cfg.MainWorkspace)
	}
}

func TestResolveRepoRootNeedsTickInAgentTickRepos(t *testing.T) {
	withAgentTickPrefix(t, "")
	t.Setenv("TICK_HOST", "lattice")
	mainRoot, _ := fakeJJRepo(t, agentTickOpStoreType)
	orig := lookPathFn
	t.Cleanup(func() { lookPathFn = orig })
	lookPathFn = func(string) (string, error) { return "", errors.New("not found") }
	if _, err := resolveRepoRoot(mainRoot); !errors.Is(err, errAgentTickNeedsTick) {
		t.Fatalf("expected the missing-tick error, got %v", err)
	}
	lookPathFn = func(string) (string, error) { return "/bin/tick", nil }
	if _, err := resolveRepoRoot(mainRoot); err != nil || agentTickPrefix != "lattice/" {
		t.Fatalf("resolveRepoRoot = %v, prefix %q", err, agentTickPrefix)
	}
	plainMain, _ := fakeJJRepo(t, "simple_op_store")
	if _, err := resolveRepoRoot(plainMain); err != nil || agentTickPrefix != "" {
		t.Fatalf("an ordinary repo clears the prefix: %v, %q", err, agentTickPrefix)
	}
}

func TestNoCleanupTrustsAgentTickBuildOfTheTrustedReleaseOnlyInAgentTickRepos(t *testing.T) {
	trusted := buildcfg.NoCleanupJJVersion
	withAgentTickPrefix(t, "")
	if supportsNoCleanupJJVersion("jj " + trusted + "-agent-tick") {
		t.Fatal("Agent Tick's jj is not trusted outside Agent Tick repos")
	}
	withAgentTickPrefix(t, "lattice/")
	if !supportsNoCleanupJJVersion("jj " + trusted + "-agent-tick") {
		t.Fatal("Agent Tick's build of the trusted release should be trusted in Agent Tick repos")
	}
	if supportsNoCleanupJJVersion("jj 0.1.0-agent-tick") {
		t.Fatal("another release stays untrusted")
	}
}

// TestAgentTickRepoLifecycle runs create, list, Stack and close in a real,
// offline Agent Tick repo next to another host's Workspace. It needs `tick`.
func TestAgentTickRepoLifecycle(t *testing.T) {
	if _, err := exec.LookPath("tick"); err != nil {
		t.Skip("tick binary not available for the Agent Tick integration test")
	}
	withAgentTickPrefix(t, "")
	home := t.TempDir()
	// An isolated HOME keeps tick's state and registry away from the real
	// host's; nothing listens on the server address, so the repo is offline.
	t.Setenv("HOME", home)
	t.Setenv("TICK_SERVER", "http://127.0.0.1:9")
	t.Setenv("TICK_HOST", "testhost")
	t.Setenv("JJ_USER", "Ajj Test")
	t.Setenv("JJ_EMAIL", "ajj@example.com")
	workspacesRoot := filepath.Join(t.TempDir(), "workspaces")
	mainPath := filepath.Join(workspacesRoot, "proj", "main")
	if err := os.MkdirAll(mainPath, 0o755); err != nil {
		t.Fatal(err)
	}
	tick := func(dir string, env []string, args ...string) {
		t.Helper()
		cmd := exec.Command("tick", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("tick %s failed: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	tick(mainPath, nil, "init", "--name", "ajj-test")
	writeConfig(t, mainPath, "workspaces_root: "+workspacesRoot+"\nproject: proj\nmain_workspace: default\n")
	other := filepath.Join(t.TempDir(), "other")
	tick(mainPath, []string{"TICK_HOST=otherhost"}, "jj", "workspace", "add", "--name", "otherhost/zulu", other)

	if _, _, err := captureOutput(func() error { return runCreate([]string{"alpha", "--repo", mainPath}) }); err != nil {
		t.Fatalf("create: %v", err)
	}
	alphaPath := filepath.Join(workspacesRoot, "proj", "alpha")
	if err := os.WriteFile(filepath.Join(alphaPath, "alpha.txt"), []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tick(alphaPath, nil, "jj", "describe", "-m", "alpha work")

	handles, err := listWorkspaceHandles(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"alpha", "default"}; !reflect.DeepEqual(handles, want) {
		t.Fatalf("handles = %v, want %v (no other host's Workspaces)", handles, want)
	}
	if _, _, err := captureOutput(func() error { return runStack([]string{"alpha", "--repo", mainPath}) }); err != nil {
		t.Fatalf("stack: %v", err)
	}
	if _, _, err := captureOutput(func() error { return runClose([]string{"alpha", "--repo", mainPath, "--yes"}) }); err != nil {
		t.Fatalf("close: %v", err)
	}
	// Another host's Workspace is invisible, so its missing path never makes
	// Tidy forget it.
	if err := os.RemoveAll(other); err != nil {
		t.Fatal(err)
	}
	if _, _, err := captureOutput(func() error { return runTidy([]string{"--repo", mainPath, "--yes"}) }); err != nil {
		t.Fatalf("tidy: %v", err)
	}
	if _, _, err := captureOutput(func() error { return runClose([]string{"zulu", "--repo", mainPath, "--yes"}) }); err == nil {
		t.Fatal("another host's Workspace must not be closable")
	}
	cmd := exec.Command("tick", "jj", "-R", mainPath, "--ignore-working-copy", "workspace", "list", "-T", `name ++ "\n"`)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("workspace list: %v\n%s", err, out)
	}
	if got := strings.Fields(string(out)); !reflect.DeepEqual(got, []string{"otherhost/zulu", "testhost/default"}) {
		t.Fatalf("jj workspaces = %v", got)
	}
	cmd = exec.Command("tick", "jj", "-R", mainPath, "--ignore-working-copy", "log", "--no-graph", "-r", `description(substring:"alpha work") & ::testhost/default@`, "-T", `change_id ++ "\n"`)
	if out, err := cmd.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) == "" {
		t.Fatalf("alpha's work should be stacked into the Main Workspace: %v\n%s", err, out)
	}
}
