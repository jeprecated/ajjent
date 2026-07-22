package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

type lifecycleCLIResult struct {
	stdout string
	stderr string
}

type lifecyclePaths struct {
	main         string
	parent       string
	children     map[string]string
	omitted      string
	stateDir     string
	guardVisible []string
	guardAbandon []string
}

// TestRecursiveWorkspaceLifecyclePublicCLI is deliberately black-box at every
// Ajj boundary. The test builds the command and invokes it as a subprocess;
// direct jj calls only create payload files and inspect the resulting graph.
func TestRecursiveWorkspaceLifecyclePublicCLI(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj binary not available for recursive lifecycle test")
	}
	binary := filepath.Join(t.TempDir(), "ajj")
	build := exec.Command("go", "build", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build black-box ajj command: %v\n%s", err, out)
	}

	for _, strategy := range []string{integrationStrategyProviderDefault, integrationStrategyOrderedLine} {
		t.Run(strategy, func(t *testing.T) {
			paths, env := setupRecursiveLifecycleCLI(t, binary, strategy)
			mainHeadBefore := jjFullCommitID(t, paths.main, "default@")
			mainAncestryBefore, err := integrationCommitIDs(paths.main, "::default@")
			if err != nil {
				t.Fatal(err)
			}
			omittedHeadBefore := jjFullCommitID(t, paths.main, "A-omitted@")
			emptyVisibleBefore, err := integrationCommitIDs(paths.main, `visible_heads() & empty() & description("") & mutable() & ~working_copies()`)
			if err != nil {
				t.Fatal(err)
			}

			childHandles := []string{"A1", "A2", "A3"}
			childHeads := make([]string, len(childHandles))
			for i, handle := range childHandles {
				childHeads[i] = jjFullCommitID(t, paths.main, handle+"@")
			}
			parentBefore := jjFullCommitID(t, paths.main, "A@")
			childOperationID := "recursive-" + strategy + "-children"
			childRequest := lifecycleRequestJSON(t, childOperationID, "A", parentBefore, strategy, childHandles, childHeads)
			childResult := runLifecycleAJJWithInheritedRepo(t, binary, paths.parent, paths.parent, env, childRequest, "integrate", "--repo", lifecycleInheritedRepoArg, "--request-json", "-")
			childReceipt := decodeLifecycleReceipt(t, childResult.stdout)
			assertLifecycleReceipt(t, paths.main, childReceipt, childOperationID, strategy, "A", childHandles)

			if got := jjFullCommitID(t, paths.main, "default@"); got != mainHeadBefore {
				t.Fatalf("A <- children moved configured Main: before=%s after=%s", mainHeadBefore, got)
			}
			mainAncestryAfter, err := integrationCommitIDs(paths.main, "::default@")
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(mainAncestryAfter, "\n") != strings.Join(mainAncestryBefore, "\n") {
				t.Fatalf("A <- children changed configured Main ancestry:\nbefore=%v\nafter=%v", mainAncestryBefore, mainAncestryAfter)
			}
			if got := jjFullCommitID(t, paths.main, "A-omitted@"); got != omittedHeadBefore {
				t.Fatalf("integration moved omitted Workspace: before=%s after=%s", omittedHeadBefore, got)
			}
			assertLifecycleVisibleGuards(t, paths.main, paths.guardVisible)
			emptyVisibleAfter, err := integrationCommitIDs(paths.main, `visible_heads() & empty() & description("") & mutable() & ~working_copies()`)
			if err != nil {
				t.Fatal(err)
			}
			sort.Strings(emptyVisibleBefore)
			sort.Strings(emptyVisibleAfter)
			if strings.Join(emptyVisibleAfter, "\n") != strings.Join(emptyVisibleBefore, "\n") {
				t.Fatalf("integration leaked generated empty visible heads: before=%v after=%v", emptyVisibleBefore, emptyVisibleAfter)
			}
			assertLifecycleJournalLocation(t, paths, childOperationID)

			// Exact request replay and fresh-process recovery remain byte-for-byte
			// idempotent when Current A is supplied through an inherited FD.
			replay := runLifecycleAJJWithInheritedRepo(t, binary, paths.parent, paths.parent, env, childRequest, "--repo", lifecycleInheritedRepoArg, "integrate", "--request-json", "-")
			if replay.stdout != childResult.stdout {
				t.Fatalf("terminal request replay changed receipt:\nfirst=%s\nreplay=%s", childResult.stdout, replay.stdout)
			}
			recovered := runLifecycleAJJWithInheritedRepo(t, binary, paths.parent, paths.parent, env, nil, "integrate", "--repo", lifecycleInheritedRepoArg, "--recover", childOperationID, "--json")
			if recovered.stdout != childResult.stdout {
				t.Fatalf("terminal recovery changed receipt:\nfirst=%s\nrecover=%s", childResult.stdout, recovered.stdout)
			}
			teardownLifecycleVisibleGuards(t, paths.main, paths.guardAbandon)

			// Automatic tidy is routed from inherited Current A. It must leave A
			// and the unique omitted Workspace, while A protects all selected
			// children. This also proves local Project config was not replaced by
			// an FD-number fallback during normal cleanup.
			tidyChildren := runLifecycleAJJWithInheritedRepo(t, binary, paths.parent, paths.parent, env, nil, "tidy", "--repo", lifecycleInheritedRepoArg, "--yes")
			for _, handle := range childHandles {
				if pathExists(paths.children[handle]) {
					t.Fatalf("normally Closable child %s survived recursive tidy; stdout=%q stderr=%q", handle, tidyChildren.stdout, tidyChildren.stderr)
				}
			}
			if !pathExists(paths.parent) {
				t.Fatal("automatic tidy selected Current Workspace A")
			}
			if !pathExists(paths.omitted) || jjRevsetCount(t, paths.main, "A-omitted@") != 1 {
				t.Fatal("automatic tidy removed or forgot omitted unique Workspace")
			}

			// The same machine operation now routes from Main and adopts exact A.
			parentHead := jjFullCommitID(t, paths.main, "A@")
			mainBefore := jjFullCommitID(t, paths.main, "default@")
			mainOperationID := "recursive-" + strategy + "-main"
			mainRequest := lifecycleRequestJSON(t, mainOperationID, "default", mainBefore, integrationStrategySingle, []string{"A"}, []string{parentHead})
			mainResult := runLifecycleAJJ(t, binary, paths.main, env, mainRequest, "--repo", paths.main, "integrate", "--request-json", "-")
			mainReceipt := decodeLifecycleReceipt(t, mainResult.stdout)
			assertLifecycleReceipt(t, paths.main, mainReceipt, mainOperationID, integrationStrategySingle, "default", []string{"A"})
			mainReplay := runLifecycleAJJ(t, binary, paths.main, env, mainRequest, "integrate", "--repo", paths.main, "--request-json", "-")
			if mainReplay.stdout != mainResult.stdout {
				t.Fatalf("Main terminal replay changed receipt:\nfirst=%s\nreplay=%s", mainResult.stdout, mainReplay.stdout)
			}
			mainRecovered := runLifecycleAJJ(t, binary, paths.main, env, nil, "--repo", paths.main, "integrate", "--recover", mainOperationID, "--json")
			if mainRecovered.stdout != mainResult.stdout {
				t.Fatalf("Main terminal recovery changed receipt:\nfirst=%s\nrecover=%s", mainResult.stdout, mainRecovered.stdout)
			}
			if got := jjFullCommitID(t, paths.main, "A-omitted@"); got != omittedHeadBefore {
				t.Fatalf("Main <- A moved omitted Workspace: before=%s after=%s", omittedHeadBefore, got)
			}
			assertLifecycleJournalLocation(t, paths, mainOperationID)

			// Main now represents A, so automatic normal Tidy must select A. Main
			// is Current and therefore excluded; the unique omitted sibling must
			// remain unselected, registered, and present.
			tidyMain := runLifecycleAJJ(t, binary, paths.main, env, nil, "tidy", "--repo", paths.main, "--yes")
			closed := strings.Fields(strings.TrimSpace(tidyMain.stdout))
			if len(closed) != 1 || closed[0] != paths.parent {
				t.Fatalf("automatic Main tidy selected the wrong Workspaces: stdout=%q want only %q", tidyMain.stdout, paths.parent)
			}
			if pathExists(paths.parent) {
				t.Fatal("A survived automatic normal Tidy after Main represented it")
			}
			if !pathExists(paths.main) {
				t.Fatal("automatic Tidy selected Current/Main Workspace")
			}
			if !pathExists(paths.omitted) || jjRevsetCount(t, paths.main, "A-omitted@") != 1 || jjFullCommitID(t, paths.main, "A-omitted@") != omittedHeadBefore {
				t.Fatal("tidying A moved, removed, or forgot omitted unique Workspace")
			}
			for _, payload := range mainReceipt.Payloads {
				for _, mapping := range payload.Changes {
					if jjRevsetCount(t, paths.main, "change_id("+mapping.ChangeID+") & ::default@") != 1 {
						t.Fatalf("closing A lost landed nested change %s", mapping.ChangeID)
					}
				}
			}
			if status := lifecycleJJOutput(t, paths.main, "status"); strings.Contains(status, ".ajj/integrations") {
				t.Fatalf("ignored integration journal leaked into working-copy status: %s", status)
			}
		})
	}
}

func setupRecursiveLifecycleCLI(t *testing.T, binary, strategy string) (lifecyclePaths, []string) {
	t.Helper()
	root := t.TempDir()
	workspacesRoot := filepath.Join(root, "workspaces")
	project := "recursive-" + strategy
	mainPath := filepath.Join(workspacesRoot, project, "default")
	if err := os.MkdirAll(filepath.Dir(mainPath), 0o755); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "git", "init", "--colocate", mainPath)
	if err := os.WriteFile(filepath.Join(mainPath, ".gitignore"), []byte(".ajj/integrations/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", mainPath, "file", "track", "root:.gitignore")
	if err := os.WriteFile(filepath.Join(mainPath, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", mainPath, "file", "track", "root:base.txt")
	runJJ(t, "-R", mainPath, "commit", "-m", "recursive base")

	xdg := filepath.Join(root, "xdg")
	configDir := filepath.Join(xdg, "ajj")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := strings.Join([]string{
		"workspaces_root: " + workspacesRoot,
		"project: " + project,
		"workspace_handles:",
		"  - A",
		"  - A1",
		"  - A2",
		"  - A3",
		"  - A-omitted",
		"handle_strategy: first-unused",
		"main_workspace: default",
		"stack:",
		"  rebase_mode: branch",
		"  shape: merge",
		"  conflict_strategy: off",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "XDG_CONFIG_HOME="+xdg, "NO_COLOR=1")

	mainFrontier := jjFullCommitID(t, mainPath, "default@-")
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	guardVisible, guardAbandon := setupLifecycleVisibleGuards(t, root, mainPath, mainFrontier)
	parentResult := runLifecycleAJJ(t, binary, mainPath, env, nil, "create", "A", "--revision", mainFrontier)
	parentPath := strings.TrimSpace(parentResult.stdout)
	if parentPath != filepath.Join(workspacesRoot, project, "A") {
		t.Fatalf("create A printed path %q", parentPath)
	}
	parentFrontier := jjFullCommitID(t, mainPath, "A@-")
	children := map[string]string{}

	// Exercise cwd routing, global --repo, and command-local --repo while A1/A2/A3
	// are created from exact Current Workspace A. The independent omitted
	// Workspace is created from Main so target evolution cannot legitimately
	// rewrite it.
	createArgs := map[string][]string{
		"A1":        {"create", "A1", "--revision", parentFrontier},
		"A2":        {"--repo", parentPath, "create", "A2", "--revision", parentFrontier},
		"A3":        {"create", "A3", "--repo", parentPath, "--revision", parentFrontier},
		"A-omitted": {"--repo", mainPath, "create", "A-omitted", "--revision", mainFrontier},
	}
	for _, handle := range []string{"A1", "A2", "A3", "A-omitted"} {
		result := runLifecycleAJJ(t, binary, parentPath, env, nil, createArgs[handle]...)
		path := strings.TrimSpace(result.stdout)
		if handle == "A-omitted" {
			children[handle] = path
		} else {
			children[handle] = path
		}
	}
	for _, handle := range []string{"A1", "A2", "A3", "A-omitted"} {
		path := children[handle]
		file := strings.ToLower(strings.ReplaceAll(handle, "-", "_")) + ".txt"
		if err := os.WriteFile(filepath.Join(path, file), []byte(handle+" payload\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runJJ(t, "-R", path, "file", "track", "root:"+file)
		runJJ(t, "-R", path, "commit", "-m", handle+" payload")
	}
	for _, path := range []string{mainPath, parentPath, children["A1"], children["A2"], children["A3"], children["A-omitted"]} {
		runJJ(t, "-R", path, "workspace", "update-stale")
	}
	// Force hostile presentation settings for every public integration, replay,
	// and recovery query in this lifecycle. Strict machine templates must remain
	// independent of user color and pager configuration.
	runJJ(t, "-R", mainPath, "config", "set", "--repo", "ui.color", "always")
	runJJ(t, "-R", mainPath, "config", "set", "--repo", "ui.paginate", "auto")
	runJJ(t, "-R", mainPath, "config", "set", "--repo", "ui.pager", `["cat"]`)
	return lifecyclePaths{
		main:         mainPath,
		parent:       parentPath,
		children:     children,
		omitted:      children["A-omitted"],
		stateDir:     filepath.Join(mainPath, ".ajj", "integrations"),
		guardVisible: guardVisible,
		guardAbandon: guardAbandon,
	}, env
}

func setupLifecycleVisibleGuards(t *testing.T, root, repo, revision string) ([]string, []string) {
	t.Helper()
	guards := []struct {
		name        string
		description string
		immutable   bool
	}{
		{name: "guard-empty"},
		{name: "guard-described", description: "described empty guard"},
		{name: "guard-immutable", immutable: true},
	}
	ids := make([]string, 0, len(guards))
	for _, guard := range guards {
		path := filepath.Join(root, "guard-workspaces", guard.name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		runJJ(t, "-R", repo, "workspace", "add", "--revision", revision, "--name", guard.name, path)
		if guard.description != "" {
			runJJ(t, "-R", path, "describe", "-m", guard.description)
		}
		id := jjFullCommitID(t, repo, guard.name+"@")
		bookmark := "lifecycle-" + guard.name
		runJJ(t, "-R", repo, "bookmark", "create", bookmark, "-r", id)
		if guard.immutable {
			runJJ(t, "-R", repo, "config", "set", "--repo", `revset-aliases."immutable_heads()"`, bookmark)
			if jjRevsetCount(t, repo, id+" & immutable()") != 1 {
				t.Fatalf("%s did not become an immutable cleanup guard", guard.name)
			}
		}
		runJJ(t, "-R", repo, "workspace", "forget", guard.name)
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
		if jjRevsetCount(t, repo, id+" & visible_heads()") != 1 {
			t.Fatalf("%s did not become a pre-existing visible cleanup guard", guard.name)
		}
		ids = append(ids, id)
	}

	leftPath := filepath.Join(root, "guard-workspaces", "guard-conflict-left")
	rightPath := filepath.Join(root, "guard-workspaces", "guard-conflict-right")
	mergePath := filepath.Join(root, "guard-workspaces", "guard-conflict")
	runJJ(t, "-R", repo, "workspace", "add", "--revision", revision, "--name", "guard-conflict-left", leftPath)
	runJJ(t, "-R", repo, "workspace", "add", "--revision", revision, "--name", "guard-conflict-right", rightPath)
	if err := os.WriteFile(filepath.Join(leftPath, "base.txt"), []byte("left guard\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rightPath, "base.txt"), []byte("right guard\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", leftPath, "commit", "-m", "left conflict guard")
	runJJ(t, "-R", rightPath, "commit", "-m", "right conflict guard")
	left := jjFullCommitID(t, repo, "guard-conflict-left@-")
	right := jjFullCommitID(t, repo, "guard-conflict-right@-")
	runJJ(t, "-R", repo, "workspace", "add", "--revision", left, "--name", "guard-conflict", mergePath)
	runJJ(t, "-R", mergePath, "new", left, right)
	conflict := jjFullCommitID(t, repo, "guard-conflict@")
	if jjRevsetCount(t, repo, conflict+" & conflicts()") != 1 {
		t.Fatal("conflict cleanup guard is not conflicted")
	}
	runJJ(t, "-R", repo, "bookmark", "create", "lifecycle-guard-conflict", "-r", conflict)
	for _, name := range []string{"guard-conflict", "guard-conflict-left", "guard-conflict-right"} {
		runJJ(t, "-R", repo, "workspace", "forget", name)
	}
	if err := os.RemoveAll(filepath.Join(root, "guard-workspaces")); err != nil {
		t.Fatal(err)
	}
	if jjRevsetCount(t, repo, conflict+" & visible_heads() & conflicts()") != 1 {
		t.Fatal("conflicted visible guard was not retained after forgetting its Workspace")
	}
	ids = append(ids, conflict)
	return ids, []string{conflict, left, right}
}

func teardownLifecycleVisibleGuards(t *testing.T, repo string, abandon []string) {
	t.Helper()
	runJJ(t, "-R", repo, "bookmark", "delete",
		"lifecycle-guard-empty",
		"lifecycle-guard-described",
		"lifecycle-guard-immutable",
		"lifecycle-guard-conflict",
	)
	// Restore the fixture's ordinary immutable boundary after proving that the
	// cleanup transaction preserved the explicit immutable visible guard.
	runJJ(t, "-R", repo, "config", "set", "--repo", `revset-aliases."immutable_heads()"`, "root()")
	if len(abandon) > 0 {
		runJJ(t, "-R", repo, "abandon", "-r", "("+strings.Join(abandon, " | ")+")")
	}
}

func assertLifecycleVisibleGuards(t *testing.T, repo string, commits []string) {
	t.Helper()
	for _, commit := range commits {
		if jjRevsetCount(t, repo, commit+" & visible_heads()") != 1 {
			t.Fatalf("cleanup changed pre-existing visible guard %s", commit)
		}
	}
}

func lifecycleRequestJSON(t *testing.T, operationID, target, targetHead, strategy string, payloads, heads []string) []byte {
	t.Helper()
	if len(payloads) != len(heads) {
		t.Fatalf("payload/head mismatch: %d != %d", len(payloads), len(heads))
	}
	entries := make([]map[string]string, len(payloads))
	for i := range payloads {
		entries[i] = map[string]string{"workspace": payloads[i], "expectedHeadCommit": heads[i]}
	}
	request := map[string]any{
		"schema":      "ajj-integrate-request-v1",
		"operationId": operationID,
		"target": map[string]string{
			"expectedWorkspace":  target,
			"expectedHeadCommit": targetHead,
		},
		"strategy": strategy,
		"payloads": entries,
	}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func runLifecycleAJJ(t *testing.T, binary, dir string, env []string, stdin []byte, args ...string) lifecycleCLIResult {
	t.Helper()
	return runLifecycleAJJCommand(t, binary, dir, env, stdin, nil, args...)
}

const lifecycleInheritedRepoArg = "{inherited-current-workspace}"

func runLifecycleAJJWithInheritedRepo(t *testing.T, binary, dir, repo string, env []string, stdin []byte, args ...string) lifecycleCLIResult {
	t.Helper()
	routedArgs := append([]string(nil), args...)
	if runtime.GOOS != "linux" {
		for i, arg := range routedArgs {
			if arg == lifecycleInheritedRepoArg {
				routedArgs[i] = repo
			}
		}
		return runLifecycleAJJCommand(t, binary, dir, env, stdin, nil, routedArgs...)
	}
	workspace, err := os.Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	for i, arg := range routedArgs {
		if arg == lifecycleInheritedRepoArg {
			routedArgs[i] = "/proc/self/fd/3"
		}
	}
	return runLifecycleAJJCommand(t, binary, dir, env, stdin, []*os.File{workspace}, routedArgs...)
}

func runLifecycleAJJCommand(t *testing.T, binary, dir string, env []string, stdin []byte, extraFiles []*os.File, args ...string) lifecycleCLIResult {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.ExtraFiles = extraFiles
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ajj %s failed: %v\nstdout:%s\nstderr:%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return lifecycleCLIResult{stdout: stdout.String(), stderr: stderr.String()}
}

func decodeLifecycleReceipt(t *testing.T, output string) integrationReceiptV1 {
	t.Helper()
	if len(output) > integrationMaxOutputBytes {
		t.Fatalf("receipt exceeds advertised output bound: %d", len(output))
	}
	var receipt integrationReceiptV1
	decoder := json.NewDecoder(strings.NewReader(output))
	if err := decoder.Decode(&receipt); err != nil {
		t.Fatalf("decode receipt: %v\n%s", err, output)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("receipt stdout contains trailing data: %v\n%s", err, output)
	}
	return receipt
}

func assertLifecycleReceipt(t *testing.T, repo string, receipt integrationReceiptV1, operationID, strategy, target string, payloads []string) {
	t.Helper()
	if receipt.Schema != integrationReceiptSchemaV1 || receipt.OperationID != operationID || receipt.Strategy != strategy || receipt.BatchDisposition != integrationBatchSucceeded {
		t.Fatalf("unexpected lifecycle receipt: %+v", receipt)
	}
	if receipt.Target.Workspace != target || receipt.Target.BeforeHeadCommit == "" || receipt.Target.IntegratedTipCommit == "" || receipt.Target.AfterHeadCommit == "" || receipt.Target.IntegratedTipCommit == receipt.Target.AfterHeadCommit {
		t.Fatalf("receipt conflates or omits target evidence: %+v", receipt.Target)
	}
	if got := jjFullCommitID(t, repo, target+"@"); got != receipt.Target.AfterHeadCommit {
		t.Fatalf("target graph/head mismatch: graph=%s receipt=%s", got, receipt.Target.AfterHeadCommit)
	}
	if len(receipt.Payloads) != len(payloads) {
		t.Fatalf("payload count=%d want=%d: %+v", len(receipt.Payloads), len(payloads), receipt.Payloads)
	}
	for i, want := range payloads {
		payload := receipt.Payloads[i]
		if payload.Workspace != want || payload.Disposition != integrationPayloadLanded || len(payload.Changes) == 0 {
			t.Fatalf("payload %d not exactly landed/mapped: %+v", i, payload)
		}
		for _, mapping := range payload.Changes {
			if mapping.ChangeID == "" || mapping.InputCommit == "" || mapping.LandedCommit == "" || jjRevsetCount(t, repo, mapping.LandedCommit+" & ::"+target+"@") != 1 {
				t.Fatalf("payload %s mapping is not reachable from target: %+v", want, mapping)
			}
		}
	}
	if receipt.JJOperations.BeforeEffect == "" || receipt.JJOperations.CommitPoint == "" || receipt.JJOperations.BeforeEffect == receipt.JJOperations.CommitPoint {
		t.Fatalf("receipt omits exact transaction boundary: %+v", receipt.JJOperations)
	}
}

func assertLifecycleJournalLocation(t *testing.T, paths lifecyclePaths, operationID string) {
	t.Helper()
	entries, err := os.ReadDir(paths.stateDir)
	if err != nil {
		t.Fatalf("read configured-Main integration state: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	found := false
	for _, name := range names {
		if strings.Contains(name, operationID) {
			found = true
		}
	}
	if !found {
		t.Fatalf("configured-Main state has no record for %s: %v", operationID, names)
	}
	if _, err := os.Stat(filepath.Join(paths.parent, ".ajj", "integrations")); !os.IsNotExist(err) {
		t.Fatalf("Current Workspace A unexpectedly owns integration state: %v", err)
	}
}

func lifecycleJJOutput(t *testing.T, repo string, args ...string) string {
	t.Helper()
	argv := append([]string{"-R", repo}, args...)
	cmd := exec.Command("jj", argv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("jj %s failed: %v\n%s", strings.Join(argv, " "), err, out)
	}
	return string(out)
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
