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
	"regexp"
	"sort"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

var (
	commandCaptureFn            = runCommandCapture
	commandToStderrFn           = runCommandToStderr
	lookPathFn                  = exec.LookPath
	stdinReader       io.Reader = os.Stdin
	stdoutWriter      io.Writer = os.Stdout
	stderrWriter      io.Writer = os.Stderr
)

type config struct {
	WorkspacesRoot     string                   `yaml:"workspaces_root"`
	Project            string                   `yaml:"project"`
	HandleStrategy     string                   `yaml:"handle_strategy"`
	WorkspaceHandles   []string                 `yaml:"workspace_handles"`
	MainWorkspace      string                   `yaml:"main_workspace"`
	AssimilatedFolders []string                 `yaml:"assimilated_folders"`
	Projects           map[string]projectConfig `yaml:"projects"`
	Stack              stackConfig              `yaml:"stack"`
	Create             createSetup              `yaml:"create"`
}

type projectConfig struct {
	AssimilatedFolders []string `yaml:"assimilated_folders"`
}

type stackConfig struct {
	RebaseMode       string `yaml:"rebase_mode"`
	Shape            string `yaml:"shape"`
	ConflictStrategy string `yaml:"conflict_strategy"`
}

type createSetup struct {
	Envrc       bool `yaml:"envrc"`
	DirenvAllow bool `yaml:"direnv_allow"`
}

type state struct {
	NextIndex int `json:"next_index"`
}

type workspaceRef struct {
	Handle       string
	TargetChange string
	Root         string
}

type workspaceInfo struct {
	Ref      workspaceRef
	Path     string
	Missing  bool
	Empty    bool
	Stacked  bool
	Conflict bool
	Current  bool
	Main     bool
	External bool
}

const (
	strategyFirstUnused = "first-unused"
	strategyNextUnused  = "next-unused"
)

var defaultWorkspaceHandles = []string{
	"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india", "juliett", "kilo", "lima", "mike", "november", "oscar", "papa", "quebec", "romeo", "sierra", "tango", "uniform", "victor", "whiskey", "xray", "yankee", "zulu",
}

var slugRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

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
	case "init":
		return runInit(args[1:])
	case "create":
		return runCreate(args[1:])
	case "open":
		return runOpen(args[1:])
	case "list":
		return runList(args[1:])
	case "main":
		return runMain(args[1:])
	case "close":
		return runClose(args[1:])
	case "tidy":
		return runTidy(args[1:])
	case "stack":
		return runStack(args[1:])
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
	fmt.Fprintln(w, "Setup:")
	fmt.Fprintln(w, "  init              Create jjw config")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Workspace lifecycle:")
	fmt.Fprintln(w, "  create [handle]   Create a Workspace and print its path")
	fmt.Fprintln(w, "  open [handle]     Print an existing Workspace path")
	fmt.Fprintln(w, "  close [handle...] Close Workspace(s)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Stacking:")
	fmt.Fprintln(w, "  stack [handle...] Stack selected Workspaces into Main")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Inspect and housekeeping:")
	fmt.Fprintln(w, "  list              List Workspaces")
	fmt.Fprintln(w, "  main              Print the Main Workspace path")
	fmt.Fprintln(w, "  tidy              Remove empty leftover Workspace directories")
}

func parseCommandFlags(fs *flag.FlagSet, args []string, usage string, summary string) (bool, error) {
	fs.SetOutput(io.Discard)
	if hasHelpFlag(args) {
		printCommandUsage(stdoutWriter, fs, usage, summary)
		return true, nil
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printCommandUsage(stdoutWriter, fs, usage, summary)
			return true, nil
		}
		return false, err
	}
	return false, nil
}

func hasHelpFlag(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

func normalizePositionalsLast(args []string, flagsWithValues map[string]struct{}) ([]string, error) {
	flags := make([]string, 0, len(args))
	positionals := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "--") {
			flags = append(flags, arg)
			if strings.Contains(arg, "=") {
				continue
			}
			if _, ok := flagsWithValues[arg]; ok {
				if i+1 >= len(args) {
					return nil, fmt.Errorf("flag %s requires a value", arg)
				}
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)
			continue
		}
		positionals = append(positionals, arg)
	}
	return append(flags, positionals...), nil
}

func printCommandUsage(w io.Writer, fs *flag.FlagSet, usage string, summary string) {
	fmt.Fprintf(w, "Usage: %s\n", usage)
	if strings.TrimSpace(summary) != "" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, summary)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Options:")
	fs.VisitAll(func(f *flag.Flag) {
		valueName, usageText := flag.UnquoteUsage(f)
		option := "--" + f.Name
		if valueName != "" {
			option += " " + valueName
		}
		fmt.Fprintf(w, "  %-28s %s", option, usageText)
		if f.DefValue != "" && f.DefValue != "false" {
			fmt.Fprintf(w, " (default %q)", f.DefValue)
		}
		fmt.Fprintln(w)
	})
}

func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	var local, force bool
	var repoRootOverride, workspacesRoot, project string
	fs.BoolVar(&local, "local", false, "write repo-local .jjw/config.yaml instead of global config")
	fs.BoolVar(&force, "force", false, "overwrite existing config")
	fs.StringVar(&repoRootOverride, "repo", "", "repo root override for --local")
	fs.StringVar(&workspacesRoot, "workspaces-root", "", "directory containing Project Workspace folders")
	fs.StringVar(&project, "project", "", "Project slug for local config")
	if handled, err := parseCommandFlags(fs, args, "jjw init [options]", "Create jjw config."); handled || err != nil {
		return err
	}
	cfgPath := ""
	if local {
		repoRoot, err := resolveRepoRoot(repoRootOverride)
		if err != nil {
			return err
		}
		cfgPath = filepath.Join(repoRoot, ".jjw", "config.yaml")
	} else {
		path, ok := globalConfigPath()
		if !ok {
			return errors.New("could not resolve global config path")
		}
		cfgPath = path
	}
	if exists(cfgPath) && !force {
		return fmt.Errorf("config already exists: %s (pass --force to overwrite)", cfgPath)
	}
	cfg := defaultConfig()
	if strings.TrimSpace(workspacesRoot) == "" {
		return errors.New("init requires --workspaces-root")
	}
	cfg.WorkspacesRoot = expandPath(workspacesRoot)
	if strings.TrimSpace(project) != "" {
		if err := validateSlug("project", project); err != nil {
			return err
		}
		cfg.Project = strings.TrimSpace(project)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
		return fmt.Errorf("write config %s: %w", cfgPath, err)
	}
	fmt.Fprintln(stdoutWriter, cfgPath)
	return nil
}

func runCreate(args []string) error {
	var err error
	args, err = normalizePositionalsLast(args, map[string]struct{}{"--repo": {}, "--project": {}, "--workspaces-root": {}})
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	var repoRootOverride, projectOverride, rootOverride string
	var envrc, direnvAllow bool
	fs.StringVar(&repoRootOverride, "repo", "", "repo root override")
	fs.StringVar(&projectOverride, "project", "", "Project override")
	fs.StringVar(&rootOverride, "workspaces-root", "", "Workspaces root override")
	fs.BoolVar(&envrc, "envrc", false, "create .envrc in the new Workspace")
	fs.BoolVar(&direnvAllow, "direnv-allow", false, "run direnv allow for the new Workspace")
	if handled, err := parseCommandFlags(fs, args, "jjw create [handle] [options]", "Create a new Workspace and print its path."); handled || err != nil {
		return err
	}
	positionals := fs.Args()
	if len(positionals) > 1 {
		return errors.New("create accepts at most one Workspace Handle")
	}
	repoRoot, cfg, project, err := commandContext(repoRootOverride, projectOverride, rootOverride)
	if err != nil {
		return err
	}
	handles, err := listWorkspaceHandles(repoRoot)
	if err != nil {
		return err
	}
	inUse := make(map[string]struct{}, len(handles))
	for _, h := range handles {
		inUse[h] = struct{}{}
	}
	handle := ""
	if len(positionals) == 1 {
		handle = strings.TrimSpace(positionals[0])
		if err := validateWorkspaceHandle(handle); err != nil {
			return err
		}
		if _, ok := inUse[handle]; ok {
			return fmt.Errorf("Workspace %q already exists; use `jjw open %s`", handle, handle)
		}
	} else {
		handle, err = chooseAutoHandle(cfg, repoRoot, inUse)
		if err != nil {
			return err
		}
	}
	target := filepath.Join(cfg.WorkspacesRoot, project, handle)
	if exists(target) {
		return fmt.Errorf("Workspace path already exists: %s", target)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create Workspace parent: %w", err)
	}
	if err := commandToStderrFn("jj", "-R", repoRoot, "workspace", "add", "--name", handle, target); err != nil {
		return err
	}
	if envrc || cfg.Create.Envrc {
		if err := ensureEnvrc(target); err != nil {
			return err
		}
	}
	if err := materializeAssimilatedFolders(mainWorkspaceRoot(repoRoot), target, cfg, project); err != nil {
		return err
	}
	if direnvAllow || cfg.Create.DirenvAllow {
		if _, err := lookPathFn("direnv"); err == nil {
			_ = commandToStderrFn("direnv", "allow", target)
		}
	}
	fmt.Fprintln(stdoutWriter, target)
	return nil
}

func runOpen(args []string) error {
	var err error
	args, err = normalizePositionalsLast(args, map[string]struct{}{"--repo": {}, "--project": {}, "--workspaces-root": {}})
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("open", flag.ContinueOnError)
	var repoRootOverride, projectOverride, rootOverride string
	fs.StringVar(&repoRootOverride, "repo", "", "repo root override")
	fs.StringVar(&projectOverride, "project", "", "Project override")
	fs.StringVar(&rootOverride, "workspaces-root", "", "Workspaces root override")
	if handled, err := parseCommandFlags(fs, args, "jjw open [handle] [options]", "Print an existing Workspace path."); handled || err != nil {
		return err
	}
	positionals := fs.Args()
	if len(positionals) > 1 {
		return errors.New("open accepts at most one Workspace Handle")
	}
	repoRoot, cfg, project, err := commandContext(repoRootOverride, projectOverride, rootOverride)
	if err != nil {
		return err
	}
	infos, _, err := loadWorkspaceInfos(repoRoot, cfg, project)
	if err != nil {
		return err
	}
	if len(positionals) == 1 {
		handle := strings.TrimSpace(positionals[0])
		if err := validateWorkspaceHandle(handle); err != nil {
			return err
		}
		for _, info := range infos {
			if info.Ref.Handle == handle {
				if info.Missing {
					return fmt.Errorf("Workspace %q path not found: %s", handle, info.Path)
				}
				if err := materializeAssimilatedFolders(mainWorkspaceRoot(repoRoot), info.Path, cfg, project); err != nil {
					return err
				}
				fmt.Fprintln(stdoutWriter, info.Path)
				return nil
			}
		}
		return fmt.Errorf("Workspace %q not found; use `jjw create %s` to create it", handle, handle)
	}
	if !canUseTUI() {
		return errors.New("open requires a Workspace Handle when not running in a terminal")
	}
	items := selectorItemsForOpen(infos)
	selected, _, err := runSelector(selectorOptions{Title: "Open Workspace", Mode: selectorSingle, Items: items})
	if err != nil {
		return err
	}
	if len(selected) == 0 {
		return errors.New("no Workspace selected")
	}
	if err := materializeAssimilatedFolders(mainWorkspaceRoot(repoRoot), selected[0].Path, cfg, project); err != nil {
		return err
	}
	fmt.Fprintln(stdoutWriter, selected[0].Path)
	return nil
}

func runList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	var repoRootOverride, projectOverride, rootOverride string
	var pathsOnly bool
	fs.StringVar(&repoRootOverride, "repo", "", "repo root override")
	fs.StringVar(&projectOverride, "project", "", "Project override")
	fs.StringVar(&rootOverride, "workspaces-root", "", "Workspaces root override")
	fs.BoolVar(&pathsOnly, "paths", false, "print Workspace paths only")
	if handled, err := parseCommandFlags(fs, args, "jjw list [options]", "List Workspaces."); handled || err != nil {
		return err
	}
	repoRoot, cfg, project, err := commandContext(repoRootOverride, projectOverride, rootOverride)
	if err != nil {
		return err
	}
	infos, _, err := loadWorkspaceInfos(repoRoot, cfg, project)
	if err != nil {
		return err
	}
	for _, info := range infos {
		if pathsOnly {
			fmt.Fprintln(stdoutWriter, info.Path)
			continue
		}
		fmt.Fprintf(stdoutWriter, "%s\t%s\t%s\t%s\n", info.Ref.Handle, strings.Join(markers(info), ","), statusLabel(info), info.Path)
	}
	return nil
}

func runMain(args []string) error {
	fs := flag.NewFlagSet("main", flag.ContinueOnError)
	var repoRootOverride, projectOverride, rootOverride string
	fs.StringVar(&repoRootOverride, "repo", "", "repo root override")
	fs.StringVar(&projectOverride, "project", "", "Project override")
	fs.StringVar(&rootOverride, "workspaces-root", "", "Workspaces root override")
	if handled, err := parseCommandFlags(fs, args, "jjw main [options]", "Print the Main Workspace path."); handled || err != nil {
		return err
	}
	repoRoot, cfg, project, err := commandContext(repoRootOverride, projectOverride, rootOverride)
	if err != nil {
		return err
	}
	infos, _, err := loadWorkspaceInfos(repoRoot, cfg, project)
	if err != nil {
		return err
	}
	for _, info := range infos {
		if info.Main {
			if info.Missing {
				return fmt.Errorf("Main Workspace path missing: %s", info.Path)
			}
			fmt.Fprintln(stdoutWriter, info.Path)
			return nil
		}
	}
	return fmt.Errorf("Main Workspace %q not found", cfg.MainWorkspace)
}

func runTidy(args []string) error {
	fs := flag.NewFlagSet("tidy", flag.ContinueOnError)
	var repoRootOverride, projectOverride, rootOverride string
	fs.StringVar(&repoRootOverride, "repo", "", "repo root override")
	fs.StringVar(&projectOverride, "project", "", "Project override")
	fs.StringVar(&rootOverride, "workspaces-root", "", "Workspaces root override")
	if handled, err := parseCommandFlags(fs, args, "jjw tidy [options]", "Remove empty leftover Workspace directories."); handled || err != nil {
		return err
	}
	repoRoot, cfg, project, err := commandContext(repoRootOverride, projectOverride, rootOverride)
	if err != nil {
		return err
	}
	refs, err := listWorkspaceRefs(repoRoot)
	if err != nil {
		return err
	}
	active := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		active[ref.Handle] = struct{}{}
	}
	projectRoot := filepath.Join(cfg.WorkspacesRoot, project)
	entries, err := os.ReadDir(projectRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(stderrWriter, "No Project Workspace directory found at %s.\n", projectRoot)
			return nil
		}
		return err
	}
	deleted := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		handle := e.Name()
		if _, ok := active[handle]; ok {
			continue
		}
		full := filepath.Join(projectRoot, handle)
		empty, err := isLiteralEmptyDir(full)
		if err != nil {
			return err
		}
		if !empty {
			fmt.Fprintf(stderrWriter, "leftover not empty: %s\n", full)
			continue
		}
		if err := os.Remove(full); err != nil {
			return fmt.Errorf("remove %s: %w", full, err)
		}
		fmt.Fprintln(stdoutWriter, full)
		deleted++
	}
	if deleted == 0 {
		fmt.Fprintln(stderrWriter, "No empty leftover Workspaces to tidy.")
	}
	return nil
}

func runClose(args []string) error {
	var err error
	args, err = normalizePositionalsLast(args, map[string]struct{}{"--repo": {}, "--project": {}, "--workspaces-root": {}})
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("close", flag.ContinueOnError)
	var repoRootOverride, projectOverride, rootOverride string
	var all, force, yes bool
	fs.StringVar(&repoRootOverride, "repo", "", "repo root override")
	fs.StringVar(&projectOverride, "project", "", "Project override")
	fs.StringVar(&rootOverride, "workspaces-root", "", "Workspaces root override")
	fs.BoolVar(&all, "all", false, "close all Closable Workspaces")
	fs.BoolVar(&force, "force", false, "forced close: abandon unique mutable changes before closing")
	fs.BoolVar(&yes, "yes", false, "skip confirmation")
	if handled, err := parseCommandFlags(fs, args, "jjw close [handle...] [options]", "Close Workspace(s)."); handled || err != nil {
		return err
	}
	positionals := fs.Args()
	if all && len(positionals) > 0 {
		return errors.New("provide either --all or Workspace Handles, not both")
	}
	repoRoot, cfg, project, err := commandContext(repoRootOverride, projectOverride, rootOverride)
	if err != nil {
		return err
	}
	infos, currentHandle, err := loadWorkspaceInfos(repoRoot, cfg, project)
	if err != nil {
		return err
	}
	byHandle := mapInfosByHandle(infos)
	targets := []workspaceInfo{}
	if all {
		for _, info := range infos {
			if info.Main || info.Missing {
				continue
			}
			if force || isClosable(info) {
				targets = append(targets, info)
			}
		}
	} else if len(positionals) > 0 {
		for _, h := range positionals {
			h = strings.TrimSpace(h)
			if err := validateWorkspaceHandle(h); err != nil {
				return err
			}
			info, ok := byHandle[h]
			if !ok {
				return fmt.Errorf("Workspace %q not found", h)
			}
			targets = append(targets, info)
		}
	} else if currentHandle != "" && currentHandle != cfg.MainWorkspace {
		info, ok := byHandle[currentHandle]
		if !ok {
			return fmt.Errorf("Current Workspace %q not found", currentHandle)
		}
		targets = append(targets, info)
	} else {
		if !canUseTUI() {
			return errors.New("close requires Workspace Handles or --all when not running in a terminal")
		}
		items := selectorItemsForClose(infos, force)
		selected, opts, err := runSelector(selectorOptions{Title: "Close Workspaces", Mode: selectorMulti, Items: items, ForceEnabled: force, AllowForceToggle: true})
		if err != nil {
			return err
		}
		force = opts.ForceEnabled
		for _, item := range selected {
			if info, ok := byHandle[item.Handle]; ok {
				targets = append(targets, info)
			}
		}
	}
	if len(targets) == 0 {
		fmt.Fprintln(stderrWriter, "No Workspaces to close.")
		return nil
	}
	for _, info := range targets {
		if info.Main {
			return fmt.Errorf("Main Workspace %q is never closable", info.Ref.Handle)
		}
		if info.Missing {
			return fmt.Errorf("Workspace %q path missing: %s", info.Ref.Handle, info.Path)
		}
		if !force && !isClosable(info) {
			return fmt.Errorf("Workspace %q is not closable; stack it first or use --force", info.Ref.Handle)
		}
	}
	mainInfo, ok := byHandle[cfg.MainWorkspace]
	if !ok {
		return fmt.Errorf("Main Workspace %q not found", cfg.MainWorkspace)
	}
	if force && !yes {
		ok, err := confirm(fmt.Sprintf("Forced Closing will abandon unique mutable changes for %d Workspace(s). Continue? [y/N]: ", len(targets)))
		if err != nil || !ok {
			return err
		}
	}
	if !force && !yes {
		ok, err := confirm(fmt.Sprintf("Close %d Workspace(s)? [y/N]: ", len(targets)))
		if err != nil || !ok {
			return err
		}
	}
	if _, err := closeWorkspaces(repoRoot, cfg, project, targets, force, yes); err != nil {
		return err
	}
	if err := abandonTopEmptyMutableAncestors(mainInfo.Path); err != nil {
		return err
	}
	fmt.Fprintln(stdoutWriter, mainInfo.Path)
	return nil
}

func runStack(args []string) error {
	var err error
	args, err = normalizePositionalsLast(args, map[string]struct{}{"--repo": {}, "--project": {}, "--workspaces-root": {}, "--workspace": {}, "--rebase-mode": {}, "--stack-shape": {}, "--conflict-strategy": {}})
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("stack", flag.ContinueOnError)
	var repoRootOverride, projectOverride, rootOverride string
	var workspaceOverride, rebaseMode, shape, conflictStrategy string
	var all, yes bool
	fs.StringVar(&repoRootOverride, "repo", "", "repo root override")
	fs.StringVar(&projectOverride, "project", "", "Project override")
	fs.StringVar(&rootOverride, "workspaces-root", "", "Workspaces root override")
	fs.StringVar(&workspaceOverride, "workspace", "", "Main Workspace handle override")
	fs.StringVar(&rebaseMode, "rebase-mode", "", "advanced: auto, branch, revision")
	fs.StringVar(&shape, "stack-shape", "", "advanced: auto, linear, merge")
	fs.StringVar(&conflictStrategy, "conflict-strategy", "", "advanced: off, prefer-clean")
	fs.BoolVar(&all, "all", false, "stack all stack-relevant Workspaces")
	fs.BoolVar(&yes, "yes", false, "skip post-Stack Close prompt")
	if handled, err := parseCommandFlags(fs, args, "jjw stack [handle...] [options]", "Stack selected Workspaces into the Main Workspace."); handled || err != nil {
		return err
	}
	positionals := fs.Args()
	if all && len(positionals) > 0 {
		return errors.New("provide either --all or Stack Input Handles, not both")
	}
	repoRoot, cfg, project, err := commandContext(repoRootOverride, projectOverride, rootOverride)
	if err != nil {
		return err
	}
	if strings.TrimSpace(workspaceOverride) != "" {
		if err := validateWorkspaceHandle(workspaceOverride); err != nil {
			return err
		}
		cfg.MainWorkspace = strings.TrimSpace(workspaceOverride)
	}
	applyStackOverrides(&cfg, rebaseMode, shape, conflictStrategy)
	infos, _, err := loadWorkspaceInfos(repoRoot, cfg, project)
	if err != nil {
		return err
	}
	byHandle := mapInfosByHandle(infos)
	inputs := []string{}
	if all {
		for _, info := range infos {
			if isStackRelevant(info) {
				inputs = append(inputs, info.Ref.Handle)
			}
		}
	} else if len(positionals) > 0 {
		for _, h := range positionals {
			h = strings.TrimSpace(h)
			if err := validateWorkspaceHandle(h); err != nil {
				return err
			}
			info, ok := byHandle[h]
			if !ok {
				return fmt.Errorf("Workspace %q not found", h)
			}
			if info.Main {
				return fmt.Errorf("Main Workspace %q cannot be a Stack Input", h)
			}
			if info.Missing {
				return fmt.Errorf("Workspace %q path missing: %s", h, info.Path)
			}
			inputs = append(inputs, h)
		}
	} else {
		if !canUseTUI() {
			return errors.New("stack requires Workspace Handles or --all when not running in a terminal")
		}
		items := selectorItemsForStack(infos)
		selected, opts, err := runSelector(selectorOptions{Title: "Stack Workspaces", Mode: selectorMulti, Items: items, AllDefault: true, StackOptions: cfg.Stack})
		if err != nil {
			return err
		}
		cfg.Stack = opts.StackOptions
		for _, item := range selected {
			inputs = append(inputs, item.Handle)
		}
	}
	inputs = uniqueNonEmptyStrings(inputs)
	if len(inputs) == 0 {
		return errors.New("no Stack Inputs selected")
	}
	mainInfo, ok := byHandle[cfg.MainWorkspace]
	if !ok {
		return fmt.Errorf("Main Workspace %q not found", cfg.MainWorkspace)
	}
	if mainInfo.Missing {
		return fmt.Errorf("Main Workspace path missing: %s", mainInfo.Path)
	}
	if err := validateLinearSelection(mainInfo.Path, inputs, cfg.Stack.Shape); err != nil {
		return err
	}
	conflicted, err := runStackRebase(mainInfo.Path, inputs, cfg.Stack)
	if err != nil {
		return err
	}
	if !conflicted {
		if err := abandonTopEmptyMutableAncestors(mainInfo.Path); err != nil {
			return err
		}
	}
	if err := commandToStderrFn("jj", "-R", mainInfo.Path, "workspace", "update-stale"); err != nil {
		return err
	}
	if !yes && canUseTUI() {
		updatedInfos, _, err := loadWorkspaceInfos(repoRoot, cfg, project)
		if err == nil {
			updated := mapInfosByHandle(updatedInfos)
			closable := []workspaceInfo{}
			for _, h := range inputs {
				if info, ok := updated[h]; ok && isClosable(info) {
					closable = append(closable, info)
				}
			}
			if len(closable) > 0 {
				ok, err := confirm(fmt.Sprintf("Close %d normally Closable Stack Input(s)? [y/N]: ", len(closable)))
				if err != nil {
					return err
				}
				if ok {
					closed, err := closeWorkspaces(repoRoot, cfg, project, closable, false, true)
					if err != nil {
						return err
					}
					for _, path := range closed {
						fmt.Fprintln(stdoutWriter, path)
					}
				}
			}
		}
	}
	return nil
}

func commandContext(repoRootOverride, projectOverride, rootOverride string) (string, config, string, error) {
	repoRoot, err := resolveRepoRoot(repoRootOverride)
	if err != nil {
		return "", config{}, "", err
	}
	cfg, err := loadConfig(repoRoot)
	if err != nil {
		return "", config{}, "", err
	}
	if strings.TrimSpace(rootOverride) != "" {
		cfg.WorkspacesRoot = expandPath(rootOverride)
	}
	if err := requireWorkspacesRoot(cfg.WorkspacesRoot); err != nil {
		return "", config{}, "", err
	}
	project := strings.TrimSpace(projectOverride)
	if project == "" {
		project = strings.TrimSpace(cfg.Project)
	}
	if project == "" {
		project = deriveProject(repoRoot)
	}
	if err := validateSlug("project", project); err != nil {
		return "", config{}, "", err
	}
	cfg.Project = project
	return repoRoot, cfg, project, nil
}

func defaultConfig() config {
	return config{
		HandleStrategy:     strategyFirstUnused,
		WorkspaceHandles:   append([]string(nil), defaultWorkspaceHandles...),
		MainWorkspace:      "default",
		AssimilatedFolders: []string{},
		Projects:           map[string]projectConfig{},
		Stack: stackConfig{
			RebaseMode:       "auto",
			Shape:            "auto",
			ConflictStrategy: "prefer-clean",
		},
	}
}

func loadConfig(repoRoot string) (config, error) {
	merged := defaultConfig()
	if globalPath, ok := globalConfigPath(); ok {
		if err := mergeConfigFile(&merged, globalPath); err != nil {
			return config{}, err
		}
	}
	localPath := filepath.Join(repoRoot, ".jjw", "config.yaml")
	if err := mergeConfigFile(&merged, localPath); err != nil {
		return config{}, err
	}
	merged.WorkspacesRoot = expandPath(merged.WorkspacesRoot)
	if strings.TrimSpace(merged.HandleStrategy) == "" {
		merged.HandleStrategy = strategyFirstUnused
	}
	if merged.HandleStrategy != strategyFirstUnused && merged.HandleStrategy != strategyNextUnused {
		return config{}, fmt.Errorf("invalid handle_strategy: %q", merged.HandleStrategy)
	}
	if strings.TrimSpace(merged.MainWorkspace) == "" {
		merged.MainWorkspace = "default"
	}
	if err := validateWorkspaceHandle(merged.MainWorkspace); err != nil {
		return config{}, err
	}
	if err := validateStackConfig(merged.Stack); err != nil {
		return config{}, err
	}
	if strings.TrimSpace(merged.Project) != "" {
		if err := validateSlug("project", merged.Project); err != nil {
			return config{}, err
		}
	}
	for _, handle := range merged.WorkspaceHandles {
		if err := validateWorkspaceHandle(handle); err != nil {
			return config{}, err
		}
	}
	folders, err := normalizeAssimilatedFolders(merged.AssimilatedFolders)
	if err != nil {
		return config{}, err
	}
	merged.AssimilatedFolders = folders
	if merged.Projects == nil {
		merged.Projects = map[string]projectConfig{}
	}
	for project, projectCfg := range merged.Projects {
		if err := validateSlug("project", project); err != nil {
			return config{}, err
		}
		folders, err := normalizeAssimilatedFolders(projectCfg.AssimilatedFolders)
		if err != nil {
			return config{}, err
		}
		projectCfg.AssimilatedFolders = folders
		merged.Projects[project] = projectCfg
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
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&src); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	if strings.TrimSpace(src.WorkspacesRoot) != "" {
		dst.WorkspacesRoot = strings.TrimSpace(src.WorkspacesRoot)
	}
	if strings.TrimSpace(src.Project) != "" {
		dst.Project = strings.TrimSpace(src.Project)
	}
	if strings.TrimSpace(src.HandleStrategy) != "" {
		dst.HandleStrategy = strings.TrimSpace(src.HandleStrategy)
	}
	if len(src.WorkspaceHandles) > 0 {
		dst.WorkspaceHandles = append([]string(nil), src.WorkspaceHandles...)
	}
	if strings.TrimSpace(src.MainWorkspace) != "" {
		dst.MainWorkspace = strings.TrimSpace(src.MainWorkspace)
	}
	if len(src.AssimilatedFolders) > 0 {
		dst.AssimilatedFolders = appendUniqueStrings(dst.AssimilatedFolders, src.AssimilatedFolders)
	}
	if len(src.Projects) > 0 {
		if dst.Projects == nil {
			dst.Projects = map[string]projectConfig{}
		}
		for project, projectCfg := range src.Projects {
			current := dst.Projects[project]
			current.AssimilatedFolders = appendUniqueStrings(current.AssimilatedFolders, projectCfg.AssimilatedFolders)
			dst.Projects[project] = current
		}
	}
	if strings.TrimSpace(src.Stack.RebaseMode) != "" {
		dst.Stack.RebaseMode = strings.TrimSpace(src.Stack.RebaseMode)
	}
	if strings.TrimSpace(src.Stack.Shape) != "" {
		dst.Stack.Shape = strings.TrimSpace(src.Stack.Shape)
	}
	if strings.TrimSpace(src.Stack.ConflictStrategy) != "" {
		dst.Stack.ConflictStrategy = strings.TrimSpace(src.Stack.ConflictStrategy)
	}
	if src.Create.Envrc {
		dst.Create.Envrc = true
	}
	if src.Create.DirenvAllow {
		dst.Create.DirenvAllow = true
	}
	return nil
}

func validateStackConfig(cfg stackConfig) error {
	if err := validateStackRebaseMode(cfg.RebaseMode); err != nil {
		return err
	}
	if err := validateStackShape(cfg.Shape); err != nil {
		return err
	}
	if _, err := resolveStackConflictStrategy(cfg.ConflictStrategy); err != nil {
		return err
	}
	return nil
}

func applyStackOverrides(cfg *config, rebaseMode, shape, conflictStrategy string) {
	if strings.TrimSpace(rebaseMode) != "" {
		cfg.Stack.RebaseMode = strings.TrimSpace(rebaseMode)
	}
	if strings.TrimSpace(shape) != "" {
		cfg.Stack.Shape = strings.TrimSpace(shape)
	}
	if strings.TrimSpace(conflictStrategy) != "" {
		cfg.Stack.ConflictStrategy = strings.TrimSpace(conflictStrategy)
	}
}

func requireWorkspacesRoot(root string) error {
	if strings.TrimSpace(root) == "" {
		return errors.New("workspaces_root is required; set it in ~/.config/jjw/config.yaml or .jjw/config.yaml, pass --workspaces-root, or run `jjw init`")
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

func validateWorkspaceHandle(handle string) error { return validateSlug("Workspace Handle", handle) }

func validateSlug(label, value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fmt.Errorf("%s is empty", label)
	}
	if trimmed != value {
		return fmt.Errorf("invalid %s %q; expected a safe single path-segment slug", label, value)
	}
	if trimmed == "." || trimmed == ".." || strings.Contains(trimmed, "/") || strings.Contains(trimmed, string(os.PathSeparator)) || !slugRE.MatchString(trimmed) {
		return fmt.Errorf("invalid %s %q; expected a safe single path-segment slug", label, value)
	}
	return nil
}

func chooseAutoHandle(cfg config, repoRoot string, inUse map[string]struct{}) (string, error) {
	if len(cfg.WorkspaceHandles) == 0 {
		return "", errors.New("workspace_handles is empty; configure handles in ~/.config/jjw/config.yaml or .jjw/config.yaml")
	}
	handles := make([]string, 0, len(cfg.WorkspaceHandles))
	for _, candidate := range cfg.WorkspaceHandles {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if err := validateWorkspaceHandle(candidate); err != nil {
			return "", err
		}
		handles = append(handles, candidate)
	}
	if len(handles) == 0 {
		return "", errors.New("workspace_handles has no usable entries")
	}
	switch cfg.HandleStrategy {
	case strategyFirstUnused:
		for _, candidate := range handles {
			if _, ok := inUse[candidate]; !ok {
				return candidate, nil
			}
		}
		return "", errors.New("all configured Workspace Handles are in use; add more workspace_handles")
	case strategyNextUnused:
		st, err := loadState(repoRoot)
		if err != nil {
			return "", err
		}
		for i := st.NextIndex; i < len(handles); i++ {
			candidate := handles[i]
			if _, ok := inUse[candidate]; ok {
				continue
			}
			st.NextIndex = i + 1
			if saveErr := saveState(repoRoot, st); saveErr != nil {
				return "", saveErr
			}
			return candidate, nil
		}
		return "", errors.New("all configured Workspace Handles are exhausted for next-unused strategy; extend workspace_handles")
	default:
		return "", fmt.Errorf("unsupported handle_strategy: %s", cfg.HandleStrategy)
	}
}

func loadWorkspaceInfos(repoRoot string, cfg config, project string) ([]workspaceInfo, string, error) {
	refs, err := listWorkspaceRefs(repoRoot)
	if err != nil {
		return nil, "", err
	}
	current := ""
	if detected, err := currentWorkspaceHandle(repoRoot, refs); err == nil {
		current = detected
	}
	infos := make([]workspaceInfo, 0, len(refs))
	for _, ref := range refs {
		path := workspacePathForRef(repoRoot, cfg.WorkspacesRoot, project, ref, current)
		info := workspaceInfo{Ref: ref, Path: path, Current: ref.Handle == current, Main: ref.Handle == cfg.MainWorkspace}
		if st, err := os.Stat(path); err != nil || !st.IsDir() {
			info.Missing = true
		}
		canonical := filepath.Clean(filepath.Join(cfg.WorkspacesRoot, project, ref.Handle))
		info.External = !info.Missing && filepath.Clean(path) != canonical && !info.Main
		info.Empty, _ = revisionMatches(pathOrRepo(repoRoot, path), "empty() & "+ref.Handle+"@")
		info.Conflict, _ = revisionMatches(pathOrRepo(repoRoot, path), "conflicts() & "+ref.Handle+"@")
		if !info.Main && cfg.MainWorkspace != "" {
			info.Stacked, _ = revisionIsAncestor(pathOrRepo(repoRoot, path), ref.Handle+"@", cfg.MainWorkspace+"@")
		}
		infos = append(infos, info)
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Ref.Handle < infos[j].Ref.Handle })
	return infos, current, nil
}

func pathOrRepo(repoRoot, path string) string {
	if path != "" {
		return path
	}
	return repoRoot
}

func mapInfosByHandle(infos []workspaceInfo) map[string]workspaceInfo {
	m := make(map[string]workspaceInfo, len(infos))
	for _, info := range infos {
		m[info.Ref.Handle] = info
	}
	return m
}

func isClosable(info workspaceInfo) bool {
	return !info.Main && !info.Missing && !info.Conflict && (info.Empty || info.Stacked)
}

func isStackRelevant(info workspaceInfo) bool {
	return !info.Main && !info.Missing && (info.Conflict || (!info.Empty && !info.Stacked))
}

func statusLabel(info workspaceInfo) string {
	if info.Missing {
		return "missing"
	}
	if info.Conflict {
		return "conflict"
	}
	if info.Empty {
		return "empty"
	}
	if info.Stacked {
		return "stacked"
	}
	return "unstacked"
}

func markers(info workspaceInfo) []string {
	var out []string
	if info.Main {
		out = append(out, "main")
	}
	if info.Current {
		out = append(out, "current")
	}
	if info.External {
		out = append(out, "external")
	}
	if len(out) == 0 {
		out = append(out, "-")
	}
	return out
}

func closeWorkspaces(repoRoot string, cfg config, project string, targets []workspaceInfo, force bool, yes bool) ([]string, error) {
	closed := []string{}
	for _, info := range targets {
		if info.External && !yes {
			ok, err := confirm(fmt.Sprintf("Workspace %q is outside the canonical Project layout: %s. Delete this directory? [y/N]: ", info.Ref.Handle, info.Path))
			if err != nil || !ok {
				return closed, err
			}
		}
		if force {
			if err := abandonUniqueMutableChanges(repoRoot, info.Ref.Handle); err != nil {
				return closed, err
			}
		}
		if err := commandToStderrFn("jj", "-R", repoRoot, "workspace", "forget", info.Ref.Handle); err != nil {
			return closed, err
		}
		if err := os.RemoveAll(info.Path); err != nil {
			return closed, fmt.Errorf("remove %s: %w", info.Path, err)
		}
		closed = append(closed, info.Path)
	}
	return closed, nil
}

func abandonUniqueMutableChanges(repoRoot, handle string) error {
	refs, err := listWorkspaceRefs(repoRoot)
	if err != nil {
		return err
	}
	var otherAncestors []string
	for _, ref := range refs {
		if ref.Handle != handle {
			otherAncestors = append(otherAncestors, "::"+ref.Handle+"@")
		}
	}
	revset := "mutable() & ::" + handle + "@"
	if len(otherAncestors) > 0 {
		revset += " & ~(" + strings.Join(otherAncestors, " | ") + ")"
	}
	has, err := revisionMatches(repoRoot, revset)
	if err != nil {
		return err
	}
	if !has {
		return nil
	}
	fmt.Fprintf(stderrWriter, "\n== Forced Closing: abandon unique mutable changes for %s ==\n", handle)
	return commandToStderrFn("jj", "-R", repoRoot, "abandon", "-r", revset)
}

func runStackRebase(mainPath string, inputs []string, stack stackConfig) (bool, error) {
	resolvedConflictStrategy, err := resolveStackConflictStrategy(stack.ConflictStrategy)
	if err != nil {
		return false, err
	}
	resolvedMode, reason, err := resolveStackRebaseMode(mainPath, stack.RebaseMode)
	if err != nil {
		return false, err
	}
	resolvedShape, shapeReason, baseDestinations, err := resolveStackShape(mainPath, inputs, stack.Shape)
	if err != nil {
		return false, err
	}
	conflicted, err := runStackRebaseAttempt(mainPath, inputs, resolvedMode, reason, resolvedShape, shapeReason, baseDestinations)
	if err != nil {
		return false, err
	}
	finalConflicted := conflicted
	requestedShape := strings.TrimSpace(strings.ToLower(stack.Shape))
	if requestedShape == "" {
		requestedShape = "auto"
	}
	if resolvedConflictStrategy == "prefer-clean" && conflicted && requestedShape == "auto" {
		alternativeShape := "merge"
		if resolvedShape == "merge" {
			alternativeShape = "linear"
		}
		alternativeResolvedShape, alternativeShapeReason, alternativeDestinations, altErr := resolveStackShape(mainPath, inputs, alternativeShape)
		if altErr == nil {
			fmt.Fprintf(stderrWriter, "\n== Conflict fallback: undo and retry with %s ==\n", alternativeResolvedShape)
			if err := commandToStderrFn("jj", "-R", mainPath, "undo"); err != nil {
				return false, err
			}
			alternativeConflicted, err := runStackRebaseAttempt(mainPath, inputs, resolvedMode, reason, alternativeResolvedShape, alternativeShapeReason, alternativeDestinations)
			if err != nil {
				return false, err
			}
			finalConflicted = alternativeConflicted
			if alternativeConflicted && alternativeResolvedShape == "linear" {
				fmt.Fprintln(stderrWriter, "\n== Both strategies conflicted; keeping merge shape ==")
				if err := commandToStderrFn("jj", "-R", mainPath, "undo"); err != nil {
					return false, err
				}
				mergeShape, mergeReason, mergeDestinations, err := resolveStackShape(mainPath, inputs, "merge")
				if err != nil {
					return false, err
				}
				mergeConflicted, err := runStackRebaseAttempt(mainPath, inputs, resolvedMode, reason, mergeShape, mergeReason, mergeDestinations)
				if err != nil {
					return false, err
				}
				finalConflicted = mergeConflicted
			}
		}
	}
	return finalConflicted, nil
}

func runStackRebaseAttempt(mainPath string, inputs []string, resolvedMode string, modeReason string, resolvedShape string, shapeReason string, baseDestinations []string) (bool, error) {
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
	fmt.Fprintf(stderrWriter, "\n== Stack Inputs: %s ==\n", strings.Join(inputs, ", "))
	if err := commandToStderrFn("jj", cmdArgs...); err != nil {
		return false, err
	}
	conflicted, err := workingCopyHasConflicts(mainPath)
	if err != nil {
		return false, err
	}
	if conflicted {
		fmt.Fprintln(stderrWriter, "\n== Stack result has conflicts ==")
	}
	return conflicted, nil
}

func validateLinearSelection(repoPath string, inputs []string, requested string) error {
	mode := strings.TrimSpace(strings.ToLower(requested))
	if mode != "linear" {
		return nil
	}
	revs := make([]string, 0, len(inputs))
	for _, name := range inputs {
		revs = append(revs, name+"@")
	}
	frontier, err := frontierHeads(repoPath, revs)
	if err != nil {
		return err
	}
	if len(frontier) != 1 {
		return fmt.Errorf("shape linear requires a single frontier head, found %d", len(frontier))
	}
	return nil
}

func resolveStackShape(repoPath string, inputs []string, requested string) (string, string, []string, error) {
	mode := strings.TrimSpace(strings.ToLower(requested))
	if mode == "" {
		mode = "auto"
	}
	inputRevs := make([]string, 0, len(inputs))
	for _, name := range inputs {
		inputRevs = append(inputRevs, name+"@")
	}
	frontier, err := frontierHeads(repoPath, inputRevs)
	if err != nil {
		return "", "", nil, err
	}
	if len(frontier) == 0 {
		return "", "", nil, errors.New("could not resolve Stack Input frontier heads")
	}
	switch mode {
	case "auto":
		if len(frontier) == 1 {
			return "linear", "single frontier head", frontier, nil
		}
		return "merge", fmt.Sprintf("%d frontier heads", len(frontier)), inputRevs, nil
	case "linear":
		if len(frontier) != 1 {
			return "", "", nil, fmt.Errorf("shape linear requires a single frontier head, found %d", len(frontier))
		}
		return "linear", "explicit", frontier, nil
	case "merge":
		return "merge", "explicit", inputRevs, nil
	default:
		return "", "", nil, fmt.Errorf("invalid shape %q (expected auto, linear, or merge)", requested)
	}
}

func resolveStackConflictStrategy(requested string) (string, error) {
	strategy := strings.TrimSpace(strings.ToLower(requested))
	if strategy == "" {
		strategy = "prefer-clean"
	}
	switch strategy {
	case "off", "prefer-clean":
		return strategy, nil
	default:
		return "", fmt.Errorf("invalid conflict_strategy: %q (expected off or prefer-clean)", requested)
	}
}

func validateStackRebaseMode(mode string) error {
	trimmed := strings.TrimSpace(strings.ToLower(mode))
	switch trimmed {
	case "", "auto", "branch", "revision":
		return nil
	default:
		return fmt.Errorf("invalid rebase_mode: %q (expected auto, branch, or revision)", mode)
	}
}

func validateStackShape(shape string) error {
	trimmed := strings.TrimSpace(strings.ToLower(shape))
	switch trimmed {
	case "", "auto", "linear", "merge":
		return nil
	default:
		return fmt.Errorf("invalid stack shape: %q (expected auto, linear, or merge)", shape)
	}
}

func resolveStackRebaseMode(repoPath string, requested string) (string, string, error) {
	mode := strings.TrimSpace(strings.ToLower(requested))
	if mode == "" {
		mode = "auto"
	}
	switch mode {
	case "branch":
		return "branch", "explicit", nil
	case "revision":
		return "revision", "explicit", nil
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
		return "", "", fmt.Errorf("invalid rebase_mode %q (expected auto, branch, or revision)", requested)
	}
}

func hasImmutableAncestors(repoPath string) (bool, error) {
	return revisionMatches(repoPath, "immutable() & ::@ & ~@")
}
func workingCopyHasConflicts(repoPath string) (bool, error) {
	return revisionMatches(repoPath, "conflicts() & @")
}

func revisionMatches(repoPath, revset string) (bool, error) {
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

func revisionIsAncestor(repoPath, ancestor, descendant string) (bool, error) {
	if ancestor == "" || descendant == "" {
		return false, nil
	}
	revset := fmt.Sprintf("%s::%s", ancestor, descendant)
	return revisionMatches(repoPath, revset)
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
	return uniqueNonEmptyStrings(strings.Split(out, "\n")), nil
}

func parentChangeIDsToPreserve(repoPath string, parents []string, destinations []string) ([]string, error) {
	preserved := make([]string, 0, len(parents))
	for _, parent := range uniqueNonEmptyStrings(parents) {
		isAncestor, err := isAncestorOfAny(repoPath, parent, destinations)
		if err != nil {
			return nil, err
		}
		if !isAncestor {
			preserved = append(preserved, parent)
		}
	}
	return preserved, nil
}

func isAncestorOfAny(repoPath string, ancestor string, descendants []string) (bool, error) {
	descendants = uniqueNonEmptyStrings(descendants)
	if ancestor == "" || len(descendants) == 0 {
		return false, nil
	}
	revset := fmt.Sprintf("%s::(%s)", ancestor, strings.Join(descendants, " | "))
	return revisionMatches(repoPath, revset)
}

func abandonTopEmptyMutableAncestors(repoPath string) error {
	revset := "empty() & mutable() & ::@ & ~@"
	hasEmpty, err := revisionMatches(repoPath, revset)
	if err != nil {
		return err
	}
	if !hasEmpty {
		return nil
	}
	fmt.Fprintln(stderrWriter, "\n== Abandon top empty commits ==")
	return commandToStderrFn("jj", "-R", repoPath, "abandon", "-r", revset)
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

func appendUniqueStrings(dst []string, src []string) []string {
	seen := make(map[string]struct{}, len(dst)+len(src))
	out := make([]string, 0, len(dst)+len(src))
	for _, item := range append(append([]string(nil), dst...), src...) {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func normalizeAssimilatedFolders(folders []string) ([]string, error) {
	out := make([]string, 0, len(folders))
	seen := map[string]struct{}{}
	for _, folder := range folders {
		normalized, err := normalizeAssimilatedFolder(folder)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out, nil
}

func normalizeAssimilatedFolder(folder string) (string, error) {
	trimmed := strings.TrimSpace(folder)
	if trimmed == "" {
		return "", errors.New("assimilated_folders contains an empty folder")
	}
	if filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("invalid assimilated_folders entry %q; expected a relative folder path", folder)
	}
	cleaned := filepath.Clean(trimmed)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid assimilated_folders entry %q; expected a relative folder path without traversal", folder)
	}
	for _, part := range strings.Split(cleaned, string(os.PathSeparator)) {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("invalid assimilated_folders entry %q; expected a relative folder path without traversal", folder)
		}
	}
	return cleaned, nil
}

func effectiveAssimilatedFolders(cfg config, project string) []string {
	folders := append([]string(nil), cfg.AssimilatedFolders...)
	if cfg.Projects != nil {
		if projectCfg, ok := cfg.Projects[project]; ok {
			folders = append(folders, projectCfg.AssimilatedFolders...)
		}
	}
	folders, _ = normalizeAssimilatedFolders(folders)
	return folders
}

func mainWorkspaceRoot(repoRoot string) string {
	if root, ok := resolveDefaultWorkspaceRoot(repoRoot); ok {
		return root
	}
	return repoRoot
}

func materializeAssimilatedFolders(mainPath string, workspacePath string, cfg config, project string) error {
	mainPath = filepath.Clean(mainPath)
	workspacePath = filepath.Clean(workspacePath)
	if mainPath == workspacePath {
		return nil
	}
	for _, folder := range effectiveAssimilatedFolders(cfg, project) {
		source := filepath.Join(mainPath, folder)
		st, err := os.Stat(source)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("stat assimilated folder source %s: %w", source, err)
		}
		if !st.IsDir() {
			return fmt.Errorf("assimilated folder source is not a directory: %s", source)
		}
		dest := filepath.Join(workspacePath, folder)
		if err := ensureAssimilatedSymlink(source, dest); err != nil {
			return err
		}
	}
	return nil
}

func ensureAssimilatedSymlink(source string, dest string) error {
	if st, err := os.Lstat(dest); err == nil {
		if st.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("refusing to replace existing Workspace content with assimilated folder symlink: %s", dest)
		}
		target, err := os.Readlink(dest)
		if err != nil {
			return fmt.Errorf("read assimilated folder symlink %s: %w", dest, err)
		}
		if !filepath.IsAbs(target) {
			target = filepath.Clean(filepath.Join(filepath.Dir(dest), target))
		}
		if filepath.Clean(target) != filepath.Clean(source) {
			return fmt.Errorf("refusing to replace existing Workspace symlink %s -> %s with assimilated folder source %s", dest, target, source)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat assimilated folder destination %s: %w", dest, err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create assimilated folder parent: %w", err)
	}
	if err := os.Symlink(source, dest); err != nil {
		return fmt.Errorf("symlink assimilated folder %s -> %s: %w", dest, source, err)
	}
	return nil
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

func deriveProject(repoRoot string) string {
	if defaultRoot, ok := resolveDefaultWorkspaceRoot(repoRoot); ok {
		return filepath.Base(defaultRoot)
	}
	return filepath.Base(repoRoot)
}

func listWorkspaceHandles(repoRoot string) ([]string, error) {
	refs, err := listWorkspaceRefs(repoRoot)
	if err != nil {
		return nil, err
	}
	handles := make([]string, 0, len(refs))
	for _, ref := range refs {
		handles = append(handles, ref.Handle)
	}
	sort.Strings(handles)
	return handles, nil
}

func listWorkspaceRefs(repoRoot string) ([]workspaceRef, error) {
	out, err := commandCaptureFn("jj", "-R", repoRoot, "workspace", "list", "-T", "name ++ \"\\t\" ++ target.change_id().short() ++ \"\\t\" ++ root ++ \"\\n\"")
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
		ref := workspaceRef{Handle: strings.TrimSpace(parts[0])}
		if len(parts) > 1 {
			ref.TargetChange = strings.TrimSpace(parts[1])
		}
		if len(parts) > 2 {
			ref.Root = strings.TrimSpace(parts[2])
		}
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Handle < refs[j].Handle })
	return refs, nil
}

func currentWorkspaceHandle(repoRoot string, refs []workspaceRef) (string, error) {
	cleanRepoRoot := filepath.Clean(repoRoot)
	for _, ref := range refs {
		if strings.TrimSpace(ref.Root) != "" && filepath.Clean(ref.Root) == cleanRepoRoot {
			return ref.Handle, nil
		}
	}
	out, err := commandCaptureFn("jj", "-R", repoRoot, "log", "-r", "@", "--no-graph", "-T", "change_id.short() ++ \"\\n\"")
	if err != nil {
		return "", err
	}
	current := strings.TrimSpace(out)
	for _, ref := range refs {
		if ref.TargetChange == current {
			return ref.Handle, nil
		}
	}
	return "", errors.New("could not detect Current Workspace")
}

func workspacePathForRef(repoRoot string, workspacesRoot string, project string, ref workspaceRef, currentHandle string) string {
	if strings.TrimSpace(ref.Root) != "" {
		return filepath.Clean(ref.Root)
	}
	return workspacePathForHandle(repoRoot, workspacesRoot, project, ref.Handle, currentHandle)
}

func workspacePathForHandle(repoRoot string, workspacesRoot string, project string, handle string, currentHandle string) string {
	if handle == "default" {
		if root, ok := resolveDefaultWorkspaceRoot(repoRoot); ok {
			return root
		}
	}
	if currentHandle != "" && handle == currentHandle {
		return repoRoot
	}
	return filepath.Join(workspacesRoot, project, handle)
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

func confirm(prompt string) (bool, error) {
	fmt.Fprint(stderrWriter, prompt)
	reader := bufio.NewReader(stdinReader)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

func canUseTUI() bool {
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

func isLiteralEmptyDir(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func expandPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if path == "~" {
		home, err := os.UserHomeDir()
		if err == nil {
			return home
		}
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return os.ExpandEnv(path)
}

// Shared Bubble Tea/Lip Gloss selector.
type selectorMode int

const (
	selectorSingle selectorMode = iota
	selectorMulti
)

type selectorItem struct {
	Handle   string
	Path     string
	Status   string
	Markers  string
	Disabled bool
	All      bool
}

type selectorOptions struct {
	Title            string
	Mode             selectorMode
	Items            []selectorItem
	AllDefault       bool
	ForceEnabled     bool
	AllowForceToggle bool
	StackOptions     stackConfig
}

type selectorResult struct {
	Items        []selectorItem
	ForceEnabled bool
	StackOptions stackConfig
}

type selectorModel struct {
	opts     selectorOptions
	cursor   int
	selected map[int]bool
	filter   string
	result   selectorResult
	cancel   bool
	width    int
}

func runSelector(opts selectorOptions) ([]selectorItem, selectorOptions, error) {
	model := selectorModel{opts: opts, selected: map[int]bool{}, width: 100}
	program := tea.NewProgram(model, tea.WithInput(stdinReader), tea.WithOutput(stderrWriter))
	out, err := program.Run()
	if err != nil {
		return nil, opts, err
	}
	m, ok := out.(selectorModel)
	if !ok || m.cancel {
		return nil, opts, nil
	}
	opts.ForceEnabled = m.result.ForceEnabled
	opts.StackOptions = m.result.StackOptions
	return m.result.Items, opts, nil
}

func (m selectorModel) Init() tea.Cmd { return nil }

func (m selectorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.cancel = true
			return m, tea.Quit
		case "q":
			m.cancel = true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.visibleItems())-1 {
				m.cursor++
			}
		case "backspace":
			if len(m.filter) > 0 {
				m.filter = m.filter[:len(m.filter)-1]
				m.cursor = 0
			}
		case " ":
			if m.opts.Mode == selectorMulti {
				visible := m.visibleItems()
				if m.cursor >= 0 && m.cursor < len(visible) {
					idx := visible[m.cursor]
					item := m.opts.Items[idx]
					if !item.Disabled && !item.All {
						m.selected[idx] = !m.selected[idx]
					}
				}
			}
		case "enter":
			return m.submit(), tea.Quit
		case "f":
			if m.opts.AllowForceToggle {
				m.opts.ForceEnabled = !m.opts.ForceEnabled
				for i := range m.opts.Items {
					if m.opts.Items[i].All {
						continue
					}
					if m.opts.ForceEnabled {
						m.opts.Items[i].Disabled = m.opts.Items[i].Status == "missing"
					} else {
						m.opts.Items[i].Disabled = m.opts.Items[i].Status != "empty" && m.opts.Items[i].Status != "stacked"
					}
				}
			}
		case "s":
			m.opts.StackOptions.Shape = cycle(m.opts.StackOptions.Shape, []string{"auto", "linear", "merge"})
		case "r":
			m.opts.StackOptions.RebaseMode = cycle(m.opts.StackOptions.RebaseMode, []string{"auto", "branch", "revision"})
		case "c":
			m.opts.StackOptions.ConflictStrategy = cycle(m.opts.StackOptions.ConflictStrategy, []string{"prefer-clean", "off"})
		default:
			if len(msg.String()) == 1 {
				r := []rune(msg.String())[0]
				if unicode.IsPrint(r) {
					m.filter += msg.String()
					m.cursor = 0
				}
			}
		}
	}
	return m, nil
}

func (m selectorModel) submit() selectorModel {
	visible := m.visibleItems()
	if len(visible) == 0 {
		m.cancel = true
		return m
	}
	if m.cursor >= len(visible) {
		m.cursor = len(visible) - 1
	}
	if m.opts.Mode == selectorSingle {
		idx := visible[m.cursor]
		item := m.opts.Items[idx]
		if item.Disabled {
			m.cancel = true
			return m
		}
		m.result = selectorResult{Items: []selectorItem{item}, ForceEnabled: m.opts.ForceEnabled, StackOptions: m.opts.StackOptions}
		return m
	}
	idx := visible[m.cursor]
	item := m.opts.Items[idx]
	if item.All {
		items := []selectorItem{}
		for _, candidate := range m.opts.Items {
			if !candidate.All && !candidate.Disabled {
				items = append(items, candidate)
			}
		}
		m.result = selectorResult{Items: items, ForceEnabled: m.opts.ForceEnabled, StackOptions: m.opts.StackOptions}
		return m
	}
	if !item.Disabled {
		m.selected[idx] = true
	}
	items := []selectorItem{}
	for selectedIdx, selected := range m.selected {
		if selected {
			items = append(items, m.opts.Items[selectedIdx])
		}
	}
	m.result = selectorResult{Items: items, ForceEnabled: m.opts.ForceEnabled, StackOptions: m.opts.StackOptions}
	return m
}

func (m selectorModel) View() string {
	var b strings.Builder
	styles := selectorStyles()
	fmt.Fprintln(&b, styles.Title.Render(m.opts.Title))
	if m.filter != "" {
		fmt.Fprintln(&b, styles.Help.Render("filter: "+m.filter))
	}
	visible := m.visibleItems()
	cursor := m.cursor
	if cursor >= len(visible) && len(visible) > 0 {
		cursor = len(visible) - 1
	}
	if len(visible) == 0 {
		fmt.Fprintln(&b, styles.Disabled.Render("No matching Workspaces"))
	}
	for row, idx := range visible {
		item := m.opts.Items[idx]
		pointer := "  "
		if row == cursor {
			pointer = "> "
		}
		mark := ""
		if m.opts.Mode == selectorMulti && !item.All {
			mark = "[ ] "
			if m.selected[idx] {
				mark = "[x] "
			}
		}
		line := fmt.Sprintf("%s%s%-14s %-10s %-18s %s", pointer, mark, item.Handle, item.Status, item.Markers, item.Path)
		if item.Disabled {
			line = styles.Disabled.Render(line)
		} else if row == m.cursor {
			line = styles.Selected.Render(line)
		} else {
			line = styleStatus(styles, item.Status, line)
		}
		fmt.Fprintln(&b, line)
	}
	footer := "↑/↓ move  type filter  enter choose  q quit"
	if m.opts.Mode == selectorMulti {
		footer = "↑/↓ move  space toggle  enter submit/all  type filter  q quit"
	}
	if m.opts.AllowForceToggle {
		footer += fmt.Sprintf("  f force:%v", m.opts.ForceEnabled)
	}
	if m.opts.StackOptions.Shape != "" || m.opts.StackOptions.RebaseMode != "" || m.opts.StackOptions.ConflictStrategy != "" {
		footer += fmt.Sprintf("  s shape:%s  r rebase:%s  c conflicts:%s", emptyDefault(m.opts.StackOptions.Shape, "auto"), emptyDefault(m.opts.StackOptions.RebaseMode, "auto"), emptyDefault(m.opts.StackOptions.ConflictStrategy, "prefer-clean"))
	}
	fmt.Fprintln(&b, styles.Help.Render(footer))
	return b.String()
}

func (m selectorModel) visibleItems() []int {
	needle := strings.ToLower(strings.TrimSpace(m.filter))
	var out []int
	for i, item := range m.opts.Items {
		if needle == "" || strings.Contains(strings.ToLower(item.Handle+" "+item.Path+" "+item.Status+" "+item.Markers), needle) {
			out = append(out, i)
		}
	}
	return out
}

func selectorItemsForOpen(infos []workspaceInfo) []selectorItem {
	items := make([]selectorItem, 0, len(infos))
	for _, info := range infos {
		items = append(items, selectorItem{Handle: info.Ref.Handle, Path: info.Path, Status: statusLabel(info), Markers: strings.Join(markers(info), ","), Disabled: info.Missing})
	}
	return items
}

func selectorItemsForClose(infos []workspaceInfo, force bool) []selectorItem {
	items := []selectorItem{}
	for _, info := range infos {
		if info.Main {
			continue
		}
		disabled := info.Missing || (!force && !isClosable(info))
		items = append(items, selectorItem{Handle: info.Ref.Handle, Path: info.Path, Status: statusLabel(info), Markers: strings.Join(markers(info), ","), Disabled: disabled})
	}
	return items
}

func selectorItemsForStack(infos []workspaceInfo) []selectorItem {
	items := []selectorItem{{Handle: "All", Status: "default", Markers: "stack-relevant", All: true}}
	for _, info := range infos {
		if info.Main {
			continue
		}
		items = append(items, selectorItem{Handle: info.Ref.Handle, Path: info.Path, Status: statusLabel(info), Markers: strings.Join(markers(info), ","), Disabled: !isStackRelevant(info)})
	}
	return items
}

type styles struct{ Title, Selected, Disabled, Help, Conflict, Stacked, Empty lipgloss.Style }

func selectorStyles() styles {
	if os.Getenv("NO_COLOR") != "" {
		base := lipgloss.NewStyle()
		return styles{Title: base.Bold(true), Selected: base.Bold(true), Disabled: base.Faint(true), Help: base.Faint(true), Conflict: base, Stacked: base, Empty: base}
	}
	return styles{
		Title:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63")),
		Selected: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212")),
		Disabled: lipgloss.NewStyle().Faint(true),
		Help:     lipgloss.NewStyle().Faint(true),
		Conflict: lipgloss.NewStyle().Foreground(lipgloss.Color("196")),
		Stacked:  lipgloss.NewStyle().Foreground(lipgloss.Color("42")),
		Empty:    lipgloss.NewStyle().Foreground(lipgloss.Color("244")),
	}
}

func styleStatus(s styles, status string, line string) string {
	switch status {
	case "conflict":
		return s.Conflict.Render(line)
	case "stacked":
		return s.Stacked.Render(line)
	case "empty", "missing":
		return s.Empty.Render(line)
	default:
		return line
	}
}

func cycle(current string, values []string) string {
	current = emptyDefault(strings.TrimSpace(strings.ToLower(current)), values[0])
	for i, value := range values {
		if current == value {
			return values[(i+1)%len(values)]
		}
	}
	return values[0]
}

func emptyDefault(value, def string) string {
	if strings.TrimSpace(value) == "" {
		return def
	}
	return value
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
