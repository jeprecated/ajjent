package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

var (
	commandCaptureFn            = runCommandCapture
	commandToStderrFn           = runCommandToStderr
	lookPathFn                  = exec.LookPath
	runFzfFn                    = runFzf
	stdinReader       io.Reader = os.Stdin
	stdoutWriter      io.Writer = os.Stdout
	stderrWriter      io.Writer = os.Stderr
)

type config struct {
	DevRoot       string          `yaml:"dev_root"`
	WorktreesRoot string          `yaml:"worktrees_root"`
	NameStrategy  string          `yaml:"name_strategy"`
	NameList      []string        `yaml:"name_list"`
	MainStack     mainStackConfig `yaml:"main_stack"`
}

type mainStackConfig struct {
	Main             string `yaml:"main"`
	RebaseMode       string `yaml:"rebase_mode"`
	StackShape       string `yaml:"stack_shape"`
	ConflictStrategy string `yaml:"conflict_strategy"`
}

type state struct {
	NextIndex int `json:"next_index"`
}

type workspaceRef struct {
	Name         string
	TargetChange string
}

const (
	strategyFirstUnused = "first-unused"
	strategyStateful    = "stateful"
)

var defaultNameList = []string{
	"alpha",
	"bravo",
	"charlie",
	"delta",
	"echo",
	"foxtrot",
	"golf",
	"hotel",
	"india",
	"juliett",
	"kilo",
	"lima",
	"mike",
	"november",
	"oscar",
	"papa",
	"quebec",
	"romeo",
	"sierra",
	"tango",
	"uniform",
	"victor",
	"whiskey",
	"xray",
	"yankee",
	"zulu",
}

func main() {
	if len(os.Args) < 2 {
		printUsage(stderrWriter)
		os.Exit(2)
	}

	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(stderrWriter, "jjw: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("missing command")
	}

	switch args[0] {
	case "create":
		return runCreate(args[1:])
	case "list":
		return runList(args[1:])
	case "select":
		return runSelect(args[1:])
	case "tidy":
		return runTidy(args[1:])
	case "cd":
		return runCd(args[1:])
	case "main":
		return runMain(args[1:])
	case "main-stack":
		return runMainStack(args[1:])
	case "help", "-h", "--help":
		printUsage(stdoutWriter)
		return nil
	default:
		return fmt.Errorf("unknown command: %s", args[0])
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: jjw <command> [options]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  create [name]   Create workspace, print path")
	fmt.Fprintln(w, "  list            List workspace paths for current repo")
	fmt.Fprintln(w, "  select          Select workspace path interactively")
	fmt.Fprintln(w, "  tidy            Select and remove defunct empty workspace dirs")
	fmt.Fprintln(w, "  cd [name]       Print path for shell wrappers")
	fmt.Fprintln(w, "  main            Print path for main workspace (default)")
	fmt.Fprintln(w, "  main-stack      Run st on all workspaces, then rebase main")
}

func runCreate(args []string) error {
	normalizedArgs, err := normalizeCreateArgs(args)
	if err != nil {
		return err
	}
	args = normalizedArgs

	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var (
		repoRootOverride string
		appOverride      string
		rootOverride     string
		nameOverride     string
		skipEnvrc        bool
		skipDirenvAllow  bool
	)
	fs.StringVar(&repoRootOverride, "repo", "", "repo root override")
	fs.StringVar(&appOverride, "app", "", "app override")
	fs.StringVar(&rootOverride, "worktrees-root", "", "worktrees root override")
	fs.StringVar(&nameOverride, "name", "", "workspace name override")
	fs.BoolVar(&skipEnvrc, "no-envrc", false, "do not create .envrc")
	fs.BoolVar(&skipDirenvAllow, "no-direnv-allow", false, "do not run direnv allow")
	if err := fs.Parse(args); err != nil {
		return err
	}

	positionals := fs.Args()
	if len(positionals) > 1 {
		return errors.New("create accepts at most one positional name")
	}
	if nameOverride != "" && len(positionals) == 1 {
		return errors.New("provide either positional name or --name, not both")
	}

	repoRoot, err := resolveRepoRoot(repoRootOverride)
	if err != nil {
		return err
	}

	cfg, err := loadConfig(repoRoot)
	if err != nil {
		return err
	}
	if rootOverride != "" {
		cfg.WorktreesRoot = expandPath(rootOverride)
	}
	if err := requireWorktreesRoot(cfg.WorktreesRoot); err != nil {
		return err
	}

	app := deriveApp(repoRoot, appOverride)
	workspaceNames, err := listWorkspaceNames(repoRoot)
	if err != nil {
		return err
	}
	nameSet := make(map[string]struct{}, len(workspaceNames))
	for _, n := range workspaceNames {
		nameSet[n] = struct{}{}
	}

	name := nameOverride
	if name == "" && len(positionals) == 1 {
		name = strings.TrimSpace(positionals[0])
	}
	if name == "" {
		generated, genErr := chooseAutoName(cfg, repoRoot, nameSet)
		if genErr != nil {
			return genErr
		}
		name = generated
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("workspace name is empty")
	}

	target := filepath.Join(cfg.WorktreesRoot, app, name)
	if exists(target) {
		return fmt.Errorf("workspace path already exists: %s", target)
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create workspace parent: %w", err)
	}

	if err := commandToStderrFn("jj", "-R", repoRoot, "workspace", "add", "--name", name, target); err != nil {
		return err
	}

	if !skipEnvrc {
		if err := ensureEnvrc(target); err != nil {
			return err
		}
	}

	if !skipDirenvAllow {
		if _, err := lookPathFn("direnv"); err == nil {
			_ = commandToStderrFn("direnv", "allow", target)
		}
	}

	fmt.Fprintln(stdoutWriter, target)
	return nil
}

func runList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var (
		repoRootOverride string
		appOverride      string
		rootOverride     string
		includeAll       bool
	)
	fs.StringVar(&repoRootOverride, "repo", "", "repo root override")
	fs.StringVar(&appOverride, "app", "", "app override")
	fs.StringVar(&rootOverride, "worktrees-root", "", "worktrees root override")
	fs.BoolVar(&includeAll, "all", false, "include current workspace")
	if err := fs.Parse(args); err != nil {
		return err
	}

	repoRoot, err := resolveRepoRoot(repoRootOverride)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(repoRoot)
	if err != nil {
		return err
	}
	if rootOverride != "" {
		cfg.WorktreesRoot = expandPath(rootOverride)
	}
	if err := requireWorktreesRoot(cfg.WorktreesRoot); err != nil {
		return err
	}

	app := deriveApp(repoRoot, appOverride)
	refs, err := listWorkspaceRefs(repoRoot)
	if err != nil {
		return err
	}

	currentName := ""
	if !includeAll {
		if detected, detectErr := currentWorkspaceName(repoRoot, refs); detectErr == nil {
			currentName = detected
		}
	}

	for _, ref := range refs {
		if !includeAll && ref.Name == currentName {
			continue
		}
		fmt.Fprintln(stdoutWriter, workspacePathForName(repoRoot, cfg.WorktreesRoot, app, ref.Name, currentName))
	}
	return nil
}

func runSelect(args []string) error {
	paths, err := listExistingWorkspacePaths(args)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return errors.New("no workspace directories found")
	}

	choice, err := selectOne(paths)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdoutWriter, choice)
	return nil
}

func runCd(args []string) error {
	_, positionals, hasNameFlag, err := parseCreateArgs(args)
	if err != nil {
		return err
	}
	if len(positionals) > 0 || hasNameFlag {
		return runCreate(args)
	}
	return runSelect(args)
}

func runMain(args []string) error {
	fs := flag.NewFlagSet("main", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var (
		repoRootOverride string
		appOverride      string
		rootOverride     string
		mainName         string
	)
	fs.StringVar(&repoRootOverride, "repo", "", "repo root override")
	fs.StringVar(&appOverride, "app", "", "app override")
	fs.StringVar(&rootOverride, "worktrees-root", "", "worktrees root override")
	fs.StringVar(&mainName, "name", "default", "main workspace name")
	if err := fs.Parse(args); err != nil {
		return err
	}

	repoRoot, err := resolveRepoRoot(repoRootOverride)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(repoRoot)
	if err != nil {
		return err
	}
	if rootOverride != "" {
		cfg.WorktreesRoot = expandPath(rootOverride)
	}
	if err := requireWorktreesRoot(cfg.WorktreesRoot); err != nil {
		return err
	}
	app := deriveApp(repoRoot, appOverride)

	refs, err := listWorkspaceRefs(repoRoot)
	if err != nil {
		return err
	}
	found := false
	for _, ref := range refs {
		if ref.Name == mainName {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("main workspace %q not found", mainName)
	}

	currentName := ""
	if detected, detectErr := currentWorkspaceName(repoRoot, refs); detectErr == nil {
		currentName = detected
	}

	target := workspacePathForName(repoRoot, cfg.WorktreesRoot, app, mainName, currentName)
	if st, statErr := os.Stat(target); statErr != nil || !st.IsDir() {
		return fmt.Errorf("main workspace path missing: %s", target)
	}

	fmt.Fprintln(stdoutWriter, target)
	return nil
}

func runMainStack(args []string) error {
	fs := flag.NewFlagSet("main-stack", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var (
		repoRootOverride string
		appOverride      string
		rootOverride     string
		mainOverride     string
		rebaseMode       string
		stackShape       string
		conflictStrategy string
	)
	fs.StringVar(&repoRootOverride, "repo", "", "repo root override")
	fs.StringVar(&appOverride, "app", "", "app override")
	fs.StringVar(&rootOverride, "worktrees-root", "", "worktrees root override")
	fs.StringVar(&mainOverride, "main", "", "main workspace name")
	fs.StringVar(&rebaseMode, "rebase-mode", "", "rebase mode: auto, branch, revision")
	fs.StringVar(&stackShape, "stack-shape", "", "stack shape: auto, linear, merge")
	fs.StringVar(&conflictStrategy, "conflict-strategy", "", "conflict strategy: off, prefer-clean")
	if err := fs.Parse(args); err != nil {
		return err
	}

	repoRoot, err := resolveRepoRoot(repoRootOverride)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(repoRoot)
	if err != nil {
		return err
	}
	if rootOverride != "" {
		cfg.WorktreesRoot = expandPath(rootOverride)
	}
	if err := requireWorktreesRoot(cfg.WorktreesRoot); err != nil {
		return err
	}
	app := deriveApp(repoRoot, appOverride)

	refs, err := listWorkspaceRefs(repoRoot)
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		return errors.New("no workspaces found")
	}

	mainName := strings.TrimSpace(mainOverride)
	if mainName == "" {
		mainName = strings.TrimSpace(cfg.MainStack.Main)
	}
	if mainName == "" {
		mainName = "default"
	}

	fmt.Fprintf(stderrWriter, "Main workspace: %s\n", mainName)
	for _, ref := range refs {
		workspacePath := filepath.Join(cfg.WorktreesRoot, app, ref.Name)
		if ref.Name == "default" {
			workspacePath = workspacePathForName(repoRoot, cfg.WorktreesRoot, app, ref.Name, mainName)
		}
		fmt.Fprintf(stderrWriter, "\n== jj st: %s ==\n", ref.Name)
		if st, statErr := os.Stat(workspacePath); statErr != nil || !st.IsDir() {
			fmt.Fprintf(stderrWriter, "skip: workspace path missing: %s\n", workspacePath)
			continue
		}
		if err := commandToStderrFn("jj", "-R", workspacePath, "workspace", "update-stale"); err != nil {
			return err
		}
		if err := commandToStderrFn("jj", "-R", workspacePath, "st"); err != nil {
			return err
		}
	}

	var others []string
	for _, ref := range refs {
		if ref.Name != mainName {
			others = append(others, ref.Name)
		}
	}
	if len(others) == 0 {
		return errors.New("no other workspaces to stack onto")
	}

	mainPath := workspacePathForName(repoRoot, cfg.WorktreesRoot, app, mainName, mainName)
	if st, statErr := os.Stat(mainPath); statErr != nil || !st.IsDir() {
		return fmt.Errorf("main workspace path missing: %s", mainPath)
	}

	requestedRebaseMode := strings.TrimSpace(rebaseMode)
	if requestedRebaseMode == "" {
		requestedRebaseMode = cfg.MainStack.RebaseMode
	}
	resolvedMode, reason, err := resolveMainStackRebaseMode(mainPath, requestedRebaseMode)
	if err != nil {
		return err
	}

	requestedConflictStrategy := strings.TrimSpace(conflictStrategy)
	if requestedConflictStrategy == "" {
		requestedConflictStrategy = cfg.MainStack.ConflictStrategy
	}
	resolvedConflictStrategy, err := resolveMainStackConflictStrategy(requestedConflictStrategy)
	if err != nil {
		return err
	}

	requestedStackShape := strings.TrimSpace(stackShape)
	if requestedStackShape == "" {
		requestedStackShape = cfg.MainStack.StackShape
	}
	resolvedShape, shapeReason, baseDestinations, err := resolveMainStackStackShape(mainPath, others, requestedStackShape)
	if err != nil {
		return err
	}

	conflicted, err := runMainStackRebaseAttempt(mainPath, others, resolvedMode, reason, resolvedShape, shapeReason, baseDestinations)
	if err != nil {
		return err
	}

	if resolvedConflictStrategy == "prefer-clean" && conflicted && strings.TrimSpace(strings.ToLower(requestedStackShape)) == "auto" {
		alternativeShape := "merge"
		if resolvedShape == "merge" {
			alternativeShape = "linear"
		}

		alternativeResolvedShape, alternativeShapeReason, alternativeDestinations, altErr := resolveMainStackStackShape(mainPath, others, alternativeShape)
		if altErr == nil {
			fmt.Fprintf(stderrWriter, "\n== Conflict fallback: undo and retry with %s ==\n", alternativeResolvedShape)
			if err := commandToStderrFn("jj", "-R", mainPath, "undo"); err != nil {
				return err
			}

			alternativeConflicted, err := runMainStackRebaseAttempt(mainPath, others, resolvedMode, reason, alternativeResolvedShape, alternativeShapeReason, alternativeDestinations)
			if err != nil {
				return err
			}

			if alternativeConflicted && alternativeResolvedShape == "linear" {
				fmt.Fprintln(stderrWriter, "\n== Both strategies conflicted; keeping merge shape ==")
				if err := commandToStderrFn("jj", "-R", mainPath, "undo"); err != nil {
					return err
				}
				mergeShape, mergeReason, mergeDestinations, err := resolveMainStackStackShape(mainPath, others, "merge")
				if err != nil {
					return err
				}
				if _, err := runMainStackRebaseAttempt(mainPath, others, resolvedMode, reason, mergeShape, mergeReason, mergeDestinations); err != nil {
					return err
				}
			}
		}
	}

	return commandToStderrFn("jj", "-R", mainPath, "workspace", "update-stale")
}

func runMainStackRebaseAttempt(mainPath string, others []string, resolvedMode string, modeReason string, resolvedShape string, shapeReason string, baseDestinations []string) (bool, error) {
	rebaseFlag := "-b"
	if resolvedMode == "revision" {
		rebaseFlag = "-r"
	}

	destinations := make([]string, 0, len(baseDestinations)+2)
	if resolvedMode == "revision" {
		parents, err := parentChangeIDs(mainPath)
		if err != nil {
			return false, err
		}
		preservedParents, err := parentChangeIDsToPreserve(mainPath, parents, baseDestinations)
		if err != nil {
			return false, err
		}
		destinations = append(destinations, preservedParents...)
	}
	destinations = append(destinations, baseDestinations...)
	destinations = uniqueNonEmptyStrings(destinations)

	cmdArgs := []string{"-R", mainPath, "rebase", rebaseFlag, "@"}
	for _, dest := range destinations {
		cmdArgs = append(cmdArgs, "-d", dest)
	}

	if modeReason == "" {
		modeReason = resolvedMode
	}
	if shapeReason == "" {
		shapeReason = resolvedShape
	}
	fmt.Fprintf(stderrWriter, "\n== Stack shape: %s (%s) ==\n", resolvedShape, shapeReason)
	fmt.Fprintf(stderrWriter, "\n== Rebase mode: %s (%s) ==\n", resolvedMode, modeReason)
	fmt.Fprintf(stderrWriter, "\n== Rebase main onto: %s ==\n", strings.Join(others, ", "))
	if err := commandToStderrFn("jj", cmdArgs...); err != nil {
		return false, err
	}

	conflicted, err := workingCopyHasConflicts(mainPath)
	if err != nil {
		return false, err
	}
	if conflicted {
		fmt.Fprintln(stderrWriter, "\n== Rebase result has conflicts ==")
	}
	return conflicted, nil
}

func resolveMainStackStackShape(repoPath string, others []string, requested string) (string, string, []string, error) {
	mode := strings.TrimSpace(strings.ToLower(requested))
	if mode == "" {
		mode = "auto"
	}

	otherRevs := make([]string, 0, len(others))
	for _, name := range others {
		otherRevs = append(otherRevs, name+"@")
	}
	frontier, err := frontierHeads(repoPath, otherRevs)
	if err != nil {
		return "", "", nil, err
	}
	if len(frontier) == 0 {
		return "", "", nil, errors.New("could not resolve workspace frontier heads")
	}

	switch mode {
	case "auto":
		if len(frontier) == 1 {
			return "linear", "single frontier head", frontier, nil
		}
		return "merge", fmt.Sprintf("%d frontier heads", len(frontier)), otherRevs, nil
	case "linear":
		if len(frontier) != 1 {
			return "", "", nil, fmt.Errorf("--stack-shape linear requires a single frontier head, found %d", len(frontier))
		}
		return "linear", "flag", frontier, nil
	case "merge":
		return "merge", "flag", otherRevs, nil
	default:
		return "", "", nil, fmt.Errorf("invalid --stack-shape %q (expected auto, linear, or merge)", requested)
	}
}

func resolveMainStackConflictStrategy(requested string) (string, error) {
	strategy := strings.TrimSpace(strings.ToLower(requested))
	if strategy == "" {
		strategy = "off"
	}
	switch strategy {
	case "off", "prefer-clean":
		return strategy, nil
	default:
		return "", fmt.Errorf("invalid --conflict-strategy %q (expected off or prefer-clean)", requested)
	}
}

func validateMainStackRebaseMode(mode string) error {
	trimmed := strings.TrimSpace(strings.ToLower(mode))
	switch trimmed {
	case "", "auto", "branch", "revision":
		return nil
	default:
		return fmt.Errorf("invalid rebase_mode: %q (expected auto, branch, or revision)", mode)
	}
}

func validateMainStackStackShape(shape string) error {
	trimmed := strings.TrimSpace(strings.ToLower(shape))
	switch trimmed {
	case "", "auto", "linear", "merge":
		return nil
	default:
		return fmt.Errorf("invalid stack_shape: %q (expected auto, linear, or merge)", shape)
	}
}

func resolveMainStackRebaseMode(repoPath string, requested string) (string, string, error) {
	mode := strings.TrimSpace(strings.ToLower(requested))
	if mode == "" {
		mode = "auto"
	}

	switch mode {
	case "branch":
		return "branch", "flag", nil
	case "revision":
		return "revision", "flag", nil
	case "auto":
		hasImmutable, err := hasImmutableAncestors(repoPath)
		if err != nil {
			return "", "", err
		}
		if hasImmutable {
			return "revision", "immutable ancestors detected", nil
		}
		return "branch", "no immutable ancestors", nil
	default:
		return "", "", fmt.Errorf("invalid --rebase-mode %q (expected auto, branch, or revision)", requested)
	}
}

func hasImmutableAncestors(repoPath string) (bool, error) {
	revset := "immutable() & ::@ & ~@"
	out, err := commandCaptureFn("jj", "-R", repoPath, "log", "-r", revset, "--no-graph", "-T", "change_id.short() ++ \"\\n\"")
	if err != nil {
		return false, err
	}

	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			return true, nil
		}
	}
	return false, nil
}

func workingCopyHasConflicts(repoPath string) (bool, error) {
	out, err := commandCaptureFn("jj", "-R", repoPath, "log", "-r", "conflicts() & @", "--no-graph", "-T", "change_id.short() ++ \"\\n\"")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			return true, nil
		}
	}
	return false, nil
}

func frontierHeads(repoPath string, revs []string) ([]string, error) {
	if len(revs) == 0 {
		return nil, nil
	}
	revset := fmt.Sprintf("heads(%s)", strings.Join(revs, " | "))
	out, err := commandCaptureFn("jj", "-R", repoPath, "log", "-r", revset, "--no-graph", "-T", "change_id.short() ++ \"\\n\"")
	if err != nil {
		return nil, err
	}
	return uniqueNonEmptyStrings(strings.Split(out, "\n")), nil
}

func parentChangeIDs(repoPath string) ([]string, error) {
	out, err := commandCaptureFn("jj", "-R", repoPath, "log", "-r", "parents(@)", "--no-graph", "-T", "change_id.short() ++ \"\\n\"")
	if err != nil {
		return nil, err
	}

	var parents []string
	for _, line := range strings.Split(out, "\n") {
		id := strings.TrimSpace(line)
		if id == "" {
			continue
		}
		parents = append(parents, id)
	}
	return uniqueNonEmptyStrings(parents), nil
}

func parentChangeIDsToPreserve(repoPath string, parents []string, destinations []string) ([]string, error) {
	preserved := make([]string, 0, len(parents))
	for _, parent := range uniqueNonEmptyStrings(parents) {
		isAncestor, err := isAncestorOfAny(repoPath, parent, destinations)
		if err != nil {
			return nil, err
		}
		if isAncestor {
			continue
		}
		preserved = append(preserved, parent)
	}
	return preserved, nil
}

func isAncestorOfAny(repoPath string, ancestor string, descendants []string) (bool, error) {
	descendants = uniqueNonEmptyStrings(descendants)
	if ancestor == "" || len(descendants) == 0 {
		return false, nil
	}
	revset := fmt.Sprintf("%s::(%s)", ancestor, strings.Join(descendants, " | "))
	out, err := commandCaptureFn("jj", "-R", repoPath, "log", "-r", revset, "--no-graph", "-T", "change_id.short() ++ \"\\n\"")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			return true, nil
		}
	}
	return false, nil
}

func uniqueNonEmptyStrings(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	return result
}

func abandonTopEmptyMutableAncestors(repoPath string) error {
	revset := "empty() & mutable() & ::@ & ~@"
	out, err := commandCaptureFn("jj", "-R", repoPath, "log", "-r", revset, "--no-graph", "-T", "change_id.short() ++ \"\\n\"")
	if err != nil {
		return err
	}

	hasEmpty := false
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			hasEmpty = true
			break
		}
	}
	if !hasEmpty {
		return nil
	}

	fmt.Fprintln(stderrWriter, "\n== Abandon top empty commits ==")
	return commandToStderrFn("jj", "-R", repoPath, "abandon", "-r", revset)
}

func normalizeCreateArgs(args []string) ([]string, error) {
	normalized, _, _, err := parseCreateArgs(args)
	if err != nil {
		return nil, err
	}
	return normalized, nil
}

func parseCreateArgs(args []string) ([]string, []string, bool, error) {
	flagsWithValues := map[string]struct{}{
		"--repo":           {},
		"--app":            {},
		"--worktrees-root": {},
		"--name":           {},
	}

	normalized := make([]string, 0, len(args))
	positionals := make([]string, 0, 1)
	hasNameFlag := false

	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "--") {
			normalized = append(normalized, a)
			if a == "--name" || strings.HasPrefix(a, "--name=") {
				hasNameFlag = true
			}
			if strings.Contains(a, "=") {
				continue
			}
			if _, ok := flagsWithValues[a]; ok {
				if i+1 >= len(args) {
					return nil, nil, false, fmt.Errorf("flag %s requires a value", a)
				}
				i++
				normalized = append(normalized, args[i])
			}
			continue
		}

		if strings.HasPrefix(a, "-") {
			normalized = append(normalized, a)
			continue
		}

		positionals = append(positionals, a)
	}

	if len(positionals) > 1 {
		return nil, nil, false, errors.New("create accepts at most one positional name")
	}

	normalized = append(normalized, positionals...)
	return normalized, positionals, hasNameFlag, nil
}

func runTidy(args []string) error {
	fs := flag.NewFlagSet("tidy", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var (
		repoRootOverride string
		appOverride      string
		rootOverride     string
		yes              bool
	)
	fs.StringVar(&repoRootOverride, "repo", "", "repo root override")
	fs.StringVar(&appOverride, "app", "", "app override")
	fs.StringVar(&rootOverride, "worktrees-root", "", "worktrees root override")
	fs.BoolVar(&yes, "yes", false, "skip confirmation")
	if err := fs.Parse(args); err != nil {
		return err
	}

	repoRoot, err := resolveRepoRoot(repoRootOverride)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(repoRoot)
	if err != nil {
		return err
	}
	if rootOverride != "" {
		cfg.WorktreesRoot = expandPath(rootOverride)
	}
	if err := requireWorktreesRoot(cfg.WorktreesRoot); err != nil {
		return err
	}

	app := deriveApp(repoRoot, appOverride)
	refs, err := listWorkspaceRefs(repoRoot)
	if err != nil {
		return err
	}
	currentName := ""
	if detected, detectErr := currentWorkspaceName(repoRoot, refs); detectErr == nil {
		currentName = detected
	}

	activeNames := make([]string, 0, len(refs))
	for _, ref := range refs {
		activeNames = append(activeNames, ref.Name)
	}
	active := make(map[string]struct{}, len(activeNames))
	for _, n := range activeNames {
		active[n] = struct{}{}
	}

	appRoot := filepath.Join(cfg.WorktreesRoot, app)
	entries, err := os.ReadDir(appRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	var candidates []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if _, ok := active[name]; ok {
			continue
		}
		full := filepath.Join(appRoot, name)
		empty, emptyErr := isLiteralEmptyDir(full)
		if emptyErr != nil || !empty {
			continue
		}
		candidates = append(candidates, full)
	}

	deleted := make([]string, 0, len(candidates))
	for _, path := range candidates {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove %s: %w", path, err)
		}
		deleted = append(deleted, path)
	}

	optional := make([]string, 0, len(refs))
	pathToName := make(map[string]string, len(refs))
	for _, ref := range refs {
		if ref.Name == "default" || ref.Name == currentName {
			continue
		}
		path := workspacePathForName(repoRoot, cfg.WorktreesRoot, app, ref.Name, currentName)
		if st, statErr := os.Stat(path); statErr == nil && st.IsDir() {
			optional = append(optional, path)
			pathToName[path] = ref.Name
		}
	}

	if len(optional) == 0 {
		for _, path := range deleted {
			fmt.Fprintln(stdoutWriter, path)
		}
		return nil
	}

	selected, err := selectMany(optional)
	if err != nil {
		return err
	}
	if len(selected) == 0 {
		for _, path := range deleted {
			fmt.Fprintln(stdoutWriter, path)
		}
		return nil
	}

	if !yes {
		confirmed, confirmErr := confirmDelete(len(selected))
		if confirmErr != nil {
			return confirmErr
		}
		if !confirmed {
			return nil
		}
	}

	for _, path := range selected {
		if name, ok := pathToName[path]; ok {
			if err := commandToStderrFn("jj", "-R", repoRoot, "workspace", "forget", name); err != nil {
				return err
			}
		}
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove %s: %w", path, err)
		}
		deleted = append(deleted, path)
	}

	if err := abandonTopEmptyMutableAncestors(repoRoot); err != nil {
		return err
	}

	for _, path := range deleted {
		fmt.Fprintln(stdoutWriter, path)
	}
	return nil
}

func listExistingWorkspacePaths(args []string) ([]string, error) {
	fs := flag.NewFlagSet("select", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var (
		repoRootOverride string
		appOverride      string
		rootOverride     string
		includeAll       bool
	)
	fs.StringVar(&repoRootOverride, "repo", "", "repo root override")
	fs.StringVar(&appOverride, "app", "", "app override")
	fs.StringVar(&rootOverride, "worktrees-root", "", "worktrees root override")
	fs.BoolVar(&includeAll, "all", false, "include current workspace")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	repoRoot, err := resolveRepoRoot(repoRootOverride)
	if err != nil {
		return nil, err
	}
	cfg, err := loadConfig(repoRoot)
	if err != nil {
		return nil, err
	}
	if rootOverride != "" {
		cfg.WorktreesRoot = expandPath(rootOverride)
	}
	if err := requireWorktreesRoot(cfg.WorktreesRoot); err != nil {
		return nil, err
	}

	app := deriveApp(repoRoot, appOverride)
	refs, err := listWorkspaceRefs(repoRoot)
	if err != nil {
		return nil, err
	}

	currentName := ""
	if !includeAll {
		if detected, detectErr := currentWorkspaceName(repoRoot, refs); detectErr == nil {
			currentName = detected
		}
	}

	var paths []string
	for _, ref := range refs {
		if !includeAll && ref.Name == currentName {
			continue
		}
		p := workspacePathForName(repoRoot, cfg.WorktreesRoot, app, ref.Name, currentName)
		if st, statErr := os.Stat(p); statErr == nil && st.IsDir() {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func workspacePathForName(repoRoot string, worktreesRoot string, app string, name string, currentName string) string {
	if name == "default" {
		if root, ok := resolveDefaultWorkspaceRoot(repoRoot); ok {
			return root
		}
	}
	if currentName != "" && name == currentName {
		return repoRoot
	}
	return filepath.Join(worktreesRoot, app, name)
}

func resolveDefaultWorkspaceRoot(repoRoot string) (string, bool) {
	repoLink := filepath.Join(repoRoot, ".jj", "repo")
	data, err := os.ReadFile(repoLink)
	if err != nil {
		return "", false
	}
	target := strings.TrimSpace(string(data))
	if target == "" {
		return "", false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Clean(filepath.Join(filepath.Dir(repoLink), target))
	}
	target = filepath.Clean(target)
	suffix := filepath.Join(".jj", "repo")
	if !strings.HasSuffix(target, suffix) {
		return "", false
	}
	root := filepath.Dir(filepath.Dir(target))
	if st, err := os.Stat(root); err == nil && st.IsDir() {
		return root, true
	}
	return "", false
}

func resolveRepoRoot(override string) (string, error) {
	if override != "" {
		root := expandPath(override)
		abs, err := filepath.Abs(root)
		if err != nil {
			return "", err
		}
		return abs, nil
	}

	if out, err := commandCaptureFn("jj", "root"); err == nil {
		root := strings.TrimSpace(out)
		if root != "" {
			return root, nil
		}
	}

	out, err := commandCaptureFn("git", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", errors.New("could not resolve repo root; run inside a jj/git repo or pass --repo")
	}
	root := strings.TrimSpace(out)
	if root == "" {
		return "", errors.New("resolved empty repo root")
	}
	return root, nil
}

func deriveApp(repoRoot string, override string) string {
	if strings.TrimSpace(override) != "" {
		return strings.TrimSpace(override)
	}
	return filepath.Base(repoRoot)
}

func listWorkspaceNames(repoRoot string) ([]string, error) {
	out, err := commandCaptureFn("jj", "-R", repoRoot, "workspace", "list", "-T", "name ++ \"\\n\"")
	if err != nil {
		return nil, err
	}

	var names []string
	seen := map[string]struct{}{}
	for _, line := range strings.Split(out, "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func listWorkspaceRefs(repoRoot string) ([]workspaceRef, error) {
	out, err := commandCaptureFn("jj", "-R", repoRoot, "workspace", "list", "-T", "name ++ \"\\t\" ++ target.change_id().short() ++ \"\\n\"")
	if err != nil {
		return nil, err
	}

	var refs []workspaceRef
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) == 1 {
			refs = append(refs, workspaceRef{Name: strings.TrimSpace(parts[0]), TargetChange: ""})
			continue
		}
		if len(parts) != 2 {
			continue
		}
		refs = append(refs, workspaceRef{Name: strings.TrimSpace(parts[0]), TargetChange: strings.TrimSpace(parts[1])})
	}
	sort.Slice(refs, func(i, j int) bool {
		return refs[i].Name < refs[j].Name
	})
	return refs, nil
}

func currentWorkspaceName(repoRoot string, refs []workspaceRef) (string, error) {
	out, err := commandCaptureFn("jj", "-R", repoRoot, "log", "-r", "@", "--no-graph", "-T", "change_id.short() ++ \"\\n\"")
	if err != nil {
		return "", err
	}
	current := strings.TrimSpace(out)
	for _, ref := range refs {
		if ref.TargetChange == current {
			return ref.Name, nil
		}
	}
	return "", errors.New("could not detect current workspace; pass --main")
}

func loadConfig(repoRoot string) (config, error) {
	defaults := config{
		NameStrategy: strategyFirstUnused,
		NameList:     append([]string(nil), defaultNameList...),
		MainStack: mainStackConfig{
			Main:             "default",
			RebaseMode:       "auto",
			StackShape:       "auto",
			ConflictStrategy: "prefer-clean",
		},
	}

	merged := defaults

	if globalPath, ok := globalConfigPath(); ok {
		if err := mergeConfigFile(&merged, globalPath); err != nil {
			return config{}, err
		}
	}

	localPath := filepath.Join(repoRoot, ".jjw", "config.yaml")
	if err := mergeConfigFile(&merged, localPath); err != nil {
		return config{}, err
	}

	merged.DevRoot = expandPath(merged.DevRoot)
	merged.WorktreesRoot = expandPath(merged.WorktreesRoot)

	if merged.NameStrategy == "" {
		merged.NameStrategy = strategyFirstUnused
	}
	if merged.NameStrategy != strategyFirstUnused && merged.NameStrategy != strategyStateful {
		return config{}, fmt.Errorf("invalid name_strategy: %q", merged.NameStrategy)
	}

	if strings.TrimSpace(merged.MainStack.Main) == "" {
		merged.MainStack.Main = "default"
	}
	if err := validateMainStackRebaseMode(merged.MainStack.RebaseMode); err != nil {
		return config{}, err
	}
	if err := validateMainStackStackShape(merged.MainStack.StackShape); err != nil {
		return config{}, err
	}
	if _, err := resolveMainStackConflictStrategy(merged.MainStack.ConflictStrategy); err != nil {
		return config{}, err
	}

	return merged, nil
}

func mergeConfigFile(dst *config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read config %s: %w", path, err)
	}

	var src config
	if err := yaml.Unmarshal(data, &src); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}

	if strings.TrimSpace(src.DevRoot) != "" {
		dst.DevRoot = strings.TrimSpace(src.DevRoot)
	}
	if strings.TrimSpace(src.WorktreesRoot) != "" {
		dst.WorktreesRoot = strings.TrimSpace(src.WorktreesRoot)
	}
	if strings.TrimSpace(src.NameStrategy) != "" {
		dst.NameStrategy = strings.TrimSpace(src.NameStrategy)
	}
	if len(src.NameList) > 0 {
		dst.NameList = append([]string(nil), src.NameList...)
	}
	if strings.TrimSpace(src.MainStack.Main) != "" {
		dst.MainStack.Main = strings.TrimSpace(src.MainStack.Main)
	}
	if strings.TrimSpace(src.MainStack.RebaseMode) != "" {
		dst.MainStack.RebaseMode = strings.TrimSpace(src.MainStack.RebaseMode)
	}
	if strings.TrimSpace(src.MainStack.StackShape) != "" {
		dst.MainStack.StackShape = strings.TrimSpace(src.MainStack.StackShape)
	}
	if strings.TrimSpace(src.MainStack.ConflictStrategy) != "" {
		dst.MainStack.ConflictStrategy = strings.TrimSpace(src.MainStack.ConflictStrategy)
	}

	return nil
}

func requireWorktreesRoot(worktreesRoot string) error {
	if strings.TrimSpace(worktreesRoot) == "" {
		return errors.New("worktrees_root is required; set it in ~/.config/jjw/config.yaml or .jjw/config.yaml, or pass --worktrees-root")
	}
	return nil
}

func globalConfigPath() (string, bool) {
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		return filepath.Join(expandPath(xdg), "jjw", "config.yaml"), true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	return filepath.Join(home, ".config", "jjw", "config.yaml"), true
}

func chooseAutoName(cfg config, repoRoot string, inUse map[string]struct{}) (string, error) {
	if len(cfg.NameList) == 0 {
		return "", errors.New("name_list is empty; configure names in ~/.config/jjw/config.yaml or .jjw/config.yaml")
	}

	sanitized := make([]string, 0, len(cfg.NameList))
	for _, candidate := range cfg.NameList {
		trimmed := strings.TrimSpace(candidate)
		if trimmed != "" {
			sanitized = append(sanitized, trimmed)
		}
	}
	if len(sanitized) == 0 {
		return "", errors.New("name_list has no usable entries")
	}

	switch cfg.NameStrategy {
	case strategyFirstUnused:
		for _, candidate := range sanitized {
			if _, ok := inUse[candidate]; ok {
				continue
			}
			return candidate, nil
		}
		return "", errors.New("all configured names are in use; add more names to name_list")
	case strategyStateful:
		st, err := loadState(repoRoot)
		if err != nil {
			return "", err
		}
		for i := st.NextIndex; i < len(sanitized); i++ {
			candidate := sanitized[i]
			if _, ok := inUse[candidate]; ok {
				continue
			}
			st.NextIndex = i + 1
			if saveErr := saveState(repoRoot, st); saveErr != nil {
				return "", saveErr
			}
			return candidate, nil
		}
		return "", errors.New("all configured names are exhausted for stateful strategy; extend name_list")
	default:
		return "", fmt.Errorf("unsupported name_strategy: %s", cfg.NameStrategy)
	}
}

func loadState(repoRoot string) (state, error) {
	path := filepath.Join(repoRoot, ".jjw", "state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return state{}, nil
		}
		return state{}, fmt.Errorf("read state %s: %w", path, err)
	}

	var st state
	if err := json.Unmarshal(data, &st); err != nil {
		return state{}, fmt.Errorf("parse state %s: %w", path, err)
	}
	if st.NextIndex < 0 {
		st.NextIndex = 0
	}
	return st, nil
}

func saveState(repoRoot string, st state) error {
	dir := filepath.Join(repoRoot, ".jjw")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	path := filepath.Join(dir, "state.json")
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write state %s: %w", path, err)
	}
	return nil
}

func ensureEnvrc(workspacePath string) error {
	path := filepath.Join(workspacePath, ".envrc")
	if exists(path) {
		return nil
	}
	if err := os.WriteFile(path, []byte("use_dev_env\n"), 0o644); err != nil {
		return fmt.Errorf("write .envrc: %w", err)
	}
	return nil
}

func selectOne(items []string) (string, error) {
	if len(items) == 1 {
		return items[0], nil
	}
	fmt.Fprintln(stderrWriter, "Choose a workspace:")
	for i, item := range items {
		fmt.Fprintf(stderrWriter, "  %2d) %s\n", i+1, item)
	}
	fmt.Fprint(stderrWriter, "Selection (number, q to cancel): ")

	reader := bufio.NewReader(stdinReader)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" || strings.EqualFold(line, "q") || strings.EqualFold(line, "quit") {
		return "", errors.New("no selection")
	}
	n, err := strconv.Atoi(line)
	if err != nil || n < 1 || n > len(items) {
		return "", errors.New("invalid selection")
	}
	return items[n-1], nil
}

func selectMany(items []string) ([]string, error) {
	sort.Strings(items)

	if canUseInteractiveMultiSelect() {
		selected, err := interactiveMultiSelect(items)
		if err != nil {
			return nil, err
		}
		if len(selected) > 0 {
			return selected, nil
		}
		return nil, nil
	}

	fmt.Fprintln(stderrWriter, "Select workspaces to delete:")
	for i, item := range items {
		fmt.Fprintf(stderrWriter, "  %2d) %s\n", i+1, item)
	}
	fmt.Fprint(stderrWriter, "Selection (e.g. 1,3-5, a=all, blank/q=cancel): ")

	reader := bufio.NewReader(stdinReader)
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimSpace(line)
	if line == "" || strings.EqualFold(line, "q") || strings.EqualFold(line, "quit") {
		return nil, nil
	}
	if strings.EqualFold(line, "a") || strings.EqualFold(line, "all") {
		return append([]string(nil), items...), nil
	}

	indices, parseErr := parseSelectionIndices(line, len(items))
	if parseErr != nil {
		return nil, parseErr
	}
	selected := make([]string, 0, len(indices))
	for _, idx := range indices {
		selected = append(selected, items[idx])
	}
	return selected, nil
}

func canUseInteractiveMultiSelect() bool {
	in, ok := stdinReader.(*os.File)
	if !ok {
		return false
	}
	out, ok := stderrWriter.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(in.Fd())) && term.IsTerminal(int(out.Fd()))
}

func interactiveMultiSelect(items []string) ([]string, error) {
	in := stdinReader.(*os.File)
	out := stderrWriter.(*os.File)

	fd := int(in.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = term.Restore(fd, oldState)
	}()

	_, _ = fmt.Fprint(out, "\x1b[?25l")
	defer func() {
		_, _ = fmt.Fprint(out, "\x1b[?25h")
		_, _ = fmt.Fprint(out, "\x1b[0m\r\n")
	}()

	selected := make(map[int]bool, len(items))
	cursor := 0
	lastRenderedRows := 0

	render := func() {
		if lastRenderedRows > 0 {
			_, _ = fmt.Fprintf(out, "\x1b[%dA", lastRenderedRows)
			for i := 0; i < lastRenderedRows; i++ {
				_, _ = fmt.Fprint(out, "\r\x1b[2K")
				if i < lastRenderedRows-1 {
					_, _ = fmt.Fprint(out, "\x1b[1B")
				}
			}
			if lastRenderedRows > 1 {
				_, _ = fmt.Fprintf(out, "\x1b[%dA", lastRenderedRows-1)
			}
		}

		lines := buildMultiSelectLines(items, cursor, selected, terminalWidth(out))

		for _, line := range lines {
			_, _ = fmt.Fprintf(out, "%s\r\n", line)
		}

		lastRenderedRows = len(lines)
	}

	render()
	reader := bufio.NewReader(in)
	for {
		b, readErr := reader.ReadByte()
		if readErr != nil {
			return nil, readErr
		}

		switch b {
		case 'q', 'Q':
			return nil, nil
		case 'a', 'A':
			invertSelections(selected, len(items))
		case 'c', 'C':
			return selectedItems(items, selected), nil
		case 'k':
			if cursor > 0 {
				cursor--
			}
		case 'j':
			if cursor < len(items) {
				cursor++
			}
		case ' ':
			if cursor < len(items) {
				selected[cursor] = !selected[cursor]
			}
		case '\r', '\n':
			nextCursor, done := applyEnter(cursor, len(items), selected)
			cursor = nextCursor
			if done {
				return selectedItems(items, selected), nil
			}
		case 0x1b:
			next, err1 := reader.ReadByte()
			if err1 != nil {
				return nil, nil
			}
			if next != '[' {
				return nil, nil
			}
			arrow, err2 := reader.ReadByte()
			if err2 != nil {
				return nil, nil
			}
			switch arrow {
			case 'A':
				if cursor > 0 {
					cursor--
				}
			case 'B':
				if cursor < len(items) {
					cursor++
				}
			}
		}

		render()
	}
}

func buildMultiSelectLines(items []string, cursor int, selected map[int]bool, width int) []string {
	lines := make([]string, 0, 3+len(items))
	lines = appendWrappedLine(lines, "", "", "Select workspaces to delete", width)
	lines = appendWrappedLine(lines, "", "", "Up/Down: move  Space: toggle  Enter: toggle+next/submit  a: invert  c: continue  q: cancel", width)
	lines = append(lines, "")

	for i, item := range items {
		pointer := " "
		if i == cursor {
			pointer = ">"
		}
		mark := "[ ]"
		if selected[i] {
			mark = "[x]"
		}

		prefix := fmt.Sprintf("%s %s ", pointer, mark)
		continuation := strings.Repeat(" ", len(prefix))
		lines = appendWrappedLine(lines, prefix, continuation, item, width)
	}

	submitPointer := " "
	if cursor == len(items) {
		submitPointer = ">"
	}
	lines = append(lines, fmt.Sprintf("%s [ continue ]", submitPointer))

	return lines
}

func selectedItems(items []string, selected map[int]bool) []string {
	outItems := make([]string, 0, len(selected))
	for i := 0; i < len(items); i++ {
		if selected[i] {
			outItems = append(outItems, items[i])
		}
	}
	return outItems
}

func invertSelections(selected map[int]bool, itemCount int) {
	for i := 0; i < itemCount; i++ {
		selected[i] = !selected[i]
	}
}

func applyEnter(cursor, itemCount int, selected map[int]bool) (nextCursor int, done bool) {
	if cursor >= itemCount {
		return cursor, true
	}
	selected[cursor] = !selected[cursor]
	if cursor < itemCount {
		cursor++
	}
	return cursor, false
}

func appendWrappedLine(lines []string, prefix, continuation, content string, width int) []string {
	effectiveWidth := width
	if effectiveWidth > 1 {
		effectiveWidth--
	}

	prefixRunes := []rune(prefix)
	contentRunes := []rune(content)
	maxFirst := effectiveWidth - len(prefixRunes)
	if effectiveWidth <= 0 || maxFirst <= 0 {
		return append(lines, prefix+content)
	}

	maxNext := effectiveWidth - len([]rune(continuation))
	if maxNext <= 0 {
		maxNext = maxFirst
	}

	if len(contentRunes) <= maxFirst {
		return append(lines, prefix+content)
	}

	lines = append(lines, prefix+string(contentRunes[:maxFirst]))
	contentRunes = contentRunes[maxFirst:]
	for len(contentRunes) > 0 {
		take := maxNext
		if len(contentRunes) < take {
			take = len(contentRunes)
		}
		lines = append(lines, continuation+string(contentRunes[:take]))
		contentRunes = contentRunes[take:]
	}

	return lines
}

func terminalWidth(f *os.File) int {
	width, _, err := term.GetSize(int(f.Fd()))
	if err != nil || width <= 0 {
		return 0
	}
	return width
}

func parseSelectionIndices(input string, max int) ([]int, error) {
	parts := strings.Split(input, ",")
	seen := map[int]struct{}{}
	indices := make([]int, 0, len(parts))
	for _, p := range parts {
		token := strings.TrimSpace(p)
		if token == "" {
			continue
		}
		if strings.Contains(token, "-") {
			bounds := strings.SplitN(token, "-", 2)
			if len(bounds) != 2 {
				return nil, fmt.Errorf("invalid selection: %q", token)
			}
			start, errStart := strconv.Atoi(strings.TrimSpace(bounds[0]))
			end, errEnd := strconv.Atoi(strings.TrimSpace(bounds[1]))
			if errStart != nil || errEnd != nil || start < 1 || end < 1 || start > max || end > max || start > end {
				return nil, fmt.Errorf("invalid selection: %q", token)
			}
			for n := start; n <= end; n++ {
				idx := n - 1
				if _, ok := seen[idx]; ok {
					continue
				}
				seen[idx] = struct{}{}
				indices = append(indices, idx)
			}
			continue
		}

		n, convErr := strconv.Atoi(token)
		if convErr != nil || n < 1 || n > max {
			return nil, fmt.Errorf("invalid selection: %q", token)
		}
		idx := n - 1
		if _, ok := seen[idx]; ok {
			continue
		}
		seen[idx] = struct{}{}
		indices = append(indices, idx)
	}
	return indices, nil
}

func confirmDelete(count int) (bool, error) {
	fmt.Fprintf(stderrWriter, "Delete %d workspace directorie(s)? [y/N]: ", count)
	if canUseInteractiveConfirm() {
		return confirmDeleteImmediate()
	}

	reader := bufio.NewReader(stdinReader)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

func canUseInteractiveConfirm() bool {
	in, ok := stdinReader.(*os.File)
	if !ok {
		return false
	}
	out, ok := stderrWriter.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(in.Fd())) && term.IsTerminal(int(out.Fd()))
}

func confirmDeleteImmediate() (bool, error) {
	in := stdinReader.(*os.File)
	out := stderrWriter.(*os.File)

	fd := int(in.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return false, err
	}
	defer func() {
		_ = term.Restore(fd, oldState)
	}()

	reader := bufio.NewReader(in)
	for {
		b, readErr := reader.ReadByte()
		if readErr != nil {
			return false, readErr
		}

		done, confirmed := interpretConfirmByte(b)
		if !done {
			continue
		}

		switch b {
		case 'y', 'Y':
			_, _ = fmt.Fprint(out, "y\r\n")
		case 'n', 'N':
			_, _ = fmt.Fprint(out, "n\r\n")
		default:
			_, _ = fmt.Fprint(out, "\r\n")
		}

		return confirmed, nil
	}
}

func interpretConfirmByte(b byte) (done bool, confirmed bool) {
	switch b {
	case 'y', 'Y':
		return true, true
	case 'n', 'N', 'q', 'Q', '\r', '\n', 0x1b:
		return true, false
	default:
		return false, false
	}
}

func runFzf(items []string, multi bool) (string, error) {
	args := []string{"--prompt", "jjw> "}
	if multi {
		args = append(args, "-m")
	}
	cmd := exec.Command("fzf", args...)
	cmd.Stderr = stderrWriter

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}

	var out bytes.Buffer
	cmd.Stdout = &out

	if err := cmd.Start(); err != nil {
		return "", err
	}

	for _, item := range items {
		if _, err := io.WriteString(stdin, item+"\n"); err != nil {
			_ = stdin.Close()
			_ = cmd.Wait()
			return "", err
		}
	}
	_ = stdin.Close()

	if err := cmd.Wait(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if exitErr.ExitCode() == 1 || exitErr.ExitCode() == 130 {
				return "", nil
			}
		}
		return "", err
	}

	return strings.TrimSpace(out.String()), nil
}

func runCommandCapture(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return "", fmt.Errorf("%s %s failed: %s", name, strings.Join(args, " "), msg)
		}
		return "", fmt.Errorf("%s %s failed: %w", name, strings.Join(args, " "), err)
	}
	return out.String(), nil
}

func runCommandToStderr(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = stderrWriter
	cmd.Stderr = stderrWriter
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s failed: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func isLiteralEmptyDir(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

func splitNonEmptyLines(s string) []string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

func expandPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			if trimmed == "~" {
				trimmed = home
			} else if strings.HasPrefix(trimmed, "~/") {
				trimmed = filepath.Join(home, strings.TrimPrefix(trimmed, "~/"))
			}
		}
	}
	abs, err := filepath.Abs(trimmed)
	if err == nil {
		return abs
	}
	return trimmed
}
