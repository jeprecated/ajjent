package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type agentConflictFacsimile struct {
	main, merge string
	paths       map[string]string
}

// Synthetic topology from the reported agent Workspace screenshot, not a copy
// of live payloads/history. Every Workspace is in a disposable test repository.
func setupAgentSiblingConflictFacsimile(t *testing.T) agentConflictFacsimile {
	t.Helper()
	_, main := setupRealCreateRepo(t)
	base := jjFullCommitID(t, main, "default@-")
	writeTrackedCommit(t, main, "base.txt", "synthetic upstream edit")
	left := jjFullCommitID(t, main, "default@-")
	runJJ(t, "-R", main, "new", base)
	writeTrackedCommit(t, main, "other.txt", "synthetic independent branch")
	right := jjFullCommitID(t, main, "default@-")
	runJJ(t, "-R", main, "new", left, right, "-m", "chore: merge")
	merge := jjFullCommitID(t, main, "default@")
	runJJ(t, "-R", main, "new", merge)
	f := agentConflictFacsimile{main: main, merge: merge, paths: map[string]string{"default": main}}
	for _, handle := range []string{"areel-ux", "jev", "overview", "recording", "claude-channel", "summon-areel-plan-status-4a9-09220ec74a23-0", "usage", "multi-open-account"} {
		path := filepath.Join(filepath.Dir(main), handle)
		f.paths[handle] = path
		revision := merge
		if handle == "multi-open-account" {
			revision = base
		}
		runJJ(t, "-R", main, "workspace", "add", "--name", handle, "--revision", revision, path)
	}
	// Materialized fresh usage head, not an empty cursor above a payload.
	if err := os.WriteFile(filepath.Join(f.paths["usage"], "usage.txt"), []byte("synthetic usage work\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", f.paths["usage"], "describe", "-m", "synthetic usage work")
	// Rebase a conflicting edit so this conflict head, too, has the shared merge
	// as its sole parent, matching the reported sibling topology.
	if err := os.WriteFile(filepath.Join(f.paths["multi-open-account"], "base.txt"), []byte("synthetic account edit\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", f.paths["multi-open-account"], "describe", "-m", "synthetic account edit")
	runJJ(t, "-R", f.paths["multi-open-account"], "rebase", "-r", "@", "-d", merge)
	parentCount := jjRevsetCount(t, main, "parents("+merge+")")
	matchingMerge := jjRevsetCount(t, main, `mutable() & empty() & description("chore: merge\n") & `+merge)
	if parentCount != 2 || matchingMerge != 1 {
		t.Fatalf("fixture requires a mutable empty described multiparent merge: parents=%d matching=%d", parentCount, matchingMerge)
	}
	for handle := range f.paths {
		if jjRevsetCount(t, main, handle+"@- & "+merge) != 1 {
			t.Fatalf("%s is not a direct sibling above the shared merge", handle)
		}
	}
	if jjRevsetCount(t, main, `areel-ux@ & empty() & description("")`) != 1 || jjRevsetCount(t, main, "usage@ & ~empty()") != 1 || jjRevsetCount(t, main, "multi-open-account@ & conflicts()") != 1 {
		t.Fatal("fixture does not reproduce clean cursor / usage payload / conflicted sibling")
	}
	// Prove the defect's connecting path, rather than accidentally relying on a
	// conflicted ancestor. Removing the shared merge disconnects the target.
	if jjRevsetCount(t, main, "conflicts() & mutable() & ::areel-ux@") != 0 || jjRevsetCount(t, main, "conflicts() & reachable(areel-ux@, mutable())") != 1 || jjRevsetCount(t, main, "conflicts() & reachable(areel-ux@, mutable() ~ "+merge+")") != 0 {
		t.Fatal("fixture does not isolate sibling traversal through the shared mutable merge")
	}
	return f
}

func TestAgentSiblingConflictDoesNotContaminateTargetHistory(t *testing.T) {
	f := setupAgentSiblingConflictFacsimile(t)
	before := currentOperationIDFullForTest(t, f.main)
	for _, handle := range []string{"default", "areel-ux", "jev", "overview", "recording", "claude-channel", "summon-areel-plan-status-4a9-09220ec74a23-0", "usage"} {
		conflict, err := workspaceHasConflictCommits(f.main, handle)
		if err != nil || conflict {
			t.Errorf("clean %s inherited sibling conflict: conflict=%v err=%v", handle, conflict, err)
		}
	}
	conflict, err := workspaceHasConflictCommits(f.main, "multi-open-account")
	if err != nil || !conflict {
		t.Fatal("actual target-head conflict was hidden")
	}
	if currentOperationIDFullForTest(t, f.main) != before {
		t.Fatal("conflict probes changed history")
	}
}

func TestAgentSiblingConflictListTidyAndPreviewClassification(t *testing.T) {
	f := setupAgentSiblingConflictFacsimile(t)
	markDisposableForTest(t, f.main, "areel-ux", "usage", "multi-open-account")
	infos := policyInfosForTest(t, f.main, "proj")
	by := mapInfosByHandle(infos)
	target := by["areel-ux"]
	if target.Conflict || !target.Empty || !isClosable(target) || statusLabel(target) != "empty" {
		t.Errorf("clean represented target misclassified: %+v", target)
	}
	if by["usage"].Conflict || by["usage"].RepresentedElsewhere || isClosable(by["usage"]) {
		t.Errorf("unique nonconflicted usage misclassified: %+v", by["usage"])
	}
	items := mapSelectorItemsByHandle(selectorItemsForTidy(infos, false))
	if !items["areel-ux"].Selected || items["areel-ux"].Disabled || items["multi-open-account"].Selected || items["usage"].Selected {
		t.Errorf("normal Tidy selected wrong siblings: %+v", items)
	}
	targets := tidyTargets(infos, false)
	if len(targets) != 1 || targets[0].Ref.Handle != "areel-ux" {
		t.Errorf("automatic Tidy did not isolate the eligible Disposable cursor: %v", workspaceHandleList(targets))
	}
	out, _, err := captureOutput(func() error { return runList([]string{"--repo", f.main}) })
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) >= 5 {
			actions[fields[0]] = fields[4]
		}
	}
	if actions["areel-ux"] != "ok" {
		t.Errorf("list reports false conflict or omits clean target: %s", out)
	}
	if actions["multi-open-account"] != "resolve-conflict" {
		t.Errorf("list hides genuine conflict: %s", out)
	}
	review, err := tidyGraphReview(f.main, infos, currentOperationIDFullForTest(t, f.main))
	if err != nil {
		t.Fatal(err)
	}
	preview, err := review.preview(context.Background(), "areel-ux", nil)
	if err != nil || strings.Contains(preview, "Conflicts at review") {
		t.Errorf("preview falsely attributes sibling conflict: %v\n%s", err, preview)
	}
}

func TestAgentSiblingConflictNormalCloseFromCurrentWithoutArguments(t *testing.T) {
	f := setupAgentSiblingConflictFacsimile(t)
	conflictHead := jjFullCommitID(t, f.main, "multi-open-account@")
	usageHead := jjFullCommitID(t, f.main, "usage@")
	conflictFile := filepath.Join(f.paths["multi-open-account"], "base.txt")
	before, err := os.ReadFile(conflictFile)
	if err != nil {
		t.Fatal(err)
	}
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(f.paths["areel-ux"]); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
	oldIn := stdinReader
	stdinReader = strings.NewReader("y\n")
	t.Cleanup(func() { stdinReader = oldIn })
	_, prompt, closeErr := captureOutput(func() error { return runClose(nil) })
	if err := os.Chdir(oldwd); err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatalf("normal argument-free Close refused clean Current Workspace: %v\nprompt: %s", closeErr, prompt)
	}
	if strings.Contains(prompt, "Force close") || strings.Contains(prompt, "Forced Closing") || !strings.Contains(prompt, "areel-ux (empty)") {
		t.Fatalf("expected normal close consent, not force: %s", prompt)
	}
	if exists(f.paths["areel-ux"]) {
		t.Fatal("normally closable Current Workspace survived")
	}
	refs, err := listWorkspaceRefs(f.main)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != len(f.paths)-1 {
		t.Fatalf("unexpected registration removal: %+v", refs)
	}
	for _, ref := range refs {
		if ref.Handle == "areel-ux" {
			t.Fatal("closed target still registered")
		}
	}
	if jjFullCommitID(t, f.main, "multi-open-account@") != conflictHead || jjRevsetCount(t, f.main, "conflicts() & "+conflictHead) != 1 || jjFullCommitID(t, f.main, "usage@") != usageHead {
		t.Fatal("unrelated sibling history was changed or abandoned")
	}
	after, err := os.ReadFile(conflictFile)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("unrelated conflicted files were changed")
	}
	usage, err := os.ReadFile(filepath.Join(f.paths["usage"], "usage.txt"))
	if err != nil || string(usage) != "synthetic usage work\n" {
		t.Fatal("unrelated unique work was lost")
	}
	if jjRevsetCount(t, f.main, f.merge+" & ::default@") != 1 {
		t.Fatal("shared relevant merge history was lost")
	}
}

func TestAgentSiblingConflictAutomaticTidyClosesOnlyOptedInCleanCursor(t *testing.T) {
	f := setupAgentSiblingConflictFacsimile(t)
	markDisposableForTest(t, f.main, "areel-ux")
	conflict := jjFullCommitID(t, f.main, "multi-open-account@")
	usage := jjFullCommitID(t, f.main, "usage@")
	before, err := os.ReadFile(filepath.Join(f.paths["multi-open-account"], "base.txt"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = captureOutput(func() error { return runTidy([]string{"--repo", f.main, "--yes"}) })
	if err != nil || exists(f.paths["areel-ux"]) {
		t.Fatalf("normal Tidy failed to close the eligible clean cursor: %v", err)
	}
	refs, err := listWorkspaceRefs(f.main)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != len(f.paths)-1 {
		t.Fatal("unexpected Tidy registration removal")
	}
	for handle, path := range f.paths {
		if handle != "areel-ux" && !exists(path) {
			t.Fatalf("Tidy removed unrelated %s", handle)
		}
	}
	if jjFullCommitID(t, f.main, "multi-open-account@") != conflict || jjFullCommitID(t, f.main, "usage@") != usage {
		t.Fatal("Tidy rewrote unrelated work")
	}
	after, err := os.ReadFile(filepath.Join(f.paths["multi-open-account"], "base.txt"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("Tidy changed conflicted sibling files")
	}
}

func TestAgentConflictScopeStillProtectsActualConflictsAndUniqueWork(t *testing.T) {
	for _, kind := range []string{"head-conflict", "mutable-ancestor-conflict", "unique-clean-work"} {
		t.Run(kind, func(t *testing.T) {
			f := setupAgentSiblingConflictFacsimile(t)
			handle := "multi-open-account"
			if kind == "mutable-ancestor-conflict" {
				handle = "areel-ux"
				runJJ(t, "-R", f.paths[handle], "new", "multi-open-account@")
				if err := os.WriteFile(filepath.Join(f.paths[handle], "base.txt"), []byte("synthetic descendant resolution\n"), 0644); err != nil {
					t.Fatal(err)
				}
				runJJ(t, "-R", f.paths[handle], "describe", "-m", "synthetic descendant resolution")
				if jjRevsetCount(t, f.main, "conflicts() & "+handle+"@") != 0 || jjRevsetCount(t, f.main, "conflicts() & mutable() & ::"+handle+"@") != 1 {
					t.Fatal("fixture must have a clean head with a genuine conflicted mutable ancestor")
				}
			}
			if kind == "unique-clean-work" {
				handle = "usage"
			} else {
				// Conflict alone must block, even when another registration represents all
				// work. Otherwise a unique-work check could conceal a weakened conflict rule.
				runJJ(t, "-R", f.main, "workspace", "add", "--name", "protector", "--revision", handle+"@", filepath.Join(filepath.Dir(f.main), "protector"))
			}
			infos := policyInfosForTest(t, f.main, "proj")
			target := mapInfosByHandle(infos)[handle]
			if kind == "unique-clean-work" {
				if target.RepresentedElsewhere || jjRevsetCount(t, f.main, "conflicts() & ::usage@") != 0 {
					t.Fatal("fixture must contain unique nonconflicted work")
				}
			} else if !target.Conflict || !target.RepresentedElsewhere {
				t.Fatalf("represented genuine conflict not detected: %+v", target)
			}
			if isClosable(target) {
				t.Fatal("unsafe target became normally closable")
			}
			before := currentOperationIDFullForTest(t, f.main)
			head := jjFullCommitID(t, f.main, handle+"@")
			data, err := os.ReadFile(filepath.Join(f.paths[handle], "base.txt"))
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = captureOutput(func() error { return runClose([]string{"--repo", f.paths[handle], "--yes"}) })
			if err == nil || !strings.Contains(err.Error(), "not normally closable") {
				t.Fatalf("unsafe normal Close accepted: %v", err)
			}
			if before != currentOperationIDFullForTest(t, f.main) || head != jjFullCommitID(t, f.main, handle+"@") || !exists(f.paths[handle]) {
				t.Fatal("refusal mutated history, registration, or Workspace")
			}
			after, err := os.ReadFile(filepath.Join(f.paths[handle], "base.txt"))
			if err != nil || !bytes.Equal(data, after) {
				t.Fatal("refusal changed target files")
			}
		})
	}
}

func TestAgentConflictScopeLineStackAdvance(t *testing.T) {
	for _, conflicted := range []bool{false, true} {
		t.Run(map[bool]string{false: "clean-sibling", true: "actual-conflicted-tip"}[conflicted], func(t *testing.T) {
			f := setupAgentSiblingConflictFacsimile(t)
			conflict := jjFullCommitID(t, f.main, "multi-open-account@")
			tip := f.merge
			if conflicted {
				tip = conflict
			}
			// Exercise the other production consumer: post-advance validation. No
			// payload rebase is needed to isolate this guard from Stack planning.
			plan := lineStackPlan{FinalTip: tip, Advances: []lineStackAdvance{{Handle: "areel-ux", Path: f.paths["areel-ux"]}}}
			_, _, err := captureOutput(func() error { return executeLineStackPlan(f.main, plan) })
			if conflicted {
				if err == nil || !strings.Contains(err.Error(), "stopped with conflicts") {
					t.Fatalf("Line Stack suppressed a genuine conflict: %v", err)
				}
			} else if err != nil {
				t.Fatalf("Line Stack inherited an unrelated sibling conflict: %v", err)
			}
			if jjFullCommitID(t, f.main, "multi-open-account@") != conflict {
				t.Fatal("advancing another Workspace changed conflicted sibling")
			}
		})
	}
}

func TestAgentSiblingConflictDoesNotPreventNonMainChildClosure(t *testing.T) {
	f := setupAgentSiblingConflictFacsimile(t)
	child := filepath.Join(filepath.Dir(f.main), "completed-child")
	runJJ(t, "-R", f.main, "workspace", "add", "--name", "completed-child", "--revision", "areel-ux@", child)
	writeTrackedCommit(t, child, "child.txt", "synthetic completed child work")
	payload := jjFullCommitID(t, f.main, "completed-child@-")
	runJJ(t, "-R", f.paths["areel-ux"], "new", payload)
	for _, protector := range []struct {
		handle string
		safe   bool
	}{{"default", false}, {"areel-ux", true}} {
		safe, err := workspaceRepresentedElsewhere(f.main, "completed-child", []string{protector.handle})
		if err != nil || safe != protector.safe {
			t.Fatalf("unexpected protection by %s: %v %v", protector.handle, safe, err)
		}
	}
	info := mapInfosByHandle(policyInfosForTest(t, f.main, "proj"))["completed-child"]
	if info.Conflict || info.Stacked || !isClosable(info) {
		t.Fatalf("non-Main represented child not normally closable: %+v", info)
	}
	conflict := jjFullCommitID(t, f.main, "multi-open-account@")
	_, _, err := captureOutput(func() error { return runClose([]string{"--repo", f.main, "--yes", "completed-child"}) })
	if err != nil || exists(child) || !exists(f.paths["areel-ux"]) {
		t.Fatalf("failed to close only completed child: %v", err)
	}
	if jjRevsetCount(t, f.main, payload+" & ::areel-ux@") != 1 || jjFullCommitID(t, f.main, "multi-open-account@") != conflict {
		t.Fatal("child closure lost parent or sibling history")
	}
}
