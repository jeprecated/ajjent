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
		fmt.Fprintf(stderrWriter, "%s %v\n", s.Danger.Render("jjw:"), err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("missing command\n\nRun `jjw help` to see available commands.")
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
	case "shell-init":
		return runShellInit(args[1:])
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
		return fmt.Errorf("unknown command: %s\n\nRun `jjw help` to see available commands.", args[0])
	}
}

func printUsage(w io.Writer) {
	s := cliStylesForWriter(w)
	fmt.Fprintln(w, s.Title.Render("jjw — Jujutsu Workspace lifecycle helper"))
	fmt.Fprintln(w)
	fmt.Fprintf(w, "%s %s\n", s.Section.Render("Usage:"), s.Command.Render("jjw <command> [options]"))
	fmt.Fprintln(w)
	fmt.Fprintln(w, s.Section.Render("Setup:"))
	fmt.Fprintf(w, "  %s%s\n", paddedStyled(s.Command, "init", 18), "Create jjw config")
	fmt.Fprintln(w)
	fmt.Fprintln(w, s.Section.Render("Workspace lifecycle:"))
	fmt.Fprintf(w, "  %s%s\n", paddedStyled(s.Command, "create [handle]", 18), "Create a Workspace and print its path")
	fmt.Fprintf(w, "  %s%s\n", paddedStyled(s.Command, "open [handle]", 18), "Open an existing Workspace; with no handle, use the selector")
	fmt.Fprintf(w, "  %s%s\n", paddedStyled(s.Command, "close [handle...]", 18), "Close Workspaces; with no handle, use the selector")
	fmt.Fprintln(w)
	fmt.Fprintln(w, s.Section.Render("Stacking:"))
	fmt.Fprintf(w, "  %s%s\n", paddedStyled(s.Command, "stack [handle...]", 18), "Stack selected Workspaces into Main; with no handles, use the selector")
	fmt.Fprintln(w)
	fmt.Fprintln(w, s.Section.Render("Inspect and housekeeping:"))
	fmt.Fprintf(w, "  %s%s\n", paddedStyled(s.Command, "list", 18), "List Workspaces with status and markers")
	fmt.Fprintf(w, "  %s%s\n", paddedStyled(s.Command, "main", 18), "Print the Main Workspace path")
	fmt.Fprintf(w, "  %s%s\n", paddedStyled(s.Command, "tidy", 18), "Close tidy Workspaces and remove empty leftover directories")
	fmt.Fprintf(w, "  %s%s\n", paddedStyled(s.Command, "shell-init", 18), "Print shell integration for cd-on-open/main")
	fmt.Fprintln(w)
	fmt.Fprintln(w, s.Muted.Render("Run `jjw <command> --help` for command-specific options."))
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
	return "jjw"
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
		value, err := promptText("Workspaces root", "~/Development/workspaces")
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
			if !canUseTUI() {
				return fmt.Errorf("Workspace %q already exists; use `jjw open %s`", handle, handle)
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
	if err := materializeAssimilatedFolders(mainWorkspaceRoot(repoRoot), target, cfg, project); err != nil {
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
			if err := materializeAssimilatedFolders(mainWorkspaceRoot(repoRoot), info.Path, cfg, project); err != nil {
				return err
			}
			printNavigationPath(info.Path, "open")
			return nil
		}
	}
	return workspaceNotFoundError(handle)
}

func workspaceNotFoundError(handle string) error {
	return fmt.Errorf("Workspace %q not found; use `jjw create %s` to create it", handle, handle)
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
	if err := materializeAssimilatedFolders(mainWorkspaceRoot(repoRoot), selected[0].Path, cfg, project); err != nil {
		return err
	}
	printNavigationPath(selected[0].Path, "open")
	return nil
}

func runShellInit(args []string) error {
	fs := flag.NewFlagSet("shell-init", flag.ContinueOnError)
	if handled, err := parseCommandFlags(fs, args, "jjw shell-init [bash|zsh]", "Print shell integration that makes navigation commands cd in the current shell."); handled || err != nil {
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
		return `# jjw shell integration: source this to make create/open/close/main change directory.
jjw() {
  local out rc
  case "$1" in
    create|open|close|main)
      out="$(JJW_SHELL_WRAPPED=1 command jjw "$@")"
      rc=$?
      if [ $rc -ne 0 ]; then
        return $rc
      fi
      if [ -n "$out" ]; then
        cd "$out" || return
      fi
      ;;
    *)
      command jjw "$@"
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
	if os.Getenv("JJW_SHELL_WRAPPED") != "" || !canUseTUI() {
		return
	}
	s := cliStylesForWriter(stderrWriter)
	fmt.Fprintf(stderrWriter, "%s %s\n", s.Muted.Render("Tip:"), navigationHint(command, filepath.Base(os.Getenv("SHELL"))))
}

func navigationHint(command string, shellName string) string {
	if shellName != "bash" && shellName != "zsh" {
		shellName = "zsh"
	}
	return fmt.Sprintf("to make `jjw %s` cd automatically, run `eval \"$(jjw shell-init %s)\"` once in your shell startup.", command, shellName)
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
	if handled, err := parseCommandFlags(fs, args, "jjw tidy [options]", "Close Workspaces with no unique non-empty commits, then remove empty leftover Workspace directories."); handled || err != nil {
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
		fmt.Fprintln(stderrWriter, cliStylesForWriter(stderrWriter).Muted.Render("No Workspaces with no unique non-empty commits to tidy."))
		return nil
	}
	fmt.Fprintf(stderrWriter, "%s\n", cliStylesForWriter(stderrWriter).Info.Render("Workspaces with no unique non-empty commits: "+workspaceSummary(targets)))
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
	closed, err := closeWorkspaces(repoRoot, cfg, project, targets, false, yes)
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
	if _, err := closeWorkspaces(repoRoot, cfg, project, targets, force, yes); err != nil {
		return err
	}
	if err := abandonTopEmptyMutableAncestors(mainInfo.Path); err != nil {
		return err
	}
	printNavigationPath(mainInfo.Path, "close")
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
	fs.BoolVar(&yes, "yes", false, "skip Stack confirmation and post-Stack Close prompt")
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
	selectorUsed := false
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
	mainInfo, ok := byHandle[cfg.MainWorkspace]
	if !ok {
		return fmt.Errorf("Main Workspace %q not found", cfg.MainWorkspace)
	}
	if mainInfo.Missing {
		return fmt.Errorf("Main Workspace path missing: %s", mainInfo.Path)
	}
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
	printStackUndoHint(undoOpID)
	return nil
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
	graphRepoPath := workspaceGraphRepoPath(repoRoot, refs, cfg, project, current)
	infos := make([]workspaceInfo, 0, len(refs))
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
			return nil, "", fmt.Errorf("probe conflict status for Workspace %q from %s: %w", ref.Handle, graphRepoPath, err)
		}
		if !info.Main && cfg.MainWorkspace != "" {
			info.Ahead, err = workspaceAheadCount(graphRepoPath, ref.Handle, cfg.MainWorkspace)
			if err != nil {
				return nil, "", fmt.Errorf("probe ahead status for Workspace %q from %s: %w", ref.Handle, graphRepoPath, err)
			}
			info.Behind, err = workspaceBehindCount(graphRepoPath, ref.Handle, cfg.MainWorkspace)
			if err != nil {
				return nil, "", fmt.Errorf("probe behind status for Workspace %q from %s: %w", ref.Handle, graphRepoPath, err)
			}
			if info.Ahead > 0 {
				info.Empty = false
				info.Stacked = false
			} else {
				info.Empty, err = revisionMatches(graphRepoPath, "empty() & "+ref.Handle+"@")
				if err != nil {
					return nil, "", fmt.Errorf("probe empty status for Workspace %q from %s: %w", ref.Handle, graphRepoPath, err)
				}
				info.Stacked, err = revisionIsAncestor(graphRepoPath, ref.Handle+"@", cfg.MainWorkspace+"@")
				if err != nil {
					return nil, "", fmt.Errorf("probe stacked status for Workspace %q from %s: %w", ref.Handle, graphRepoPath, err)
				}
			}
		} else {
			info.Empty, err = revisionMatches(graphRepoPath, "empty() & "+ref.Handle+"@")
			if err != nil {
				return nil, "", fmt.Errorf("probe empty status for Workspace %q from %s: %w", ref.Handle, graphRepoPath, err)
			}
		}
		infos = append(infos, info)
	}
	sortWorkspaceInfos(infos)
	return infos, current, nil
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
				if strings.TrimSpace(path) != "" {
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
	return "::" + handle + "@ & ~::" + mainHandle + "@ & ~empty()"
}

func workspaceBehindRevset(handle, mainHandle string) string {
	return "::" + mainHandle + "@ & ~::" + handle + "@ & ~empty()"
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

func closeWorkspaces(repoRoot string, cfg config, project string, targets []workspaceInfo, force bool, yes bool) ([]string, error) {
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
	out, err := commandCaptureFn("jj", "-R", repoPath, "--ignore-working-copy", "log", "-r", revset, "--no-graph", "-T", "change_id.short() ++ \"\\n\"")
	if err != nil {
		return 0, err
	}
	count := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count, nil
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

func materializeAssimilatedFolders(mainPath string, workspacePath string, cfg config, project string) error {
	mainPath = filepath.Clean(mainPath)
	workspacePath = filepath.Clean(workspacePath)
	if mainPath == workspacePath {
		return nil
	}
	paths, err := expandAssimilatedPaths(mainPath, effectiveAssimilatedPaths(cfg, project))
	if err != nil {
		return err
	}
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
			return fmt.Errorf("stat assimilated path source %s: %w", source, err)
		}
		if !st.IsDir() && !st.Mode().IsRegular() {
			return fmt.Errorf("assimilated path source is not a regular file or directory: %s", source)
		}
		dest := filepath.Join(workspacePath, path)
		if err := ensureAssimilatedSymlink(source, dest); err != nil {
			return err
		}
		if st.IsDir() {
			symlinkedDirs = append(symlinkedDirs, path)
		}
	}
	return nil
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

func ensureAssimilatedSymlink(source string, dest string) error {
	if st, err := os.Lstat(dest); err == nil {
		if st.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("refusing to replace existing Workspace content with assimilated path symlink: %s", dest)
		}
		target, err := os.Readlink(dest)
		if err != nil {
			return fmt.Errorf("read assimilated path symlink %s: %w", dest, err)
		}
		if !filepath.IsAbs(target) {
			target = filepath.Clean(filepath.Join(filepath.Dir(dest), target))
		}
		if filepath.Clean(target) != filepath.Clean(source) {
			return fmt.Errorf("refusing to replace existing Workspace symlink %s -> %s with assimilated path source %s", dest, target, source)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat assimilated path destination %s: %w", dest, err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create assimilated path parent: %w", err)
	}
	if err := os.Symlink(source, dest); err != nil {
		return fmt.Errorf("symlink assimilated path %s -> %s: %w", dest, source, err)
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
	items := []selectorItem{}
	for idx, item := range m.opts.Items {
		if m.selected[idx] && !item.Disabled && !item.All {
			items = append(items, item)
		}
	}
	return items
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
	if m.opts.StackOptions.Shape != "" || m.opts.StackOptions.RebaseMode != "" || m.opts.StackOptions.ConflictStrategy != "" {
		footer += fmt.Sprintf("  s shape:%s  r rebase:%s  c conflicts:%s", emptyDefault(m.opts.StackOptions.Shape, "auto"), emptyDefault(m.opts.StackOptions.RebaseMode, "auto"), emptyDefault(m.opts.StackOptions.ConflictStrategy, "prefer-clean"))
	}
	fmt.Fprintln(&b, styles.Help.Render(footer))
	return b.String()
}

func selectorLegend(opts selectorOptions) string {
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
	items := []selectorItem{{Handle: "All", Status: "default", Markers: "stack-relevant", All: true}}
	for _, info := range infos {
		if info.Main {
			continue
		}
		items = append(items, selectorItem{Handle: info.Ref.Handle, Path: info.Path, Status: statusLabel(info), Markers: strings.Join(markers(info), ","), Disabled: !isStackRelevant(info)})
	}
	return items
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
