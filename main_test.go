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

func TestLoadConfigUsesAssimilatedPathsVocabulary(t *testing.T) {
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
	paths := effectiveAssimilatedPaths(cfg, "my-project")
	if strings.Join(paths, ",") != "scratch,.local-notes" {
		t.Fatalf("unexpected assimilated paths: %v", paths)
	}
}

func TestLoadConfigAcceptsDeprecatedAssimilatedFoldersAlias(t *testing.T) {
	repoRoot := t.TempDir()
	xdg := t.TempDir()
	if err := os.MkdirAll(filepath.Join(xdg, "jjw"), 0o755); err != nil {
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
	if err := os.WriteFile(filepath.Join(xdg, "jjw", "config.yaml"), cfgText, 0o644); err != nil {
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
	if err := os.MkdirAll(filepath.Join(xdg, "jjw"), 0o755); err != nil {
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
	if err := os.WriteFile(filepath.Join(xdg, "jjw", "config.yaml"), cfgText, 0o644); err != nil {
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
	if err := os.MkdirAll(filepath.Join(xdg, "jjw"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgText := []byte("workspaces_root: /tmp/workspaces\nassimilated_paths:\n  - '**/.env*'\n")
	if err := os.WriteFile(filepath.Join(xdg, "jjw", "config.yaml"), cfgText, 0o644); err != nil {
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
	if err := os.MkdirAll(filepath.Join(xdg, "jjw"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgText := []byte("workspaces_root: /tmp/workspaces\nassimilated_paths:\n  - ../scratch\n")
	if err := os.WriteFile(filepath.Join(xdg, "jjw", "config.yaml"), cfgText, 0o644); err != nil {
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
	if err == nil || !strings.Contains(err.Error(), "jjw help") {
		t.Fatalf("expected help suggestion, got %v", err)
	}
}

func TestShellInitPrintsNavigationWrapperIncludingMain(t *testing.T) {
	out, err := captureStdout(func() error { return run([]string{"shell-init", "zsh"}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"jjw()", "create|open|close|main", "JJW_SHELL_WRAPPED=1 command jjw", "cd \"$out\""} {
		if !strings.Contains(out, want) {
			t.Fatalf("shell init missing %q in:\n%s", want, out)
		}
	}
}

func TestNavigationHintMentionsShellInitForRawMain(t *testing.T) {
	hint := navigationHint("main", "zsh")
	for _, want := range []string{"jjw main", "cd automatically", "eval \"$(jjw shell-init zsh)\""} {
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
	if got := cliStylesForRenderer(renderer).Danger.Render("jjw:"); !strings.Contains(got, "\x1b[") {
		t.Fatalf("expected CLI stderr style to use output renderer color, got %q", got)
	}
	if got := selectorStylesForRenderer(renderer, false).Empty.Render("empty"); !strings.Contains(got, "\x1b[") {
		t.Fatalf("expected selector style to use output renderer color, got %q", got)
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

func selectorHandles(items []selectorItem) []string {
	handles := make([]string, 0, len(items))
	for _, item := range items {
		handles = append(handles, item.Handle)
	}
	return handles
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

func visibleColumnOfSubstring(t *testing.T, line string, substring string) int {
	t.Helper()
	idx := strings.Index(line, substring)
	if idx < 0 {
		t.Fatalf("%q does not contain %q", line, substring)
	}
	return lipgloss.Width(line[:idx])
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

func TestCreateWorkspaceHelperCreatesPrintsAndMaterializes(t *testing.T) {
	mainPath := t.TempDir()
	workspacesRoot := filepath.Join(t.TempDir(), "workspaces")
	if err := os.MkdirAll(filepath.Join(mainPath, "scratch"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config{WorkspacesRoot: workspacesRoot, Project: "proj", MainWorkspace: "default", AssimilatedPaths: []string{"scratch"}}
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
	out, err := captureStdout(func() error { return createWorkspace(mainPath, cfg, "proj", "alpha", false, false) })
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

	_, err := closeWorkspaces(repoRoot, config{}, "proj", []workspaceInfo{
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
