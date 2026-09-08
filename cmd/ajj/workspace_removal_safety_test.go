package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCloseAndTidyProtectNestedRegisteredWorkspace(t *testing.T) {
	mainPath, parent, _ := setupMutuallyRepresentedCloseRepo(t)
	child := filepath.Join(parent, "child")
	runJJ(t, "-R", mainPath, "workspace", "add", "--name", "child", "--revision", "alpha@-", child)
	marker := filepath.Join(child, "unsnapshotted.txt")
	if err := os.WriteFile(marker, []byte("keep me"), 0644); err != nil {
		t.Fatal(err)
	}
	parentInfo := workspaceInfo{Ref: workspaceRef{Handle: "alpha"}, Path: parent, RepresentedElsewhere: true}
	childInfo := workspaceInfo{Ref: workspaceRef{Handle: "child"}, Path: child, RepresentedElsewhere: true}
	before := currentOperationIDFullForTest(t, mainPath)
	for _, mode := range []string{"normal", "forced", "whole-set", "tidy"} {
		t.Run(mode, func(t *testing.T) {
			var err error
			if mode == "tidy" {
				infos := []workspaceInfo{{Ref: workspaceRef{Handle: "default"}, Path: mainPath, Main: true}, parentInfo}
				err = tidyWorkspaces(mainPath, config{MainWorkspace: "default"}, "proj", infos, true, true)
			} else {
				targets := []workspaceInfo{parentInfo}
				if mode == "whole-set" {
					targets = append(targets, childInfo)
				}
				_, err = closeWorkspaces(mainPath, targets, mode != "normal", true)
			}
			if err == nil || !strings.Contains(err.Error(), "contains registered Workspace") {
				t.Fatalf("expected nested Workspace refusal: %v", err)
			}
			data, readErr := os.ReadFile(marker)
			if readErr != nil || string(data) != "keep me" || before != currentOperationIDFullForTest(t, mainPath) {
				t.Fatalf("refusal mutated repository or child: %v", readErr)
			}
		})
	}
}

func TestRemovalProtectsAncestorsAndSymlinkedRelativeOwner(t *testing.T) {
	root := t.TempDir()
	owner := filepath.Join(root, "owner-parent", "owner")
	linked := filepath.Join(root, "deep", "linked")
	createJJWorkspaceLink(t, owner, linked)
	pointer, err := filepath.Rel(filepath.Join(linked, ".jj"), filepath.Join(owner, ".jj", "repo"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(linked, ".jj", "repo"), []byte(pointer), 0644); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(linked, alias); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{owner, filepath.Dir(owner), linked, filepath.Dir(linked)} {
		info := workspaceInfo{Ref: workspaceRef{Handle: "victim"}, Path: target}
		if err := validateWorkspaceRemovalTarget(alias, info, nil); err == nil {
			t.Fatalf("allowed removal of protected path %s through symlinked workspace", target)
		}
	}
	if workspacePathContains(filepath.Join(root, "parent"), filepath.Join(root, "parent-sibling")) {
		t.Fatal("path containment must respect directory boundaries")
	}
}

func TestTidyMissingRootPreservesReplacementEntry(t *testing.T) {
	for _, kind := range []string{"file", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			workspacesRoot, mainPath := setupRealCreateRepo(t)
			missing := filepath.Join(workspacesRoot, "proj", "missing")
			runJJ(t, "-R", mainPath, "workspace", "add", "--name", "missing", missing)
			if err := os.Rename(missing, filepath.Join(t.TempDir(), "saved-workspace")); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "file":
				if err := os.WriteFile(missing, []byte("unrelated"), 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(filepath.Join(t.TempDir(), "absent"), missing); err != nil {
					t.Fatal(err)
				}
			}
			entry, err := os.Lstat(missing)
			if err != nil {
				t.Fatal(err)
			}
			if err := runTidy([]string{"--repo", mainPath, "--yes"}); err != nil {
				t.Fatal(err)
			}
			after, err := os.Lstat(missing)
			if err != nil || !os.SameFile(entry, after) {
				t.Fatalf("tidy changed replacement %s: %v", kind, err)
			}
			refs, err := listWorkspaceRefs(mainPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, ref := range refs {
				if ref.Handle == "missing" {
					t.Fatal("missing registration was not forgotten")
				}
			}
		})
	}
}

func TestMissingTargetIsForgetOnlyEvenIfPathExists(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "keep")
	if err := os.WriteFile(file, []byte("untouched"), 0644); err != nil {
		t.Fatal(err)
	}
	withCommandCapture(t, func(string, ...string) (string, error) {
		t.Fatal("missing target must not trigger abandonment probes")
		return "", nil
	})
	calls := []string{}
	withCommandToStderr(t, func(_ string, args ...string) error {
		calls = append(calls, strings.Join(args, " "))
		return nil
	})
	_, err := closeWorkspacesWithProtection(t.TempDir(), []workspaceInfo{
		{Ref: workspaceRef{Handle: "missing"}, Path: file, Missing: true},
		{Ref: workspaceRef{Handle: "missing-dir"}, Path: root, Missing: true},
	}, true, true, closeProtectionContext{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "untouched" || len(calls) != 2 || !strings.HasSuffix(calls[0], "workspace forget missing") || !strings.HasSuffix(calls[1], "workspace forget missing-dir") {
		t.Fatalf("missing target was not forget-only: %v, %v", err, calls)
	}
}

func TestCloseAndTidyRejectReplacedWorkspaceRoot(t *testing.T) {
	for _, kind := range []string{"plain", "foreign-repo", "broken-pointer"} {
		t.Run(kind, func(t *testing.T) {
			workspacesRoot, mainPath := setupRealCreateRepo(t)
			safe := filepath.Join(workspacesRoot, "proj", "alpha")
			feature := filepath.Join(workspacesRoot, "proj", "feature")
			runJJ(t, "-R", mainPath, "workspace", "add", "--name", "alpha", safe)
			runJJ(t, "-R", mainPath, "workspace", "add", "--name", "feature", feature)
			if err := os.Rename(feature, filepath.Join(t.TempDir(), "saved-workspace")); err != nil {
				t.Fatal(err)
			}
			if kind == "foreign-repo" {
				runJJ(t, "git", "init", feature)
			} else if err := os.MkdirAll(feature, 0755); err != nil {
				t.Fatal(err)
			}
			if kind == "broken-pointer" {
				if err := os.Mkdir(filepath.Join(feature, ".jj"), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(feature, ".jj", "repo"), []byte("missing-store"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			marker := filepath.Join(feature, "unrelated.txt")
			if err := os.WriteFile(marker, []byte("keep me"), 0644); err != nil {
				t.Fatal(err)
			}
			before := currentOperationIDFullForTest(t, mainPath)
			for _, mode := range []string{"close", "force-close", "tidy", "force-tidy"} {
				t.Run(mode, func(t *testing.T) {
					var err error
					args := []string{"--repo", mainPath, "--yes"}
					if strings.HasPrefix(mode, "force-") {
						args = append(args, "--force")
					}
					if strings.HasSuffix(mode, "tidy") {
						err = runTidy(args)
					} else {
						err = runClose(append([]string{"alpha", "feature"}, args...))
					}
					if err == nil || (!strings.Contains(err.Error(), "repository identity") && !strings.Contains(err.Error(), "different repository")) {
						t.Fatalf("expected repository identity refusal, got %v", err)
					}
					data, readErr := os.ReadFile(marker)
					if readErr != nil || string(data) != "keep me" || !exists(safe) || before != currentOperationIDFullForTest(t, mainPath) {
						t.Fatalf("identity refusal mutated repository or either target: %v", readErr)
					}
				})
			}
		})
	}
}

func TestPostStackCloseNamesPathsAndRequiresExternalConsent(t *testing.T) {
	mainPath, alpha, _ := setupMutuallyRepresentedCloseRepo(t)
	oldIn, oldErr := stdinReader, stderrWriter
	stdinReader = &bytewiseReader{Reader: strings.NewReader("y\nn\n")}
	var output bytes.Buffer
	stderrWriter = &output
	t.Cleanup(func() { stdinReader, stderrWriter = oldIn, oldErr })
	before := currentOperationIDFullForTest(t, mainPath)
	closed, err := closeStackInputs(mainPath, []workspaceInfo{{Ref: workspaceRef{Handle: "alpha"}, Path: alpha, External: true, RepresentedElsewhere: true}})
	if err != nil || len(closed) != 0 || !exists(alpha) || before != currentOperationIDFullForTest(t, mainPath) {
		t.Fatalf("declined external consent mutated repository: closed=%v err=%v", closed, err)
	}
	for _, want := range []string{"alpha: " + alpha, "outside the canonical Project layout"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing %q in prompts: %s", want, output.String())
		}
	}
}
