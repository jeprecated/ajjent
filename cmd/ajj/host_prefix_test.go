package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// withHostPrefix sets this host's prefix for an ordinary repo for one test.
func withHostPrefix(t *testing.T, prefix string) {
	t.Helper()
	origPrefix, origHandles := hostPrefix, hostPrefixedHandles
	hostPrefix, hostPrefixedHandles = prefix, map[string]bool{}
	t.Cleanup(func() { hostPrefix, hostPrefixedHandles = origPrefix, origHandles })
}

func TestOrdinaryRepoMapsThisHostsPrefixedNamesToHandles(t *testing.T) {
	withAgentTickPrefix(t, "")
	withHostPrefix(t, "lattice/")
	if got := jjWorkspaceName("t3-5bd925cb"); got != "t3-5bd925cb" {
		t.Fatalf("unread Handle is named unprefixed, got %q", got)
	}
	for name, want := range map[string]string{
		"lattice/t3-5bd925cb": "t3-5bd925cb",
		"default":             "default",
		"prism/t3-5bd925cb":   "prism/t3-5bd925cb",
		"lattice/":            "lattice/",
		"latticex/alpha":      "latticex/alpha",
	} {
		if handle, ours := handleFromJJWorkspaceName(name); !ours || handle != want {
			t.Errorf("handleFromJJWorkspaceName(%q) = %q, %v; want %q, true", name, handle, ours, want)
		}
	}
	for handle, want := range map[string]string{"t3-5bd925cb": "lattice/t3-5bd925cb", "default": "default", "prism/t3-5bd925cb": "prism/t3-5bd925cb"} {
		if got := jjWorkspaceName(handle); got != want {
			t.Errorf("jjWorkspaceName(%q) = %q, want %q", handle, got, want)
		}
	}
}

func TestListWorkspaceRefsRejectsPrefixedAndBareNamesForOneHandle(t *testing.T) {
	withAgentTickPrefix(t, "")
	withHostPrefix(t, "lattice/")
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		return "default\tabc\t/repo\nt3-x\tdef\t/ws/a\nlattice/t3-x\tghi\t/ws/b\n", nil
	})
	if _, err := listWorkspaceRefs("/repo"); err == nil || !strings.Contains(err.Error(), `jj workspaces "t3-x" and "lattice/t3-x" are both Workspace "t3-x"`) {
		t.Fatalf("expected a collision error, got %v", err)
	}
}

func TestOrdinaryRepoHostPrefixedWorkspaceIsInLayoutAndTidies(t *testing.T) {
	t.Setenv("TICK_HOST", "lattice")
	mainPath, alphaPath, _ := setupMutuallyRepresentedCloseRepo(t)
	path := filepath.Join(filepath.Dir(alphaPath), "t3-5bd925cb")
	runJJ(t, "-R", mainPath, "workspace", "add", "--revision", "@-", "--name", "lattice/t3-5bd925cb", path)
	markDisposableForTest(t, mainPath, "t3-5bd925cb")
	stdout, _, err := captureOutput(func() error { return runList([]string{"--repo", mainPath}) })
	if err != nil {
		t.Fatal(err)
	}
	var row string
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "t3-5bd925cb\t") {
			row = line
		}
	}
	if fields := strings.Split(row, "\t"); len(fields) < 2 || fields[1] != "-" {
		t.Fatalf("want an in-layout t3-5bd925cb row, got %q in:\n%s", row, stdout)
	}
	if _, diagnostics, err := captureOutput(func() error { return runTidy([]string{"--repo", mainPath, "--yes"}) }); err != nil {
		t.Fatalf("tidy: %v\n%s", err, diagnostics)
	}
	if exists(path) || workspaceRegistered(t, mainPath, "lattice/t3-5bd925cb") {
		t.Fatal("Tidy did not close lattice/t3-5bd925cb")
	}
}
