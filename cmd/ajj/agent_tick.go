package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Agent Tick repos share one operation log across hosts, so every host sees
// every host's Workspaces. Agent Tick names each jj workspace `<host>/<name>`
// and refuses other names, and only its own jj (`tick jj`) can open the repo.
// Ajj keeps its Handles, directories and output unchanged: it adds this host's
// prefix when it names a Workspace to jj, strips it when it reads one back, and
// never sees another host's Workspaces, so it can't close or tidy them.

const agentTickOpStoreType = "agent_tick_op_store"

// agentTickPrefix is this host's workspace name prefix (`<host>/`) once the
// resolved repo is an Agent Tick repo, and "" otherwise.
var agentTickPrefix string

// In an ordinary repo, Agent Tick's jj may still have named some workspaces
// `<host>/<name>`. hostPrefix is this host's prefix there ("" when the host
// name is unusable), and hostPrefixedHandles holds the Handles read back from
// such names, so jjWorkspaceName names them the way jj does.
var (
	hostPrefix          string
	hostPrefixedHandles map[string]bool
)

// configureAgentTick records whether the repo at root is an Agent Tick repo.
// Every repo-aware command resolves its repo through resolveRepoRoot, which
// calls this before any other jj command runs.
func configureAgentTick(root string) error {
	agentTickPrefix, hostPrefix, hostPrefixedHandles = "", "", map[string]bool{}
	if !isAgentTickRepo(root) {
		if host, err := agentTickHost(); err == nil {
			hostPrefix = host + "/"
		}
		return nil
	}
	host, err := agentTickHost()
	if err != nil {
		return err
	}
	agentTickPrefix = host + "/"
	return nil
}

// isAgentTickRepo reports whether the workspace at root belongs to a repo on
// Agent Tick's operation store. A secondary workspace's `.jj/repo` is a file
// naming the repo directory, relative to `.jj`.
func isAgentTickRepo(root string) bool {
	if root == "" {
		return false
	}
	jjDir := filepath.Join(root, ".jj")
	repo := filepath.Join(jjDir, "repo")
	if data, err := os.ReadFile(repo); err == nil {
		target := strings.TrimSpace(string(data))
		if !filepath.IsAbs(target) {
			target = filepath.Join(jjDir, target)
		}
		repo = target
	}
	kind, err := os.ReadFile(filepath.Join(repo, "op_store", "type"))
	return err == nil && strings.TrimSpace(string(kind)) == agentTickOpStoreType
}

// nearestJJWorkspace walks up from dir to the nearest jj workspace root, as
// `jj root` does, so the `jj root` that finds it already runs the right jj.
func nearestJJWorkspace(dir string) string {
	for {
		if st, err := os.Stat(filepath.Join(dir, ".jj")); err == nil && st.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// agentTickHost is Agent Tick's host name: `TICK_HOST`, else the lowercased
// first label of the hostname.
func agentTickHost() (string, error) {
	name := os.Getenv("TICK_HOST")
	if name == "" {
		var err error
		if name, err = os.Hostname(); err != nil {
			return "", fmt.Errorf("Agent Tick repo: cannot read the host name: %w", err)
		}
	}
	label := strings.ToLower(strings.SplitN(name, ".", 2)[0])
	if label == "" || strings.Contains(label, "/") {
		return "", fmt.Errorf("Agent Tick repo: unusable host name %q", name)
	}
	return label, nil
}

// jjWorkspaceName is the jj workspace name for a Handle.
func jjWorkspaceName(handle string) string {
	if hostPrefixedHandles[handle] {
		return hostPrefix + handle
	}
	return agentTickPrefix + handle
}

// handleFromJJWorkspaceName is the Handle for a jj workspace name, and false
// for another host's workspace in an Agent Tick repo. In an ordinary repo it
// strips this host's prefix and remembers that it did; other names, including
// other hosts' `<host>/<name>`, pass through unchanged.
func handleFromJJWorkspaceName(name string) (string, bool) {
	if agentTickPrefix == "" {
		if handle, ok := strings.CutPrefix(name, hostPrefix); hostPrefix != "" && ok && handle != "" {
			hostPrefixedHandles[handle] = true
			return handle, true
		}
		return name, true
	}
	handle, ok := strings.CutPrefix(name, agentTickPrefix)
	return handle, ok && handle != ""
}

// jjProgram is the program and leading arguments that run jj: `tick jj` in an
// Agent Tick repo, whatever `name` is otherwise.
func jjProgram(name string, args []string) (string, []string) {
	if agentTickPrefix == "" {
		return name, args
	}
	if base := filepath.Base(name); base != "jj" && base != "jj.exe" {
		return name, args
	}
	return "tick", append([]string{"jj"}, args...)
}

var errAgentTickNeedsTick = errors.New("this is an Agent Tick repo, which only Agent Tick's jj can open, and `tick` is not on PATH; install the agent-tick package")

// checkAgentTickTick fails clearly when an Agent Tick repo has no `tick` to
// open it.
func checkAgentTickTick() error {
	if agentTickPrefix == "" {
		return nil
	}
	if _, err := lookPathFn("tick"); err != nil {
		return errAgentTickNeedsTick
	}
	return nil
}

// splitWorkspaceListLine splits one tab-separated `jj workspace list` line
// whose first field is the workspace name, putting the Handle in its place. It
// is false for another host's workspace, which the caller skips.
func splitWorkspaceListLine(line string, n int) ([]string, bool) {
	parts := strings.SplitN(line, "\t", n)
	handle, ours := handleFromJJWorkspaceName(parts[0])
	if !ours {
		return nil, false
	}
	parts[0] = handle
	return parts, true
}

// configureAgentTickFrom configures for the workspace around override, or
// around the current directory, before the repo root is fully resolved: `jj
// root` and `jj --version` must already run the jj that opens this repo.
func configureAgentTickFrom(override string) error {
	dir := override
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil
		}
		dir = wd
	} else if abs, err := filepath.Abs(expandPath(dir)); err == nil {
		dir = abs
	}
	return configureAgentTick(nearestJJWorkspace(dir))
}

// adoptAgentTickMainWorkspace picks this host's Main Workspace in an Agent
// Tick repo. A host that joined the repo named its main directory
// `<host>/<directory>`, not `<host>/default`, so when the configured Main
// Workspace isn't one of this host's Workspaces, the Main Workspace is the one
// at the repo's main directory.
func adoptAgentTickMainWorkspace(repoRoot string, cfg *config) {
	if agentTickPrefix == "" {
		return
	}
	refs, err := listWorkspaceRefs(repoRoot)
	if err != nil {
		return
	}
	for _, ref := range refs {
		if ref.Handle == cfg.MainWorkspace {
			return
		}
	}
	mainRoot := mainWorkspaceRoot(repoRoot)
	for _, ref := range refs {
		if root := cleanWorkspaceRoot(ref.Root); root != "" && sameDirectoryPath(root, mainRoot) {
			cfg.MainWorkspace = ref.Handle
			return
		}
	}
}
