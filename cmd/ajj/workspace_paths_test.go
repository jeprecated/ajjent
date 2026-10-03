package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCurrentWorkspaceHandleRecognizesDirectoryAliasWithoutSnapshot(t *testing.T) {
	root := t.TempDir()
	physical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(physical, alias); err != nil {
		t.Fatal(err)
	}
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		t.Fatalf("directory identity must resolve the Current Workspace without a snapshotting jj query: %s %v", name, args)
		return "", nil
	})
	refs := []workspaceRef{
		{Handle: "alpha", Root: physical, TargetChange: "shared"},
		{Handle: "bravo", Root: t.TempDir(), TargetChange: "shared"},
	}
	got, err := currentWorkspaceHandle(alias, refs)
	if err != nil || got != "alpha" {
		t.Fatalf("Current Workspace through alias = %q, %v", got, err)
	}
}

func TestWorkspaceLayoutRecognizesAliasedRoot(t *testing.T) {
	workspaces, repo := setupRealCreateRepo(t)
	alias := filepath.Join(t.TempDir(), "workspaces")
	if err := os.Symlink(workspaces, alias); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(workspaces, "proj", "alpha")
	runJJ(t, "-R", repo, "workspace", "add", "--name", "alpha", child)
	outside := filepath.Join(t.TempDir(), "bravo")
	runJJ(t, "-R", repo, "workspace", "add", "--name", "bravo", outside)
	infos, _, err := loadWorkspaceInfos(repo, config{WorkspacesRoot: alias, MainWorkspace: "default"}, "proj")
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range infos {
		if want := info.Ref.Handle == "bravo"; info.External != want {
			t.Errorf("Workspace %s: outside-layout = %v, want %v", info.Ref.Handle, info.External, want)
		}
	}
}

func TestAssimilationLeavesMainWorkspaceAliasUntouched(t *testing.T) {
	main := t.TempDir()
	alias := filepath.Join(t.TempDir(), "main")
	if err := os.Symlink(main, alias); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(main, ".env")
	if err := os.WriteFile(file, []byte("local settings\n"), 0600); err != nil {
		t.Fatal(err)
	}
	links, err := materializeAssimilatedFolderSymlinks(main, alias, config{AssimilatedPaths: []string{".env"}}, "proj")
	if err != nil || len(links) != 0 {
		t.Fatalf("assimilation into Main alias created links: %v, %v", links, err)
	}
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("Main file was replaced: %v, %v", info, err)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "local settings\n" {
		t.Fatalf("Main file content changed: %q, %v", data, err)
	}
}
