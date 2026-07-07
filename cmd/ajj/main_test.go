package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func TestLoadConfigRejectsLegacyUnknownKeys(t *testing.T) {
	repoRoot := t.TempDir()
	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "ajj"), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := []byte("worktrees_root: /tmp/worktrees\nname_list:\n  - alpha\n")
	if err := os.WriteFile(filepath.Join(xdg, "ajj", "config.yaml"), legacy, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)
	_, err := loadConfig(repoRoot)
	if err == nil || !strings.Contains(err.Error(), "field worktrees_root not found") {
		t.Fatalf("expected unknown legacy key error, got %v", err)
	}
}

func TestLoadConfigUsesAssimilatedPathsVocabulary(t *testing.T) {
	repoRoot := t.TempDir()
	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "ajj"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgText := []byte(strings.Join([]string{
		"workspaces_root: /tmp/workspaces",
		"project: my-project",
		"handle_strategy: next-unused",
		"workspace_handles:",
		"  - kilo",
		"main_workspace: default",
		"assimilated_paths:",
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
		"    assimilated_paths:",
		"      - .local-notes",
		"",
	}, "\n"))
	if err := os.WriteFile(filepath.Join(xdg, "ajj", "config.yaml"), cfgText, 0o644); err != nil {
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
	paths := effectiveAssimilatedPaths(cfg, "my-project")
	if strings.Join(paths, ",") != "scratch,.local-notes" {
		t.Fatalf("unexpected assimilated paths: %v", paths)
	}
}

func TestLoadConfigAcceptsDeprecatedAssimilatedFoldersAlias(t *testing.T) {
	repoRoot := t.TempDir()
	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "ajj"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgText := []byte(strings.Join([]string{
		"workspaces_root: /tmp/workspaces",
		"assimilated_folders:",
		"  - scratch",
		"projects:",
		"  proj:",
		"    assimilated_folders:",
		"      - .local-tools",
		"",
	}, "\n"))
	if err := os.WriteFile(filepath.Join(xdg, "ajj", "config.yaml"), cfgText, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)
	cfg, err := loadConfig(repoRoot)
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	paths := effectiveAssimilatedPaths(cfg, "proj")
	if strings.Join(paths, ",") != "scratch,.local-tools" {
		t.Fatalf("unexpected assimilated paths from deprecated alias: %v", paths)
	}
}

func TestLoadConfigMergesGlobalAndProjectAssimilatedPaths(t *testing.T) {
	repoRoot := t.TempDir()
	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "ajj"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgText := []byte(strings.Join([]string{
		"workspaces_root: /tmp/workspaces",
		"assimilated_paths:",
		"  - scratch",
		"  - logs",
		"projects:",
		"  proj:",
		"    assimilated_paths:",
		"      - logs",
		"      - .local-tools",
		"",
	}, "\n"))
	if err := os.WriteFile(filepath.Join(xdg, "ajj", "config.yaml"), cfgText, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)
	cfg, err := loadConfig(repoRoot)
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	paths := effectiveAssimilatedPaths(cfg, "proj")
	if strings.Join(paths, ",") != "scratch,logs,.local-tools" {
		t.Fatalf("unexpected merged assimilated paths: %v", paths)
	}
}

func TestLoadConfigAcceptsAssimilatedPathGlobs(t *testing.T) {
	repoRoot := t.TempDir()
	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "ajj"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgText := []byte("workspaces_root: /tmp/workspaces\nassimilated_paths:\n  - '**/.env*'\n")
	if err := os.WriteFile(filepath.Join(xdg, "ajj", "config.yaml"), cfgText, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)
	cfg, err := loadConfig(repoRoot)
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	paths := effectiveAssimilatedPaths(cfg, "proj")
	if strings.Join(paths, ",") != "**/.env*" {
		t.Fatalf("unexpected assimilated glob paths: %v", paths)
	}
}

func TestLoadConfigRejectsUnsafeAssimilatedPaths(t *testing.T) {
	repoRoot := t.TempDir()
	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "ajj"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgText := []byte("workspaces_root: /tmp/workspaces\nassimilated_paths:\n  - ../scratch\n")
	if err := os.WriteFile(filepath.Join(xdg, "ajj", "config.yaml"), cfgText, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)
	_, err := loadConfig(repoRoot)
	if err == nil || !strings.Contains(err.Error(), "assimilated_paths") {
		t.Fatalf("expected unsafe assimilated_paths error, got %v", err)
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

func TestRunUnknownCommandSuggestsHelp(t *testing.T) {
	err := run([]string{"wat"})
	if err == nil || !strings.Contains(err.Error(), "ajj help") {
		t.Fatalf("expected help suggestion, got %v", err)
	}
}

func TestRunAcceptsGlobalRepoFlag(t *testing.T) {
	repoRoot := t.TempDir()
	workspacesRoot := filepath.Join(t.TempDir(), "workspaces")
	mainPath := filepath.Join(workspacesRoot, "proj", "default")
	if err := os.MkdirAll(mainPath, 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, repoRoot, "workspaces_root: "+workspacesRoot+"\nproject: proj\nmain_workspace: default\n")
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "workspace list") {
			if len(args) < 2 || args[0] != "-R" || args[1] != repoRoot {
				t.Fatalf("expected jj -R %s, got jj %s", repoRoot, joined)
			}
			return "default\tmain111\t" + mainPath + "\n", nil
		}
		return "", nil
	})
	out, err := captureStdout(func() error { return run([]string{"--repo", repoRoot, "list", "--paths"}) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != mainPath {
		t.Fatalf("expected main path from global --repo, got %q", out)
	}
}

func TestRunRejectsGlobalAndCommandRepoFlagsTogether(t *testing.T) {
	err := run([]string{"--repo", "/repo-a", "list", "--repo", "/repo-b", "--paths"})
	if err == nil || !strings.Contains(err.Error(), "either before the command or in command options") {
		t.Fatalf("expected conflicting --repo flags error, got %v", err)
	}
}

func TestRunRejectsDuplicateRepoFlags(t *testing.T) {
	cases := [][]string{
		{"--repo", "/repo-a", "--repo", "/repo-b", "list"},
		{"list", "--repo", "/repo-a", "--repo", "/repo-b"},
	}
	for _, args := range cases {
		err := run(args)
		if err == nil || !strings.Contains(err.Error(), "provide --repo at most once") {
			t.Fatalf("expected duplicate --repo error for %v, got %v", args, err)
		}
	}
}

func TestRunRejectsGlobalRepoFlagWithoutValue(t *testing.T) {
	err := run([]string{"--repo"})
	if err == nil || !strings.Contains(err.Error(), "requires a value") {
		t.Fatalf("expected missing --repo value error, got %v", err)
	}
}

func TestShellInitPrintsNavigationWrapperIncludingMain(t *testing.T) {
	out, err := captureStdout(func() error { return run([]string{"shell-init", "zsh"}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ajj()", "--repo=*)", "cmd=\"$3\"", "create|open|close|main", "AJJ_SHELL_WRAPPED=1 command ajj", "cd \"$out\""} {
		if !strings.Contains(out, want) {
			t.Fatalf("shell init missing %q in:\n%s", want, out)
		}
	}
}

func TestNavigationHintMentionsShellInitForRawMain(t *testing.T) {
	hint := navigationHint("main", "zsh")
	for _, want := range []string{"ajj main", "cd automatically", "eval \"$(ajj shell-init zsh)\""} {
		if !strings.Contains(hint, want) {
			t.Fatalf("navigation hint missing %q in %q", want, hint)
		}
	}
}

func TestRunShellInitRejectsUnknownShell(t *testing.T) {
	err := run([]string{"shell-init", "fish"})
	if err == nil || !strings.Contains(err.Error(), "expected bash or zsh") {
		t.Fatalf("expected shell rejection, got %v", err)
	}
}

func TestSelectorHintMentionsForceToggleInsteadOfReRun(t *testing.T) {
	hint := selectorHint(selectorOptions{Mode: selectorMulti, AllowForceToggle: true})
	if !strings.Contains(hint, "Press f") || !strings.Contains(hint, "instead of re-running") {
		t.Fatalf("expected force-toggle hint, got %q", hint)
	}
}

func TestStackSelectorHintExplainsAllRowDoesNotOverrideCheckedBoxes(t *testing.T) {
	hint := selectorHint(selectorOptions{Mode: selectorMulti, AllDefault: true})
	if !strings.Contains(hint, "only when no boxes are checked") {
		t.Fatalf("expected All-row caveat, got %q", hint)
	}
}

func TestTidySelectorHintExplainsPreselectedTidyRows(t *testing.T) {
	hint := selectorHint(selectorOptions{Mode: selectorMulti, Tidy: true})
	if !strings.Contains(hint, "Tidy rows start checked") || !strings.Contains(hint, "leave one alone") {
		t.Fatalf("expected tidy preselection hint, got %q", hint)
	}
}

func TestStackPlanPromptShowsExactInputsAndOptions(t *testing.T) {
	prompt := stackPlanPrompt([]string{"alpha", "charlie"}, stackConfig{Shape: "merge", RebaseMode: "revision", ConflictStrategy: "off"})
	for _, want := range []string{"Stack 2 Workspaces", "alpha, charlie", "shape:merge", "rebase:revision", "conflicts:off"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected %q in prompt %q", want, prompt)
		}
	}
	if strings.Contains(prompt, "bravo") {
		t.Fatalf("prompt should only show selected inputs, got %q", prompt)
	}
}

func TestHelpMentionsLineStacking(t *testing.T) {
	stackOut, err := captureStdout(func() error { return runStack([]string{"--help"}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--line", "--workspace", "target Workspace", "current --repo/cwd Workspace", "Line Stack", "ordered Line Stacking"} {
		if !strings.Contains(stackOut, want) {
			t.Fatalf("expected stack help to mention %q, got %q", want, stackOut)
		}
	}
	topOut, err := captureStdout(func() error { return run([]string{"help"}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"stack --line [handle...]", "move-to-main [handle...]"} {
		if !strings.Contains(topOut, want) {
			t.Fatalf("expected top-level help to mention %q, got %q", want, topOut)
		}
	}
}

func TestShouldConfirmStackPlanOnlyForInteractiveSelectorWithoutYes(t *testing.T) {
	cases := []struct {
		name         string
		selectorUsed bool
		yes          bool
		canUseTUI    bool
		want         bool
	}{
		{name: "interactive selector", selectorUsed: true, canUseTUI: true, want: true},
		{name: "yes skips", selectorUsed: true, yes: true, canUseTUI: true, want: false},
		{name: "positional skips", selectorUsed: false, canUseTUI: true, want: false},
		{name: "non tty skips", selectorUsed: true, canUseTUI: false, want: false},
	}
	for _, tc := range cases {
		if got := shouldConfirmStackPlan(tc.selectorUsed, tc.yes, tc.canUseTUI); got != tc.want {
			t.Fatalf("%s: expected %v, got %v", tc.name, tc.want, got)
		}
	}
}

func TestHumanFacingStylesUseOutputRendererEvenWhenStdoutIsPlain(t *testing.T) {
	defaultRenderer := lipgloss.DefaultRenderer()
	plainDefault := lipgloss.NewRenderer(io.Discard)
	plainDefault.SetColorProfile(termenv.Ascii)
	lipgloss.SetDefaultRenderer(plainDefault)
	t.Cleanup(func() { lipgloss.SetDefaultRenderer(defaultRenderer) })

	renderer := lipgloss.NewRenderer(io.Discard)
	renderer.SetColorProfile(termenv.ANSI256)
	if got := cliStylesForRenderer(renderer).Danger.Render("ajj:"); !strings.Contains(got, "\x1b[") {
		t.Fatalf("expected CLI stderr style to use output renderer color, got %q", got)
	}
	if got := selectorStylesForRenderer(renderer, false).Empty.Render("empty"); !strings.Contains(got, "\x1b[") {
		t.Fatalf("expected selector style to use output renderer color, got %q", got)
	}
}

func TestMoveToMainSelectorPreselectsOnlyMovableWorkspaces(t *testing.T) {
	items := selectorItemsForMoveToMain([]workspaceInfo{
		{Ref: workspaceRef{Handle: "default"}, Main: true},
		{Ref: workspaceRef{Handle: "alpha"}, Empty: true, Behind: 2},
		{Ref: workspaceRef{Handle: "bravo"}, Empty: true},
		{Ref: workspaceRef{Handle: "billing"}, Ahead: 1, Behind: 2},
		{Ref: workspaceRef{Handle: "missing"}, Missing: true},
	})
	byHandle := mapSelectorItemsByHandle(items)
	if !byHandle["alpha"].Selected || byHandle["alpha"].Disabled || byHandle["alpha"].Status != "move-to-main" {
		t.Fatalf("expected alpha selected as movable, got %+v", byHandle["alpha"])
	}
	for _, handle := range []string{"bravo", "billing", "missing"} {
		if byHandle[handle].Selected || !byHandle[handle].Disabled {
			t.Fatalf("expected %s disabled and not preselected, got %+v", handle, byHandle[handle])
		}
	}
}

func TestMoveToMainSelectorPreselectsWorkspaceBehindDescribedEmptyMerge(t *testing.T) {
	items := selectorItemsForMoveToMain([]workspaceInfo{
		{Ref: workspaceRef{Handle: "default"}, Main: true, Empty: true},
		{Ref: workspaceRef{Handle: "helper"}, Empty: true, Behind: 1},
		{Ref: workspaceRef{Handle: "research"}, Empty: true},
	})
	byHandle := mapSelectorItemsByHandle(items)
	if !byHandle["helper"].Selected || byHandle["helper"].Disabled || byHandle["helper"].Status != "move-to-main" {
		t.Fatalf("expected cursor behind a described empty merge to be selected, got %+v", byHandle["helper"])
	}
	if byHandle["research"].Selected || !byHandle["research"].Disabled || byHandle["research"].Status != "up-to-main" {
		t.Fatalf("expected cursor with no relevant changes behind to stay disabled, got %+v", byHandle["research"])
	}
}

func TestTidySelectorPreselectsOnlyClosableWorkspaces(t *testing.T) {
	items := selectorItemsForTidy([]workspaceInfo{
		{Ref: workspaceRef{Handle: "default"}, Main: true},
		{Ref: workspaceRef{Handle: "empty"}, Empty: true},
		{Ref: workspaceRef{Handle: "stacked"}, Stacked: true},
		{Ref: workspaceRef{Handle: "unstacked"}, Ahead: 1},
		{Ref: workspaceRef{Handle: "conflict"}, Conflict: true},
		{Ref: workspaceRef{Handle: "missing"}, Missing: true},
	})
	byHandle := mapSelectorItemsByHandle(items)
	for _, handle := range []string{"empty", "stacked"} {
		if !byHandle[handle].Selected || byHandle[handle].Disabled {
			t.Fatalf("expected %s selected as tidy, got %+v", handle, byHandle[handle])
		}
	}
	for _, handle := range []string{"unstacked", "conflict", "missing"} {
		if byHandle[handle].Selected || !byHandle[handle].Disabled {
			t.Fatalf("expected %s disabled and not selected, got %+v", handle, byHandle[handle])
		}
	}
	if _, ok := byHandle["default"]; ok {
		t.Fatal("Main Workspace should not appear in tidy selector")
	}
}

func TestWorkspaceAheadBehindRevsetsIgnoreOnlyEmptyUndescribedChanges(t *testing.T) {
	wantRelevant := `~(empty() & description(""))`
	if got := workspaceRelevantRevset(); got != wantRelevant {
		t.Fatalf("expected relevant revset %q, got %q", wantRelevant, got)
	}
	if got := workspaceAheadRevset("alpha", "default"); !strings.Contains(got, wantRelevant) || strings.Contains(got, "~empty()") {
		t.Fatalf("expected ahead revset to ignore only empty undescribed changes, got %q", got)
	}
	if got := workspaceBehindRevset("alpha", "default"); !strings.Contains(got, wantRelevant) || strings.Contains(got, "~empty()") {
		t.Fatalf("expected behind revset to ignore only empty undescribed changes, got %q", got)
	}
}

func TestPreselectedMultiSelectorSubmitsDefaultCheckedRows(t *testing.T) {
	model := selectorModel{
		opts: selectorOptions{Mode: selectorMulti, MoveToMain: true, Items: []selectorItem{
			{Handle: "alpha", Selected: true},
			{Handle: "bravo", Selected: true},
			{Handle: "billing", Disabled: true},
		}},
		selected: map[int]bool{0: true, 1: true},
	}
	model = model.submit()
	if got := selectorHandles(model.result.Items); strings.Join(got, ",") != "alpha,bravo" {
		t.Fatalf("expected preselected handles, got %v", got)
	}
}

func TestMultiSelectorEnterDoesNotReAddExcludedCursorItem(t *testing.T) {
	model := selectorModel{
		opts: selectorOptions{Mode: selectorMulti, Items: []selectorItem{
			{Handle: "All", All: true},
			{Handle: "alpha"},
			{Handle: "bravo"},
			{Handle: "charlie"},
		}},
		cursor:   2, // bravo is highlighted but intentionally not selected.
		selected: map[int]bool{1: true, 3: true},
	}
	model = model.submit()
	if got := selectorHandles(model.result.Items); strings.Join(got, ",") != "alpha,charlie" {
		t.Fatalf("expected explicit selections without cursor item, got %v", got)
	}
}

func TestMultiSelectorAllRowRespectsExplicitSelections(t *testing.T) {
	model := selectorModel{
		opts: selectorOptions{Mode: selectorMulti, Items: []selectorItem{
			{Handle: "All", All: true},
			{Handle: "alpha"},
			{Handle: "bravo"},
			{Handle: "charlie"},
		}},
		cursor:   0,
		selected: map[int]bool{1: true, 3: true},
	}
	model = model.submit()
	if got := selectorHandles(model.result.Items); strings.Join(got, ",") != "alpha,charlie" {
		t.Fatalf("expected explicit selections from All row, got %v", got)
	}
}

func TestOrderedMultiSelectorPreservesSelectionOrderAndReselectsAtEnd(t *testing.T) {
	model := selectorModel{
		opts: selectorOptions{Mode: selectorMulti, OrderedSelection: true, AllowRoleToggle: true, Items: []selectorItem{
			{Handle: "alpha", Status: "unstacked"},
			{Handle: "bravo", Status: "unstacked"},
			{Handle: "charlie", Status: "unstacked"},
		}},
	}
	model.toggleSelection(1) // bravo first
	model.toggleSelection(0) // alpha second
	model.toggleSelection(1) // bravo removed
	model.toggleSelection(2) // charlie third
	model.toggleSelection(1) // bravo reselected at the end

	model = model.submit()
	if got := selectorHandles(model.result.Items); strings.Join(got, ",") != "alpha,charlie,bravo" {
		t.Fatalf("expected ordered selections with reselected item at end, got %v", got)
	}
	if got := selectorRoles(model.result.Items); strings.Join(got, ",") != "payload,payload,payload" {
		t.Fatalf("expected selected unstacked Workspaces to default to payload, got %v", got)
	}
}

func TestOrderedLineSelectorRoleToggleAndFollowOnly(t *testing.T) {
	model := selectorModel{
		opts: selectorOptions{Mode: selectorMulti, OrderedSelection: true, AllowRoleToggle: true, Items: []selectorItem{
			{Handle: "alpha", Status: "unstacked"},
			{Handle: "empty", Status: "empty", Role: selectorRoleFollow},
			{Handle: "stacked", Status: "stacked", Role: selectorRoleFollow},
		}},
	}
	model.toggleSelection(0)
	model.toggleSelectedRole(0)
	model.toggleSelection(1)
	model.toggleSelection(2)

	model = model.submit()
	if got := selectorHandles(model.result.Items); strings.Join(got, ",") != "alpha,empty,stacked" {
		t.Fatalf("expected ordered handles, got %v", got)
	}
	if got := selectorRoles(model.result.Items); strings.Join(got, ",") != "follow,follow,follow" {
		t.Fatalf("expected toggled alpha plus empty/stacked follow-only roles, got %v", got)
	}
}

func TestLineStackSelectorViewShowsOrderAndRole(t *testing.T) {
	model := selectorModel{
		opts: selectorOptions{Title: "Line Stack Workspaces", Mode: selectorMulti, OrderedSelection: true, AllowRoleToggle: true, Items: []selectorItem{
			{Handle: "alpha", Status: "unstacked", Markers: "-", Role: selectorRolePayload, Path: "/repo/alpha"},
			{Handle: "empty", Status: "empty", Markers: "-", Role: selectorRoleFollow, Path: "/repo/empty"},
			{Handle: "missing", Status: "missing", Markers: "-", Disabled: true, Path: "/repo/missing"},
		}},
		selected:      map[int]bool{0: true, 1: true},
		selectedOrder: []int{1, 0},
	}
	view := model.View()
	for _, want := range []string{"[2:P] alpha", "[1:F] empty", "a toggle payload/follow", "selection order defines the line"} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected line-stack selector view to contain %q, got %q", want, view)
		}
	}
}

func TestUnorderedMultiSelectorStillUsesItemOrder(t *testing.T) {
	model := selectorModel{
		opts: selectorOptions{Mode: selectorMulti, Items: []selectorItem{
			{Handle: "alpha"},
			{Handle: "bravo"},
			{Handle: "charlie"},
		}},
		selected:      map[int]bool{0: true, 1: true, 2: true},
		selectedOrder: []int{2, 0, 1},
	}
	model = model.submit()
	if got := selectorHandles(model.result.Items); strings.Join(got, ",") != "alpha,bravo,charlie" {
		t.Fatalf("expected unordered selector to keep item order, got %v", got)
	}
}

func TestLineStackSelectorItemsDisableMissingAndAllowEmptyStackedAsFollowOnly(t *testing.T) {
	items := selectorItemsForLineStack([]workspaceInfo{
		{Ref: workspaceRef{Handle: "default"}, Main: true, Empty: true},
		{Ref: workspaceRef{Handle: "alpha"}, Empty: true},
		{Ref: workspaceRef{Handle: "bravo"}, Stacked: true},
		{Ref: workspaceRef{Handle: "charlie"}, Conflict: true},
		{Ref: workspaceRef{Handle: "delta"}},
		{Ref: workspaceRef{Handle: "missing"}, Missing: true},
	}, stackTargetResolution{})
	byHandle := mapSelectorItemsByHandle(items)
	if byHandle["default"].Disabled || byHandle["default"].Role != selectorRolePayload {
		t.Fatalf("expected Main Workspace to default to payload even with an empty cursor, got disabled=%v role=%q", byHandle["default"].Disabled, byHandle["default"].Role)
	}
	for _, handle := range []string{"alpha", "bravo"} {
		if byHandle[handle].Disabled {
			t.Fatalf("expected %s to be selectable as follow-only, got disabled", handle)
		}
		if byHandle[handle].Role != selectorRoleFollow {
			t.Fatalf("expected %s to default to follow-only, got role %q", handle, byHandle[handle].Role)
		}
	}
	for _, handle := range []string{"charlie", "delta"} {
		if byHandle[handle].Disabled || byHandle[handle].Role != selectorRolePayload {
			t.Fatalf("expected %s to be selectable payload, got disabled=%v role=%q", handle, byHandle[handle].Disabled, byHandle[handle].Role)
		}
	}
	if !byHandle["missing"].Disabled {
		t.Fatal("expected missing Workspace to be disabled")
	}
}

func TestLineStackSelectorItemsDisableTargetWorkspace(t *testing.T) {
	items := selectorItemsForLineStack([]workspaceInfo{
		{Ref: workspaceRef{Handle: "default"}, Current: true, Empty: true},
		{Ref: workspaceRef{Handle: "alpha"}},
	}, stackTargetResolution{Handle: "default"})
	byHandle := mapSelectorItemsByHandle(items)
	if !byHandle["default"].Disabled {
		t.Fatal("expected target Workspace to be disabled")
	}
	if byHandle["default"].Markers != "current,target" {
		t.Fatalf("expected target marker on disabled target Workspace, got %q", byHandle["default"].Markers)
	}
	if byHandle["alpha"].Disabled {
		t.Fatal("expected non-target Workspace to remain selectable")
	}
}

func selectorHandles(items []selectorItem) []string {
	handles := make([]string, 0, len(items))
	for _, item := range items {
		handles = append(handles, item.Handle)
	}
	return handles
}

func selectorRoles(items []selectorItem) []string {
	roles := make([]string, 0, len(items))
	for _, item := range items {
		roles = append(roles, item.Role)
	}
	return roles
}

func mapSelectorItemsByHandle(items []selectorItem) map[string]selectorItem {
	m := make(map[string]selectorItem, len(items))
	for _, item := range items {
		m[item.Handle] = item
	}
	return m
}

func TestMarkersUseOutsideLayoutVocabularyForExternalPaths(t *testing.T) {
	got := strings.Join(markers(workspaceInfo{External: true}), ",")
	if got != "outside-layout" {
		t.Fatalf("expected user-facing outside-layout marker, got %q", got)
	}
}

func TestFormatAlignedListRowsKeepsPathColumnStable(t *testing.T) {
	rows := []listRow{
		{Handle: "default", Markers: "main", Ahead: "0", Behind: "0", Action: "main", Path: "/repo/default"},
		{Handle: "alpha", Markers: "outside-layout", Ahead: "0", Behind: "2", Action: "move-to-main", Path: "/outside/alpha"},
		{Handle: "cli", Markers: "current", Ahead: "\x1b[36m1\x1b[0m", Behind: "0", Action: "\x1b[36mstack\x1b[0m", Path: "/repo/cli"},
		{Handle: "notifications", Markers: "-", Ahead: "2", Behind: "1", Action: "rebase-or-merge", Path: "/repo/notifications"},
	}
	lines := formatAlignedListRows(rows)
	if len(lines) != len(rows) {
		t.Fatalf("expected %d lines, got %d", len(rows), len(lines))
	}
	wantPathColumn := visibleColumnOfSubstring(t, lines[0], rows[0].Path)
	for i, line := range lines {
		if strings.Contains(line, "\t") {
			t.Fatalf("line %d should use spaces, not tabs: %q", i, line)
		}
		if got := visibleColumnOfSubstring(t, line, rows[i].Path); got != wantPathColumn {
			t.Fatalf("line %d path starts at visible column %d, want %d: %q", i, got, wantPathColumn, line)
		}
	}
}

func TestSelectorViewKeepsPathColumnStableWithLongHandlesAndMarkers(t *testing.T) {
	paths := []string{
		"/work/acme-api",
		"/work/workspaces/acme-api/billing",
		"/work/workspaces/acme-api/worker",
		"/work/workspaces/acme-api/mobile",
	}
	model := selectorModel{
		opts: selectorOptions{Title: "Open Workspace", Mode: selectorSingle, Items: []selectorItem{
			{Handle: "default", Status: "empty", Markers: "main,current", Path: paths[0]},
			{Handle: "billing", Status: "unstacked", Markers: "-", Path: paths[1]},
			{Handle: "worker", Status: "unstacked", Markers: "-", Path: paths[2]},
			{Handle: "mobile", Status: "unstacked", Markers: "outside-layout,current", Path: paths[3]},
		}},
		cursor:   2,
		selected: map[int]bool{},
		width:    120,
	}
	view := model.View()
	wantPathColumn := visibleColumnOfSubstring(t, lineContaining(t, view, paths[0]), paths[0])
	for i, path := range paths[1:] {
		line := lineContaining(t, view, path)
		if got := visibleColumnOfSubstring(t, line, path); got != wantPathColumn {
			t.Fatalf("selector item line %d path starts at visible column %d, want %d: %q", i+1, got, wantPathColumn, line)
		}
	}
}

func visibleColumnOfSubstring(t *testing.T, line string, substring string) int {
	t.Helper()
	idx := strings.Index(line, substring)
	if idx < 0 {
		t.Fatalf("%q does not contain %q", line, substring)
	}
	return lipgloss.Width(line[:idx])
}

func lineContaining(t *testing.T, text string, substring string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, substring) {
			return line
		}
	}
	t.Fatalf("%q does not contain line with %q", text, substring)
	return ""
}

func TestWorkspaceActionSummarizesAheadBehindCounts(t *testing.T) {
	cases := []struct {
		name string
		info workspaceInfo
		want string
	}{
		{name: "main", info: workspaceInfo{Main: true}, want: "main"},
		{name: "clean", info: workspaceInfo{}, want: "ok"},
		{name: "behind only", info: workspaceInfo{Behind: 2}, want: "move-to-main"},
		{name: "ahead only", info: workspaceInfo{Ahead: 1}, want: "stack"},
		{name: "ahead and behind", info: workspaceInfo{Ahead: 2, Behind: 1}, want: "rebase-or-merge"},
		{name: "conflict", info: workspaceInfo{Ahead: 1, Conflict: true}, want: "resolve-conflict"},
		{name: "missing", info: workspaceInfo{Missing: true}, want: "missing"},
	}
	for _, tc := range cases {
		if got := workspaceAction(tc.info); got != tc.want {
			t.Fatalf("%s: expected %q, got %q", tc.name, tc.want, got)
		}
	}
}

func TestWorkspaceSummaryIncludesStatuses(t *testing.T) {
	summary := workspaceSummary([]workspaceInfo{{Ref: workspaceRef{Handle: "alpha"}}, {Ref: workspaceRef{Handle: "bravo"}, Conflict: true}})
	if summary != "Workspaces alpha (unstacked), bravo (conflict)" {
		t.Fatalf("unexpected summary: %q", summary)
	}
}

func TestPromptTextFallbackReadsLine(t *testing.T) {
	origIn, origErr := stdinReader, stderrWriter
	stdinReader = strings.NewReader("~/w\n")
	stderrWriter = io.Discard
	defer func() { stdinReader, stderrWriter = origIn, origErr }()
	got, err := promptText("Workspaces root", "")
	if err != nil || got != "~/w" {
		t.Fatalf("expected prompt value, got %q err=%v", got, err)
	}
}

func TestRunOpenRejectsMultipleHandles(t *testing.T) {
	err := runOpen([]string{"alpha", "bravo"})
	if err == nil || !strings.Contains(err.Error(), "at most one") {
		t.Fatalf("expected multiple handle error, got %v", err)
	}
}

func TestSortWorkspaceInfosPutsMainFirst(t *testing.T) {
	infos := []workspaceInfo{
		{Ref: workspaceRef{Handle: "alpha"}},
		{Ref: workspaceRef{Handle: "default"}, Main: true},
		{Ref: workspaceRef{Handle: "bravo"}},
	}
	sortWorkspaceInfos(infos)
	got := []string{infos[0].Ref.Handle, infos[1].Ref.Handle, infos[2].Ref.Handle}
	if strings.Join(got, ",") != "default,alpha,bravo" {
		t.Fatalf("expected main/default first, got %v", got)
	}
}

func TestLoadWorkspaceInfosTreatsEmptyHeadWithUnstackedAncestorAsUnstacked(t *testing.T) {
	repoRoot := t.TempDir()
	workspacesRoot := filepath.Join(t.TempDir(), "workspaces")
	mainPath := filepath.Join(workspacesRoot, "proj", "default")
	alphaPath := filepath.Join(workspacesRoot, "proj", "alpha")
	for _, path := range []string{mainPath, alphaPath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config{WorkspacesRoot: workspacesRoot, Project: "proj", MainWorkspace: "default"}
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "workspace list") {
			return "default\tmain111\t" + mainPath + "\nalpha\talpha111\t" + alphaPath + "\n", nil
		}
		if strings.Contains(joined, "log -r @") {
			return "alpha111\n", nil
		}
		if strings.Contains(joined, workspaceAheadRevset("alpha", "default")) {
			return "lower-unstacked\n", nil
		}
		if strings.Contains(joined, "empty() & alpha@") {
			return "alpha111\n", nil
		}
		return "", nil
	})
	infos, _, err := loadWorkspaceInfos(repoRoot, cfg, "proj")
	if err != nil {
		t.Fatal(err)
	}
	byHandle := mapInfosByHandle(infos)
	alpha := byHandle["alpha"]
	if alpha.Empty || alpha.Stacked || statusLabel(alpha) != "unstacked" {
		t.Fatalf("expected alpha to be unstacked despite empty head, got empty=%v stacked=%v status=%s", alpha.Empty, alpha.Stacked, statusLabel(alpha))
	}
}

func TestWorkspaceHasUnstackedCommitsChecksAncestorStackNotJustHead(t *testing.T) {
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, workspaceAheadRevset("alpha", "default")) {
			t.Fatalf("expected ancestor-stack revset, got %q", joined)
		}
		return "lower-unstacked\n", nil
	})
	got, err := workspaceHasUnstackedCommits("/repo", "alpha", "default")
	if err != nil || !got {
		t.Fatalf("expected unstacked commit match, got %v err=%v", got, err)
	}
}

func TestLoadWorkspaceInfosUsesMainRepoForStatusWhenWorkspacePathIsStale(t *testing.T) {
	workspacesRoot := filepath.Join(t.TempDir(), "workspaces")
	mainPath := filepath.Join(workspacesRoot, "proj", "default")
	repoRoot := mainPath
	deltaPath := filepath.Join(workspacesRoot, "proj", "delta")
	for _, path := range []string{mainPath, deltaPath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config{WorkspacesRoot: workspacesRoot, Project: "proj", MainWorkspace: "default"}
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "workspace list") {
			return "default\tmain111\t" + mainPath + "\ndelta\tdelta111\t" + deltaPath + "\n", nil
		}
		if len(args) >= 2 && args[0] == "-R" && args[1] == deltaPath {
			return "", errors.New("The working copy is stale")
		}
		if len(args) >= 2 && args[0] == "-R" && args[1] != mainPath {
			t.Fatalf("expected status probe through main path %q, got args %v", mainPath, args)
		}
		if strings.Contains(joined, workspaceAheadRevset("delta", "default")) {
			return "", nil
		}
		if strings.Contains(joined, "empty() & delta@") {
			return "delta-head\n", nil
		}
		if strings.Contains(joined, "delta@::default@") {
			return "stacked\n", nil
		}
		return "", nil
	})
	infos, _, err := loadWorkspaceInfos(repoRoot, cfg, "proj")
	if err != nil {
		t.Fatal(err)
	}
	delta := mapInfosByHandle(infos)["delta"]
	if !delta.Empty || statusLabel(delta) != "empty" {
		t.Fatalf("expected stale-path delta to be classified from main graph as empty, got empty=%v stacked=%v status=%s", delta.Empty, delta.Stacked, statusLabel(delta))
	}
}

func TestLoadWorkspaceInfosFallsBackWhenJjReportsNoRecordedPath(t *testing.T) {
	workspacesRoot := filepath.Join(t.TempDir(), "workspaces")
	repoRoot := filepath.Join(workspacesRoot, "proj", "alpha")
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config{WorkspacesRoot: workspacesRoot, Project: "proj", MainWorkspace: "default"}
	badRoot := "<Error: Workspace has no recorded path: default>"
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, badRoot) {
			t.Fatalf("jj template error should not be used as a repo path: %s %v", name, args)
		}
		if strings.Contains(joined, "workspace list") {
			return "default\tmain111\t" + badRoot + "\nalpha\talpha111\t" + repoRoot + "\n", nil
		}
		if strings.Contains(joined, "log -r @") {
			return "alpha111\n", nil
		}
		if len(args) >= 2 && args[0] == "-R" && args[1] != repoRoot {
			t.Fatalf("expected graph probe to fall back to current repo path %q, got args %v", repoRoot, args)
		}
		return "", nil
	})
	infos, current, err := loadWorkspaceInfos(repoRoot, cfg, "proj")
	if err != nil {
		t.Fatal(err)
	}
	if current != "alpha" {
		t.Fatalf("expected current Workspace alpha, got %q", current)
	}
	byHandle := mapInfosByHandle(infos)
	if strings.Contains(byHandle["default"].Path, "<Error:") || !byHandle["default"].Missing {
		t.Fatalf("expected default path to fall back to a missing canonical path, got %+v", byHandle["default"])
	}
}

func TestLoadWorkspaceInfosSurfacesStatusProbeErrors(t *testing.T) {
	workspacesRoot := filepath.Join(t.TempDir(), "workspaces")
	mainPath := filepath.Join(workspacesRoot, "proj", "default")
	repoRoot := mainPath
	alphaPath := filepath.Join(workspacesRoot, "proj", "alpha")
	for _, path := range []string{mainPath, alphaPath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config{WorkspacesRoot: workspacesRoot, Project: "proj", MainWorkspace: "default"}
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "workspace list") {
			return "default\tmain111\t" + mainPath + "\nalpha\talpha111\t" + alphaPath + "\n", nil
		}
		if strings.Contains(joined, workspaceAheadRevset("alpha", "default")) {
			return "", errors.New("graph probe failed")
		}
		return "", nil
	})
	_, _, err := loadWorkspaceInfos(repoRoot, cfg, "proj")
	if err == nil || !strings.Contains(err.Error(), "probe ahead status for Workspace \"alpha\"") || !strings.Contains(err.Error(), "graph probe failed") {
		t.Fatalf("expected clear status probe error, got %v", err)
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

func TestRunMoveToMainAllMovesOnlyBehindTidyWorkspaces(t *testing.T) {
	workspacesRoot := filepath.Join(t.TempDir(), "workspaces")
	mainPath := filepath.Join(workspacesRoot, "proj", "default")
	alphaPath := filepath.Join(workspacesRoot, "proj", "alpha")
	memePath := filepath.Join(workspacesRoot, "proj", "billing")
	for _, path := range []string{mainPath, alphaPath, memePath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(t, mainPath, "workspaces_root: "+workspacesRoot+"\nproject: proj\nmain_workspace: default\n")
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "workspace list") {
			return "default\tmain111\t" + mainPath + "\nalpha\talpha111\t" + alphaPath + "\nbilling\tmeme111\t" + memePath + "\n", nil
		}
		if strings.Contains(joined, "op log") {
			return "op123\n", nil
		}
		if strings.Contains(joined, "log -r @") {
			return "main111\n", nil
		}
		if strings.Contains(joined, workspaceAheadRevset("alpha", "default")) {
			return "", nil
		}
		if strings.Contains(joined, workspaceBehindRevset("alpha", "default")) {
			return "main-commit\n", nil
		}
		if strings.Contains(joined, "empty() & alpha@") {
			return "alpha-empty\n", nil
		}
		if strings.Contains(joined, workspaceAheadRevset("billing", "default")) {
			return "unique-meme\n", nil
		}
		if strings.Contains(joined, "empty() & default@") {
			return "main-empty\n", nil
		}
		return "", nil
	})
	commands := []string{}
	withCommandToStderr(t, func(name string, args ...string) error {
		commands = append(commands, name+" "+strings.Join(args, " "))
		return nil
	})
	_, _, err := captureOutput(func() error { return runMoveToMain([]string{"--repo", mainPath, "--all", "--yes"}) })
	if err != nil {
		t.Fatal(err)
	}
	joinedCommands := strings.Join(commands, "\n")
	if !strings.Contains(joinedCommands, "-R "+alphaPath+" new default@-") {
		t.Fatalf("expected alpha to move to default@-, got commands:\n%s", joinedCommands)
	}
	if strings.Contains(joinedCommands, memePath+" new") {
		t.Fatalf("billing has unique commits and should not move, got commands:\n%s", joinedCommands)
	}
}

func TestValidateMoveToMainRejectsWorkspaceWithUniqueCommits(t *testing.T) {
	err := validateMoveToMainTarget(workspaceInfo{Ref: workspaceRef{Handle: "billing"}, Ahead: 1, Behind: 1})
	if err == nil || !strings.Contains(err.Error(), "unique content or described commits") {
		t.Fatalf("expected unique-commits rejection, got %v", err)
	}
}

func TestRunTidyClosesWorkspacesWithNoUniqueNonEmptyCommits(t *testing.T) {
	workspacesRoot := filepath.Join(t.TempDir(), "workspaces")
	mainPath := filepath.Join(workspacesRoot, "proj", "default")
	deltaPath := filepath.Join(workspacesRoot, "proj", "delta")
	alphaPath := filepath.Join(workspacesRoot, "proj", "alpha")
	for _, path := range []string{mainPath, deltaPath, alphaPath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(t, mainPath, "workspaces_root: "+workspacesRoot+"\nproject: proj\nmain_workspace: default\n")
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "workspace list") {
			return "default\tmain111\t" + mainPath + "\ndelta\tdelta111\t" + deltaPath + "\nalpha\talpha111\t" + alphaPath + "\n", nil
		}
		if strings.Contains(joined, workspaceAheadRevset("alpha", "default")) {
			return "unique-alpha\n", nil
		}
		if strings.Contains(joined, workspaceAheadRevset("delta", "default")) {
			return "", nil
		}
		if strings.Contains(joined, "empty() & delta@") {
			return "delta-empty\n", nil
		}
		if strings.Contains(joined, "empty() & mutable() & (delta@)") {
			return "delta-empty\n", nil
		}
		return "", nil
	})
	forgotDelta := false
	abandonedDelta := false
	updatedStale := false
	withCommandToStderr(t, func(name string, args ...string) error {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "workspace forget delta") {
			forgotDelta = true
		}
		if strings.Contains(joined, "workspace forget alpha") {
			t.Fatal("tidy must not close Workspace with unique non-empty commits")
		}
		if strings.Contains(joined, "abandon -r empty() & mutable() & (delta@)") {
			abandonedDelta = true
		}
		if strings.Contains(joined, "workspace update-stale") {
			updatedStale = true
		}
		return nil
	})
	origOut, origErr := stdoutWriter, stderrWriter
	var out, errOut bytes.Buffer
	stdoutWriter, stderrWriter = &out, &errOut
	defer func() { stdoutWriter, stderrWriter = origOut, origErr }()
	if err := runTidy([]string{"--repo", mainPath, "--yes"}); err != nil {
		t.Fatal(err)
	}
	if !forgotDelta || !abandonedDelta || !updatedStale {
		t.Fatalf("expected forget/abandon/update-stale, got forget=%v abandon=%v update=%v", forgotDelta, abandonedDelta, updatedStale)
	}
	if exists(deltaPath) {
		t.Fatal("expected tidy to remove closed delta Workspace directory")
	}
	if !exists(alphaPath) {
		t.Fatal("tidy removed Workspace with unique non-empty commits")
	}
	if !strings.Contains(out.String(), deltaPath) {
		t.Fatalf("expected closed Workspace path on stdout, got %q", out.String())
	}
	if !strings.Contains(errOut.String(), "delta") || strings.Contains(errOut.String(), "alpha (unstacked)") {
		t.Fatalf("expected stderr to identify only tidyable Workspace, got %q", errOut.String())
	}
}

func TestRunInitWritesAssimilatedPathsVocabulary(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	out, err := captureStdout(func() error { return runInit([]string{"--workspaces-root", "/tmp/workspaces"}) })
	if err != nil {
		t.Fatalf("init failed: %v", err)
	}
	cfgPath := strings.TrimSpace(out)
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "assimilated_paths:") {
		t.Fatalf("expected assimilated_paths in generated config, got:\n%s", text)
	}
	if strings.Contains(text, "assimilated_folders:") {
		t.Fatalf("generated config should not use deprecated assimilated_folders, got:\n%s", text)
	}
}

func TestRunInitRefusesExistingConfigUnlessForce(t *testing.T) {
	xdg := t.TempDir()
	cfgDir := filepath.Join(xdg, "ajj")
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
	dir := filepath.Join(repoRoot, ".ajj")
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
	out, _, err := captureOutput(fn)
	return out, err
}

func captureOutput(fn func() error) (string, string, error) {
	origOut := stdoutWriter
	origErr := stderrWriter
	defer func() {
		stdoutWriter = origOut
		stderrWriter = origErr
	}()
	var out bytes.Buffer
	var errOut bytes.Buffer
	stdoutWriter = &out
	stderrWriter = &errOut
	err := fn()
	return out.String(), errOut.String(), err
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
	if err := os.WriteFile(filepath.Join(mainPath, ".env.local"), []byte("secret-ish local config"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config{AssimilatedPaths: []string{"scratch", ".env.local", "missing"}}
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
	fileTarget, err := os.Readlink(filepath.Join(workspacePath, ".env.local"))
	if err != nil {
		t.Fatalf("expected file symlink: %v", err)
	}
	if fileTarget != filepath.Join(mainPath, ".env.local") {
		t.Fatalf("expected symlink to main file, got %q", fileTarget)
	}
	if exists(filepath.Join(workspacePath, "missing")) {
		t.Fatal("missing source should be skipped, not created")
	}
}

func TestMaterializeAssimilatedFoldersExpandsGlobstarEnvFiles(t *testing.T) {
	mainPath := t.TempDir()
	workspacePath := filepath.Join(t.TempDir(), "workspace")
	files := []string{
		".env",
		"apps/web/.env",
		"apps/web/.env.local",
		"services/api/.env.test",
	}
	for _, rel := range files {
		path := filepath.Join(mainPath, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(rel), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(mainPath, "apps", "web", "README.md"), []byte("not env"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config{AssimilatedPaths: []string{"**/.env*"}}
	if err := materializeAssimilatedFolders(mainPath, workspacePath, cfg, "proj"); err != nil {
		t.Fatal(err)
	}
	for _, rel := range files {
		target, err := os.Readlink(filepath.Join(workspacePath, rel))
		if err != nil {
			t.Fatalf("expected %s symlink: %v", rel, err)
		}
		if target != filepath.Join(mainPath, rel) {
			t.Fatalf("expected %s symlink to main path, got %q", rel, target)
		}
	}
	if exists(filepath.Join(workspacePath, "apps", "web", "README.md")) {
		t.Fatal("glob should not materialize non-matching files")
	}
}

func TestMaterializeAssimilatedFoldersExpandsGlobstarExactEnv(t *testing.T) {
	mainPath := t.TempDir()
	workspacePath := filepath.Join(t.TempDir(), "workspace")
	matching := []string{
		".env",
		"apps/web/.env",
	}
	nonMatching := []string{
		"apps/web/.env.local",
		"services/api/.env.test",
	}
	for _, rel := range append(matching, nonMatching...) {
		path := filepath.Join(mainPath, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(rel), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config{AssimilatedPaths: []string{"**/.env"}}
	if err := materializeAssimilatedFolders(mainPath, workspacePath, cfg, "proj"); err != nil {
		t.Fatal(err)
	}
	for _, rel := range matching {
		target, err := os.Readlink(filepath.Join(workspacePath, rel))
		if err != nil {
			t.Fatalf("expected %s symlink: %v", rel, err)
		}
		if target != filepath.Join(mainPath, rel) {
			t.Fatalf("expected %s symlink to main path, got %q", rel, target)
		}
	}
	for _, rel := range nonMatching {
		if exists(filepath.Join(workspacePath, rel)) {
			t.Fatalf("glob should not materialize non-matching file %s", rel)
		}
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
	cfg := config{AssimilatedPaths: []string{"scratch"}}
	if err := materializeAssimilatedFolders(mainPath, workspacePath, cfg, "proj"); err == nil || !strings.Contains(err.Error(), "refusing to replace") {
		t.Fatalf("expected refusal to replace existing content, got %v", err)
	}
}

func TestMaterializeAssimilatedFoldersReplacesIdenticalWorkspaceFile(t *testing.T) {
	mainPath := t.TempDir()
	workspacePath := t.TempDir()
	for _, path := range []string{filepath.Join(mainPath, ".envrc"), filepath.Join(workspacePath, ".envrc")} {
		if err := os.WriteFile(path, []byte("use devenv\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config{AssimilatedPaths: []string{".envrc"}}
	if err := materializeAssimilatedFolders(mainPath, workspacePath, cfg, "proj"); err != nil {
		t.Fatalf("expected identical Workspace file to be safely assimilated, got %v", err)
	}
	if target, err := os.Readlink(filepath.Join(workspacePath, ".envrc")); err != nil || target != filepath.Join(mainPath, ".envrc") {
		t.Fatalf("expected identical .envrc to be replaced with symlink to main, got target=%q err=%v", target, err)
	}
}

func TestMaterializeAssimilatedFoldersGlobSkipsRepoTrackedFiles(t *testing.T) {
	mainPath := t.TempDir()
	workspacePath := filepath.Join(t.TempDir(), "workspace")
	// `.env` is treated as an ignored local file (want symlinked); `.env.example`
	// and `.envrc` are treated as checked-in tracked files that the glob must
	// skip silently rather than symlink over.
	for _, rel := range []string{".env", ".env.example", ".envrc"} {
		if err := os.WriteFile(filepath.Join(mainPath, rel), []byte(rel), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		if name == "jj" && strings.Contains(strings.Join(args, " "), "file list") {
			return ".env.example\n.envrc\n", nil
		}
		return "", nil
	})
	cfg := config{AssimilatedPaths: []string{"**/.env*"}}
	if err := materializeAssimilatedFolders(mainPath, workspacePath, cfg, "proj"); err != nil {
		t.Fatalf("expected no error materializing with tracked files skipped, got %v", err)
	}
	// Ignored local file is symlinked into the target Workspace.
	if target, err := os.Readlink(filepath.Join(workspacePath, ".env")); err != nil {
		t.Fatalf("expected .env symlink: %v", err)
	} else if target != filepath.Join(mainPath, ".env") {
		t.Fatalf("expected .env symlink to main path, got %q", target)
	}
	// Tracked files are skipped: no symlink is created for them.
	for _, rel := range []string{".env.example", ".envrc"} {
		if exists(filepath.Join(workspacePath, rel)) {
			t.Fatalf("expected tracked file %s to be skipped (no symlink created)", rel)
		}
	}
}

func TestMaterializeAssimilatedFoldersGlobFallsBackWhenNotARepo(t *testing.T) {
	mainPath := t.TempDir()
	workspacePath := filepath.Join(t.TempDir(), "workspace")
	for _, rel := range []string{".env", ".envrc"} {
		if err := os.WriteFile(filepath.Join(mainPath, rel), []byte(rel), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// The tracked-set lookup fails (not a jj repo); glob expansion must fall
	// back to an empty tracked set and materialize every glob match.
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		if name == "jj" && strings.Contains(strings.Join(args, " "), "file list") {
			return "", errors.New("jj: not a jj repo")
		}
		return "", nil
	})
	cfg := config{AssimilatedPaths: []string{"**/.env*"}}
	if err := materializeAssimilatedFolders(mainPath, workspacePath, cfg, "proj"); err != nil {
		t.Fatalf("expected no error on fallback to empty tracked set, got %v", err)
	}
	for _, rel := range []string{".env", ".envrc"} {
		if target, err := os.Readlink(filepath.Join(workspacePath, rel)); err != nil {
			t.Fatalf("expected %s symlink on fallback: %v", rel, err)
		} else if target != filepath.Join(mainPath, rel) {
			t.Fatalf("expected %s symlink to main path, got %q", rel, target)
		}
	}
}

func TestCreateWorkspaceHelperCreatesPrintsAndMaterializes(t *testing.T) {
	mainPath := t.TempDir()
	workspacesRoot := filepath.Join(t.TempDir(), "workspaces")
	if err := os.MkdirAll(filepath.Join(mainPath, "scratch"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mainPath, ".env.local"), []byte("secret-ish local config"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config{WorkspacesRoot: workspacesRoot, Project: "proj", MainWorkspace: "default", AssimilatedPaths: []string{"scratch", ".env.local"}}
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "workspace list") {
			return "default\tmain111\t" + mainPath + "\n", nil
		}
		return "", nil
	})
	withCommandToStderr(t, func(name string, args ...string) error {
		if name == "jj" && strings.Contains(strings.Join(args, " "), "workspace add") {
			return os.MkdirAll(args[len(args)-1], 0o755)
		}
		return nil
	})
	out, errOut, err := captureOutput(func() error { return createWorkspace(mainPath, cfg, "proj", "alpha", false, false) })
	if err != nil {
		t.Fatal(err)
	}
	workspacePath := filepath.Join(workspacesRoot, "proj", "alpha")
	if strings.TrimSpace(out) != workspacePath {
		t.Fatalf("expected helper to print workspace path, got %q", out)
	}
	if target, err := os.Readlink(filepath.Join(workspacePath, "scratch")); err != nil || target != filepath.Join(mainPath, "scratch") {
		t.Fatalf("expected helper to materialize scratch symlink to main, got target=%q err=%v", target, err)
	}
	if target, err := os.Readlink(filepath.Join(workspacePath, ".env.local")); err != nil || target != filepath.Join(mainPath, ".env.local") {
		t.Fatalf("expected helper to materialize .env.local symlink to main, got target=%q err=%v", target, err)
	}
	for _, want := range []string{
		"Linked assimilated path: " + filepath.Join(workspacePath, "scratch") + " -> " + filepath.Join(mainPath, "scratch"),
		"Linked assimilated path: " + filepath.Join(workspacePath, ".env.local") + " -> " + filepath.Join(mainPath, ".env.local"),
	} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("expected stderr to report %q, got %q", want, errOut)
		}
	}
}

func TestCreateWorkspaceHonorsDirenvAllowWhenAssimilationFails(t *testing.T) {
	mainPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(mainPath, "scratch"), 0o755); err != nil {
		t.Fatal(err)
	}
	workspacesRoot := filepath.Join(t.TempDir(), "workspaces")
	cfg := config{WorkspacesRoot: workspacesRoot, Project: "proj", MainWorkspace: "default", AssimilatedPaths: []string{"scratch"}}
	workspacePath := filepath.Join(workspacesRoot, "proj", "alpha")

	var direnvAllowTargets []string
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "workspace list") {
			return "default\tmain111\t" + mainPath + "\n", nil
		}
		return "", nil
	})
	withCommandToStderr(t, func(name string, args ...string) error {
		if name == "jj" && strings.Contains(strings.Join(args, " "), "workspace add") {
			target := args[len(args)-1]
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			// Pre-seed a real file at the assimilated dest so the explicit-path
			// conflict is fatal (the existing intended behavior), exercising the
			// deferred direnv-allow path.
			return os.WriteFile(filepath.Join(target, "scratch"), []byte("local"), 0o644)
		}
		if name == "direnv" && len(args) >= 2 && args[0] == "allow" {
			direnvAllowTargets = append(direnvAllowTargets, args[1])
		}
		return nil
	})
	withLookPath(t, func(file string) (string, error) {
		if file == "direnv" {
			return "/usr/bin/direnv", nil
		}
		return "", os.ErrNotExist
	})

	err := createWorkspace(mainPath, cfg, "proj", "alpha", false, true)
	if err == nil || !strings.Contains(err.Error(), "refusing to replace") {
		t.Fatalf("expected assimilation refusal error, got %v", err)
	}
	if len(direnvAllowTargets) != 1 || direnvAllowTargets[0] != workspacePath {
		t.Fatalf("expected direnv allow for %s on partial-failure path, got %v", workspacePath, direnvAllowTargets)
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
		"assimilated_paths:",
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
		"assimilated_paths:",
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

func TestRunOpenWarnsButStillPrintsPathWhenAssimilationFails(t *testing.T) {
	mainPath := t.TempDir()
	workspacePath := filepath.Join(t.TempDir(), "alpha")
	if err := os.WriteFile(filepath.Join(mainPath, ".envrc"), []byte("main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspacePath, ".envrc"), []byte("workspace-local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, mainPath, strings.Join([]string{
		"workspaces_root: /tmp/workspaces",
		"project: proj",
		"assimilated_paths:",
		"  - .envrc",
		"",
	}, "\n"))
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "workspace list") {
			return "default\tmain111\t" + mainPath + "\nalpha\talpha111\t" + workspacePath + "\n", nil
		}
		return "", nil
	})
	out, errOut, err := captureOutput(func() error { return runOpen([]string{"alpha", "--repo", mainPath}) })
	if err != nil {
		t.Fatalf("open should warn, not fail, when assimilation cannot replace local content: %v", err)
	}
	if strings.TrimSpace(out) != workspacePath {
		t.Fatalf("expected open to print workspace path despite warning, got %q", out)
	}
	if !strings.Contains(errOut, "Warning:") || !strings.Contains(errOut, "refusing to replace") {
		t.Fatalf("expected assimilation warning on stderr, got %q", errOut)
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

func TestCloseWorkspacesConfirmsExternalDeletionOnce(t *testing.T) {
	repoRoot := t.TempDir()
	alphaPath := filepath.Join(t.TempDir(), "alpha")
	bravoPath := filepath.Join(t.TempDir(), "bravo")
	for _, path := range []string{alphaPath, bravoPath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	origIn, origErr := stdinReader, stderrWriter
	stdinReader = strings.NewReader("y\n")
	var errOut bytes.Buffer
	stderrWriter = &errOut
	defer func() { stdinReader, stderrWriter = origIn, origErr }()
	forgot := []string{}
	withCommandToStderr(t, func(name string, args ...string) error {
		if len(args) >= 5 && args[2] == "workspace" && args[3] == "forget" {
			forgot = append(forgot, args[4])
		}
		return nil
	})

	_, err := closeWorkspaces(repoRoot, []workspaceInfo{
		{Ref: workspaceRef{Handle: "alpha"}, Path: alphaPath, External: true},
		{Ref: workspaceRef{Handle: "bravo"}, Path: bravoPath, External: true},
	}, false, false)
	if err != nil {
		t.Fatalf("expected one confirmation to cover all external deletes, got %v", err)
	}
	if strings.Count(errOut.String(), "outside the canonical Project layout") != 1 {
		t.Fatalf("expected one external delete prompt, got %q", errOut.String())
	}
	if strings.Join(forgot, ",") != "alpha,bravo" {
		t.Fatalf("expected both workspaces forgotten, got %v", forgot)
	}
}

func TestRunCloseCurrentWorkspaceForgetsFromMainWorkspace(t *testing.T) {
	workspacesRoot := filepath.Join(t.TempDir(), "workspaces")
	mainPath := filepath.Join(workspacesRoot, "proj", "default")
	currentPath := filepath.Join(workspacesRoot, "proj", "test")
	for _, path := range []string{mainPath, currentPath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(t, currentPath, "workspaces_root: "+workspacesRoot+"\nproject: proj\nmain_workspace: default\n")
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "workspace list") {
			return "default\tmain111\t" + mainPath + "\ntest\ttest111\t" + currentPath + "\n", nil
		}
		if strings.Contains(joined, workspaceAheadRevset("test", "default")) || strings.Contains(joined, workspaceBehindRevset("test", "default")) {
			return "", nil
		}
		if strings.Contains(joined, "empty() & test@") {
			return "test-empty\n", nil
		}
		return "", nil
	})
	forgotRepoPath := ""
	withCommandToStderr(t, func(name string, args ...string) error {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "workspace forget test") {
			if len(args) < 2 || args[0] != "-R" {
				t.Fatalf("expected forget to use -R, got %v", args)
			}
			forgotRepoPath = args[1]
		}
		return nil
	})
	out, err := captureStdout(func() error { return runClose([]string{"--repo", currentPath, "--yes"}) })
	if err != nil {
		t.Fatalf("expected current Workspace to close cleanly, got %v", err)
	}
	if forgotRepoPath != mainPath {
		t.Fatalf("expected forget to run from Main Workspace %q, got %q", mainPath, forgotRepoPath)
	}
	if exists(currentPath) {
		t.Fatal("expected current Workspace directory removed")
	}
	if strings.TrimSpace(out) != mainPath {
		t.Fatalf("expected close to print Main Workspace path, got %q", out)
	}
}

func TestRunCloseStackedStaleWorkspaceDoesNotRequireForcedClosing(t *testing.T) {
	workspacesRoot := filepath.Join(t.TempDir(), "workspaces")
	mainPath := filepath.Join(workspacesRoot, "proj", "default")
	deltaPath := filepath.Join(workspacesRoot, "proj", "delta")
	for _, path := range []string{mainPath, deltaPath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(t, mainPath, "workspaces_root: "+workspacesRoot+"\nproject: proj\nmain_workspace: default\n")
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "workspace list") {
			return "default\tmain111\t" + mainPath + "\ndelta\tdelta111\t" + deltaPath + "\n", nil
		}
		if len(args) >= 2 && args[0] == "-R" && args[1] == deltaPath {
			return "", errors.New("The working copy is stale")
		}
		if strings.Contains(joined, workspaceAheadRevset("delta", "default")) {
			return "", nil
		}
		if strings.Contains(joined, "empty() & delta@") {
			return "", nil
		}
		if strings.Contains(joined, "delta@::default@") {
			return "stacked\n", nil
		}
		return "", nil
	})
	forgot := false
	withCommandToStderr(t, func(name string, args ...string) error {
		if strings.Contains(strings.Join(args, " "), "workspace forget delta") {
			forgot = true
		}
		return nil
	})
	out, err := captureStdout(func() error { return runClose([]string{"delta", "--repo", mainPath, "--yes"}) })
	if err != nil {
		t.Fatalf("expected normal close without forced-closing prompt/error, got %v", err)
	}
	if !forgot {
		t.Fatal("expected delta workspace to be forgotten")
	}
	if exists(deltaPath) {
		t.Fatalf("expected delta workspace directory removed, stdout=%q", out)
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

func TestRunStackExplicitWorkspaceStacksPayloadParentThenAdvancesWorkspaceHead(t *testing.T) {
	workspacesRoot := filepath.Join(t.TempDir(), "workspaces")
	mainPath := filepath.Join(workspacesRoot, "proj", "default")
	teamsPath := filepath.Join(workspacesRoot, "proj", "teams")
	for _, path := range []string{mainPath, teamsPath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(t, mainPath, "workspaces_root: "+workspacesRoot+"\nproject: proj\nmain_workspace: default\n")
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "workspace list") {
			return "default\tmain111\t" + mainPath + "\nteams\tteams111\t" + teamsPath + "\n", nil
		}
		if strings.Contains(joined, " op log ") {
			return "op-before-stack\n", nil
		}
		if strings.Contains(joined, "heads(teams@-)") {
			return "teams-parent\n", nil
		}
		if strings.Contains(joined, workspaceAheadRevset("teams", "default")) {
			return "", nil
		}
		if strings.Contains(joined, "empty() & teams@") {
			return "empty\n", nil
		}
		return "", nil
	})
	rebaseCommands := [][]string{}
	withCommandToStderr(t, func(name string, args ...string) error {
		if strings.Contains(strings.Join(args, " "), " rebase ") {
			rebaseCommands = append(rebaseCommands, append([]string(nil), args...))
		}
		return nil
	})
	origErr := stderrWriter
	var errOut bytes.Buffer
	stderrWriter = &errOut
	defer func() { stderrWriter = origErr }()
	if err := runStack([]string{"teams", "--repo", mainPath, "--yes"}); err != nil {
		t.Fatalf("expected explicit Workspace to stack through its payload parent and advance its head, got %v", err)
	}
	if len(rebaseCommands) != 2 {
		t.Fatalf("expected stack rebase plus Workspace advance rebase, got %v", rebaseCommands)
	}
	stackDests := rebaseDestinations(rebaseCommands[0])
	if strings.Join(stackDests, ",") != "teams-parent" {
		t.Fatalf("expected teams@- payload destination, got args=%v dests=%v", rebaseCommands[0], stackDests)
	}
	advanceDests := rebaseDestinations(rebaseCommands[1])
	if !strings.Contains(strings.Join(rebaseCommands[1], " "), "-r teams@") || strings.Join(advanceDests, ",") != "@" {
		t.Fatalf("expected teams@ to advance onto new Main @, got args=%v dests=%v", rebaseCommands[1], advanceDests)
	}
	if !strings.Contains(errOut.String(), "To undo this run: jj op restore op-before-stack") {
		t.Fatalf("expected undo restore hint, got stderr %q", errOut.String())
	}
}

func TestRunStackFromCurrentNonDefaultWorkspaceTargetsCurrentWorkspace(t *testing.T) {
	workspacesRoot := filepath.Join(t.TempDir(), "workspaces")
	defaultPath := filepath.Join(workspacesRoot, "proj", "default")
	speedPath := filepath.Join(workspacesRoot, "proj", "speed")
	childPath := filepath.Join(workspacesRoot, "proj", "agm-speed-transition")
	for _, path := range []string{defaultPath, speedPath, childPath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(t, defaultPath, "workspaces_root: "+workspacesRoot+"\nproject: proj\nmain_workspace: default\n")
	createJJWorkspaceLink(t, defaultPath, speedPath)
	createJJWorkspaceLink(t, defaultPath, childPath)
	withCommandCapture(t, stackTargetWorkspaceFixture(t, workspacesRoot, defaultPath, speedPath, childPath, "speed111"))
	mutatingRepos := []string{}
	rebaseCommands := [][]string{}
	withCommandToStderr(t, func(name string, args ...string) error {
		joined := strings.Join(args, " ")
		if name == "jj" && len(args) >= 2 && (strings.Contains(joined, " rebase ") || strings.Contains(joined, "workspace update-stale")) {
			mutatingRepos = append(mutatingRepos, args[1])
		}
		if strings.Contains(joined, " rebase ") {
			rebaseCommands = append(rebaseCommands, append([]string(nil), args...))
		}
		return nil
	})
	_, errOut, err := captureOutput(func() error {
		return runStack([]string{"agm-speed-transition", "--repo", speedPath, "--yes"})
	})
	if err != nil {
		t.Fatalf("expected stack from speed to target speed, got %v\nstderr:%s", err, errOut)
	}
	if len(mutatingRepos) == 0 {
		t.Fatal("expected stack to run mutating jj commands")
	}
	for _, repo := range mutatingRepos {
		if repo == defaultPath {
			t.Fatalf("default Workspace must not move when --repo points at speed; mutating repos=%v stderr=%q", mutatingRepos, errOut)
		}
	}
	if mutatingRepos[0] != speedPath {
		t.Fatalf("expected Stack rebase to target speed path %q, got mutating repos=%v", speedPath, mutatingRepos)
	}
	if len(rebaseCommands) != 2 {
		t.Fatalf("expected stack rebase plus child head advance, got %v", rebaseCommands)
	}
	if strings.Join(rebaseDestinations(rebaseCommands[0]), ",") != "child-payload" {
		t.Fatalf("expected child payload parent to be stacked into speed, got %v", rebaseCommands[0])
	}
	if !strings.Contains(strings.Join(rebaseCommands[1], " "), "-r agm-speed-transition@") || strings.Join(rebaseDestinations(rebaseCommands[1]), ",") != "@" {
		t.Fatalf("expected child Workspace head to advance onto new speed@, got %v", rebaseCommands[1])
	}
	if !strings.Contains(errOut, "Stack target workspace: speed ("+speedPath+")") || !strings.Contains(errOut, "Configured main_workspace default was not used") {
		t.Fatalf("expected target workspace preview and fallback note, got %q", errOut)
	}
}

func TestRunStackAllFromCurrentNonDefaultDoesNotMoveConfiguredMainWorkspace(t *testing.T) {
	workspacesRoot := filepath.Join(t.TempDir(), "workspaces")
	defaultPath := filepath.Join(workspacesRoot, "proj", "default")
	speedPath := filepath.Join(workspacesRoot, "proj", "speed")
	childPath := filepath.Join(workspacesRoot, "proj", "agm-speed-transition")
	for _, path := range []string{defaultPath, speedPath, childPath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(t, defaultPath, "workspaces_root: "+workspacesRoot+"\nproject: proj\nmain_workspace: default\n")
	createJJWorkspaceLink(t, defaultPath, speedPath)
	createJJWorkspaceLink(t, defaultPath, childPath)
	withCommandCapture(t, stackTargetWorkspaceFixture(t, workspacesRoot, defaultPath, speedPath, childPath, "speed111"))
	rebaseCommands := [][]string{}
	withCommandToStderr(t, func(name string, args ...string) error {
		if strings.Contains(strings.Join(args, " "), " rebase ") {
			rebaseCommands = append(rebaseCommands, append([]string(nil), args...))
		}
		return nil
	})
	_, errOut, err := captureOutput(func() error {
		return runStack([]string{"--all", "--repo", speedPath, "--yes"})
	})
	if err != nil {
		t.Fatalf("expected --all from speed to stack non-main inputs, got %v\nstderr:%s", err, errOut)
	}
	for _, cmd := range rebaseCommands {
		if strings.Contains(strings.Join(cmd, " "), "default@") {
			t.Fatalf("configured main_workspace default must not be silently moved by --all from speed, got %v", rebaseCommands)
		}
	}
}

func TestRunStackExplicitWorkspaceOverrideStillWins(t *testing.T) {
	workspacesRoot := filepath.Join(t.TempDir(), "workspaces")
	defaultPath := filepath.Join(workspacesRoot, "proj", "default")
	speedPath := filepath.Join(workspacesRoot, "proj", "speed")
	childPath := filepath.Join(workspacesRoot, "proj", "agm-speed-transition")
	for _, path := range []string{defaultPath, speedPath, childPath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(t, defaultPath, "workspaces_root: "+workspacesRoot+"\nproject: proj\nmain_workspace: default\n")
	createJJWorkspaceLink(t, defaultPath, speedPath)
	createJJWorkspaceLink(t, defaultPath, childPath)
	withCommandCapture(t, stackTargetWorkspaceFixture(t, workspacesRoot, defaultPath, speedPath, childPath, "speed111"))
	mutatingRepos := []string{}
	withCommandToStderr(t, func(name string, args ...string) error {
		if name == "jj" && len(args) >= 2 && (strings.Contains(strings.Join(args, " "), " rebase ") || strings.Contains(strings.Join(args, " "), "workspace update-stale")) {
			mutatingRepos = append(mutatingRepos, args[1])
		}
		return nil
	})
	_, errOut, err := captureOutput(func() error {
		return runStack([]string{"agm-speed-transition", "--repo", speedPath, "--workspace", "default", "--yes"})
	})
	if err != nil {
		t.Fatalf("expected explicit --workspace default to target default, got %v\nstderr:%s", err, errOut)
	}
	if len(mutatingRepos) == 0 || mutatingRepos[0] != defaultPath {
		t.Fatalf("expected explicit override to stack into default path %q, got mutating repos=%v", defaultPath, mutatingRepos)
	}
}

func TestRunStackSelfTargetGuardRequiresExplicitWorkspace(t *testing.T) {
	workspacesRoot := filepath.Join(t.TempDir(), "workspaces")
	defaultPath := filepath.Join(workspacesRoot, "proj", "default")
	speedPath := filepath.Join(workspacesRoot, "proj", "speed")
	childPath := filepath.Join(workspacesRoot, "proj", "agm-speed-transition")
	for _, path := range []string{defaultPath, speedPath, childPath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(t, defaultPath, "workspaces_root: "+workspacesRoot+"\nproject: proj\nmain_workspace: default\n")
	createJJWorkspaceLink(t, defaultPath, speedPath)
	createJJWorkspaceLink(t, defaultPath, childPath)
	withCommandCapture(t, stackTargetWorkspaceFixture(t, workspacesRoot, defaultPath, speedPath, childPath, "child111"))
	err := runStack([]string{"agm-speed-transition", "--repo", childPath, "--yes"})
	if err == nil || !strings.Contains(err.Error(), "target workspace") || !strings.Contains(err.Error(), "--workspace") {
		t.Fatalf("expected self-target guard with --workspace guidance, got %v", err)
	}
}

func TestRunStackLineSelfTargetGuardRequiresExplicitWorkspace(t *testing.T) {
	workspacesRoot := filepath.Join(t.TempDir(), "workspaces")
	defaultPath := filepath.Join(workspacesRoot, "proj", "default")
	speedPath := filepath.Join(workspacesRoot, "proj", "speed")
	childPath := filepath.Join(workspacesRoot, "proj", "agm-speed-transition")
	for _, path := range []string{defaultPath, speedPath, childPath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(t, defaultPath, "workspaces_root: "+workspacesRoot+"\nproject: proj\nmain_workspace: default\n")
	createJJWorkspaceLink(t, defaultPath, speedPath)
	createJJWorkspaceLink(t, defaultPath, childPath)
	withCommandCapture(t, stackTargetWorkspaceFixture(t, workspacesRoot, defaultPath, speedPath, childPath, "child111"))
	err := runStack([]string{"--line", "agm-speed-transition", "--repo", childPath, "--yes"})
	if err == nil || !strings.Contains(err.Error(), "target workspace") || !strings.Contains(err.Error(), "--workspace") {
		t.Fatalf("expected line-stack self-target guard with --workspace guidance, got %v", err)
	}
}

func TestRunStackLineFromCurrentNonDefaultWorkspaceTargetsCurrentWorkspace(t *testing.T) {
	workspacesRoot := filepath.Join(t.TempDir(), "workspaces")
	defaultPath := filepath.Join(workspacesRoot, "proj", "default")
	speedPath := filepath.Join(workspacesRoot, "proj", "speed")
	childPath := filepath.Join(workspacesRoot, "proj", "agm-speed-transition")
	for _, path := range []string{defaultPath, speedPath, childPath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(t, defaultPath, "workspaces_root: "+workspacesRoot+"\nproject: proj\nmain_workspace: default\n")
	createJJWorkspaceLink(t, defaultPath, speedPath)
	createJJWorkspaceLink(t, defaultPath, childPath)
	withCommandCapture(t, stackTargetWorkspaceFixture(t, workspacesRoot, defaultPath, speedPath, childPath, "speed111"))
	mutatingRepos := []string{}
	withCommandToStderr(t, func(name string, args ...string) error {
		if name == "jj" && len(args) >= 2 && (strings.Contains(strings.Join(args, " "), " new ") || strings.Contains(strings.Join(args, " "), "workspace update-stale")) {
			mutatingRepos = append(mutatingRepos, args[1])
		}
		return nil
	})
	_, errOut, err := captureOutput(func() error {
		return runStack([]string{"--line", "agm-speed-transition", "--repo", speedPath, "--yes"})
	})
	if err != nil {
		t.Fatalf("expected line stack from speed to target speed, got %v\nstderr:%s", err, errOut)
	}
	for _, repo := range mutatingRepos {
		if repo == defaultPath {
			t.Fatalf("default Workspace must not move for line stack from speed; mutating repos=%v stderr=%q", mutatingRepos, errOut)
		}
	}
	if !strings.Contains(errOut, "Stack target workspace: speed ("+speedPath+")") {
		t.Fatalf("expected target workspace preview, got %q", errOut)
	}
}

func createJJWorkspaceLink(t *testing.T, defaultPath, workspacePath string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(defaultPath, ".jj", "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspacePath, ".jj"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspacePath, ".jj", "repo"), []byte(filepath.Join(defaultPath, ".jj", "repo")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func stackTargetWorkspaceFixture(t *testing.T, workspacesRoot, defaultPath, speedPath, childPath, currentChange string) func(string, ...string) (string, error) {
	t.Helper()
	return func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "workspace list") {
			return strings.Join([]string{
				"default\tdefault111\t" + defaultPath,
				"speed\tspeed111\t" + speedPath,
				"agm-speed-transition\tchild111\t" + childPath,
			}, "\n") + "\n", nil
		}
		if strings.Contains(joined, "log -r @") {
			return currentChange + "\n", nil
		}
		if strings.Contains(joined, " op log ") {
			return "op-before-stack\n", nil
		}
		if strings.Contains(joined, "heads(agm-speed-transition@-)") || strings.Contains(joined, lineStackPayloadDestinationRevset("agm-speed-transition")) {
			return "child-payload\n", nil
		}
		if strings.Contains(joined, workspaceAheadRevset("agm-speed-transition", "default")) || strings.Contains(joined, workspaceAheadRevset("agm-speed-transition", "speed")) || strings.Contains(joined, workspaceAheadRevset("speed", "default")) || strings.Contains(joined, workspaceAheadRevset("default", "speed")) {
			return "ahead\n", nil
		}
		if strings.Contains(joined, "empty() & speed@") || strings.Contains(joined, "empty() & default@") {
			return "empty\n", nil
		}
		_ = workspacesRoot
		return "", nil
	}
}

func TestBuildLineStackPlanTreatsMainEmptyCursorAsPayload(t *testing.T) {
	plan, err := buildLineStackPlan([]workspaceInfo{
		{Ref: workspaceRef{Handle: "default"}, Main: true, Empty: true},
		{Ref: workspaceRef{Handle: "loop"}},
	}, []lineStackInput{{Handle: "default"}, {Handle: "loop"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := lineStackInputHandles(plan.Payloads); strings.Join(got, ",") != "default,loop" {
		t.Fatalf("expected selected Main Workspace to contribute its non-empty ancestor payload by default, got %v", got)
	}
	if got := lineStackInputHandles(plan.FollowOnly); len(got) != 0 {
		t.Fatalf("did not expect Main Workspace to be forced follow-only because only its cursor is empty, got %v", got)
	}
}

func TestBuildLineStackPlanPreservesOrderRolesAndExcludedWorkspaces(t *testing.T) {
	infos := []workspaceInfo{
		{Ref: workspaceRef{Handle: "default"}, Main: true},
		{Ref: workspaceRef{Handle: "helper"}},
		{Ref: workspaceRef{Handle: "ingest"}},
		{Ref: workspaceRef{Handle: "loop"}, Empty: true},
		{Ref: workspaceRef{Handle: "mobile-docs"}},
	}
	plan, err := buildLineStackPlan(infos, []lineStackInput{
		{Handle: "loop", Role: selectorRoleFollow},
		{Handle: "helper", Role: selectorRolePayload},
		{Handle: "ingest", Role: selectorRolePayload},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := lineStackInputHandles(plan.Inputs); strings.Join(got, ",") != "loop,helper,ingest" {
		t.Fatalf("expected input order preserved, got %v", got)
	}
	if got := lineStackInputHandles(plan.Payloads); strings.Join(got, ",") != "helper,ingest" {
		t.Fatalf("expected first payload as bottom and second as top, got %v", got)
	}
	if got := lineStackInputHandles(plan.FollowOnly); strings.Join(got, ",") != "loop" {
		t.Fatalf("expected follow-only loop, got %v", got)
	}
	if got := strings.Join(plan.Excluded, ","); got != "default,mobile-docs" {
		t.Fatalf("expected excluded context to include omitted Workspaces, got %v", plan.Excluded)
	}
	if len(plan.PayloadRebases) != 1 || plan.PayloadRebases[0].SourceRevset != lineStackPayloadSourceRevset("ingest", "helper") || plan.PayloadRebases[0].DestinationRevset != lineStackPayloadDestinationRevset("helper") {
		t.Fatalf("unexpected payload rebase plan: %+v", plan.PayloadRebases)
	}
	if plan.FinalTip != lineStackPayloadDestinationRevset("ingest") {
		t.Fatalf("expected final tip to be last payload frontier, got %q", plan.FinalTip)
	}
}

func TestLineStackPayloadRevsetsUseUniqueNonEmptyFrontiers(t *testing.T) {
	source := lineStackPayloadSourceRevset("ingest", "helper")
	for _, want := range []string{"::ingest@", "~::helper@", "~empty()"} {
		if !strings.Contains(source, want) {
			t.Fatalf("expected source revset %q to contain %q", source, want)
		}
	}
	for _, notWant := range []string{"@-", "roots(", "mutable()"} {
		if strings.Contains(source, notWant) {
			t.Fatalf("source revset should not contain %q, got %q", notWant, source)
		}
	}
	destination := lineStackPayloadDestinationRevset("helper")
	for _, want := range []string{"heads(", "::helper@", "~empty()"} {
		if !strings.Contains(destination, want) {
			t.Fatalf("expected destination revset %q to contain %q", destination, want)
		}
	}
	if strings.Contains(destination, "@-") {
		t.Fatalf("destination revset should not use raw Workspace payload parent, got %q", destination)
	}
}

func TestResolveLineStackPlanRejectsOmittedWorkspaceDescendants(t *testing.T) {
	plan, err := buildLineStackPlan([]workspaceInfo{
		{Ref: workspaceRef{Handle: "alpha"}},
		{Ref: workspaceRef{Handle: "bravo"}},
		{Ref: workspaceRef{Handle: "omitted"}},
	}, []lineStackInput{{Handle: "alpha", Role: selectorRolePayload}, {Handle: "bravo", Role: selectorRolePayload}})
	if err != nil {
		t.Fatal(err)
	}
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, lineStackPayloadDestinationRevset("alpha")):
			return "alpha-tip\n", nil
		case strings.Contains(joined, lineStackPayloadDestinationRevset("bravo")):
			return "bravo-tip\n", nil
		case strings.Contains(joined, "omitted@ & descendants("+lineStackPayloadSourceRevset("bravo", "alpha")+")"):
			return "omitted-tip\n", nil
		case strings.Contains(joined, lineStackPayloadSourceRevset("bravo", "alpha")) && !strings.Contains(joined, "immutable()") && !strings.Contains(joined, "descendants"):
			return "bravo-tip\n", nil
		}
		return "", nil
	})
	_, err = resolveLineStackPlan("/repo", plan)
	if err == nil || !strings.Contains(err.Error(), "omitted Workspace \"omitted\"") {
		t.Fatalf("expected omitted descendant rejection, got %v", err)
	}
}

func TestResolveLineStackPlanRejectsOmittedWorkspaceDescendantOfInProgressHead(t *testing.T) {
	plan, err := buildLineStackPlan([]workspaceInfo{
		{Ref: workspaceRef{Handle: "alpha"}},
		{Ref: workspaceRef{Handle: "loop"}},
		{Ref: workspaceRef{Handle: "omitted"}},
	}, []lineStackInput{{Handle: "alpha", Role: selectorRolePayload}, {Handle: "loop", Role: selectorRolePayload}})
	if err != nil {
		t.Fatal(err)
	}
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, lineStackInProgressHeadRevset("loop")):
			return "loop-wip\n", nil
		case strings.Contains(joined, "omitted@ & descendants(loop-wip)"):
			return "omitted-tip\n", nil
		case strings.Contains(joined, lineStackPayloadDestinationRevset("alpha")):
			return "alpha-tip\n", nil
		case strings.Contains(joined, lineStackPayloadDestinationRevsetExcluding("loop", []string{"loop-wip"})):
			return "loop-payload-tip\n", nil
		case strings.Contains(joined, lineStackPayloadSourceRevset("loop", "alpha")) && strings.Contains(joined, "~(loop-wip)") && !strings.Contains(joined, "immutable()") && !strings.Contains(joined, "descendants"):
			return "loop-payload-tip\n", nil
		}
		return "", nil
	})
	_, err = resolveLineStackPlan("/repo", plan)
	if err == nil || !strings.Contains(err.Error(), "in-progress Workspace \"loop\"") || !strings.Contains(err.Error(), "omitted Workspace \"omitted\"") {
		t.Fatalf("expected omitted descendant of in-progress head rejection, got %v", err)
	}
}

func TestResolveLineStackPlanRejectsImmutableUniquePayload(t *testing.T) {
	plan, err := buildLineStackPlan([]workspaceInfo{
		{Ref: workspaceRef{Handle: "alpha"}},
		{Ref: workspaceRef{Handle: "bravo"}},
	}, []lineStackInput{{Handle: "alpha", Role: selectorRolePayload}, {Handle: "bravo", Role: selectorRolePayload}})
	if err != nil {
		t.Fatal(err)
	}
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, lineStackPayloadDestinationRevset("alpha")):
			return "alpha-tip\n", nil
		case strings.Contains(joined, lineStackPayloadDestinationRevset("bravo")):
			return "bravo-tip\n", nil
		case strings.Contains(joined, "immutable() & "+lineStackPayloadSourceRevset("bravo", "alpha")):
			return "immutable-bravo\n", nil
		}
		return "", nil
	})
	_, err = resolveLineStackPlan("/repo", plan)
	if err == nil || !strings.Contains(err.Error(), "unique immutable commits") {
		t.Fatalf("expected immutable payload rejection, got %v", err)
	}
}

func TestResolveLineStackPlanExcludesInProgressHeadFromPayloadAndRebasesIt(t *testing.T) {
	plan, err := buildLineStackPlan([]workspaceInfo{
		{Ref: workspaceRef{Handle: "alpha"}},
		{Ref: workspaceRef{Handle: "loop"}},
	}, []lineStackInput{{Handle: "alpha", Role: selectorRolePayload}, {Handle: "loop", Role: selectorRolePayload}})
	if err != nil {
		t.Fatal(err)
	}
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, lineStackInProgressHeadRevset("loop")):
			return "loop-wip\n", nil
		case strings.Contains(joined, lineStackPayloadDestinationRevset("alpha")):
			return "alpha-tip\n", nil
		case strings.Contains(joined, lineStackPayloadDestinationRevsetExcluding("loop", []string{"loop-wip"})):
			return "loop-payload-tip\n", nil
		case strings.Contains(joined, lineStackPayloadSourceRevset("loop", "alpha")) && strings.Contains(joined, "~(loop-wip)") && !strings.Contains(joined, "immutable()") && !strings.Contains(joined, "descendants"):
			return "loop-payload-tip\n", nil
		}
		return "", nil
	})
	resolved, err := resolveLineStackPlan("/repo", plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.PayloadRebases) != 1 || resolved.PayloadRebases[0].SourceRevset != "loop-payload-tip" || strings.Contains(resolved.PayloadRebases[0].SourceRevset, "loop-wip") {
		t.Fatalf("expected in-progress loop@ to be excluded from payload rebase, got %+v", resolved.PayloadRebases)
	}
	if resolved.FinalTip != "loop-payload-tip" {
		t.Fatalf("expected final tip to be payload below in-progress head, got %q", resolved.FinalTip)
	}
	advance := lineStackAdvanceByHandle(resolved.Advances, "loop")
	if !advance.InProgress || advance.InProgressRevset != "loop-wip" {
		t.Fatalf("expected loop advance to rebase in-progress head loop-wip, got %+v", advance)
	}
}

func TestLineStackNeutralExampleRebasesLoopInProgressHeadOnTop(t *testing.T) {
	infos := []workspaceInfo{
		{Ref: workspaceRef{Handle: "default"}, Path: "/acme-api/default", Main: true},
		{Ref: workspaceRef{Handle: "loop"}, Path: "/acme-api/loop"},
		{Ref: workspaceRef{Handle: "audit-log"}, Path: "/acme-api/audit-log"},
		{Ref: workspaceRef{Handle: "task-20260625084133-1ljpys"}, Path: "/acme-api/task-20260625084133-1ljpys", Empty: true},
		{Ref: workspaceRef{Handle: "mobile-docs"}, Path: "/acme-api/mobile-docs", Empty: true},
		{Ref: workspaceRef{Handle: "helper"}, Path: "/acme-api/helper"},
	}
	plan, err := buildLineStackPlan(infos, []lineStackInput{
		{Handle: "default"},
		{Handle: "loop"},
		{Handle: "audit-log"},
		{Handle: "task-20260625084133-1ljpys"},
		{Handle: "mobile-docs"},
	})
	if err != nil {
		t.Fatal(err)
	}
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, lineStackInProgressHeadRevset("loop")):
			return "nr-loop-wip\n", nil
		case strings.Contains(joined, "description(\"\")"):
			return "", nil
		case strings.Contains(joined, lineStackPayloadDestinationRevset("default")):
			return "uxn-default-tip\n", nil
		case strings.Contains(joined, lineStackPayloadDestinationRevsetExcluding("loop", []string{"nr-loop-wip"})):
			return "uxo-loop-payload\n", nil
		case strings.Contains(joined, lineStackPayloadDestinationRevset("audit-log")):
			return "ys-session-tip\n", nil
		case strings.Contains(joined, lineStackPayloadSourceRevset("loop", "default")) && strings.Contains(joined, "~(nr-loop-wip)") && !strings.Contains(joined, "immutable()") && !strings.Contains(joined, "descendants"):
			return "uxo-loop-payload\n", nil
		case strings.Contains(joined, lineStackPayloadSourceRevset("audit-log", "loop")) && !strings.Contains(joined, "immutable()") && !strings.Contains(joined, "descendants"):
			return "vv-session-base\nys-session-tip\n", nil
		}
		return "", nil
	})
	resolved, err := resolveLineStackPlan("/acme-api/default", plan)
	if err != nil {
		t.Fatal(err)
	}
	if got := lineStackInputHandles(resolved.Payloads); strings.Join(got, ",") != "default,loop,audit-log" {
		t.Fatalf("expected task/mobile empty rows to be follow-only and not payloads, got %v", got)
	}
	loopAdvance := lineStackAdvanceByHandle(resolved.Advances, "loop")
	if !loopAdvance.InProgress || loopAdvance.InProgressRevset != "nr-loop-wip" {
		t.Fatalf("expected loop@ nr/no-description head to be tracked as in-progress, got %+v", loopAdvance)
	}
	if len(resolved.PayloadRebases) != 2 {
		t.Fatalf("expected loop and audit-log payload rebases, got %+v", resolved.PayloadRebases)
	}
	if resolved.PayloadRebases[0].Handle != "loop" || resolved.PayloadRebases[0].SourceRevset != "uxo-loop-payload" || strings.Contains(resolved.PayloadRebases[0].SourceRevset, "nr-loop-wip") {
		t.Fatalf("expected loop payload rebase to use uxo below nr, got %+v", resolved.PayloadRebases[0])
	}
	if resolved.FinalTip != "ys-session-tip" {
		t.Fatalf("expected final line tip to be audit-log payload frontier, got %q", resolved.FinalTip)
	}
	preview := lineStackPlanText(resolved, "op-before-example", "")
	for _, want := range []string{"In-progress Workspace rebases:", "loop@ -> ys-session-tip", "Follow-only advances:", "task-20260625084133-1ljpys@ -> ys-session-tip", "mobile-docs@ -> ys-session-tip"} {
		if !strings.Contains(preview, want) {
			t.Fatalf("expected preview to contain %q, got %q", want, preview)
		}
	}
	rebaseCommands := [][]string{}
	newCommands := [][]string{}
	withCommandToStderr(t, func(name string, args ...string) error {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, " rebase ") {
			rebaseCommands = append(rebaseCommands, append([]string(nil), args...))
		}
		if strings.Contains(joined, " new ") {
			newCommands = append(newCommands, append([]string(nil), args...))
		}
		return nil
	})
	if err := executeLineStackPlan("/acme-api/default", resolved); err != nil {
		t.Fatal(err)
	}
	if len(rebaseCommands) != 3 {
		t.Fatalf("expected two payload rebases plus loop WIP rebase, got %v", rebaseCommands)
	}
	if !strings.Contains(strings.Join(rebaseCommands[0], " "), "-r uxo-loop-payload") || strings.Contains(strings.Join(rebaseCommands[0], " "), "nr-loop-wip") {
		t.Fatalf("expected loop payload command to exclude nr WIP head, got %v", rebaseCommands[0])
	}
	if !strings.Contains(strings.Join(rebaseCommands[2], " "), "-R /acme-api/loop rebase -r loop@ -d ys-session-tip") {
		t.Fatalf("expected loop WIP head to be rebased on top of final line, got %v", rebaseCommands[2])
	}
	for _, cmd := range newCommands {
		if strings.Contains(strings.Join(cmd, " "), "/acme-api/loop") {
			t.Fatalf("loop WIP Workspace should not be replaced with jj new cursor, got new commands %v", newCommands)
		}
	}
}

func TestExecuteLineStackPlanRebasesInProgressHeadInsteadOfCreatingCursor(t *testing.T) {
	plan := lineStackPlan{
		Advances: []lineStackAdvance{
			{Handle: "alpha", Path: "/repo/alpha", Role: selectorRolePayload},
			{Handle: "loop", Path: "/repo/loop", Role: selectorRolePayload, InProgress: true, InProgressRevset: "loop-wip"},
		},
		FinalTip: "line-tip",
	}
	newCommands := [][]string{}
	rebaseCommands := [][]string{}
	withCommandToStderr(t, func(name string, args ...string) error {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, " new ") {
			newCommands = append(newCommands, append([]string(nil), args...))
		}
		if strings.Contains(joined, " rebase ") {
			rebaseCommands = append(rebaseCommands, append([]string(nil), args...))
		}
		return nil
	})
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		return "", nil
	})
	if err := executeLineStackPlan("/repo", plan); err != nil {
		t.Fatal(err)
	}
	if len(newCommands) != 1 || !strings.Contains(strings.Join(newCommands[0], " "), "-R /repo/alpha new line-tip") {
		t.Fatalf("expected only non-WIP Workspace to receive new cursor, got %v", newCommands)
	}
	if len(rebaseCommands) != 1 || !strings.Contains(strings.Join(rebaseCommands[0], " "), "-R /repo/loop rebase -r loop@ -d line-tip") {
		t.Fatalf("expected WIP Workspace head to be rebased onto final tip, got %v", rebaseCommands)
	}
}

func TestLineStackProjectedLogShowsPlannedFinalLine(t *testing.T) {
	plan := lineStackPlan{
		Payloads: []lineStackInput{{Handle: "alpha", Role: selectorRolePayload}, {Handle: "bravo", Role: selectorRolePayload}, {Handle: "charlie", Role: selectorRolePayload}},
		Advances: []lineStackAdvance{{Handle: "alpha", Role: selectorRolePayload}, {Handle: "bravo", Role: selectorRolePayload}, {Handle: "charlie", Role: selectorRolePayload}, {Handle: "loop", Role: selectorRoleFollow}},
	}
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "description.first_line") {
			return "", nil
		}
		switch {
		case strings.Contains(joined, lineStackPayloadSourceRevset("charlie", "bravo")):
			return "c3\tcharlie top\n", nil
		case strings.Contains(joined, lineStackPayloadSourceRevset("bravo", "alpha")):
			return "b2\tbravo top\nb1\tbravo bottom\n", nil
		case strings.Contains(joined, lineStackFirstPayloadPreviewRevset("alpha", "bravo")):
			return "a1\talpha base\n", nil
		default:
			return "", nil
		}
	})
	projected, err := lineStackProjectedLog("/repo", plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"@  alpha@ (planned empty cursor)", "│ ○  bravo@ (planned empty cursor)", "│ ○  charlie@ (planned empty cursor)", "│ ○  loop@ (planned empty cursor)", "○  c3 charlie top", "○  b2 bravo top", "○  b1 bravo bottom", "○  a1 alpha base"} {
		if !strings.Contains(projected, want) {
			t.Fatalf("expected projected log to contain %q, got %q", want, projected)
		}
	}
	if strings.Index(projected, "c3") > strings.Index(projected, "b2") || strings.Index(projected, "b2") > strings.Index(projected, "a1") {
		t.Fatalf("expected projected log to list final top-to-bottom order, got %q", projected)
	}
}

func TestLineStackProjectedLogUsesColourWhenRequested(t *testing.T) {
	plan := lineStackPlan{
		Payloads: []lineStackInput{{Handle: "alpha", Role: selectorRolePayload}, {Handle: "bravo", Role: selectorRolePayload}},
		Advances: []lineStackAdvance{{Handle: "alpha", Role: selectorRolePayload}, {Handle: "bravo", Role: selectorRolePayload}},
	}
	sawColorAlways := false
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "--color=always") {
			sawColorAlways = true
		}
		switch {
		case strings.Contains(joined, lineStackPayloadSourceRevset("bravo", "alpha")):
			return "\x1b[38;5;5mb1\x1b[39m\tbravo payload\n", nil
		case strings.Contains(joined, lineStackFirstPayloadPreviewRevset("alpha", "bravo")):
			return "\x1b[38;5;5ma1\x1b[39m\talpha payload\n", nil
		default:
			return "", nil
		}
	})
	projected, err := lineStackProjectedLogWithColor("/repo", plan, true)
	if err != nil {
		t.Fatal(err)
	}
	if !sawColorAlways {
		t.Fatalf("expected projected log query to request jj colour")
	}
	if !strings.Contains(projected, "\x1b[") {
		t.Fatalf("expected coloured projected log to contain ANSI escapes, got %q", projected)
	}
	plain := stripANSI(projected)
	for _, want := range []string{"@  alpha@ (planned empty cursor)", "│ ○  bravo@ (planned empty cursor)", "○  b1 bravo payload", "○  a1 alpha payload"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("expected plain projected log to contain %q after stripping ANSI, got %q", want, plain)
		}
	}
}

func TestLineStackProjectedLogStaysPlainWhenColourDisabled(t *testing.T) {
	plan := lineStackPlan{
		Payloads: []lineStackInput{{Handle: "alpha", Role: selectorRolePayload}},
		Advances: []lineStackAdvance{{Handle: "alpha", Role: selectorRolePayload}},
	}
	sawColorNever := false
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "--color=always") {
			t.Fatalf("plain projected log should not request forced colour: %v", args)
		}
		if strings.Contains(joined, "--color=never") {
			sawColorNever = true
		}
		return "a1\talpha payload\n", nil
	})
	projected, err := lineStackProjectedLogWithColor("/repo", plan, false)
	if err != nil {
		t.Fatal(err)
	}
	if !sawColorNever {
		t.Fatalf("expected projected log query to explicitly disable jj colour")
	}
	if strings.Contains(projected, "\x1b[") {
		t.Fatalf("expected plain projected log to have no ANSI escapes, got %q", projected)
	}
	if !strings.Contains(projected, "○  a1 alpha payload") {
		t.Fatalf("expected plain projected log content to be preserved, got %q", projected)
	}
}

func TestLineStackPlanTextScopesColourToProjectedLogBlock(t *testing.T) {
	plan := lineStackPlan{
		Inputs:   []lineStackInput{{Handle: "alpha", Role: selectorRolePayload}},
		Payloads: []lineStackInput{{Handle: "alpha", Role: selectorRolePayload}},
		Advances: []lineStackAdvance{{Handle: "alpha", Role: selectorRolePayload}},
		FinalTip: "alpha-tip",
	}
	preview := lineStackPlanText(plan, "op-preview", "\x1b[38;5;10m@\x1b[39m  alpha@ (planned empty cursor)\n\x1b[38;5;7m○\x1b[39m  \x1b[38;5;5ma1\x1b[39m alpha payload")
	projectedHeader := strings.Index(preview, "Projected jj log after Line Stack:")
	optionsHeader := strings.Index(preview, "Options:")
	if projectedHeader < 0 || optionsHeader < 0 || projectedHeader > optionsHeader {
		t.Fatalf("expected preview to contain projected log before options, got %q", preview)
	}
	if strings.Contains(preview[:projectedHeader], "\x1b[") {
		t.Fatalf("expected no ANSI before projected log block, got %q", preview[:projectedHeader])
	}
	if !strings.Contains(preview[projectedHeader:optionsHeader], "\x1b[") {
		t.Fatalf("expected ANSI inside projected log block, got %q", preview[projectedHeader:optionsHeader])
	}
	if strings.Contains(preview[optionsHeader:], "\x1b[") {
		t.Fatalf("expected no ANSI after projected log block, got %q", preview[optionsHeader:])
	}
}

func TestLineStackProjectedLogShowsInProgressHeadAbovePayloadWithoutDuplicatingIt(t *testing.T) {
	plan := lineStackPlan{
		Payloads: []lineStackInput{{Handle: "alpha", Role: selectorRolePayload}, {Handle: "loop", Role: selectorRolePayload}},
		Advances: []lineStackAdvance{{Handle: "alpha", Role: selectorRolePayload}, {Handle: "loop", Role: selectorRolePayload, InProgress: true, InProgressRevset: "loop-wip"}},
	}
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "description.first_line") {
			return "", nil
		}
		switch {
		case strings.Contains(joined, lineStackPayloadSourceRevset("loop", "alpha")) && strings.Contains(joined, "~(loop-wip)"):
			return "loop-payload\tloop payload\n", nil
		case strings.Contains(joined, lineStackPayloadSourceRevset("loop", "alpha")):
			return "loop-wip\t\nloop-payload\tloop payload\n", nil
		case strings.Contains(joined, lineStackFirstPayloadPreviewRevset("alpha", "loop")):
			return "alpha-payload\talpha payload\n", nil
		default:
			return "", nil
		}
	})
	projected, err := lineStackProjectedLog("/repo", plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"│ ○  loop-wip loop@ (in-progress; will rebase on top)", "○  loop-payload loop payload", "○  alpha-payload alpha payload"} {
		if !strings.Contains(projected, want) {
			t.Fatalf("expected projected log to contain %q, got %q", want, projected)
		}
	}
	if strings.Count(projected, "loop-wip") != 1 {
		t.Fatalf("in-progress head should be shown once as the Workspace cursor, not duplicated in payload rows: %q", projected)
	}
}

func TestLineStackProjectedLogStopsFirstPayloadAtNextPayloadMerge(t *testing.T) {
	plan := lineStackPlan{
		Payloads: []lineStackInput{{Handle: "alpha", Role: selectorRolePayload}, {Handle: "bravo", Role: selectorRolePayload}},
		Advances: []lineStackAdvance{{Handle: "alpha", Role: selectorRolePayload}, {Handle: "bravo", Role: selectorRolePayload}},
	}
	narrowFirstPayload := "(::alpha@ & ~::bravo@ & ~empty())"
	firstPayloadRevsets := []string{}
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "description.first_line") {
			return "", nil
		}
		revset := ""
		for i, arg := range args {
			if arg == "-r" && i+1 < len(args) {
				revset = args[i+1]
				break
			}
		}
		switch revset {
		case lineStackPayloadSourceRevset("bravo", "alpha"):
			return "b1\tbravo payload\n", nil
		case narrowFirstPayload:
			firstPayloadRevsets = append(firstPayloadRevsets, revset)
			return "a1\talpha payload\n", nil
		case "(::alpha@ & ~empty())":
			return "ancient\tancient repo history\na1\talpha payload\n", nil
		case lineStackPayloadDestinationRevset("alpha"):
			return "a-head\talpha head fallback\n", nil
		default:
			return "", nil
		}
	})
	projected, err := lineStackProjectedLog("/repo", plan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(projected, "ancient repo history") {
		t.Fatalf("projected log should not include broad first-payload ancestors, got %q", projected)
	}
	if !strings.Contains(projected, "○  a1 alpha payload") || !strings.Contains(projected, "○  b1 bravo payload") {
		t.Fatalf("expected projected log to include relevant payload rows, got %q", projected)
	}
	if len(firstPayloadRevsets) != 1 {
		t.Fatalf("expected first payload to be queried with narrow revset %q, got %v", narrowFirstPayload, firstPayloadRevsets)
	}
}

func TestLineStackPreviewSeparatesInProgressRebasesFromCursorAdvances(t *testing.T) {
	plan := lineStackPlan{
		Inputs:   []lineStackInput{{Handle: "alpha", Role: selectorRolePayload}, {Handle: "loop", Role: selectorRolePayload}},
		Payloads: []lineStackInput{{Handle: "alpha", Role: selectorRolePayload}, {Handle: "loop", Role: selectorRolePayload}},
		Advances: []lineStackAdvance{
			{Handle: "alpha", Role: selectorRolePayload},
			{Handle: "loop", Role: selectorRolePayload, InProgress: true, InProgressRevset: "loop-wip"},
		},
		PayloadRebases: []lineStackPayloadRebase{{Handle: "loop", SourceRevset: "loop-payload", DestinationRevset: "alpha-tip"}},
		FinalTip:       "loop-payload",
	}
	preview := lineStackPlanText(plan, "op-preview", "")
	for _, want := range []string{"In-progress Workspace rebases:", "loop@ -> loop-payload", "Payload Workspace head advances:", "alpha@ -> loop-payload"} {
		if !strings.Contains(preview, want) {
			t.Fatalf("expected preview to contain %q, got %q", want, preview)
		}
	}
	if strings.Count(preview, "loop@ -> loop-payload") != 1 {
		t.Fatalf("expected in-progress loop advance to be shown once outside payload cursor advances, got %q", preview)
	}
}

func TestLineStackPreviewListsInputsRebasesFollowExcludedOptionsAndUndo(t *testing.T) {
	plan, err := buildLineStackPlan([]workspaceInfo{
		{Ref: workspaceRef{Handle: "default"}, Main: true},
		{Ref: workspaceRef{Handle: "alpha"}},
		{Ref: workspaceRef{Handle: "bravo"}},
		{Ref: workspaceRef{Handle: "charlie"}, Empty: true},
		{Ref: workspaceRef{Handle: "omitted"}},
	}, []lineStackInput{{Handle: "alpha", Role: selectorRolePayload}, {Handle: "charlie", Role: selectorRoleFollow}, {Handle: "bravo", Role: selectorRolePayload}})
	if err != nil {
		t.Fatal(err)
	}
	projected := "@  alpha@ (planned empty cursor)\n│ ○  bravo@ (planned empty cursor)\n├─╯\n│ ○  charlie@ (planned empty cursor)\n├─╯\n○  bravo-tip bravo top\n○  alpha-tip alpha base"
	preview := lineStackPlanText(plan, "op-preview", projected)
	for _, want := range []string{"Line Stack preview", "Projected jj log after Line Stack:", "@  alpha@ (planned empty cursor)", "│ ○  bravo@ (planned empty cursor)", "○  bravo-tip bravo top", "Options:", "mode: line", "1. alpha (payload)", "2. charlie (follow-only)", "3. bravo (payload)", "bravo payload:", "Follow-only advances:", "charlie@ -> " + lineStackPayloadDestinationRevset("bravo"), "Payload Workspace head advances:", "alpha@ -> " + lineStackPayloadDestinationRevset("bravo"), "bravo@ -> " + lineStackPayloadDestinationRevset("bravo"), "Excluded:", "default", "omitted", "To undo this run: jj op restore op-preview"} {
		if !strings.Contains(preview, want) {
			t.Fatalf("expected preview to contain %q, got %q", want, preview)
		}
	}
	if strings.Count(preview, "charlie@ ->") != 1 {
		t.Fatalf("expected follow-only Workspace advance to be shown once, got %q", preview)
	}
}

func TestBuildLineStackPlanForcesContextRowsToFollowAndRejectsNoPayload(t *testing.T) {
	infos := []workspaceInfo{
		{Ref: workspaceRef{Handle: "empty"}, Empty: true},
		{Ref: workspaceRef{Handle: "stacked"}, Stacked: true},
		{Ref: workspaceRef{Handle: "payload"}},
	}
	plan, err := buildLineStackPlan(infos, []lineStackInput{{Handle: "empty", Role: selectorRolePayload}, {Handle: "stacked", Role: selectorRolePayload}, {Handle: "payload", Role: selectorRolePayload}})
	if err != nil {
		t.Fatal(err)
	}
	if got := lineStackInputHandles(plan.FollowOnly); strings.Join(got, ",") != "empty,stacked" {
		t.Fatalf("expected empty/stacked Workspaces to be forced follow-only, got %v", got)
	}
	_, err = buildLineStackPlan(infos[:2], []lineStackInput{{Handle: "empty", Role: selectorRolePayload}, {Handle: "stacked", Role: selectorRoleFollow}})
	if err == nil || !strings.Contains(err.Error(), "requires at least one payload") {
		t.Fatalf("expected no-payload rejection, got %v", err)
	}
}

func lineStackInputHandles(inputs []lineStackInput) []string {
	handles := make([]string, 0, len(inputs))
	for _, input := range inputs {
		handles = append(handles, input.Handle)
	}
	return handles
}

func lineStackAdvanceByHandle(advances []lineStackAdvance, handle string) lineStackAdvance {
	for _, advance := range advances {
		if advance.Handle == handle {
			return advance
		}
	}
	return lineStackAdvance{}
}

func TestRunStackLineUsesOrderedPayloadRebasesAndAdvancesSelectedHeadsOnly(t *testing.T) {
	workspacesRoot := filepath.Join(t.TempDir(), "workspaces")
	mainPath := filepath.Join(workspacesRoot, "proj", "default")
	handles := []string{"default", "helper", "ingest", "worker", "loop", "mobile-docs"}
	for _, handle := range handles {
		path := filepath.Join(workspacesRoot, "proj", handle)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(t, mainPath, "workspaces_root: "+workspacesRoot+"\nproject: proj\nmain_workspace: default\n")
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "workspace list") {
			lines := []string{}
			for _, handle := range handles {
				lines = append(lines, handle+"\t"+handle+"111\t"+filepath.Join(workspacesRoot, "proj", handle))
			}
			return strings.Join(lines, "\n") + "\n", nil
		}
		if strings.Contains(joined, " op log ") {
			return "op-before-line\n", nil
		}
		if strings.Contains(joined, workspaceAheadRevset("helper", "default")) || strings.Contains(joined, workspaceAheadRevset("ingest", "default")) || strings.Contains(joined, workspaceAheadRevset("worker", "default")) || strings.Contains(joined, workspaceAheadRevset("mobile-docs", "default")) {
			return "ahead\n", nil
		}
		if strings.Contains(joined, "empty() & loop@") && !strings.Contains(joined, "description(\"\")") {
			return "empty\n", nil
		}
		switch {
		case strings.Contains(joined, lineStackPayloadDestinationRevset("helper")):
			return "agent-tip\n", nil
		case strings.Contains(joined, lineStackPayloadDestinationRevset("ingest")):
			return "manual-tip\n", nil
		case strings.Contains(joined, lineStackPayloadDestinationRevset("worker")):
			return "switch-tip\n", nil
		case strings.Contains(joined, lineStackPayloadSourceRevset("ingest", "helper")) && !strings.Contains(joined, "immutable()") && !strings.Contains(joined, "descendants"):
			return "manual-root\nmanual-tip\n", nil
		case strings.Contains(joined, lineStackPayloadSourceRevset("worker", "ingest")) && !strings.Contains(joined, "immutable()") && !strings.Contains(joined, "descendants"):
			return "switch-tip\n", nil
		}
		return "", nil
	})
	rebaseCommands := [][]string{}
	newCommands := [][]string{}
	updateStale := false
	withCommandToStderr(t, func(name string, args ...string) error {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, " rebase ") {
			rebaseCommands = append(rebaseCommands, append([]string(nil), args...))
		}
		if strings.Contains(joined, " new ") {
			newCommands = append(newCommands, append([]string(nil), args...))
		}
		if strings.Contains(joined, "workspace update-stale") {
			updateStale = true
		}
		return nil
	})
	out, errOut, err := captureOutput(func() error {
		return runStack([]string{"--line", "helper", "ingest", "worker", "loop", "--repo", mainPath, "--yes"})
	})
	if err != nil {
		t.Fatalf("expected ordered line stack to run, got %v\nstderr:%s", err, errOut)
	}
	if out != "" {
		t.Fatalf("expected Line Stack preview/progress to stay off stdout, got %q", out)
	}
	if len(rebaseCommands) != 2 {
		t.Fatalf("expected two payload rebases, got %d: %v", len(rebaseCommands), rebaseCommands)
	}
	if len(newCommands) != 4 {
		t.Fatalf("expected four selected Workspace `jj new` advances, got %d: %v", len(newCommands), newCommands)
	}
	if !strings.Contains(strings.Join(rebaseCommands[0], " "), "-r (manual-root | manual-tip)") || strings.Join(rebaseDestinations(rebaseCommands[0]), ",") != "agent-tip" {
		t.Fatalf("expected materialized ingest payload rebase onto helper frontier, got %v", rebaseCommands[0])
	}
	if !strings.Contains(strings.Join(rebaseCommands[1], " "), "-r switch-tip") || strings.Join(rebaseDestinations(rebaseCommands[1]), ",") != "manual-tip" {
		t.Fatalf("expected materialized worker payload rebase onto ingest frontier, got %v", rebaseCommands[1])
	}
	for i, handle := range []string{"helper", "ingest", "worker", "loop"} {
		cmd := strings.Join(newCommands[i], " ")
		if !strings.Contains(cmd, "-R "+filepath.Join(workspacesRoot, "proj", handle)) || !strings.Contains(cmd, " new switch-tip") {
			t.Fatalf("expected selected Workspace %s to get a new cursor at final tip, got %v", handle, newCommands[i])
		}
	}
	for _, cmd := range append(rebaseCommands, newCommands...) {
		if strings.Contains(strings.Join(cmd, " "), "mobile-docs@") || strings.Contains(strings.Join(cmd, " "), filepath.Join(workspacesRoot, "proj", "mobile-docs")) {
			t.Fatalf("unselected Workspace was unexpectedly rebased or advanced: rebase=%v new=%v", rebaseCommands, newCommands)
		}
	}
	if !updateStale {
		t.Fatal("expected workspace update-stale after line stack")
	}
	for _, want := range []string{"Line Stack preview", "helper", "ingest", "worker", "loop", "mobile-docs", "To undo this run: jj op restore op-before-line"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("expected stderr preview/undo to contain %q, got %q", want, errOut)
		}
	}
}

func TestRunStackLineStopsOnConflictBeforeAdvances(t *testing.T) {
	workspacesRoot := filepath.Join(t.TempDir(), "workspaces")
	mainPath := filepath.Join(workspacesRoot, "proj", "default")
	handles := []string{"default", "alpha", "bravo", "loop"}
	for _, handle := range handles {
		if err := os.MkdirAll(filepath.Join(workspacesRoot, "proj", handle), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(t, mainPath, "workspaces_root: "+workspacesRoot+"\nproject: proj\nmain_workspace: default\n")
	rebaseOccurred := false
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "workspace list") {
			lines := []string{}
			for _, handle := range handles {
				lines = append(lines, handle+"\t"+handle+"111\t"+filepath.Join(workspacesRoot, "proj", handle))
			}
			return strings.Join(lines, "\n") + "\n", nil
		}
		if strings.Contains(joined, " op log ") {
			return "op-before-conflict\n", nil
		}
		if strings.Contains(joined, workspaceAheadRevset("alpha", "default")) || strings.Contains(joined, workspaceAheadRevset("bravo", "default")) {
			return "ahead\n", nil
		}
		if strings.Contains(joined, "empty() & loop@") && !strings.Contains(joined, "description(\"\")") {
			return "empty\n", nil
		}
		switch {
		case strings.Contains(joined, lineStackPayloadDestinationRevset("alpha")):
			return "alpha-tip\n", nil
		case strings.Contains(joined, lineStackPayloadDestinationRevset("bravo")):
			return "bravo-tip\n", nil
		case strings.Contains(joined, lineStackPayloadSourceRevset("bravo", "alpha")) && !strings.Contains(joined, "immutable()") && !strings.Contains(joined, "descendants"):
			return "bravo-tip\n", nil
		}
		if rebaseOccurred && strings.Contains(joined, "conflicts() & bravo-tip") {
			return "conflict\n", nil
		}
		return "", nil
	})
	rebaseCommands := [][]string{}
	updateStale := false
	withCommandToStderr(t, func(name string, args ...string) error {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, " rebase ") {
			rebaseCommands = append(rebaseCommands, append([]string(nil), args...))
			rebaseOccurred = true
		}
		if strings.Contains(joined, "workspace update-stale") {
			updateStale = true
		}
		return nil
	})
	_, errOut, err := captureOutput(func() error {
		return runStack([]string{"--line", "alpha", "bravo", "loop", "--repo", mainPath, "--yes"})
	})
	if err == nil || !strings.Contains(err.Error(), "conflicts in Workspace \"bravo\"") {
		t.Fatalf("expected conflict error for bravo, got %v", err)
	}
	if len(rebaseCommands) != 1 {
		t.Fatalf("expected only payload rebase before conflict stop, got %v", rebaseCommands)
	}
	if updateStale {
		t.Fatal("did not expect update-stale after conflict")
	}
	if !strings.Contains(errOut, "To undo this run: jj op restore op-before-conflict") {
		t.Fatalf("expected undo hint in preview, got %q", errOut)
	}
}

func TestRunStackLineRejectsIncompatibleModes(t *testing.T) {
	if err := runStack([]string{"--line", "--all"}); err == nil || !strings.Contains(err.Error(), "--line cannot be combined with --all") {
		t.Fatalf("expected --line --all rejection, got %v", err)
	}
	if err := runStack([]string{"--line", "--stack-shape", "merge", "alpha"}); err == nil || !strings.Contains(err.Error(), "--line cannot be combined") {
		t.Fatalf("expected --line graph flag rejection, got %v", err)
	}
}

func TestCurrentOperationIDReadsCurrentOperationWithoutSnapshot(t *testing.T) {
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if name != "jj" || !strings.Contains(joined, "--ignore-working-copy --at-op=@ op log") || !strings.Contains(joined, "id.short()") {
			t.Fatalf("expected current op log probe without snapshot, got %s %s", name, joined)
		}
		return "abc123\n", nil
	})
	got, err := currentOperationID("/repo")
	if err != nil || got != "abc123" {
		t.Fatalf("expected current operation id, got %q err=%v", got, err)
	}
}

func TestRunCommandsUseRepositoryFlagAsWorkingDirectory(t *testing.T) {
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldwd) })

	repoPath := t.TempDir()
	deletedParent := t.TempDir()
	deletedCwd := filepath.Join(deletedParent, "deleted-cwd")
	if err := os.MkdirAll(deletedCwd, 0o755); err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	fakeJJ := filepath.Join(binDir, "jj")
	if err := os.WriteFile(fakeJJ, []byte("#!/bin/sh\npwd\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.Chdir(deletedCwd); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(deletedCwd); err != nil {
		t.Fatal(err)
	}

	out, err := runCommandCapture("jj", "-R", repoPath, "--ignore-working-copy", "log")
	if err != nil {
		t.Fatalf("expected capture to run from -R path after cwd deletion, got %v", err)
	}
	if strings.TrimSpace(out) != repoPath {
		t.Fatalf("expected capture cwd %q, got %q", repoPath, out)
	}

	origErr := stderrWriter
	var errOut bytes.Buffer
	stderrWriter = &errOut
	defer func() { stderrWriter = origErr }()
	if err := runCommandToStderr("jj", "-R", repoPath, "workspace", "update-stale"); err != nil {
		t.Fatalf("expected stderr command to run from -R path after cwd deletion, got %v", err)
	}
	if strings.TrimSpace(errOut.String()) != repoPath {
		t.Fatalf("expected stderr command cwd %q, got %q", repoPath, errOut.String())
	}
}

func TestRevisionCountIgnoresWorkingCopyStaleness(t *testing.T) {
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if name != "jj" || !strings.Contains(joined, " --ignore-working-copy log ") {
			t.Fatalf("expected graph probe to ignore stale working copies, got %s %s", name, joined)
		}
		return "one\ntwo\n", nil
	})
	got, err := revisionCount("/repo", "conflicts() & reachable(other@, mutable())")
	if err != nil || got != 2 {
		t.Fatalf("expected revision count 2, got %d err=%v", got, err)
	}
}

func TestListWorkspaceRefsIgnoresWorkingCopyStaleness(t *testing.T) {
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if name != "jj" || !strings.Contains(joined, " --ignore-working-copy workspace list ") {
			t.Fatalf("expected workspace list to ignore stale working copies, got %s %s", name, joined)
		}
		return "default\tmain111\t/repo\n", nil
	})
	refs, err := listWorkspaceRefs("/repo")
	if err != nil || len(refs) != 1 || refs[0].Handle != "default" {
		t.Fatalf("expected one default ref, got %#v err=%v", refs, err)
	}
}

func TestCommandCaptureErrorsCanBeRestored(t *testing.T) {
	withCommandCapture(t, func(string, ...string) (string, error) { return "", errors.New("boom") })
	_, err := resolveRepoRoot("")
	if err == nil {
		t.Fatal("expected error")
	}
}

func withLookPath(t *testing.T, fn func(string) (string, error)) {
	t.Helper()
	orig := lookPathFn
	lookPathFn = fn
	t.Cleanup(func() { lookPathFn = orig })
}

func withJJVersion(t *testing.T, fn func() (string, error)) {
	t.Helper()
	orig := jjVersionFn
	jjVersionFn = fn
	t.Cleanup(func() { jjVersionFn = orig })
}

func TestVersionStringUsesInjectedVersion(t *testing.T) {
	orig := version
	version = "9.9.9-test"
	t.Cleanup(func() { version = orig })
	if got := versionString(); got != "ajj 9.9.9-test" {
		t.Fatalf("versionString() = %q, want %q", got, "ajj 9.9.9-test")
	}
}

func TestVersionStringFallsBackToDev(t *testing.T) {
	orig := version
	version = "dev"
	t.Cleanup(func() { version = orig })
	got := versionString()
	// In test binaries build info may or may not carry a VCS revision, so we
	// only assert the stable "ajj" prefix and that it is a single line.
	if !strings.HasPrefix(got, "ajj ") {
		t.Fatalf("versionString() = %q, want prefix %q", got, "ajj ")
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("versionString() must be a single line, got %q", got)
	}
}

func TestRunVersionCommandPrintsToStdout(t *testing.T) {
	origVer := version
	version = "9.9.9-test"
	t.Cleanup(func() { version = origVer })
	for _, arg := range []string{"version", "--version"} {
		out, stderr, err := captureOutput(func() error { return run([]string{arg}) })
		if err != nil {
			t.Fatalf("run(%q) error: %v", arg, err)
		}
		if strings.TrimSpace(out) != "ajj 9.9.9-test" {
			t.Fatalf("run(%q) stdout = %q, want %q", arg, out, "ajj 9.9.9-test")
		}
		if stderr != "" {
			t.Fatalf("run(%q) stderr = %q, want empty", arg, stderr)
		}
	}
}

func TestParseJJVersion(t *testing.T) {
	cases := map[string]string{
		"jj 0.42.0\n":                  "0.42.0",
		"jj 0.20.0-abcdef (2024...)\n": "0.20.0",
		"jj 0.9\n":                     "0.9",
		"no version here":              "",
	}
	for in, want := range cases {
		if got := parseJJVersion(in); got != want {
			t.Fatalf("parseJJVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnsureJJ(t *testing.T) {
	tests := []struct {
		name        string
		lookPath    func(string) (string, error)
		versionOut  string
		versionErr  error
		wantErr     bool
		wantErrText string
		wantWarn    bool
	}{
		{
			name:        "jj-missing",
			lookPath:    func(string) (string, error) { return "", errors.New("not found") },
			wantErr:     true,
			wantErrText: "Jujutsu (jj) is required",
		},
		{
			name:       "jj-old",
			lookPath:   func(string) (string, error) { return "/usr/bin/jj", nil },
			versionOut: "jj 0.15.0\n",
			wantWarn:   true,
		},
		{
			name:       "jj-ok",
			lookPath:   func(string) (string, error) { return "/usr/bin/jj", nil },
			versionOut: "jj 0.42.0\n",
			wantWarn:   false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withLookPath(t, tc.lookPath)
			withJJVersion(t, func() (string, error) { return tc.versionOut, tc.versionErr })
			resetJJCheck()
			t.Cleanup(resetJJCheck)

			_, stderr, err := captureOutput(func() error { return ensureJJ() })
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ensureJJ() expected error, got nil")
				}
				if !strings.Contains(err.Error(), tc.wantErrText) {
					t.Fatalf("ensureJJ() error = %q, want contains %q", err.Error(), tc.wantErrText)
				}
				if !strings.Contains(err.Error(), "github.com/jj-vcs/jj") {
					t.Fatalf("ensureJJ() error = %q, want install link", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("ensureJJ() unexpected error: %v", err)
			}
			hasWarn := strings.Contains(stderr, "warning:") && strings.Contains(stderr, jjMinVersion)
			if hasWarn != tc.wantWarn {
				t.Fatalf("ensureJJ() warning presence = %v (stderr=%q), want %v", hasWarn, stderr, tc.wantWarn)
			}
		})
	}
}

func TestEnsureJJCachesResult(t *testing.T) {
	calls := 0
	withLookPath(t, func(string) (string, error) { return "/usr/bin/jj", nil })
	withJJVersion(t, func() (string, error) {
		calls++
		return "jj 0.42.0\n", nil
	})
	resetJJCheck()
	t.Cleanup(resetJJCheck)
	for i := 0; i < 3; i++ {
		if err := ensureJJ(); err != nil {
			t.Fatalf("ensureJJ() error: %v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("jjVersionFn called %d times, want 1 (cached)", calls)
	}
}
