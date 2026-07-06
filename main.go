package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

// version is the build-time version string. It defaults to "dev" for plain
// `go build`/`go install` without ldflags and is overridden via
// `-ldflags "-X main.version=<v>"` at release/package build time.
var version = "dev"

// Jujutsu (jj) support floor and the version the tool is exercised against.
const (
	jjMinVersion    = "0.20.0"
	jjTestedVersion = "0.42.x"
)

var (
	commandCaptureFn            = runCommandCapture
	commandToStderrFn           = runCommandToStderr
	lookPathFn                  = exec.LookPath
	jjVersionFn                 = defaultJJVersion
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
	AssimilatedPaths   []string                 `yaml:"assimilated_paths"`
	AssimilatedFolders []string                 `yaml:"assimilated_folders,omitempty"`
	Projects           map[string]projectConfig `yaml:"projects"`
	Stack              stackConfig              `yaml:"stack"`
	Create             createSetup              `yaml:"create"`
}

type projectConfig struct {
	AssimilatedPaths   []string `yaml:"assimilated_paths,omitempty"`
	AssimilatedFolders []string `yaml:"assimilated_folders,omitempty"`
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
	Ahead    int
	Behind   int
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
		s := cliStylesForWriter(stderrWriter)
		fmt.Fprintf(stderrWriter, "%s %v\n", s.Danger.Render("ajj:"), err)
		os.Exit(1)
	}
}

func run(args []string) error {
	globalRepo, args, err := extractGlobalRepoFlag(args)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return errors.New("missing command\n\nRun `ajj help` to see available commands.")
	}
	commandArgs := args[1:]
	if commandAcceptsRepoFlag(args[0]) {
		commandRepoFlags := countRepoFlags(commandArgs)
		if globalRepo != "" && commandRepoFlags > 0 {
			return errors.New("provide --repo either before the command or in command options, not both")
		}
		if commandRepoFlags > 1 {
			return errors.New("provide --repo at most once")
		}
		if globalRepo != "" {
			commandArgs = append([]string{"--repo", globalRepo}, commandArgs...)
		}
	}
	if commandNeedsJJ(args[0]) {
		if err := ensureJJ(); err != nil {
			return err
		}
	}
	switch args[0] {
	case "init":
		return runInit(commandArgs)
	case "create":
		return runCreate(commandArgs)
	case "open":
		return runOpen(commandArgs)
	case "list":
		return runList(commandArgs)
	case "main":
		return runMain(commandArgs)
	case "shell-init":
		return runShellInit(commandArgs)
	case "close":
		return runClose(commandArgs)
	case "tidy":
		return runTidy(commandArgs)
	case "stack":
		return runStack(commandArgs)
	case "move-to-main", "catch-up":
		return runMoveToMain(commandArgs)
	case "version", "--version":
		fmt.Fprintln(stdoutWriter, versionString())
		return nil
	case "help", "-h", "--help":
		printUsage(stdoutWriter)
		return nil
	default:
		return fmt.Errorf("unknown command: %s\n\nRun `ajj help` to see available commands.", args[0])
	}
}

func extractGlobalRepoFlag(args []string) (string, []string, error) {
	repo := ""
	seenRepo := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--repo" {
			if seenRepo {
				return "", nil, errors.New("provide --repo at most once")
			}
			if i+1 >= len(args) {
				return "", nil, errors.New("flag --repo requires a value")
			}
			repo = args[i+1]
			if strings.TrimSpace(repo) == "" {
				return "", nil, errors.New("flag --repo requires a value")
			}
			seenRepo = true
			i++
			continue
		}
		if strings.HasPrefix(arg, "--repo=") {
			if seenRepo {
				return "", nil, errors.New("provide --repo at most once")
			}
			repo = strings.TrimPrefix(arg, "--repo=")
			if strings.TrimSpace(repo) == "" {
				return "", nil, errors.New("flag --repo requires a value")
			}
			seenRepo = true
			continue
		}
		return repo, args[i:], nil
	}
	return repo, nil, nil
}

func countRepoFlags(args []string) int {
	count := 0
	for _, arg := range args {
		if arg == "--repo" || strings.HasPrefix(arg, "--repo=") {
			count++
		}
	}
	return count
}

func commandAcceptsRepoFlag(command string) bool {
	switch command {
	case "init", "create", "open", "list", "main", "close", "tidy", "stack", "move-to-main", "catch-up":
		return true
	default:
		return false
	}
}

func printUsage(w io.Writer) {
	s := cliStylesForWriter(w)
	fmt.Fprintln(w, s.Title.Render("ajj — Jujutsu Workspace lifecycle helper"))
	fmt.Fprintln(w)
	fmt.Fprintf(w, "%s %s\n", s.Section.Render("Usage:"), s.Command.Render("ajj [--repo PATH] <command> [options]"))
	fmt.Fprintln(w)
	fmt.Fprintln(w, s.Section.Render("Global options:"))
	fmt.Fprintf(w, "  %s%s\n", paddedStyled(s.Option, "--repo PATH", 18), "run a repo-aware command against this repository root")
	fmt.Fprintln(w)
	fmt.Fprintln(w, s.Section.Render("Setup:"))
	fmt.Fprintf(w, "  %s%s\n", paddedStyled(s.Command, "init", 18), "Create ajj config")
	fmt.Fprintln(w)
	fmt.Fprintln(w, s.Section.Render("Workspace lifecycle:"))
	fmt.Fprintf(w, "  %s%s\n", paddedStyled(s.Command, "create [handle]", 18), "Create a Workspace and print its path")
	fmt.Fprintf(w, "  %s%s\n", paddedStyled(s.Command, "open [handle]", 18), "Open an existing Workspace; with no handle, use the selector")
	fmt.Fprintf(w, "  %s%s\n", paddedStyled(s.Command, "close [handle...]", 18), "Close Workspaces; with no handle, use the selector")
	fmt.Fprintln(w)
	fmt.Fprintln(w, s.Section.Render("Stacking:"))
	fmt.Fprintf(w, "  %s%s\n", paddedStyled(s.Command, "stack [handle...]", 18), "Stack selected Workspaces into the target Workspace; with no handles, use the selector")
	fmt.Fprintf(w, "  %s%s\n", paddedStyled(s.Command, "stack --line [handle...]", 18), "Line Stack selected Workspaces in explicit order")
	fmt.Fprintf(w, "  %s%s\n", paddedStyled(s.Command, "move-to-main [handle...]", 18), "Move selected tidy Workspace cursors up to the Main Workspace line")
	fmt.Fprintln(w)
	fmt.Fprintln(w, s.Section.Render("Inspect and housekeeping:"))
	fmt.Fprintf(w, "  %s%s\n", paddedStyled(s.Command, "list", 18), "List Workspaces with status and markers")
	fmt.Fprintf(w, "  %s%s\n", paddedStyled(s.Command, "main", 18), "Print the Main Workspace path")
	fmt.Fprintf(w, "  %s%s\n", paddedStyled(s.Command, "tidy", 18), "Close tidy Workspaces and remove empty leftover directories")
	fmt.Fprintf(w, "  %s%s\n", paddedStyled(s.Command, "shell-init", 18), "Print shell integration for cd-on-open/main")
	fmt.Fprintf(w, "  %s%s\n", paddedStyled(s.Command, "version", 18), "Print the ajj version (also --version)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, s.Muted.Render("Run `ajj <command> --help` for command-specific options."))
}

type cliStyles struct{ Title, Section, Command, Option, Muted, Success, Warn, Danger, Info, Marker lipgloss.Style }

func cliStylesForWriter(w io.Writer) cliStyles {
	if !canColorWriter(w) {
		return cliStyles{}
	}
	return cliStylesForRenderer(lipgloss.NewRenderer(w))
}

func cliStylesForRenderer(r *lipgloss.Renderer) cliStyles {
	base := r.NewStyle()
	return cliStyles{
		Title:   base.Bold(true).Foreground(lipgloss.Color("63")),
		Section: base.Bold(true).Foreground(lipgloss.Color("75")),
		Command: base.Bold(true).Foreground(lipgloss.Color("212")),
		Option:  base.Foreground(lipgloss.Color("220")),
		Muted:   base.Faint(true),
		Success: base.Foreground(lipgloss.Color("42")),
		Warn:    base.Foreground(lipgloss.Color("214")),
		Danger:  base.Foreground(lipgloss.Color("196")),
		Info:    base.Foreground(lipgloss.Color("81")),
		Marker:  base.Foreground(lipgloss.Color("141")),
	}
}

func paddedStyled(style lipgloss.Style, text string, width int) string {
	padding := width - lipgloss.Width(text)
	if padding < 1 {
		padding = 1
	}
	return style.Render(text) + strings.Repeat(" ", padding)
}

func canColorWriter(w io.Writer) bool {
	return os.Getenv("NO_COLOR") == "" && isTerminalWriter(w)
}

func isTerminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
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
		return false, fmt.Errorf("%w\n\nRun `%s --help` for options.", err, commandNameFromUsage(usage))
	}
	return false, nil
}

func commandNameFromUsage(usage string) string {
	fields := strings.Fields(usage)
	if len(fields) >= 2 {
		return fields[0] + " " + fields[1]
	}
	if len(fields) == 1 {
		return fields[0]
	}
	return "ajj"
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
	s := cliStylesForWriter(w)
	fmt.Fprintf(w, "%s %s\n", s.Section.Render("Usage:"), s.Command.Render(usage))
	if strings.TrimSpace(summary) != "" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, summary)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, s.Section.Render("Options:"))
	fs.VisitAll(func(f *flag.Flag) {
		valueName, usageText := flag.UnquoteUsage(f)
		option := "--" + f.Name
		if valueName != "" {
			option += " " + valueName
		}
		fmt.Fprintf(w, "  %s%s", paddedStyled(s.Option, option, 30), usageText)
		if f.DefValue != "" && f.DefValue != "false" {
			fmt.Fprintf(w, " %s", s.Muted.Render("(default "+strconvQuote(f.DefValue)+")"))
		}
		fmt.Fprintln(w)
	})
	fmt.Fprintln(w)
	fmt.Fprintf(w, "%s %s\n", s.Muted.Render("Tip:"), "most lifecycle commands accept --repo, --project, and --workspaces-root overrides.")
}

func strconvQuote(value string) string { return fmt.Sprintf("%q", value) }

func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	var local, force bool
	var repoRootOverride, workspacesRoot, project string
	fs.BoolVar(&local, "local", false, "write repo-local .ajj/config.yaml instead of global config")
	fs.BoolVar(&force, "force", false, "overwrite existing config")
	fs.StringVar(&repoRootOverride, "repo", "", "repo root override for --local")
	fs.StringVar(&workspacesRoot, "workspaces-root", "", "directory containing Project Workspace folders")
	fs.StringVar(&project, "project", "", "Project slug for local config")
	if handled, err := parseCommandFlags(fs, args, "ajj init [options]", "Create ajj config."); handled || err != nil {
		return err
	}
	cfgPath := ""
	if local {
		repoRoot, err := resolveRepoRoot(repoRootOverride)
		if err != nil {
			return err
		}
		cfgPath = filepath.Join(repoRoot, ".ajj", "config.yaml")
	} else {
		path, ok := globalConfigPath()
		if !ok {
			return errors.New("could not resolve global config path")
		}
		cfgPath = path
	}
	if exists(cfgPath) && !force {
		if !canUseTUI() {
			return fmt.Errorf("config already exists: %s (pass --force to overwrite)", cfgPath)
		}
		ok, err := confirm(fmt.Sprintf("Config already exists: %s. Overwrite it? [y/N]: ", cfgPath))
		if err != nil || !ok {
			return err
		}
	}
	cfg := defaultConfig()
	if strings.TrimSpace(workspacesRoot) == "" {
		if !canUseTUI() {
			return errors.New("init requires --workspaces-root")
		}
		value, err := promptText("Workspaces root", "~/workspaces")
		if err != nil || strings.TrimSpace(value) == "" {
			return err
		}
		workspacesRoot = value
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
	if handled, err := parseCommandFlags(fs, args, "ajj create [handle] [options]", "Create a new Workspace and print its path."); handled || err != nil {
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
			if !canUseTUI() {
				return fmt.Errorf("Workspace %q already exists; use `ajj open %s`", handle, handle)
			}
			ok, err := confirm(fmt.Sprintf("Workspace %q already exists. Open it instead? [y/N]: ", handle))
			if err != nil || !ok {
				return err
			}
			return openExistingWorkspace(repoRoot, cfg, project, handle)
		}
	} else {
		handle, err = chooseAutoHandle(cfg, repoRoot, inUse)
		if err != nil {
			return err
		}
	}
	return createWorkspace(repoRoot, cfg, project, handle, envrc, direnvAllow)
}

func createWorkspace(repoRoot string, cfg config, project string, handle string, envrc bool, direnvAllow bool) error {
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
	if err := materializeAndReportAssimilatedFolders(mainWorkspaceRoot(repoRoot), target, cfg, project); err != nil {
		return err
	}
	if direnvAllow || cfg.Create.DirenvAllow {
		if _, err := lookPathFn("direnv"); err == nil {
			_ = commandToStderrFn("direnv", "allow", target)
		}
	}
	printNavigationPath(target, "create")
	return nil
}

func openExistingWorkspace(repoRoot string, cfg config, project string, handle string) error {
	infos, _, err := loadWorkspaceInfos(repoRoot, cfg, project)
	if err != nil {
		return err
	}
	for _, info := range infos {
		if info.Ref.Handle == handle {
			if info.Missing {
				return fmt.Errorf("Workspace %q path not found: %s", handle, info.Path)
			}
			if err := materializeAndReportAssimilatedFolders(mainWorkspaceRoot(repoRoot), info.Path, cfg, project); err != nil {
				return err
			}
			printNavigationPath(info.Path, "open")
			return nil
		}
	}
	return workspaceNotFoundError(handle)
}

func workspaceNotFoundError(handle string) error {
	return fmt.Errorf("Workspace %q not found; use `ajj create %s` to create it", handle, handle)
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
	if handled, err := parseCommandFlags(fs, args, "ajj open [handle] [options]", "Print an existing Workspace path."); handled || err != nil {
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
				if err := materializeAndReportAssimilatedFolders(mainWorkspaceRoot(repoRoot), info.Path, cfg, project); err != nil {
					return err
				}
				printNavigationPath(info.Path, "open")
				return nil
			}
		}
		if canUseTUI() {
			ok, err := confirm(fmt.Sprintf("Workspace %q does not exist. Create it now? [y/N]: ", handle))
			if err != nil || !ok {
				return err
			}
			return createWorkspace(repoRoot, cfg, project, handle, false, false)
		}
		return workspaceNotFoundError(handle)
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
	if err := materializeAndReportAssimilatedFolders(mainWorkspaceRoot(repoRoot), selected[0].Path, cfg, project); err != nil {
		return err
	}
	printNavigationPath(selected[0].Path, "open")
	return nil
}

func runShellInit(args []string) error {
	fs := flag.NewFlagSet("shell-init", flag.ContinueOnError)
	if handled, err := parseCommandFlags(fs, args, "ajj shell-init [bash|zsh]", "Print shell integration that makes navigation commands cd in the current shell."); handled || err != nil {
		return err
	}
	positionals := fs.Args()
	if len(positionals) > 1 {
		return errors.New("shell-init accepts at most one shell name")
	}
	shellName := ""
	if len(positionals) == 1 {
		shellName = strings.TrimSpace(positionals[0])
	} else {
		shellName = filepath.Base(os.Getenv("SHELL"))
	}
	snippet, err := shellIntegrationSnippet(shellName)
	if err != nil {
		return err
	}
	fmt.Fprint(stdoutWriter, snippet)
	return nil
}

func shellIntegrationSnippet(shellName string) (string, error) {
	switch strings.TrimSpace(shellName) {
	case "bash", "zsh":
		return `# ajj shell integration: source this to make create/open/close/main change directory.
ajj() {
  local out rc cmd
  case "$1" in
    --repo)
      cmd="$3"
      ;;
    --repo=*)
      cmd="$2"
      ;;
    *)
      cmd="$1"
      ;;
  esac
  case "$cmd" in
    create|open|close|main)
      out="$(AJJ_SHELL_WRAPPED=1 command ajj "$@")"
      rc=$?
      if [ $rc -ne 0 ]; then
        return $rc
      fi
      if [ -n "$out" ]; then
        cd "$out" || return
      fi
      ;;
    *)
      command ajj "$@"
      ;;
  esac
}
`, nil
	default:
		return "", fmt.Errorf("unsupported shell %q (expected bash or zsh)", shellName)
	}
}

func printNavigationPath(path string, command string) {
	fmt.Fprintln(stdoutWriter, path)
	maybePrintNavigationHint(command)
}

func maybePrintNavigationHint(command string) {
	if os.Getenv("AJJ_SHELL_WRAPPED") != "" || !canUseTUI() {
		return
	}
	s := cliStylesForWriter(stderrWriter)
	fmt.Fprintf(stderrWriter, "%s %s\n", s.Muted.Render("Tip:"), navigationHint(command, filepath.Base(os.Getenv("SHELL"))))
}

func navigationHint(command string, shellName string) string {
	if shellName != "bash" && shellName != "zsh" {
		shellName = "zsh"
	}
	return fmt.Sprintf("to make `ajj %s` cd automatically, run `eval \"$(ajj shell-init %s)\"` once in your shell startup.", command, shellName)
}

func runList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	var repoRootOverride, projectOverride, rootOverride string
	var pathsOnly bool
	fs.StringVar(&repoRootOverride, "repo", "", "repo root override")
	fs.StringVar(&projectOverride, "project", "", "Project override")
	fs.StringVar(&rootOverride, "workspaces-root", "", "Workspaces root override")
	fs.BoolVar(&pathsOnly, "paths", false, "print Workspace paths only")
	if handled, err := parseCommandFlags(fs, args, "ajj list [options]", "List Workspaces."); handled || err != nil {
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
	if pathsOnly {
		for _, info := range infos {
			fmt.Fprintln(stdoutWriter, info.Path)
		}
		return nil
	}
	humanTable := isTerminalWriter(stdoutWriter)
	color := humanTable && os.Getenv("NO_COLOR") == ""
	rows := listRows(infos, color)
	if humanTable {
		rows = append([]listRow{listHeaderRow()}, rows...)
		for _, line := range formatAlignedListRows(rows) {
			fmt.Fprintln(stdoutWriter, line)
		}
		return nil
	}
	for _, row := range rows {
		fmt.Fprintf(stdoutWriter, "%s\t%s\t%s\t%s\t%s\t%s\n", row.Handle, row.Markers, row.Ahead, row.Behind, row.Action, row.Path)
	}
	return nil
}

func runMain(args []string) error {
	fs := flag.NewFlagSet("main", flag.ContinueOnError)
	var repoRootOverride, projectOverride, rootOverride string
	fs.StringVar(&repoRootOverride, "repo", "", "repo root override")
	fs.StringVar(&projectOverride, "project", "", "Project override")
	fs.StringVar(&rootOverride, "workspaces-root", "", "Workspaces root override")
	if handled, err := parseCommandFlags(fs, args, "ajj main [options]", "Print the Main Workspace path."); handled || err != nil {
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
			printNavigationPath(info.Path, "main")
			return nil
		}
	}
	return fmt.Errorf("Main Workspace %q not found", cfg.MainWorkspace)
}

func runTidy(args []string) error {
	fs := flag.NewFlagSet("tidy", flag.ContinueOnError)
	var repoRootOverride, projectOverride, rootOverride string
	var yes bool
	fs.StringVar(&repoRootOverride, "repo", "", "repo root override")
	fs.StringVar(&projectOverride, "project", "", "Project override")
	fs.StringVar(&rootOverride, "workspaces-root", "", "Workspaces root override")
	fs.BoolVar(&yes, "yes", false, "skip confirmation")
	if handled, err := parseCommandFlags(fs, args, "ajj tidy [options]", "Close Workspaces with no unique content or described commits, then remove empty leftover Workspace directories."); handled || err != nil {
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
	if err := tidyClosableWorkspaces(repoRoot, cfg, project, infos, yes); err != nil {
		return err
	}
	active := make(map[string]struct{}, len(infos))
	for _, info := range infos {
		active[info.Ref.Handle] = struct{}{}
	}
	projectRoot := filepath.Join(cfg.WorkspacesRoot, project)
	entries, err := os.ReadDir(projectRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(stderrWriter, "%s\n", cliStylesForWriter(stderrWriter).Warn.Render(fmt.Sprintf("No Project Workspace directory found at %s.", projectRoot)))
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
			fmt.Fprintf(stderrWriter, "%s\n", cliStylesForWriter(stderrWriter).Warn.Render(fmt.Sprintf("leftover not empty: %s", full)))
			continue
		}
		if err := os.Remove(full); err != nil {
			return fmt.Errorf("remove %s: %w", full, err)
		}
		fmt.Fprintln(stdoutWriter, full)
		deleted++
	}
	if deleted == 0 {
		fmt.Fprintln(stderrWriter, cliStylesForWriter(stderrWriter).Muted.Render("No empty leftover Workspace directories to tidy."))
	}
	return nil
}

func tidyClosableWorkspaces(repoRoot string, cfg config, project string, infos []workspaceInfo, yes bool) error {
	targets := []workspaceInfo{}
	for _, info := range infos {
		if isClosable(info) {
			targets = append(targets, info)
		}
	}
	if len(targets) == 0 {
		fmt.Fprintln(stderrWriter, cliStylesForWriter(stderrWriter).Muted.Render("No Workspaces with no unique content or described commits to tidy."))
		return nil
	}
	fmt.Fprintf(stderrWriter, "%s\n", cliStylesForWriter(stderrWriter).Info.Render("Workspaces with no unique content or described commits: "+workspaceSummary(targets)))
	if !yes {
		ok, err := confirm(fmt.Sprintf("Close %d tidy Workspace(s)? [y/N]: ", len(targets)))
		if err != nil || !ok {
			return err
		}
	}
	mainInfo, ok := mapInfosByHandle(infos)[cfg.MainWorkspace]
	if !ok {
		return fmt.Errorf("Main Workspace %q not found", cfg.MainWorkspace)
	}
	if mainInfo.Missing {
		return fmt.Errorf("Main Workspace path missing: %s", mainInfo.Path)
	}
	if err := abandonEmptyWorkspaceHeads(mainInfo.Path, targets); err != nil {
		return err
	}
	closed, err := closeWorkspaces(mainInfo.Path, targets, false, yes)
	if err != nil {
		return err
	}
	for _, path := range closed {
		fmt.Fprintln(stdoutWriter, path)
	}
	if err := abandonTopEmptyMutableAncestors(mainInfo.Path); err != nil {
		return err
	}
	return commandToStderrFn("jj", "-R", mainInfo.Path, "workspace", "update-stale")
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
	if handled, err := parseCommandFlags(fs, args, "ajj close [handle...] [options]", "Close Workspace(s)."); handled || err != nil {
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
		fmt.Fprintln(stderrWriter, cliStylesForWriter(stderrWriter).Muted.Render("No Workspaces to close."))
		return nil
	}
	unsafeTargets := []workspaceInfo{}
	for _, info := range targets {
		if info.Main {
			return fmt.Errorf("Main Workspace %q is never closable", info.Ref.Handle)
		}
		if info.Missing {
			return fmt.Errorf("Workspace %q path missing: %s", info.Ref.Handle, info.Path)
		}
		if !force && !isClosable(info) {
			unsafeTargets = append(unsafeTargets, info)
		}
	}
	confirmedForce := false
	if len(unsafeTargets) > 0 {
		if !canUseTUI() {
			return fmt.Errorf("%s not normally closable; stack first or run this close with --force", workspaceSummary(unsafeTargets))
		}
		ok, err := confirm(fmt.Sprintf("%s not normally closable. Forced Closing abandons unique mutable changes. Force close instead? [y/N]: ", workspaceSummary(unsafeTargets)))
		if err != nil || !ok {
			return err
		}
		force = true
		confirmedForce = true
	}
	mainInfo, ok := byHandle[cfg.MainWorkspace]
	if !ok {
		return fmt.Errorf("Main Workspace %q not found", cfg.MainWorkspace)
	}
	if mainInfo.Missing {
		return fmt.Errorf("Main Workspace path missing: %s", mainInfo.Path)
	}
	if force && !yes && !confirmedForce {
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
	if _, err := closeWorkspaces(mainInfo.Path, targets, force, yes); err != nil {
		return err
	}
	if err := abandonTopEmptyMutableAncestors(mainInfo.Path); err != nil {
		return err
	}
	printNavigationPath(mainInfo.Path, "close")
	return nil
}

type stackTargetResolution struct {
	Handle         string
	Path           string
	ConfiguredMain string
	Explicit       bool
	FromCurrent    bool
}

func resolveStackTargetWorkspace(repoRoot string, cfg config, project string, workspaceOverride string) (config, []workspaceInfo, map[string]workspaceInfo, stackTargetResolution, error) {
	refs, err := listWorkspaceRefs(repoRoot)
	if err != nil {
		return config{}, nil, nil, stackTargetResolution{}, err
	}
	current := ""
	if detected, err := currentWorkspaceHandle(repoRoot, refs); err == nil {
		current = detected
	} else if strings.Contains(err.Error(), "ambiguous Current Workspace") {
		return config{}, nil, nil, stackTargetResolution{}, err
	}
	configuredMain := cfg.MainWorkspace
	targetHandle := strings.TrimSpace(workspaceOverride)
	resolution := stackTargetResolution{ConfiguredMain: configuredMain}
	if targetHandle != "" {
		if err := validateWorkspaceHandle(targetHandle); err != nil {
			return config{}, nil, nil, stackTargetResolution{}, err
		}
		resolution.Explicit = true
	} else if strings.TrimSpace(current) != "" {
		targetHandle = current
		resolution.FromCurrent = true
	} else {
		targetHandle = configuredMain
	}
	cfg.MainWorkspace = targetHandle
	infos, err := workspaceInfosForRefs(repoRoot, cfg, project, refs, current)
	if err != nil {
		return config{}, nil, nil, stackTargetResolution{}, err
	}
	byHandle := mapInfosByHandle(infos)
	targetInfo, ok := byHandle[targetHandle]
	if !ok {
		return config{}, nil, nil, stackTargetResolution{}, fmt.Errorf("target Workspace %q not found", targetHandle)
	}
	if targetInfo.Missing {
		return config{}, nil, nil, stackTargetResolution{}, fmt.Errorf("target Workspace path missing: %s", targetInfo.Path)
	}
	resolution.Handle = targetHandle
	resolution.Path = targetInfo.Path
	return cfg, infos, byHandle, resolution, nil
}

func printStackTargetResolution(target stackTargetResolution) {
	fmt.Fprintf(stderrWriter, "Stack target workspace: %s (%s)\n", target.Handle, target.Path)
	if target.FromCurrent && target.ConfiguredMain != "" && target.ConfiguredMain != target.Handle {
		fmt.Fprintf(stderrWriter, "Configured main_workspace %s was not used because --repo/cwd resolved to %s.\n", target.ConfiguredMain, target.Handle)
	}
}

func rejectTargetWorkspaceInput(handle string) error {
	return fmt.Errorf("Stack Input %q is the target workspace; pass --workspace to choose a different target workspace", handle)
}

func stackInputProtectedByTarget(info workspaceInfo, target stackTargetResolution) bool {
	return target.FromCurrent && target.ConfiguredMain != "" && target.ConfiguredMain != target.Handle && info.Ref.Handle == target.ConfiguredMain
}

func runMoveToMain(args []string) error {
	var err error
	args, err = normalizePositionalsLast(args, map[string]struct{}{"--repo": {}, "--project": {}, "--workspaces-root": {}})
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("move-to-main", flag.ContinueOnError)
	var repoRootOverride, projectOverride, rootOverride string
	var all, yes bool
	fs.StringVar(&repoRootOverride, "repo", "", "repo root override")
	fs.StringVar(&projectOverride, "project", "", "Project override")
	fs.StringVar(&rootOverride, "workspaces-root", "", "Workspaces root override")
	fs.BoolVar(&all, "all", false, "move all movable Workspaces")
	fs.BoolVar(&yes, "yes", false, "skip confirmation prompts")
	if handled, err := parseCommandFlags(fs, args, "ajj move-to-main [handle...] [options]", "Move selected tidy Workspace cursors to the current Main Workspace line. If the Main Workspace head is empty, selected Workspaces are moved to main@- so they become siblings of the Main Workspace cursor."); handled || err != nil {
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
	infos, _, err := loadWorkspaceInfos(repoRoot, cfg, project)
	if err != nil {
		return err
	}
	byHandle := mapInfosByHandle(infos)
	mainInfo, ok := byHandle[cfg.MainWorkspace]
	if !ok {
		return fmt.Errorf("Main Workspace %q not found", cfg.MainWorkspace)
	}
	if mainInfo.Missing {
		return fmt.Errorf("Main Workspace path missing: %s", mainInfo.Path)
	}
	if mainInfo.Conflict {
		return fmt.Errorf("Main Workspace %q has conflicts; resolve them before moving other Workspaces to it", cfg.MainWorkspace)
	}
	targets := []workspaceInfo{}
	if all {
		for _, info := range infos {
			if isMovableToMain(info) {
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
			if err := validateMoveToMainTarget(info); err != nil {
				return err
			}
			targets = append(targets, info)
		}
	} else {
		if !canUseTUI() {
			return errors.New("move-to-main requires Workspace Handles or --all when not running in a terminal")
		}
		items := selectorItemsForMoveToMain(infos)
		selected, _, err := runSelector(selectorOptions{Title: "Move Workspaces to Main", Mode: selectorMulti, Items: items, MoveToMain: true})
		if err != nil {
			return err
		}
		for _, item := range selected {
			if info, ok := byHandle[item.Handle]; ok {
				targets = append(targets, info)
			}
		}
	}
	targets = uniqueWorkspaceInfos(targets)
	if len(targets) == 0 {
		fmt.Fprintln(stderrWriter, cliStylesForWriter(stderrWriter).Muted.Render("No Workspaces to move to Main."))
		return nil
	}
	destination := moveToMainDestinationRevset(mainInfo)
	if !yes && canUseTUI() {
		ok, err := confirm(moveToMainPrompt(targets, destination))
		if err != nil || !ok {
			fmt.Fprintln(stderrWriter, cliStylesForWriter(stderrWriter).Muted.Render("Move to Main cancelled."))
			return err
		}
	}
	undoOpID, err := currentOperationID(mainInfo.Path)
	if err != nil {
		return fmt.Errorf("record pre-Move operation id: %w", err)
	}
	fmt.Fprintf(stderrWriter, "\n%s\n", stderrHeading("Move Workspaces to %s", destination))
	for _, info := range targets {
		if err := commandToStderrFn("jj", "-R", info.Path, "workspace", "update-stale"); err != nil {
			return fmt.Errorf("update stale Workspace %q before move: %w", info.Ref.Handle, err)
		}
		if err := commandToStderrFn("jj", "-R", info.Path, "new", destination); err != nil {
			return fmt.Errorf("move Workspace %q to Main: %w", info.Ref.Handle, err)
		}
	}
	if err := commandToStderrFn("jj", "-R", mainInfo.Path, "workspace", "update-stale"); err != nil {
		return err
	}
	printStackUndoHint(undoOpID)
	return nil
}

func isMovableToMain(info workspaceInfo) bool {
	return !info.Main && !info.Missing && !info.Conflict && info.Ahead == 0 && info.Behind > 0
}

func validateMoveToMainTarget(info workspaceInfo) error {
	if info.Main {
		return fmt.Errorf("Workspace %q is the Main Workspace", info.Ref.Handle)
	}
	if info.Missing {
		return fmt.Errorf("Workspace %q path missing: %s", info.Ref.Handle, info.Path)
	}
	if info.Conflict {
		return fmt.Errorf("Workspace %q has conflicts; resolve or Stack it before moving to Main", info.Ref.Handle)
	}
	if info.Ahead > 0 {
		return fmt.Errorf("Workspace %q has unique content or described commits; Stack or Line Stack it before moving to Main", info.Ref.Handle)
	}
	if info.Behind == 0 {
		return fmt.Errorf("Workspace %q is already on the Main Workspace line", info.Ref.Handle)
	}
	return nil
}

func moveToMainDestinationRevset(mainInfo workspaceInfo) string {
	if mainInfo.Empty {
		return mainInfo.Ref.Handle + "@-"
	}
	return mainInfo.Ref.Handle + "@"
}

func moveToMainPrompt(targets []workspaceInfo, destination string) string {
	handles := make([]string, 0, len(targets))
	for _, info := range targets {
		handles = append(handles, info.Ref.Handle)
	}
	return fmt.Sprintf("Move %d Workspace(s) to %s: %s. Continue? [y/N]: ", len(targets), destination, strings.Join(handles, ", "))
}

func uniqueWorkspaceInfos(infos []workspaceInfo) []workspaceInfo {
	seen := map[string]bool{}
	out := make([]workspaceInfo, 0, len(infos))
	for _, info := range infos {
		handle := strings.TrimSpace(info.Ref.Handle)
		if handle == "" || seen[handle] {
			continue
		}
		seen[handle] = true
		out = append(out, info)
	}
	return out
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
	var all, yes, lineStack bool
	fs.StringVar(&repoRootOverride, "repo", "", "repo root override")
	fs.StringVar(&projectOverride, "project", "", "Project override")
	fs.StringVar(&rootOverride, "workspaces-root", "", "Workspaces root override")
	fs.StringVar(&workspaceOverride, "workspace", "", "target Workspace handle override (defaults to current Workspace, then main_workspace)")
	fs.StringVar(&rebaseMode, "rebase-mode", "", "advanced: auto, branch, revision")
	fs.StringVar(&shape, "stack-shape", "", "advanced: auto, linear, merge")
	fs.StringVar(&conflictStrategy, "conflict-strategy", "", "advanced: off, prefer-clean")
	fs.BoolVar(&all, "all", false, "stack all stack-relevant Workspaces")
	fs.BoolVar(&lineStack, "line", false, "Line Stack selected Workspaces onto one ordered line")
	fs.BoolVar(&yes, "yes", false, "skip confirmation prompts")
	if handled, err := parseCommandFlags(fs, args, "ajj stack [handle...] [options]", "Stack selected Workspaces into the target Workspace. The target defaults to --workspace, then the current --repo/cwd Workspace, then configured main_workspace; use --line for ordered Line Stacking."); handled || err != nil {
		return err
	}
	positionals := fs.Args()
	if all && len(positionals) > 0 {
		return errors.New("provide either --all or Stack Input Handles, not both")
	}
	if lineStack && all {
		return errors.New("--line cannot be combined with --all")
	}
	if lineStack && (strings.TrimSpace(rebaseMode) != "" || strings.TrimSpace(shape) != "" || strings.TrimSpace(conflictStrategy) != "") {
		return errors.New("--line cannot be combined with --rebase-mode, --stack-shape, or --conflict-strategy")
	}
	repoRoot, cfg, project, err := commandContext(repoRootOverride, projectOverride, rootOverride)
	if err != nil {
		return err
	}
	applyStackOverrides(&cfg, rebaseMode, shape, conflictStrategy)
	cfg, infos, byHandle, target, err := resolveStackTargetWorkspace(repoRoot, cfg, project, workspaceOverride)
	if err != nil {
		return err
	}
	if lineStack {
		return runLineStack(cfg, infos, byHandle, target, positionals, yes)
	}
	inputs := []string{}
	selectorUsed := false
	if all {
		for _, info := range infos {
			if stackInputProtectedByTarget(info, target) {
				continue
			}
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
				return rejectTargetWorkspaceInput(h)
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
		items := selectorItemsForStackWithTarget(infos, target)
		selected, opts, err := runSelector(selectorOptions{Title: "Stack Workspaces", Mode: selectorMulti, Items: items, AllDefault: true, StackOptions: cfg.Stack})
		if err != nil {
			return err
		}
		selectorUsed = true
		cfg.Stack = opts.StackOptions
		for _, item := range selected {
			inputs = append(inputs, item.Handle)
		}
	}
	inputs = uniqueNonEmptyStrings(inputs)
	if len(inputs) == 0 {
		return errors.New("no Stack Inputs selected")
	}
	if shouldConfirmStackPlan(selectorUsed, yes, canUseTUI()) {
		ok, err := confirm(stackPlanPrompt(inputs, cfg.Stack))
		if err != nil || !ok {
			fmt.Fprintln(stderrWriter, cliStylesForWriter(stderrWriter).Muted.Render("Stack cancelled."))
			return err
		}
	}
	mainInfo := byHandle[cfg.MainWorkspace]
	printStackTargetResolution(target)
	undoOpID, err := currentOperationID(mainInfo.Path)
	if err != nil {
		return fmt.Errorf("record pre-Stack operation id: %w", err)
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
	if err := advanceStackInputWorkspaces(mainInfo.Path, inputs); err != nil {
		return err
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
					closed, err := closeWorkspaces(mainInfo.Path, closable, false, true)
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
	printStackUndoHint(undoOpID)
	return nil
}

type lineStackInput struct {
	Handle string
	Role   string
}

type lineStackPayloadRebase struct {
	Handle            string
	SourceRevset      string
	DestinationRevset string
}

type lineStackAdvance struct {
	Handle           string
	Path             string
	Role             string
	InProgress       bool
	InProgressRevset string
}

type lineStackPlan struct {
	Inputs         []lineStackInput
	Payloads       []lineStackInput
	FollowOnly     []lineStackInput
	Excluded       []string
	PayloadRebases []lineStackPayloadRebase
	Advances       []lineStackAdvance
	FinalTip       string
}

func runLineStack(cfg config, infos []workspaceInfo, byHandle map[string]workspaceInfo, target stackTargetResolution, positionals []string, yes bool) error {
	mainInfo, ok := byHandle[cfg.MainWorkspace]
	if !ok {
		return fmt.Errorf("target Workspace %q not found", cfg.MainWorkspace)
	}
	if mainInfo.Missing {
		return fmt.Errorf("target Workspace path missing: %s", mainInfo.Path)
	}
	requested := []lineStackInput{}
	if len(positionals) > 0 {
		for _, h := range positionals {
			requested = append(requested, lineStackInput{Handle: h})
		}
	} else {
		if !canUseTUI() {
			return errors.New("stack --line requires ordered Workspace Handles when not running in a terminal")
		}
		items := selectorItemsForLineStack(infos, target)
		var opts selectorOptions
		var err error
		selected, opts, err := runSelector(selectorOptions{Title: "Line Stack Workspaces", Mode: selectorMulti, Items: items, OrderedSelection: true, AllowRoleToggle: true})
		_ = opts
		if err != nil {
			return err
		}
		requested = lineStackInputsFromSelectorItems(selected)
	}
	if len(requested) == 0 {
		return errors.New("no Line Stack Workspaces selected")
	}
	for _, input := range requested {
		if strings.TrimSpace(input.Handle) == cfg.MainWorkspace {
			return fmt.Errorf("Line Stack Input %q is the target workspace; pass --workspace to choose a different target workspace", input.Handle)
		}
	}
	plan, err := buildLineStackPlan(infos, requested)
	if err != nil {
		return err
	}
	plan, err = resolveLineStackPlan(mainInfo.Path, plan)
	if err != nil {
		return err
	}
	projectedLog, err := lineStackProjectedLogForWriter(mainInfo.Path, plan, stderrWriter)
	if err != nil {
		return fmt.Errorf("build Line Stack projected log: %w", err)
	}
	undoOpID, err := currentOperationID(mainInfo.Path)
	if err != nil {
		return fmt.Errorf("record pre-Line Stack operation id: %w", err)
	}
	printStackTargetResolution(target)
	fmt.Fprintln(stderrWriter, lineStackPlanText(plan, undoOpID, projectedLog))
	if !yes && canUseTUI() {
		ok, err := confirm("Run Line Stack? [y/N]: ")
		if err != nil || !ok {
			fmt.Fprintln(stderrWriter, cliStylesForWriter(stderrWriter).Muted.Render("Line Stack cancelled."))
			return err
		}
	}
	if err := executeLineStackPlan(mainInfo.Path, plan); err != nil {
		printStackUndoHint(undoOpID)
		return err
	}
	printStackUndoHint(undoOpID)
	return nil
}

func selectorItemForLineStackInfo(info workspaceInfo) selectorItem {
	return selectorItem{Handle: info.Ref.Handle, Path: info.Path, Status: statusLabel(info), Markers: strings.Join(markers(info), ","), Role: lineStackRoleForInfo(info), Disabled: info.Missing}
}

func lineStackInputsFromSelectorItems(items []selectorItem) []lineStackInput {
	inputs := make([]lineStackInput, 0, len(items))
	for _, item := range items {
		inputs = append(inputs, lineStackInput{Handle: item.Handle, Role: item.Role})
	}
	return inputs
}

func buildLineStackPlan(infos []workspaceInfo, requested []lineStackInput) (lineStackPlan, error) {
	byHandle := mapInfosByHandle(infos)
	plan := lineStackPlan{}
	selected := map[string]bool{}
	for _, input := range requested {
		handle := strings.TrimSpace(input.Handle)
		if err := validateWorkspaceHandle(handle); err != nil {
			return lineStackPlan{}, err
		}
		if selected[handle] {
			return lineStackPlan{}, fmt.Errorf("duplicate Line Stack Workspace %q", handle)
		}
		info, ok := byHandle[handle]
		if !ok {
			return lineStackPlan{}, fmt.Errorf("Workspace %q not found", handle)
		}
		if info.Missing {
			return lineStackPlan{}, fmt.Errorf("Workspace %q path missing: %s", handle, info.Path)
		}
		role, err := normalizeLineStackRole(input.Role)
		if err != nil {
			return lineStackPlan{}, err
		}
		if role == "" {
			role = lineStackRoleForInfo(info)
		}
		if lineStackRoleForInfo(info) == selectorRoleFollow {
			role = selectorRoleFollow
		}
		if role == selectorRoleFollow && !info.Empty && !info.Stacked {
			return lineStackPlan{}, fmt.Errorf("Line Stack follow-only Workspace %q has unique non-empty commits; select it as payload or omit it", handle)
		}
		normalized := lineStackInput{Handle: handle, Role: role}
		selected[handle] = true
		plan.Inputs = append(plan.Inputs, normalized)
		plan.Advances = append(plan.Advances, lineStackAdvance{Handle: handle, Path: info.Path, Role: role})
		if role == selectorRoleFollow {
			plan.FollowOnly = append(plan.FollowOnly, normalized)
		} else {
			plan.Payloads = append(plan.Payloads, normalized)
		}
	}
	if len(plan.Inputs) == 0 {
		return lineStackPlan{}, errors.New("no Line Stack Workspaces selected")
	}
	if len(plan.Payloads) == 0 {
		return lineStackPlan{}, errors.New("Line Stack requires at least one payload Workspace")
	}
	for _, info := range infos {
		if !selected[info.Ref.Handle] {
			plan.Excluded = append(plan.Excluded, info.Ref.Handle)
		}
	}
	for i := 1; i < len(plan.Payloads); i++ {
		previous := plan.Payloads[i-1].Handle
		current := plan.Payloads[i].Handle
		plan.PayloadRebases = append(plan.PayloadRebases, lineStackPayloadRebase{Handle: current, SourceRevset: lineStackPayloadSourceRevset(current, previous), DestinationRevset: lineStackPayloadDestinationRevset(previous)})
	}
	plan.FinalTip = lineStackPayloadDestinationRevset(plan.Payloads[len(plan.Payloads)-1].Handle)
	return plan, nil
}

func resolveLineStackPlan(repoPath string, plan lineStackPlan) (lineStackPlan, error) {
	inProgressHeads := map[string][]string{}
	for i := range plan.Advances {
		ids, err := revisionChangeIDs(repoPath, lineStackInProgressHeadRevset(plan.Advances[i].Handle))
		if err != nil {
			return lineStackPlan{}, err
		}
		if len(ids) == 0 {
			continue
		}
		plan.Advances[i].InProgress = true
		plan.Advances[i].InProgressRevset = revsetUnion(ids)
		inProgressHeads[plan.Advances[i].Handle] = ids
	}
	for _, advance := range plan.Advances {
		if !advance.InProgress {
			continue
		}
		for _, excluded := range plan.Excluded {
			descendant, err := revisionMatches(repoPath, excluded+"@ & descendants("+advance.InProgressRevset+")")
			if err != nil {
				return lineStackPlan{}, err
			}
			if descendant {
				return lineStackPlan{}, fmt.Errorf("Line Stack would rewrite omitted Workspace %q because it descends from in-progress Workspace %q", excluded, advance.Handle)
			}
		}
	}
	frontiers := map[string][]string{}
	for _, payload := range plan.Payloads {
		frontierRevset := lineStackPayloadDestinationRevsetExcluding(payload.Handle, inProgressHeads[payload.Handle])
		frontier, err := revisionChangeIDs(repoPath, frontierRevset)
		if err != nil {
			return lineStackPlan{}, err
		}
		if len(frontier) == 0 {
			return lineStackPlan{}, fmt.Errorf("Line Stack payload Workspace %q has no non-empty frontier", payload.Handle)
		}
		frontiers[payload.Handle] = frontier
	}
	resolvedRebases := make([]lineStackPayloadRebase, 0, len(plan.PayloadRebases))
	for i := 1; i < len(plan.Payloads); i++ {
		previous := plan.Payloads[i-1].Handle
		current := plan.Payloads[i].Handle
		sourceRevset := lineStackExcludeRevset(lineStackPayloadSourceRevset(current, previous), inProgressHeads[current])
		immutable, err := revisionMatches(repoPath, "immutable() & "+sourceRevset)
		if err != nil {
			return lineStackPlan{}, err
		}
		if immutable {
			return lineStackPlan{}, fmt.Errorf("Line Stack payload Workspace %q has unique immutable commits; rebase or make them mutable before Line Stacking", current)
		}
		for _, excluded := range plan.Excluded {
			descendant, err := revisionMatches(repoPath, excluded+"@ & descendants("+sourceRevset+")")
			if err != nil {
				return lineStackPlan{}, err
			}
			if descendant {
				return lineStackPlan{}, fmt.Errorf("Line Stack would rewrite omitted Workspace %q because it descends from payload Workspace %q", excluded, current)
			}
		}
		sourceIDs, err := revisionChangeIDs(repoPath, sourceRevset)
		if err != nil {
			return lineStackPlan{}, err
		}
		if len(sourceIDs) == 0 {
			return lineStackPlan{}, fmt.Errorf("Line Stack payload Workspace %q has no unique non-empty commits relative to %q", current, previous)
		}
		resolvedRebases = append(resolvedRebases, lineStackPayloadRebase{Handle: current, SourceRevset: revsetUnion(sourceIDs), DestinationRevset: revsetUnion(frontiers[previous])})
	}
	plan.PayloadRebases = resolvedRebases
	plan.FinalTip = revsetUnion(frontiers[plan.Payloads[len(plan.Payloads)-1].Handle])
	return plan, nil
}

func lineStackRoleForInfo(info workspaceInfo) string {
	if !info.Main && (info.Empty || info.Stacked) {
		return selectorRoleFollow
	}
	return selectorRolePayload
}

func normalizeLineStackRole(role string) (string, error) {
	switch strings.TrimSpace(strings.ToLower(role)) {
	case "":
		return "", nil
	case selectorRolePayload:
		return selectorRolePayload, nil
	case selectorRoleFollow, "follow-only":
		return selectorRoleFollow, nil
	default:
		return "", fmt.Errorf("invalid Line Stack role %q (expected payload or follow-only)", role)
	}
}

func lineStackPayloadSourceRevset(handle string, baseHandle string) string {
	return fmt.Sprintf("(::%s@ & ~::%s@ & ~empty())", handle, baseHandle)
}

func lineStackPayloadDestinationRevset(handle string) string {
	return lineStackPayloadDestinationRevsetExcluding(handle, nil)
}

func lineStackPayloadDestinationRevsetExcluding(handle string, excluded []string) string {
	return fmt.Sprintf("heads(%s)", lineStackExcludeRevset(fmt.Sprintf("(::%s@ & ~empty())", handle), excluded))
}

func lineStackInProgressHeadRevset(handle string) string {
	return fmt.Sprintf("(description(\"\") & ~empty() & %s@)", handle)
}

func lineStackExcludeRevset(revset string, excluded []string) string {
	excluded = uniqueNonEmptyStrings(excluded)
	if len(excluded) == 0 {
		return revset
	}
	return fmt.Sprintf("(%s & ~(%s))", revset, revsetUnion(excluded))
}

func lineStackFirstPayloadPreviewRevset(handle string, nextHandle string) string {
	if strings.TrimSpace(nextHandle) == "" {
		return lineStackPayloadDestinationRevset(handle)
	}
	return fmt.Sprintf("(::%s@ & ~::%s@ & ~empty())", handle, nextHandle)
}

func lineStackProjectedLog(repoPath string, plan lineStackPlan) (string, error) {
	return lineStackProjectedLogWithColor(repoPath, plan, false)
}

func lineStackProjectedLogForWriter(repoPath string, plan lineStackPlan, w io.Writer) (string, error) {
	return lineStackProjectedLogWithColor(repoPath, plan, canColorWriter(w))
}

func lineStackProjectedLogWithColor(repoPath string, plan lineStackPlan, color bool) (string, error) {
	if len(plan.Payloads) == 0 {
		return "", nil
	}
	seen := map[string]bool{}
	rows := []lineStackProjectedRow{}
	advancesByHandle := lineStackAdvancesByHandle(plan.Advances)
	for i := len(plan.Payloads) - 1; i >= 0; i-- {
		advance := advancesByHandle[plan.Payloads[i].Handle]
		revset := lineStackExcludeRevset(lineStackProjectedPayloadPreviewRevset(plan.Payloads, i), lineStackInProgressExcludes(advance))
		payloadRows, err := lineStackProjectedRowsWithColor(repoPath, revset, plan.Payloads[i].Handle, color)
		if err != nil {
			return "", err
		}
		if i == 0 && len(payloadRows) == 0 && len(plan.Payloads) > 1 {
			fallbackRevset := lineStackPayloadDestinationRevsetExcluding(plan.Payloads[i].Handle, lineStackInProgressExcludes(advance))
			payloadRows, err = lineStackProjectedRowsWithColor(repoPath, fallbackRevset, plan.Payloads[i].Handle, color)
			if err != nil {
				return "", err
			}
		}
		for _, row := range payloadRows {
			if seen[row.ChangeID] {
				continue
			}
			seen[row.ChangeID] = true
			rows = append(rows, row)
		}
	}
	var b strings.Builder
	for _, line := range lineStackProjectedCursorLinesStyled(plan.Advances, color) {
		fmt.Fprintln(&b, line)
	}
	if len(rows) == 0 {
		fmt.Fprintf(&b, "%s  %s\n", lineStackGraphStyle("│", color), lineStackMutedStyle("(no non-empty payload commits to show)", color))
		return strings.TrimRight(b.String(), "\n"), nil
	}
	for _, row := range rows {
		description := row.RenderedDescription
		if strings.TrimSpace(stripANSI(description)) == "" {
			description = lineStackDescriptionStyle("(no description set)", color)
		} else if color && !strings.Contains(description, "\x1b[") {
			description = lineStackDescriptionStyle(description, color)
		}
		changeID := row.RenderedChangeID
		if strings.TrimSpace(stripANSI(changeID)) == "" {
			changeID = row.ChangeID
		}
		if color && !strings.Contains(changeID, "\x1b[") {
			changeID = lineStackChangeIDStyle(changeID, color)
		}
		fmt.Fprintf(&b, "%s  %s %s\n", lineStackGraphStyle("○", color), changeID, description)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func lineStackProjectedPayloadPreviewRevset(payloads []lineStackInput, index int) string {
	if index <= 0 {
		next := ""
		if len(payloads) > 1 {
			next = payloads[1].Handle
		}
		return lineStackFirstPayloadPreviewRevset(payloads[0].Handle, next)
	}
	return lineStackPayloadSourceRevset(payloads[index].Handle, payloads[index-1].Handle)
}

type lineStackProjectedRow struct {
	ChangeID            string
	Description         string
	RenderedChangeID    string
	RenderedDescription string
	Handle              string
}

func lineStackProjectedRows(repoPath string, revset string, handle string) ([]lineStackProjectedRow, error) {
	return lineStackProjectedRowsWithColor(repoPath, revset, handle, false)
}

func lineStackProjectedRowsWithColor(repoPath string, revset string, handle string, color bool) ([]lineStackProjectedRow, error) {
	args := []string{"-R", repoPath, "--ignore-working-copy"}
	if color {
		args = append(args, "--color=always")
	} else {
		args = append(args, "--color=never")
	}
	args = append(args, "log", "-r", revset, "--no-graph", "-T", "change_id.short() ++ \"\\t\" ++ description.first_line() ++ \"\\n\"")
	out, err := commandCaptureFn("jj", args...)
	if err != nil {
		return nil, err
	}
	rows := []lineStackProjectedRow{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		renderedChangeID := strings.TrimSpace(parts[0])
		row := lineStackProjectedRow{ChangeID: strings.TrimSpace(stripANSI(renderedChangeID)), RenderedChangeID: renderedChangeID, Handle: handle}
		if len(parts) > 1 {
			row.RenderedDescription = strings.TrimSpace(parts[1])
			row.Description = strings.TrimSpace(stripANSI(row.RenderedDescription))
		}
		if row.ChangeID != "" {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func lineStackProjectedCursorLines(advances []lineStackAdvance) []string {
	return lineStackProjectedCursorLinesStyled(advances, false)
}

func lineStackProjectedCursorLinesStyled(advances []lineStackAdvance, color bool) []string {
	if len(advances) == 0 {
		return []string{lineStackCursorStyle("@", color) + "  " + lineStackMutedStyle("<planned Workspace cursor>", color)}
	}
	lines := make([]string, 0, len(advances)*2-1)
	for i, advance := range advances {
		label := lineStackProjectedCursorLabelStyled(advance, color)
		if i == 0 {
			lines = append(lines, lineStackCursorStyle("@", color)+"  "+label)
			continue
		}
		lines = append(lines, lineStackGraphStyle("│", color)+" "+lineStackGraphStyle("○", color)+"  "+label)
		lines = append(lines, lineStackGraphStyle("├─╯", color))
	}
	return lines
}

func lineStackProjectedCursorLabel(advance lineStackAdvance) string {
	return lineStackProjectedCursorLabelStyled(advance, false)
}

func lineStackProjectedCursorLabelStyled(advance lineStackAdvance, color bool) string {
	if advance.InProgress {
		id := strings.TrimSpace(advance.InProgressRevset)
		if id == "" {
			id = advance.Handle + "@"
		}
		return fmt.Sprintf("%s %s %s", lineStackChangeIDStyle(id, color), lineStackWorkspaceStyle(advance.Handle+"@", color), lineStackMutedStyle("(in-progress; will rebase on top)", color))
	}
	return fmt.Sprintf("%s %s", lineStackWorkspaceStyle(advance.Handle+"@", color), lineStackMutedStyle("(planned empty cursor)", color))
}

var ansiEscapeRE = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

func stripANSI(s string) string {
	return ansiEscapeRE.ReplaceAllString(s, "")
}

func lineStackGraphStyle(s string, color bool) string {
	return lineStackANSI256(s, color, 7)
}

func lineStackCursorStyle(s string, color bool) string {
	return lineStackANSI256(s, color, 10)
}

func lineStackWorkspaceStyle(s string, color bool) string {
	return lineStackANSI256(s, color, 13)
}

func lineStackChangeIDStyle(s string, color bool) string {
	return lineStackANSI256(s, color, 5)
}

func lineStackDescriptionStyle(s string, color bool) string {
	return lineStackANSI256(s, color, 10)
}

func lineStackMutedStyle(s string, color bool) string {
	return lineStackANSI256(s, color, 8)
}

func lineStackANSI256(s string, color bool, code int) string {
	if !color || s == "" {
		return s
	}
	return fmt.Sprintf("\x1b[38;5;%dm%s\x1b[39m", code, s)
}

func lineStackAdvancesByHandle(advances []lineStackAdvance) map[string]lineStackAdvance {
	byHandle := map[string]lineStackAdvance{}
	for _, advance := range advances {
		byHandle[advance.Handle] = advance
	}
	return byHandle
}

func lineStackInProgressExcludes(advance lineStackAdvance) []string {
	if !advance.InProgress || strings.TrimSpace(advance.InProgressRevset) == "" {
		return nil
	}
	return []string{advance.InProgressRevset}
}

func lineStackPlanText(plan lineStackPlan, undoOpID string, projectedLog string) string {
	var b strings.Builder
	fmt.Fprintln(&b, "Line Stack preview")
	if strings.TrimSpace(projectedLog) != "" {
		fmt.Fprintln(&b, "Projected jj log after Line Stack:")
		for _, line := range strings.Split(strings.TrimRight(projectedLog, "\n"), "\n") {
			fmt.Fprintln(&b, line)
		}
	}
	fmt.Fprintln(&b, "Options:")
	fmt.Fprintln(&b, "  mode: line")
	fmt.Fprintln(&b, "Inputs:")
	for i, input := range plan.Inputs {
		fmt.Fprintf(&b, "  %d. %s (%s)\n", i+1, input.Handle, lineStackRoleLabel(input.Role))
	}
	fmt.Fprintln(&b, "Payload rebases:")
	if len(plan.PayloadRebases) == 0 {
		fmt.Fprintln(&b, "  (none; single payload Workspace)")
	}
	for _, op := range plan.PayloadRebases {
		fmt.Fprintf(&b, "  %s payload: %s -> %s\n", op.Handle, op.SourceRevset, op.DestinationRevset)
	}
	fmt.Fprintln(&b, "Follow-only advances:")
	if len(plan.FollowOnly) == 0 {
		fmt.Fprintln(&b, "  (none)")
	} else {
		for _, input := range plan.FollowOnly {
			fmt.Fprintf(&b, "  %s@ -> %s\n", input.Handle, plan.FinalTip)
		}
	}
	fmt.Fprintln(&b, "In-progress Workspace rebases:")
	inProgressAdvanceCount := 0
	for _, advance := range plan.Advances {
		if advance.InProgress {
			inProgressAdvanceCount++
			fmt.Fprintf(&b, "  %s@ -> %s\n", advance.Handle, plan.FinalTip)
		}
	}
	if inProgressAdvanceCount == 0 {
		fmt.Fprintln(&b, "  (none)")
	}
	fmt.Fprintln(&b, "Payload Workspace head advances:")
	payloadAdvanceCount := 0
	for _, advance := range plan.Advances {
		if advance.Role != selectorRoleFollow && !advance.InProgress {
			payloadAdvanceCount++
			fmt.Fprintf(&b, "  %s@ -> %s\n", advance.Handle, plan.FinalTip)
		}
	}
	if payloadAdvanceCount == 0 {
		fmt.Fprintln(&b, "  (none)")
	}
	fmt.Fprintln(&b, "Excluded:")
	if len(plan.Excluded) == 0 {
		fmt.Fprintln(&b, "  (none)")
	} else {
		for _, handle := range plan.Excluded {
			fmt.Fprintf(&b, "  %s\n", handle)
		}
	}
	if strings.TrimSpace(undoOpID) != "" {
		fmt.Fprintf(&b, "To undo this run: jj op restore %s\n", undoOpID)
	}
	return strings.TrimRight(b.String(), "\n")
}

func lineStackRoleLabel(role string) string {
	if role == selectorRoleFollow {
		return "follow-only"
	}
	return "payload"
}

func executeLineStackPlan(repoPath string, plan lineStackPlan) error {
	for _, op := range plan.PayloadRebases {
		fmt.Fprintf(stderrWriter, "\n%s\n", stderrHeading("Line Stack payload: %s", op.Handle))
		if err := commandToStderrFn("jj", "-R", repoPath, "rebase", "-r", op.SourceRevset, "-d", op.DestinationRevset); err != nil {
			return err
		}
		conflicted, err := revisionMatches(repoPath, "conflicts() & "+op.SourceRevset)
		if err != nil {
			return err
		}
		if conflicted {
			return fmt.Errorf("Line Stacking stopped with conflicts in Workspace %q; resolve conflicts manually or undo with the printed operation id", op.Handle)
		}
	}
	fmt.Fprintf(stderrWriter, "\n%s\n", stderrHeading("Advance Line Stack Workspaces"))
	for _, advance := range plan.Advances {
		if err := commandToStderrFn("jj", "-R", advance.Path, "workspace", "update-stale"); err != nil {
			return fmt.Errorf("update stale Workspace %q before Line Stack advance: %w", advance.Handle, err)
		}
		if advance.InProgress {
			if err := commandToStderrFn("jj", "-R", advance.Path, "rebase", "-r", advance.Handle+"@", "-d", plan.FinalTip); err != nil {
				return fmt.Errorf("rebase in-progress Workspace %q to Line Stack tip: %w", advance.Handle, err)
			}
		} else if err := commandToStderrFn("jj", "-R", advance.Path, "new", plan.FinalTip); err != nil {
			return fmt.Errorf("advance Workspace %q to Line Stack tip: %w", advance.Handle, err)
		}
		conflicted, err := workspaceHasConflictCommits(repoPath, advance.Handle)
		if err != nil {
			return err
		}
		if conflicted {
			return fmt.Errorf("Line Stacking stopped with conflicts in Workspace %q; resolve conflicts manually or undo with the printed operation id", advance.Handle)
		}
	}
	return commandToStderrFn("jj", "-R", repoPath, "workspace", "update-stale")
}

func shouldConfirmStackPlan(selectorUsed bool, yes bool, canUseTUI bool) bool {
	return selectorUsed && !yes && canUseTUI
}

func stackPlanPrompt(inputs []string, stack stackConfig) string {
	return fmt.Sprintf(
		"Stack %d Workspaces: %s. Options: shape:%s rebase:%s conflicts:%s. Continue? [y/N]: ",
		len(inputs),
		strings.Join(inputs, ", "),
		emptyDefault(stack.Shape, "auto"),
		emptyDefault(stack.RebaseMode, "auto"),
		emptyDefault(stack.ConflictStrategy, "prefer-clean"),
	)
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
		HandleStrategy:   strategyFirstUnused,
		WorkspaceHandles: append([]string(nil), defaultWorkspaceHandles...),
		MainWorkspace:    "default",
		AssimilatedPaths: []string{},
		Projects:         map[string]projectConfig{},
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
	localRoots := []string{repoRoot}
	if defaultRoot, ok := resolveDefaultWorkspaceRoot(repoRoot); ok && filepath.Clean(defaultRoot) != filepath.Clean(repoRoot) {
		localRoots = []string{defaultRoot, repoRoot}
	}
	for _, root := range localRoots {
		localPath := filepath.Join(root, ".ajj", "config.yaml")
		if err := mergeConfigFile(&merged, localPath); err != nil {
			return config{}, err
		}
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
	paths, err := normalizeAssimilatedPaths(appendUniqueStrings(merged.AssimilatedPaths, merged.AssimilatedFolders))
	if err != nil {
		return config{}, err
	}
	merged.AssimilatedPaths = paths
	merged.AssimilatedFolders = nil
	if merged.Projects == nil {
		merged.Projects = map[string]projectConfig{}
	}
	for project, projectCfg := range merged.Projects {
		if err := validateSlug("project", project); err != nil {
			return config{}, err
		}
		paths, err := normalizeAssimilatedPaths(appendUniqueStrings(projectCfg.AssimilatedPaths, projectCfg.AssimilatedFolders))
		if err != nil {
			return config{}, err
		}
		projectCfg.AssimilatedPaths = paths
		projectCfg.AssimilatedFolders = nil
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
	if len(src.AssimilatedPaths) > 0 {
		dst.AssimilatedPaths = appendUniqueStrings(dst.AssimilatedPaths, src.AssimilatedPaths)
	}
	if len(src.AssimilatedFolders) > 0 {
		dst.AssimilatedPaths = appendUniqueStrings(dst.AssimilatedPaths, src.AssimilatedFolders)
	}
	if len(src.Projects) > 0 {
		if dst.Projects == nil {
			dst.Projects = map[string]projectConfig{}
		}
		for project, projectCfg := range src.Projects {
			current := dst.Projects[project]
			current.AssimilatedPaths = appendUniqueStrings(current.AssimilatedPaths, projectCfg.AssimilatedPaths)
			current.AssimilatedPaths = appendUniqueStrings(current.AssimilatedPaths, projectCfg.AssimilatedFolders)
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
		return errors.New("workspaces_root is required; set it in ~/.config/ajj/config.yaml or .ajj/config.yaml, pass --workspaces-root, or run `ajj init`")
	}
	return nil
}

func globalConfigPath() (string, bool) {
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		return filepath.Join(expandPath(xdg), "ajj", "config.yaml"), true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	return filepath.Join(home, ".config", "ajj", "config.yaml"), true
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
		return "", errors.New("workspace_handles is empty; configure handles in ~/.config/ajj/config.yaml or .ajj/config.yaml")
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
	infos, err := workspaceInfosForRefs(repoRoot, cfg, project, refs, current)
	return infos, current, err
}

func workspaceInfosForRefs(repoRoot string, cfg config, project string, refs []workspaceRef, current string) ([]workspaceInfo, error) {
	graphRepoPath := workspaceGraphRepoPath(repoRoot, refs, cfg, project, current)
	infos := make([]workspaceInfo, 0, len(refs))
	var err error
	for _, ref := range refs {
		path := workspacePathForRef(repoRoot, cfg.WorkspacesRoot, project, ref, current)
		info := workspaceInfo{Ref: ref, Path: path, Current: ref.Handle == current, Main: ref.Handle == cfg.MainWorkspace}
		if st, err := os.Stat(path); err != nil || !st.IsDir() {
			info.Missing = true
		}
		canonical := filepath.Clean(filepath.Join(cfg.WorkspacesRoot, project, ref.Handle))
		info.External = !info.Missing && filepath.Clean(path) != canonical && !info.Main
		info.Conflict, err = workspaceHasConflictCommits(graphRepoPath, ref.Handle)
		if err != nil {
			return nil, fmt.Errorf("probe conflict status for Workspace %q from %s: %w", ref.Handle, graphRepoPath, err)
		}
		if !info.Main && cfg.MainWorkspace != "" {
			info.Ahead, err = workspaceAheadCount(graphRepoPath, ref.Handle, cfg.MainWorkspace)
			if err != nil {
				return nil, fmt.Errorf("probe ahead status for Workspace %q from %s: %w", ref.Handle, graphRepoPath, err)
			}
			info.Behind, err = workspaceBehindCount(graphRepoPath, ref.Handle, cfg.MainWorkspace)
			if err != nil {
				return nil, fmt.Errorf("probe behind status for Workspace %q from %s: %w", ref.Handle, graphRepoPath, err)
			}
			if info.Ahead > 0 {
				info.Empty = false
				info.Stacked = false
			} else {
				info.Empty, err = revisionMatches(graphRepoPath, "empty() & "+ref.Handle+"@")
				if err != nil {
					return nil, fmt.Errorf("probe empty status for Workspace %q from %s: %w", ref.Handle, graphRepoPath, err)
				}
				info.Stacked, err = revisionIsAncestor(graphRepoPath, ref.Handle+"@", cfg.MainWorkspace+"@")
				if err != nil {
					return nil, fmt.Errorf("probe stacked status for Workspace %q from %s: %w", ref.Handle, graphRepoPath, err)
				}
			}
		} else {
			info.Empty, err = revisionMatches(graphRepoPath, "empty() & "+ref.Handle+"@")
			if err != nil {
				return nil, fmt.Errorf("probe empty status for Workspace %q from %s: %w", ref.Handle, graphRepoPath, err)
			}
		}
		infos = append(infos, info)
	}
	sortWorkspaceInfos(infos)
	return infos, nil
}

func sortWorkspaceInfos(infos []workspaceInfo) {
	sort.Slice(infos, func(i, j int) bool {
		if infos[i].Main != infos[j].Main {
			return infos[i].Main
		}
		return infos[i].Ref.Handle < infos[j].Ref.Handle
	})
}

func workspaceGraphRepoPath(repoRoot string, refs []workspaceRef, cfg config, project string, currentHandle string) string {
	if strings.TrimSpace(cfg.MainWorkspace) != "" {
		for _, ref := range refs {
			if ref.Handle == cfg.MainWorkspace {
				path := workspacePathForRef(repoRoot, cfg.WorkspacesRoot, project, ref, currentHandle)
				if workspacePathExists(path) {
					return path
				}
			}
		}
	}
	return repoRoot
}

func workspaceHasConflictCommits(repoPath, handle string) (bool, error) {
	return revisionMatches(repoPath, "conflicts() & reachable("+handle+"@, mutable())")
}

func workspaceHasUnstackedCommits(repoPath, handle, mainHandle string) (bool, error) {
	return revisionMatches(repoPath, workspaceAheadRevset(handle, mainHandle))
}

func workspaceAheadCount(repoPath, handle, mainHandle string) (int, error) {
	return revisionCount(repoPath, workspaceAheadRevset(handle, mainHandle))
}

func workspaceBehindCount(repoPath, handle, mainHandle string) (int, error) {
	return revisionCount(repoPath, workspaceBehindRevset(handle, mainHandle))
}

func workspaceAheadRevset(handle, mainHandle string) string {
	return "::" + handle + "@ & ~::" + mainHandle + "@ & " + workspaceRelevantRevset()
}

func workspaceBehindRevset(handle, mainHandle string) string {
	return "::" + mainHandle + "@ & ~::" + handle + "@ & " + workspaceRelevantRevset()
}

func workspaceRelevantRevset() string {
	return "~(empty() & description(\"\"))"
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

func workspaceSummary(infos []workspaceInfo) string {
	parts := make([]string, 0, len(infos))
	for _, info := range infos {
		parts = append(parts, fmt.Sprintf("%s (%s)", info.Ref.Handle, statusLabel(info)))
	}
	if len(parts) == 1 {
		return "Workspace " + parts[0]
	}
	return "Workspaces " + strings.Join(parts, ", ")
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

type listRow struct {
	Handle  string
	Markers string
	Ahead   string
	Behind  string
	Action  string
	Path    string
}

func listRows(infos []workspaceInfo, color bool) []listRow {
	rows := make([]listRow, 0, len(infos))
	for _, info := range infos {
		rows = append(rows, listRowForInfo(info, color))
	}
	return rows
}

func listHeaderRow() listRow {
	return listRow{Handle: "workspace", Markers: "markers", Ahead: "ahead", Behind: "behind", Action: "action", Path: "path"}
}

func formatAlignedListRows(rows []listRow) []string {
	widths := [5]int{}
	for _, row := range rows {
		widths[0] = max(widths[0], lipgloss.Width(row.Handle))
		widths[1] = max(widths[1], lipgloss.Width(row.Markers))
		widths[2] = max(widths[2], lipgloss.Width(row.Ahead))
		widths[3] = max(widths[3], lipgloss.Width(row.Behind))
		widths[4] = max(widths[4], lipgloss.Width(row.Action))
	}
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		lines = append(lines, padVisible(row.Handle, widths[0])+"  "+padVisible(row.Markers, widths[1])+"  "+padVisible(row.Ahead, widths[2])+"  "+padVisible(row.Behind, widths[3])+"  "+padVisible(row.Action, widths[4])+"  "+row.Path)
	}
	return lines
}

func padVisible(text string, width int) string {
	padding := width - lipgloss.Width(text)
	if padding < 0 {
		padding = 0
	}
	return text + strings.Repeat(" ", padding)
}

func listRowForInfo(info workspaceInfo, color bool) listRow {
	handle := info.Ref.Handle
	markerText := strings.Join(markers(info), ",")
	ahead := fmt.Sprintf("%d", info.Ahead)
	behind := fmt.Sprintf("%d", info.Behind)
	action := workspaceAction(info)
	path := info.Path
	if !color {
		return listRow{Handle: handle, Markers: markerText, Ahead: ahead, Behind: behind, Action: action, Path: path}
	}
	s := cliStylesForWriter(stdoutWriter)
	if info.Main {
		handle = s.Section.Render(handle)
	} else if info.Current {
		handle = s.Command.Render(handle)
	}
	markerText = s.Marker.Render(markerText)
	action = styleCLIAction(s, action)
	if info.Ahead > 0 {
		ahead = s.Info.Render(ahead)
	}
	if info.Behind > 0 {
		behind = s.Warn.Render(behind)
	}
	if info.Missing || info.External {
		path = s.Warn.Render(path)
	} else {
		path = s.Muted.Render(path)
	}
	return listRow{Handle: handle, Markers: markerText, Ahead: ahead, Behind: behind, Action: action, Path: path}
}

func workspaceAction(info workspaceInfo) string {
	if info.Missing {
		return "missing"
	}
	if info.Conflict {
		return "resolve-conflict"
	}
	if info.Main {
		return "main"
	}
	if isMovableToMain(info) {
		return "move-to-main"
	}
	if info.Ahead == 0 && info.Behind == 0 {
		return "ok"
	}
	if info.Ahead == 0 {
		return "move-to-main"
	}
	if info.Behind == 0 {
		return "stack"
	}
	return "rebase-or-merge"
}

func styleCLIAction(s cliStyles, action string) string {
	switch action {
	case "main":
		return s.Section.Render(action)
	case "ok":
		return s.Success.Render(action)
	case "stack":
		return s.Info.Render(action)
	case "move-to-main", "rebase-or-merge":
		return s.Warn.Render(action)
	case "resolve-conflict", "missing":
		return s.Danger.Render(action)
	default:
		return action
	}
}

func listFields(info workspaceInfo, color bool) (string, string, string, string) {
	handle := info.Ref.Handle
	markerText := strings.Join(markers(info), ",")
	status := statusLabel(info)
	path := info.Path
	if !color {
		return handle, markerText, status, path
	}
	s := cliStylesForWriter(stdoutWriter)
	if info.Main {
		handle = s.Section.Render(handle)
	} else if info.Current {
		handle = s.Command.Render(handle)
	}
	markerText = s.Marker.Render(markerText)
	status = styleCLIStatus(s, status, status)
	if info.Missing || info.External {
		path = s.Warn.Render(path)
	} else {
		path = s.Muted.Render(path)
	}
	return handle, markerText, status, path
}

func styleCLIStatus(s cliStyles, status string, text string) string {
	switch status {
	case "conflict", "missing":
		return s.Danger.Render(text)
	case "stacked":
		return s.Success.Render(text)
	case "empty":
		return s.Muted.Render(text)
	case "unstacked":
		return s.Info.Render(text)
	default:
		return text
	}
}

func stderrHeading(format string, args ...any) string {
	return cliStylesForWriter(stderrWriter).Section.Render("== " + fmt.Sprintf(format, args...) + " ==")
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
		out = append(out, "outside-layout")
	}
	if len(out) == 0 {
		out = append(out, "-")
	}
	return out
}

func closeWorkspaces(repoPath string, targets []workspaceInfo, force bool, yes bool) ([]string, error) {
	closed := []string{}
	externalTargets := []workspaceInfo{}
	for _, info := range targets {
		if info.External {
			externalTargets = append(externalTargets, info)
		}
	}
	if len(externalTargets) > 0 && !yes {
		ok, err := confirm(externalDeletePrompt(externalTargets))
		if err != nil || !ok {
			return closed, err
		}
	}
	for _, info := range targets {
		if force {
			if err := abandonUniqueMutableChanges(repoPath, info.Ref.Handle); err != nil {
				return closed, err
			}
		}
		if err := commandToStderrFn("jj", "-R", repoPath, "workspace", "forget", info.Ref.Handle); err != nil {
			return closed, err
		}
		if err := os.RemoveAll(info.Path); err != nil {
			return closed, fmt.Errorf("remove %s: %w", info.Path, err)
		}
		closed = append(closed, info.Path)
	}
	return closed, nil
}

func externalDeletePrompt(targets []workspaceInfo) string {
	if len(targets) == 1 {
		info := targets[0]
		return fmt.Sprintf("Workspace %q is outside the canonical Project layout: %s. Delete this directory? [y/N]: ", info.Ref.Handle, info.Path)
	}
	handles := make([]string, 0, len(targets))
	for _, info := range targets {
		handles = append(handles, info.Ref.Handle)
	}
	return fmt.Sprintf("%d Workspaces are outside the canonical Project layout: %s. Delete these directories? [y/N]: ", len(targets), strings.Join(handles, ", "))
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
	fmt.Fprintf(stderrWriter, "\n%s\n", stderrHeading("Forced Closing: abandon unique mutable changes for %s", handle))
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
			fmt.Fprintf(stderrWriter, "\n%s\n", stderrHeading("Conflict fallback: undo and retry with %s", alternativeResolvedShape))
			if err := commandToStderrFn("jj", "-R", mainPath, "undo"); err != nil {
				return false, err
			}
			alternativeConflicted, err := runStackRebaseAttempt(mainPath, inputs, resolvedMode, reason, alternativeResolvedShape, alternativeShapeReason, alternativeDestinations)
			if err != nil {
				return false, err
			}
			finalConflicted = alternativeConflicted
			if alternativeConflicted && alternativeResolvedShape == "linear" {
				fmt.Fprintf(stderrWriter, "\n%s\n", stderrHeading("Both strategies conflicted; keeping merge shape"))
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
	fmt.Fprintf(stderrWriter, "\n%s\n", stderrHeading("Stack shape: %s (%s)", resolvedShape, shapeReason))
	fmt.Fprintf(stderrWriter, "\n%s\n", stderrHeading("Rebase mode: %s (%s)", resolvedMode, modeReason))
	fmt.Fprintf(stderrWriter, "\n%s\n", stderrHeading("Stack Inputs: %s", strings.Join(inputs, ", ")))
	if err := commandToStderrFn("jj", cmdArgs...); err != nil {
		return false, err
	}
	conflicted, err := workingCopyHasConflicts(mainPath)
	if err != nil {
		return false, err
	}
	if conflicted {
		fmt.Fprintf(stderrWriter, "\n%s\n", stderrHeading("Stack result has conflicts"))
	}
	return conflicted, nil
}

func validateLinearSelection(repoPath string, inputs []string, requested string) error {
	mode := strings.TrimSpace(strings.ToLower(requested))
	if mode != "linear" {
		return nil
	}
	revs := stackInputPayloadRevsets(inputs)
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
	inputRevs := stackInputPayloadRevsets(inputs)
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

func stackInputPayloadRevsets(inputs []string) []string {
	revs := make([]string, 0, len(inputs))
	for _, name := range inputs {
		revs = append(revs, stackInputPayloadRevset(name))
	}
	return revs
}

func stackInputPayloadRevset(handle string) string {
	return handle + "@-"
}

func advanceStackInputWorkspaces(mainPath string, inputs []string) error {
	inputs = uniqueNonEmptyStrings(inputs)
	if len(inputs) == 0 {
		return nil
	}
	fmt.Fprintf(stderrWriter, "\n%s\n", stderrHeading("Advance Stack Input Workspaces"))
	for _, handle := range inputs {
		if err := commandToStderrFn("jj", "-R", mainPath, "rebase", "-r", handle+"@", "-d", "@"); err != nil {
			return fmt.Errorf("advance Workspace %q onto Main: %w", handle, err)
		}
	}
	return nil
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
	count, err := revisionCount(repoPath, revset)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func revisionCount(repoPath, revset string) (int, error) {
	ids, err := revisionChangeIDs(repoPath, revset)
	if err != nil {
		return 0, err
	}
	return len(ids), nil
}

func revisionChangeIDs(repoPath, revset string) ([]string, error) {
	out, err := commandCaptureFn("jj", "-R", repoPath, "--ignore-working-copy", "log", "-r", revset, "--no-graph", "-T", "change_id.short() ++ \"\\n\"")
	if err != nil {
		return nil, err
	}
	return uniqueNonEmptyStrings(strings.Split(out, "\n")), nil
}

func revsetUnion(revs []string) string {
	revs = uniqueNonEmptyStrings(revs)
	if len(revs) == 0 {
		return "none()"
	}
	if len(revs) == 1 {
		return revs[0]
	}
	return "(" + strings.Join(revs, " | ") + ")"
}

func currentOperationID(repoPath string) (string, error) {
	out, err := commandCaptureFn("jj", "-R", repoPath, "--ignore-working-copy", "--at-op=@", "op", "log", "-n", "1", "--no-graph", "-T", "id.short() ++ \"\\n\"")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(out)
	if id == "" {
		return "", errors.New("current operation id is empty")
	}
	return id, nil
}

func printStackUndoHint(opID string) {
	opID = strings.TrimSpace(opID)
	if opID == "" {
		return
	}
	fmt.Fprintf(stderrWriter, "\n%s %s\n", cliStylesForWriter(stderrWriter).Muted.Render("To undo this run:"), "jj op restore "+opID)
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

func abandonEmptyWorkspaceHeads(repoPath string, infos []workspaceInfo) error {
	revs := []string{}
	for _, info := range infos {
		if info.Empty {
			revs = append(revs, info.Ref.Handle+"@")
		}
	}
	revs = uniqueNonEmptyStrings(revs)
	if len(revs) == 0 {
		return nil
	}
	revset := "empty() & mutable() & (" + strings.Join(revs, " | ") + ")"
	hasEmpty, err := revisionMatches(repoPath, revset)
	if err != nil {
		return err
	}
	if !hasEmpty {
		return nil
	}
	fmt.Fprintf(stderrWriter, "\n%s\n", stderrHeading("Abandon empty Workspace heads"))
	return commandToStderrFn("jj", "-R", repoPath, "abandon", "-r", revset)
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
	fmt.Fprintf(stderrWriter, "\n%s\n", stderrHeading("Abandon top empty commits"))
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

func normalizeAssimilatedPaths(paths []string) ([]string, error) {
	out := make([]string, 0, len(paths))
	seen := map[string]struct{}{}
	for _, path := range paths {
		normalized, err := normalizeAssimilatedPath(path)
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

func normalizeAssimilatedPath(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", errors.New("assimilated_paths contains an empty path")
	}
	if filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("invalid assimilated_paths entry %q; expected a relative path", path)
	}
	for _, part := range strings.Split(filepath.ToSlash(trimmed), "/") {
		if part == ".." {
			return "", fmt.Errorf("invalid assimilated_paths entry %q; expected a relative path without traversal", path)
		}
	}
	cleaned := filepath.Clean(trimmed)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid assimilated_paths entry %q; expected a relative path without traversal", path)
	}
	for _, part := range strings.Split(filepath.ToSlash(cleaned), "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("invalid assimilated_paths entry %q; expected a relative path without traversal", path)
		}
		if hasGlobMeta(part) {
			if _, err := pathpkg.Match(part, ""); err != nil {
				return "", fmt.Errorf("invalid assimilated_paths glob entry %q: %w", path, err)
			}
		}
	}
	return cleaned, nil
}

func effectiveAssimilatedPaths(cfg config, project string) []string {
	paths := appendUniqueStrings(cfg.AssimilatedPaths, cfg.AssimilatedFolders)
	if cfg.Projects != nil {
		if projectCfg, ok := cfg.Projects[project]; ok {
			paths = appendUniqueStrings(paths, projectCfg.AssimilatedPaths)
			paths = appendUniqueStrings(paths, projectCfg.AssimilatedFolders)
		}
	}
	paths, _ = normalizeAssimilatedPaths(paths)
	return paths
}

func mainWorkspaceRoot(repoRoot string) string {
	if root, ok := resolveDefaultWorkspaceRoot(repoRoot); ok {
		return root
	}
	return repoRoot
}

type assimilatedSymlink struct {
	Source string
	Dest   string
}

func materializeAndReportAssimilatedFolders(mainPath string, workspacePath string, cfg config, project string) error {
	links, err := materializeAssimilatedFolderSymlinks(mainPath, workspacePath, cfg, project)
	if err != nil {
		return err
	}
	printAssimilatedSymlinks(links)
	return nil
}

func materializeAssimilatedFolders(mainPath string, workspacePath string, cfg config, project string) error {
	_, err := materializeAssimilatedFolderSymlinks(mainPath, workspacePath, cfg, project)
	return err
}

func materializeAssimilatedFolderSymlinks(mainPath string, workspacePath string, cfg config, project string) ([]assimilatedSymlink, error) {
	mainPath = filepath.Clean(mainPath)
	workspacePath = filepath.Clean(workspacePath)
	if mainPath == workspacePath {
		return nil, nil
	}
	paths, err := expandAssimilatedPaths(mainPath, effectiveAssimilatedPaths(cfg, project))
	if err != nil {
		return nil, err
	}
	links := []assimilatedSymlink{}
	symlinkedDirs := []string{}
	for _, path := range paths {
		if isWithinAssimilatedDir(path, symlinkedDirs) {
			continue
		}
		source := filepath.Join(mainPath, path)
		st, err := os.Stat(source)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("stat assimilated path source %s: %w", source, err)
		}
		if !st.IsDir() && !st.Mode().IsRegular() {
			return nil, fmt.Errorf("assimilated path source is not a regular file or directory: %s", source)
		}
		dest := filepath.Join(workspacePath, path)
		created, err := ensureAssimilatedSymlink(source, dest)
		if err != nil {
			return nil, err
		}
		if created {
			links = append(links, assimilatedSymlink{Source: source, Dest: dest})
		}
		if st.IsDir() {
			symlinkedDirs = append(symlinkedDirs, path)
		}
	}
	return links, nil
}

func printAssimilatedSymlinks(links []assimilatedSymlink) {
	for _, link := range links {
		fmt.Fprintf(stderrWriter, "Linked assimilated path: %s -> %s\n", link.Dest, link.Source)
	}
}

func isWithinAssimilatedDir(candidate string, dirs []string) bool {
	candidate = filepath.ToSlash(filepath.Clean(candidate))
	for _, dir := range dirs {
		dir = filepath.ToSlash(filepath.Clean(dir))
		if candidate != dir && strings.HasPrefix(candidate, dir+"/") {
			return true
		}
	}
	return false
}

func expandAssimilatedPaths(mainPath string, configured []string) ([]string, error) {
	out := []string{}
	seen := map[string]struct{}{}
	for _, configuredPath := range configured {
		matches, err := expandAssimilatedPath(mainPath, configuredPath)
		if err != nil {
			return nil, err
		}
		for _, match := range matches {
			if _, ok := seen[match]; ok {
				continue
			}
			seen[match] = struct{}{}
			out = append(out, match)
		}
	}
	return out, nil
}

func expandAssimilatedPath(mainPath string, configuredPath string) ([]string, error) {
	if !hasGlobMeta(configuredPath) {
		return []string{configuredPath}, nil
	}
	matches := []string{}
	if err := filepath.WalkDir(mainPath, func(candidate string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if candidate == mainPath {
			return nil
		}
		rel, err := filepath.Rel(mainPath, candidate)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		ok, err := matchAssimilatedGlob(filepath.ToSlash(configuredPath), rel)
		if err != nil {
			return fmt.Errorf("invalid assimilated_paths glob %q: %w", configuredPath, err)
		}
		if ok {
			matches = append(matches, filepath.FromSlash(rel))
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("expand assimilated_paths glob %q: %w", configuredPath, err)
	}
	sort.Strings(matches)
	return matches, nil
}

func hasGlobMeta(value string) bool {
	return strings.ContainsAny(value, "*?[")
}

func matchAssimilatedGlob(pattern string, name string) (bool, error) {
	patternParts := strings.Split(pattern, "/")
	nameParts := strings.Split(name, "/")
	return matchAssimilatedGlobParts(patternParts, nameParts)
}

func matchAssimilatedGlobParts(patternParts []string, nameParts []string) (bool, error) {
	if len(patternParts) == 0 {
		return len(nameParts) == 0, nil
	}
	if patternParts[0] == "**" {
		ok, err := matchAssimilatedGlobParts(patternParts[1:], nameParts)
		if ok || err != nil {
			return ok, err
		}
		if len(nameParts) == 0 {
			return false, nil
		}
		return matchAssimilatedGlobParts(patternParts, nameParts[1:])
	}
	if len(nameParts) == 0 {
		return false, nil
	}
	ok, err := pathpkg.Match(patternParts[0], nameParts[0])
	if err != nil || !ok {
		return ok, err
	}
	return matchAssimilatedGlobParts(patternParts[1:], nameParts[1:])
}

func ensureAssimilatedSymlink(source string, dest string) (bool, error) {
	if st, err := os.Lstat(dest); err == nil {
		if st.Mode()&os.ModeSymlink == 0 {
			return false, fmt.Errorf("refusing to replace existing Workspace content with assimilated path symlink: %s", dest)
		}
		target, err := os.Readlink(dest)
		if err != nil {
			return false, fmt.Errorf("read assimilated path symlink %s: %w", dest, err)
		}
		if !filepath.IsAbs(target) {
			target = filepath.Clean(filepath.Join(filepath.Dir(dest), target))
		}
		if filepath.Clean(target) != filepath.Clean(source) {
			return false, fmt.Errorf("refusing to replace existing Workspace symlink %s -> %s with assimilated path source %s", dest, target, source)
		}
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("stat assimilated path destination %s: %w", dest, err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return false, fmt.Errorf("create assimilated path parent: %w", err)
	}
	if err := os.Symlink(source, dest); err != nil {
		return false, fmt.Errorf("symlink assimilated path %s -> %s: %w", dest, source, err)
	}
	return true, nil
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
	out, err := commandCaptureFn("jj", "-R", repoRoot, "--ignore-working-copy", "workspace", "list", "-T", "name ++ \"\\t\" ++ target.change_id().short() ++ \"\\t\" ++ root ++ \"\\n\"")
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
			ref.Root = cleanWorkspaceRoot(parts[2])
		}
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Handle < refs[j].Handle })
	return refs, nil
}

func currentWorkspaceHandle(repoRoot string, refs []workspaceRef) (string, error) {
	cleanRepoRoot := filepath.Clean(repoRoot)
	rootMatches := []string{}
	for _, ref := range refs {
		if strings.TrimSpace(ref.Root) != "" && filepath.Clean(ref.Root) == cleanRepoRoot {
			rootMatches = append(rootMatches, ref.Handle)
		}
	}
	if len(rootMatches) == 1 {
		return rootMatches[0], nil
	}
	if len(rootMatches) > 1 {
		return "", fmt.Errorf("ambiguous Current Workspace for %s: %s", repoRoot, strings.Join(rootMatches, ", "))
	}
	out, err := commandCaptureFn("jj", "-R", repoRoot, "log", "-r", "@", "--no-graph", "-T", "change_id.short() ++ \"\\n\"")
	if err != nil {
		return "", err
	}
	current := strings.TrimSpace(out)
	changeMatches := []string{}
	for _, ref := range refs {
		if ref.TargetChange == current {
			changeMatches = append(changeMatches, ref.Handle)
		}
	}
	if len(changeMatches) == 1 {
		return changeMatches[0], nil
	}
	if len(changeMatches) > 1 {
		return "", fmt.Errorf("ambiguous Current Workspace for change %s: %s", current, strings.Join(changeMatches, ", "))
	}
	return "", errors.New("could not detect Current Workspace")
}

func workspacePathForRef(repoRoot string, workspacesRoot string, project string, ref workspaceRef, currentHandle string) string {
	if root := cleanWorkspaceRoot(ref.Root); root != "" {
		return filepath.Clean(root)
	}
	return workspacePathForHandle(repoRoot, workspacesRoot, project, ref.Handle, currentHandle)
}

func cleanWorkspaceRoot(root string) string {
	root = strings.TrimSpace(root)
	if root == "" || isJjTemplateError(root) {
		return ""
	}
	return root
}

func isJjTemplateError(value string) bool {
	return strings.HasPrefix(value, "<Error:") && strings.HasSuffix(value, ">")
}

func workspacePathExists(path string) bool {
	if strings.TrimSpace(path) == "" || isJjTemplateError(strings.TrimSpace(path)) {
		return false
	}
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
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
	path := filepath.Join(repoRoot, ".ajj", "state.json")
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
	dir := filepath.Join(repoRoot, ".ajj")
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
	if canUseTUI() {
		return runConfirmTUI(prompt)
	}
	s := cliStylesForWriter(stderrWriter)
	fmt.Fprint(stderrWriter, s.Warn.Render(prompt))
	reader := bufio.NewReader(stdinReader)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false, err
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

type confirmModel struct {
	prompt string
	yes    bool
	cancel bool
}

func runConfirmTUI(prompt string) (bool, error) {
	model := confirmModel{prompt: strings.TrimSpace(prompt), yes: false}
	program := tea.NewProgram(model, tea.WithInput(stdinReader), tea.WithOutput(stderrWriter))
	out, err := program.Run()
	if err != nil {
		return false, err
	}
	m, ok := out.(confirmModel)
	if !ok || m.cancel {
		return false, nil
	}
	return m.yes, nil
}

func (m confirmModel) Init() tea.Cmd { return nil }

func (m confirmModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			m.cancel = true
			return m, tea.Quit
		case "left", "h", "n":
			m.yes = false
		case "right", "l", "y":
			m.yes = true
		case "tab", " ":
			m.yes = !m.yes
		case "enter":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m confirmModel) View() string {
	s := selectorStyles()
	prompt := strings.TrimSpace(m.prompt)
	prompt = strings.TrimSuffix(prompt, ":")
	prompt = strings.TrimSuffix(prompt, "[y/N]")
	prompt = strings.TrimSpace(prompt)
	noStyle := s.Selected
	yesStyle := s.Help
	if m.yes {
		noStyle = s.Help
		yesStyle = s.Selected
	}
	return fmt.Sprintf("%s\n\n  %s   %s\n\n%s\n", s.Title.Render(prompt), noStyle.Render("No"), yesStyle.Render("Yes"), s.Help.Render("←/→ choose  enter confirm  q cancel"))
}

type textPromptModel struct {
	prompt      string
	placeholder string
	value       string
	cancel      bool
}

func promptText(prompt string, placeholder string) (string, error) {
	if canUseTUI() {
		model := textPromptModel{prompt: prompt, placeholder: placeholder}
		program := tea.NewProgram(model, tea.WithInput(stdinReader), tea.WithOutput(stderrWriter))
		out, err := program.Run()
		if err != nil {
			return "", err
		}
		m, ok := out.(textPromptModel)
		if !ok || m.cancel {
			return "", nil
		}
		return strings.TrimSpace(m.value), nil
	}
	s := cliStylesForWriter(stderrWriter)
	fmt.Fprintf(stderrWriter, "%s ", s.Warn.Render(prompt+":"))
	reader := bufio.NewReader(stdinReader)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func (m textPromptModel) Init() tea.Cmd { return nil }

func (m textPromptModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.cancel = true
			return m, tea.Quit
		case "enter":
			return m, tea.Quit
		case "backspace", "ctrl+h":
			if len(m.value) > 0 {
				runes := []rune(m.value)
				m.value = string(runes[:len(runes)-1])
			}
		default:
			if len(msg.String()) == 1 {
				r := []rune(msg.String())[0]
				if unicode.IsPrint(r) {
					m.value += msg.String()
				}
			}
		}
	}
	return m, nil
}

func (m textPromptModel) View() string {
	s := selectorStyles()
	value := m.value
	if value == "" && m.placeholder != "" {
		value = s.Help.Render(m.placeholder)
	}
	return fmt.Sprintf("%s\n\n  %s\n\n%s\n", s.Title.Render(m.prompt), value, s.Help.Render("type a path  enter accept  esc cancel"))
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

const (
	selectorRolePayload = "payload"
	selectorRoleFollow  = "follow"
)

type selectorItem struct {
	Handle   string
	Path     string
	Status   string
	Markers  string
	Role     string
	Disabled bool
	All      bool
	Selected bool
}

type selectorOptions struct {
	Title            string
	Mode             selectorMode
	Items            []selectorItem
	AllDefault       bool
	ForceEnabled     bool
	AllowForceToggle bool
	OrderedSelection bool
	AllowRoleToggle  bool
	MoveToMain       bool
	StackOptions     stackConfig
}

type selectorResult struct {
	Items        []selectorItem
	ForceEnabled bool
	StackOptions stackConfig
}

type selectorModel struct {
	opts          selectorOptions
	cursor        int
	selected      map[int]bool
	selectedOrder []int
	selectedRoles map[int]string
	filter        string
	result        selectorResult
	cancel        bool
	width         int
}

func runSelector(opts selectorOptions) ([]selectorItem, selectorOptions, error) {
	model := selectorModel{opts: opts, selected: map[int]bool{}, selectedRoles: map[int]string{}, width: 100}
	for i, item := range opts.Items {
		if item.Selected && !item.Disabled && !item.All {
			model.selected[i] = true
			if opts.OrderedSelection {
				model.selectedOrder = append(model.selectedOrder, i)
			}
		}
	}
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
					m.toggleSelection(visible[m.cursor])
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
		case "a":
			if m.opts.AllowRoleToggle {
				visible := m.visibleItems()
				if m.cursor >= 0 && m.cursor < len(visible) {
					m.toggleSelectedRole(visible[m.cursor])
				}
			}
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

func (m *selectorModel) toggleSelection(idx int) {
	if idx < 0 || idx >= len(m.opts.Items) {
		return
	}
	item := m.opts.Items[idx]
	if item.Disabled || item.All {
		return
	}
	if m.selected == nil {
		m.selected = map[int]bool{}
	}
	if m.selected[idx] {
		delete(m.selected, idx)
		delete(m.selectedRoles, idx)
		m.selectedOrder = removeInt(m.selectedOrder, idx)
		return
	}
	m.selected[idx] = true
	if m.opts.OrderedSelection {
		m.selectedOrder = append(m.selectedOrder, idx)
	}
}

func (m *selectorModel) toggleSelectedRole(idx int) {
	if idx < 0 || idx >= len(m.opts.Items) || !m.selected[idx] {
		return
	}
	if m.selectedRoles == nil {
		m.selectedRoles = map[int]string{}
	}
	if m.selectedRole(idx) == selectorRoleFollow {
		m.selectedRoles[idx] = selectorRolePayload
	} else {
		m.selectedRoles[idx] = selectorRoleFollow
	}
}

func (m selectorModel) selectedRole(idx int) string {
	if m.selectedRoles != nil {
		if role := strings.TrimSpace(m.selectedRoles[idx]); role != "" {
			return role
		}
	}
	if idx >= 0 && idx < len(m.opts.Items) {
		if role := strings.TrimSpace(m.opts.Items[idx].Role); role != "" {
			return role
		}
	}
	return selectorRolePayload
}

func removeInt(items []int, value int) []int {
	out := items[:0]
	for _, item := range items {
		if item != value {
			out = append(out, item)
		}
	}
	return out
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
	selectedItems := m.selectedItems()
	if len(selectedItems) > 0 {
		m.result = selectorResult{Items: selectedItems, ForceEnabled: m.opts.ForceEnabled, StackOptions: m.opts.StackOptions}
		return m
	}
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
		m.result = selectorResult{Items: []selectorItem{item}, ForceEnabled: m.opts.ForceEnabled, StackOptions: m.opts.StackOptions}
		return m
	}
	m.result = selectorResult{ForceEnabled: m.opts.ForceEnabled, StackOptions: m.opts.StackOptions}
	return m
}

func (m selectorModel) selectedItems() []selectorItem {
	if m.opts.OrderedSelection {
		items := []selectorItem{}
		seen := map[int]bool{}
		for _, idx := range m.selectedOrder {
			if idx < 0 || idx >= len(m.opts.Items) || seen[idx] {
				continue
			}
			seen[idx] = true
			if m.selected[idx] && !m.opts.Items[idx].Disabled && !m.opts.Items[idx].All {
				item := m.opts.Items[idx]
				item.Role = m.selectedRole(idx)
				items = append(items, item)
			}
		}
		return items
	}
	items := []selectorItem{}
	for idx, item := range m.opts.Items {
		if m.selected[idx] && !item.Disabled && !item.All {
			item.Role = m.selectedRole(idx)
			items = append(items, item)
		}
	}
	return items
}

func (m selectorModel) selectedOrdinal(idx int) int {
	if !m.opts.OrderedSelection {
		return 0
	}
	ordinal := 1
	for _, selectedIdx := range m.selectedOrder {
		if selectedIdx < 0 || selectedIdx >= len(m.opts.Items) || !m.selected[selectedIdx] || m.opts.Items[selectedIdx].Disabled || m.opts.Items[selectedIdx].All {
			continue
		}
		if selectedIdx == idx {
			return ordinal
		}
		ordinal++
	}
	return 0
}

func selectorRoleInitial(role string) string {
	if strings.TrimSpace(role) == selectorRoleFollow {
		return "F"
	}
	return "P"
}

func (m selectorModel) View() string {
	var b strings.Builder
	styles := selectorStyles()
	fmt.Fprintln(&b, styles.Title.Render(m.opts.Title))
	fmt.Fprintln(&b, styles.Help.Render(selectorHint(m.opts)))
	if m.filter != "" {
		fmt.Fprintln(&b, styles.Help.Render("filter: "+m.filter))
	}
	visible := m.visibleItems()
	widths := selectorColumnWidthsForItems(m.opts.Items, visible)
	cursor := m.cursor
	if cursor >= len(visible) && len(visible) > 0 {
		cursor = len(visible) - 1
	}
	if len(visible) == 0 {
		fmt.Fprintln(&b, styles.Disabled.Render("No matching Workspaces"))
	}
	for row, idx := range visible {
		item := m.opts.Items[idx]
		displayItem := item
		pointer := "  "
		if row == cursor {
			pointer = "> "
		}
		mark := ""
		if m.opts.Mode == selectorMulti && !item.All {
			mark = "[ ] "
			if m.selected[idx] {
				if m.opts.OrderedSelection {
					mark = fmt.Sprintf("[%d:%s] ", max(1, m.selectedOrdinal(idx)), selectorRoleInitial(m.selectedRole(idx)))
				} else {
					mark = "[x] "
				}
			}
		}
		line := formatSelectorItemLine(pointer, mark, displayItem, widths)
		if item.Disabled {
			line = styles.Disabled.Render(line)
		} else if row == m.cursor {
			line = styles.Selected.Render(line)
		} else if itemHasMarker(item, "main") {
			line = styles.Main.Render(line)
		} else {
			line = styleStatus(styles, item.Status, line)
		}
		fmt.Fprintln(&b, line)
	}
	fmt.Fprintln(&b, styles.Help.Render(selectorLegend(m.opts)))
	footer := "↑/↓ move  type filter  enter choose  q quit"
	if m.opts.Mode == selectorMulti {
		footer = "↑/↓ move  space toggle  enter submit selected  type filter  q quit"
	}
	if m.opts.AllowForceToggle {
		footer += fmt.Sprintf("  f force:%v", m.opts.ForceEnabled)
	}
	if m.opts.AllowRoleToggle {
		footer += "  a toggle payload/follow-only"
	}
	if m.opts.StackOptions.Shape != "" || m.opts.StackOptions.RebaseMode != "" || m.opts.StackOptions.ConflictStrategy != "" {
		footer += fmt.Sprintf("  s shape:%s  r rebase:%s  c conflicts:%s", emptyDefault(m.opts.StackOptions.Shape, "auto"), emptyDefault(m.opts.StackOptions.RebaseMode, "auto"), emptyDefault(m.opts.StackOptions.ConflictStrategy, "prefer-clean"))
	}
	fmt.Fprintln(&b, styles.Help.Render(footer))
	return b.String()
}

type selectorColumnWidths struct {
	Handle  int
	Status  int
	Markers int
}

const (
	selectorHandleMinWidth  = 14
	selectorStatusMinWidth  = 10
	selectorMarkersMinWidth = 18
)

func selectorColumnWidthsForItems(items []selectorItem, visible []int) selectorColumnWidths {
	widths := selectorColumnWidths{Handle: selectorHandleMinWidth, Status: selectorStatusMinWidth, Markers: selectorMarkersMinWidth}
	for _, idx := range visible {
		if idx < 0 || idx >= len(items) {
			continue
		}
		item := items[idx]
		widths.Handle = max(widths.Handle, lipgloss.Width(item.Handle))
		widths.Status = max(widths.Status, lipgloss.Width(item.Status))
		widths.Markers = max(widths.Markers, lipgloss.Width(item.Markers))
	}
	return widths
}

func formatSelectorItemLine(pointer string, mark string, item selectorItem, widths selectorColumnWidths) string {
	line := pointer + mark + padVisible(item.Handle, widths.Handle) + " " + padVisible(item.Status, widths.Status) + " " + padVisible(item.Markers, widths.Markers)
	if item.Path != "" {
		line += " " + item.Path
	}
	return line
}

func selectorLegend(opts selectorOptions) string {
	if opts.OrderedSelection {
		return "status: selection order defines the line; P = payload, F = follow-only; missing rows are disabled"
	}
	if opts.MoveToMain {
		return "status: only Workspaces with no unique commits and behind Main are selected by default; uncheck any to leave alone"
	}
	if opts.AllDefault {
		return "status: unstacked/conflict = stack-relevant; stacked/empty/missing = shown for context"
	}
	if opts.AllowForceToggle {
		return "status: empty/stacked = closable; unstacked/conflict need Stacking or Forced Closing; missing cannot close"
	}
	return "status: current/main markers show where you are; missing rows cannot be opened"
}

func selectorHint(opts selectorOptions) string {
	if opts.Mode == selectorSingle {
		return "Choose the Workspace to open. Type to filter by handle, status, marker, or path."
	}
	if opts.OrderedSelection {
		return "Choose Line Stacking order. Space selects in order; press a on a selected row to toggle payload/follow-only. The target Workspace is disabled."
	}
	if opts.MoveToMain {
		return "Choose Workspaces to move to the Main Workspace line. Movable rows start checked; press space to leave one alone."
	}
	if opts.AllDefault {
		return "Choose Stack Inputs. The All row submits every stack-relevant Workspace only when no boxes are checked. Disabled rows are shown for context."
	}
	if opts.AllowForceToggle {
		return "Choose Workspaces to close. Press f to preview Forced Closing availability instead of re-running with --force."
	}
	return "Choose Workspaces. Type to filter by handle, status, marker, or path."
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
	return selectorItemsForStackWithTarget(infos, stackTargetResolution{})
}

func selectorItemsForStackWithTarget(infos []workspaceInfo, target stackTargetResolution) []selectorItem {
	items := []selectorItem{{Handle: "All", Status: "default", Markers: "stack-relevant", All: true}}
	for _, info := range infos {
		if info.Main {
			continue
		}
		disabled := !isStackRelevant(info) || stackInputProtectedByTarget(info, target)
		items = append(items, selectorItem{Handle: info.Ref.Handle, Path: info.Path, Status: statusLabel(info), Markers: strings.Join(markers(info), ","), Disabled: disabled})
	}
	return items
}

func selectorItemsForMoveToMain(infos []workspaceInfo) []selectorItem {
	items := make([]selectorItem, 0, len(infos))
	for _, info := range infos {
		if info.Main {
			continue
		}
		movable := isMovableToMain(info)
		items = append(items, selectorItem{Handle: info.Ref.Handle, Path: info.Path, Status: moveToMainStatusLabel(info), Markers: strings.Join(markers(info), ","), Disabled: !movable, Selected: movable})
	}
	return items
}

func moveToMainStatusLabel(info workspaceInfo) string {
	if isMovableToMain(info) {
		return "move-to-main"
	}
	if !info.Main && !info.Missing && !info.Conflict && info.Ahead == 0 && info.Behind == 0 {
		return "up-to-main"
	}
	return statusLabel(info)
}

func selectorItemsForLineStack(infos []workspaceInfo, target stackTargetResolution) []selectorItem {
	items := make([]selectorItem, 0, len(infos))
	for _, info := range infos {
		itemMarkers := markers(info)
		isTarget := target.Handle != "" && info.Ref.Handle == target.Handle
		if isTarget && !markerPresent(itemMarkers, "target") {
			if len(itemMarkers) == 1 && itemMarkers[0] == "-" {
				itemMarkers = []string{"target"}
			} else {
				itemMarkers = append(itemMarkers, "target")
			}
		}
		items = append(items, selectorItem{Handle: info.Ref.Handle, Path: info.Path, Status: statusLabel(info), Markers: strings.Join(itemMarkers, ","), Role: lineStackRoleForInfo(info), Disabled: info.Missing || isTarget})
	}
	return items
}

func markerPresent(markers []string, needle string) bool {
	for _, marker := range markers {
		if marker == needle {
			return true
		}
	}
	return false
}

type styles struct{ Title, Selected, Disabled, Help, Conflict, Stacked, Empty, Missing, Unstacked, Marker, Main lipgloss.Style }

func selectorStyles() styles {
	return selectorStylesForWriter(stderrWriter)
}

func selectorStylesForWriter(w io.Writer) styles {
	return selectorStylesForRenderer(lipgloss.NewRenderer(w), os.Getenv("NO_COLOR") != "")
}

func selectorStylesForRenderer(r *lipgloss.Renderer, noColor bool) styles {
	base := r.NewStyle()
	if noColor {
		return styles{Title: base.Bold(true), Selected: base.Bold(true), Disabled: base.Faint(true), Help: base.Faint(true), Conflict: base, Stacked: base, Empty: base, Missing: base, Unstacked: base, Marker: base, Main: base.Bold(true)}
	}
	return styles{
		Title:     r.NewStyle().Bold(true).Foreground(lipgloss.Color("63")),
		Selected:  r.NewStyle().Bold(true).Foreground(lipgloss.Color("212")),
		Disabled:  r.NewStyle().Faint(true),
		Help:      r.NewStyle().Faint(true),
		Conflict:  r.NewStyle().Foreground(lipgloss.Color("196")),
		Stacked:   r.NewStyle().Foreground(lipgloss.Color("42")),
		Empty:     r.NewStyle().Foreground(lipgloss.Color("244")),
		Missing:   r.NewStyle().Foreground(lipgloss.Color("214")),
		Unstacked: r.NewStyle().Foreground(lipgloss.Color("81")),
		Marker:    r.NewStyle().Foreground(lipgloss.Color("141")),
		Main:      r.NewStyle().Bold(true).Foreground(lipgloss.Color("111")),
	}
}

func itemHasMarker(item selectorItem, marker string) bool {
	for _, part := range strings.Split(item.Markers, ",") {
		if strings.TrimSpace(part) == marker {
			return true
		}
	}
	return false
}

func styleStatus(s styles, status string, line string) string {
	switch status {
	case "conflict":
		return s.Conflict.Render(line)
	case "stacked":
		return s.Stacked.Render(line)
	case "empty":
		return s.Empty.Render(line)
	case "missing":
		return s.Missing.Render(line)
	case "unstacked":
		return s.Unstacked.Render(line)
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

// versionString returns a stable, parseable single-line version identifier.
// When built with `-ldflags "-X main.version=<v>"` it reports that value.
// Otherwise (e.g. `go install ...@v0.1.0`) it falls back to build metadata
// from runtime/debug so installed binaries surface something better than
// bare "dev".
func versionString() string {
	if version != "" && version != "dev" {
		return "ajj " + version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return "ajj " + strings.TrimPrefix(v, "v")
		}
		var rev string
		var dirty bool
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				rev = setting.Value
			case "vcs.modified":
				dirty = setting.Value == "true"
			}
		}
		if rev != "" {
			if len(rev) > 12 {
				rev = rev[:12]
			}
			if dirty {
				rev += "-dirty"
			}
			return "ajj dev (" + rev + ")"
		}
	}
	return "ajj dev"
}

// commandNeedsJJ reports whether a command shells out to jj and therefore
// requires the presence/minimum-version check. Commands like version, help,
// shell-init and init do not touch jj and must not trigger the check.
func commandNeedsJJ(command string) bool {
	switch command {
	case "create", "open", "list", "main", "close", "tidy", "stack", "move-to-main", "catch-up":
		return true
	default:
		return false
	}
}

var (
	jjCheckMu   sync.Mutex
	jjCheckDone bool
	jjCheckErr  error
)

// ensureJJ verifies jj is installed and warns when it is older than the
// documented minimum. It parses `jj --version` at most once and caches the
// result, so repeated calls incur no additional subprocess cost.
func ensureJJ() error {
	jjCheckMu.Lock()
	defer jjCheckMu.Unlock()
	if jjCheckDone {
		return jjCheckErr
	}
	jjCheckDone = true
	jjCheckErr = checkJJ()
	return jjCheckErr
}

// resetJJCheck clears the cached jj check result. Intended for tests.
func resetJJCheck() {
	jjCheckMu.Lock()
	defer jjCheckMu.Unlock()
	jjCheckDone = false
	jjCheckErr = nil
}

func checkJJ() error {
	if _, err := lookPathFn("jj"); err != nil {
		return fmt.Errorf("Jujutsu (jj) is required but was not found on your PATH.\n"+
			"ajj drives jj to manage Workspaces; install it (>= %s), then re-run.\n"+
			"Install instructions: https://github.com/jj-vcs/jj", jjMinVersion)
	}
	out, err := jjVersionFn()
	if err != nil {
		// jj is present but we could not determine its version; proceed
		// rather than blocking on an unexpected --version failure.
		return nil
	}
	got := parseJJVersion(out)
	if got == "" {
		return nil
	}
	if compareVersions(got, jjMinVersion) < 0 {
		s := cliStylesForWriter(stderrWriter)
		fmt.Fprintf(stderrWriter, "%s jj %s is older than the minimum supported version %s "+
			"(tested against jj %s). Some commands may misbehave; please upgrade: "+
			"https://github.com/jj-vcs/jj\n",
			s.Warn.Render("warning:"), got, jjMinVersion, jjTestedVersion)
	}
	return nil
}

// defaultJJVersion runs `jj --version` and returns its raw output.
func defaultJJVersion() (string, error) {
	return runCommandCapture("jj", "--version")
}

var jjVersionRe = regexp.MustCompile(`([0-9]+(?:\.[0-9]+){1,2})`)

// parseJJVersion extracts a dotted numeric version (e.g. "0.42.0") from
// `jj --version` output like "jj 0.42.0". Returns "" if none is found.
func parseJJVersion(out string) string {
	return jjVersionRe.FindString(out)
}

// compareVersions compares two dotted numeric version strings. It returns a
// negative number when a < b, zero when equal, and a positive number when
// a > b. Non-numeric or missing components are treated as 0.
func compareVersions(a, b string) int {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		ai, bi := 0, 0
		if i < len(as) {
			ai, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			bi, _ = strconv.Atoi(bs[i])
		}
		if ai != bi {
			return ai - bi
		}
	}
	return 0
}

func runCommandCapture(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	setCommandWorkingDir(cmd, name, args...)
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
	setCommandWorkingDir(cmd, name, args...)
	cmd.Stdout = stderrWriter
	cmd.Stderr = stderrWriter
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s failed: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

func setCommandWorkingDir(cmd *exec.Cmd, name string, args ...string) {
	if dir := commandWorkingDir(name, args...); dir != "" {
		cmd.Dir = dir
	}
}

func commandWorkingDir(name string, args ...string) string {
	base := filepath.Base(name)
	if base != "jj" && base != "jj.exe" {
		return ""
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-R" || arg == "--repository":
			if i+1 >= len(args) {
				return ""
			}
			return existingCommandDir(args[i+1])
		case strings.HasPrefix(arg, "--repository="):
			return existingCommandDir(strings.TrimPrefix(arg, "--repository="))
		}
	}
	return ""
}

func existingCommandDir(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = expandPath(path)
	if !filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return ""
		}
		path = abs
	}
	path = filepath.Clean(path)
	if st, err := os.Stat(path); err == nil && st.IsDir() {
		return path
	}
	return ""
}
