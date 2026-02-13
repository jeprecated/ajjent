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
	DevRoot       string   `yaml:"dev_root"`
	WorktreesRoot string   `yaml:"worktrees_root"`
	NameStrategy  string   `yaml:"name_strategy"`
	NameList      []string `yaml:"name_list"`
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

func runMainStack(args []string) error {
	fs := flag.NewFlagSet("main-stack", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var (
		repoRootOverride string
		appOverride      string
		rootOverride     string
		mainOverride     string
	)
	fs.StringVar(&repoRootOverride, "repo", "", "repo root override")
	fs.StringVar(&appOverride, "app", "", "app override")
	fs.StringVar(&rootOverride, "worktrees-root", "", "worktrees root override")
	fs.StringVar(&mainOverride, "main", "", "main workspace name")
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
		mainName, err = currentWorkspaceName(repoRoot, refs)
		if err != nil {
			return err
		}
	}

	fmt.Fprintf(stderrWriter, "Main workspace: %s\n", mainName)
	for _, ref := range refs {
		workspacePath := filepath.Join(cfg.WorktreesRoot, app, ref.Name)
		fmt.Fprintf(stderrWriter, "\n== jj st: %s ==\n", ref.Name)
		if st, statErr := os.Stat(workspacePath); statErr != nil || !st.IsDir() {
			fmt.Fprintf(stderrWriter, "skip: workspace path missing: %s\n", workspacePath)
			continue
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

	mainPath := filepath.Join(cfg.WorktreesRoot, app, mainName)
	if st, statErr := os.Stat(mainPath); statErr != nil || !st.IsDir() {
		return fmt.Errorf("main workspace path missing: %s", mainPath)
	}

	cmdArgs := []string{"-R", mainPath, "rebase", "-b", "@"}
	for _, name := range others {
		cmdArgs = append(cmdArgs, "-d", name+"@")
	}

	fmt.Fprintf(stderrWriter, "\n== Rebase main onto: %s ==\n", strings.Join(others, ", "))
	return commandToStderrFn("jj", cmdArgs...)
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

	app := deriveApp(repoRoot, appOverride)
	activeNames, err := listWorkspaceNames(repoRoot)
	if err != nil {
		return err
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

	if len(candidates) == 0 {
		return nil
	}

	selected, err := selectMany(candidates)
	if err != nil {
		return err
	}
	if len(selected) == 0 {
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
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove %s: %w", path, err)
		}
	}

	for _, path := range selected {
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
	if currentName != "" && name == currentName {
		return repoRoot
	}
	return filepath.Join(worktreesRoot, app, name)
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
	devRoot := os.Getenv("DEV_ROOT")
	if strings.TrimSpace(devRoot) == "" {
		devRoot = "~/Development"
	}
	defaults := config{
		DevRoot:      expandPath(devRoot),
		NameStrategy: strategyFirstUnused,
	}
	defaults.WorktreesRoot = filepath.Join(defaults.DevRoot, "worktrees")

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

	if strings.TrimSpace(merged.DevRoot) == "" {
		merged.DevRoot = expandPath("~/Development")
	}
	if strings.TrimSpace(merged.WorktreesRoot) == "" {
		merged.WorktreesRoot = filepath.Join(merged.DevRoot, "worktrees")
	}

	if merged.NameStrategy == "" {
		merged.NameStrategy = strategyFirstUnused
	}
	if merged.NameStrategy != strategyFirstUnused && merged.NameStrategy != strategyStateful {
		return config{}, fmt.Errorf("invalid name_strategy: %q", merged.NameStrategy)
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
	if _, err := lookPathFn("fzf"); err == nil {
		out, fzfErr := runFzfFn(items, false)
		if fzfErr != nil {
			return "", fzfErr
		}
		if out == "" {
			return "", errors.New("no selection")
		}
		return out, nil
	}

	fmt.Fprintln(stderrWriter, "Select a workspace:")
	for i, item := range items {
		fmt.Fprintf(stderrWriter, "%d) %s\n", i+1, item)
	}
	fmt.Fprint(stderrWriter, "Enter number: ")

	reader := bufio.NewReader(stdinReader)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 1 || n > len(items) {
		return "", errors.New("invalid selection")
	}
	return items[n-1], nil
}

func selectMany(items []string) ([]string, error) {
	sort.Strings(items)
	if _, err := lookPathFn("fzf"); err == nil {
		out, fzfErr := runFzfFn(items, true)
		if fzfErr != nil {
			return nil, fzfErr
		}
		if out == "" {
			return nil, nil
		}
		lines := splitNonEmptyLines(out)
		return lines, nil
	}

	fmt.Fprintln(stderrWriter, "Select directories to delete (comma-separated numbers):")
	for i, item := range items {
		fmt.Fprintf(stderrWriter, "%d) %s\n", i+1, item)
	}
	fmt.Fprint(stderrWriter, "Enter selections (or blank to cancel): ")

	reader := bufio.NewReader(stdinReader)
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, nil
	}

	parts := strings.Split(line, ",")
	seen := map[int]struct{}{}
	selected := make([]string, 0, len(parts))
	for _, p := range parts {
		n, convErr := strconv.Atoi(strings.TrimSpace(p))
		if convErr != nil || n < 1 || n > len(items) {
			return nil, fmt.Errorf("invalid selection: %q", p)
		}
		idx := n - 1
		if _, ok := seen[idx]; ok {
			continue
		}
		seen[idx] = struct{}{}
		selected = append(selected, items[idx])
	}
	return selected, nil
}

func confirmDelete(count int) (bool, error) {
	fmt.Fprintf(stderrWriter, "Delete %d workspace directorie(s)? [y/N]: ", count)
	reader := bufio.NewReader(stdinReader)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
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
