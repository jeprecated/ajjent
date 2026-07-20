package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRecursiveIntegrationTourSetupAndGuideContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tour requires Bash and Jujutsu workspace filesystem semantics")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not available")
	}

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(repoRoot, "scripts", "recursive-integration-tour.sh")
	guide := filepath.Join(repoRoot, "docs", "recursive-integration-tour.md")
	info, err := os.Stat(script)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("tour setup script is not executable: mode=%v", info.Mode())
	}
	if _, err := os.Lstat(filepath.Join(repoRoot, "result")); !os.IsNotExist(err) {
		t.Fatalf("generated Nix result artifact must not exist in source checkout: %v", err)
	}

	binary := filepath.Join(t.TempDir(), "ajj")
	build := exec.Command("go", "build", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Ajj for tour: %v\n%s", err, out)
	}
	rootParent := t.TempDir()
	invalidRoot := filepath.Join(rootParent, "must-not-be-created")
	invalidAjj := filepath.Join(rootParent, "invalid-ajj")
	if err := os.WriteFile(invalidAjj, []byte("#!"+tourTestBashPath(t)+"\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	invalidSetup := exec.Command("bash", script, "--root", invalidRoot, "--ajj", invalidAjj)
	invalidOut, err := invalidSetup.CombinedOutput()
	if err == nil || !strings.Contains(string(invalidOut), "does not provide machine capabilities") {
		t.Fatalf("setup accepted incompatible Ajj or returned an unclear error: err=%v output=%s", err, invalidOut)
	}
	if _, err := os.Stat(invalidRoot); !os.IsNotExist(err) {
		t.Fatalf("setup mutated fixture root before validating Ajj: %v", err)
	}

	assertTourDefaultBuildValidatedBeforeRootMutation(t, script)
	assertTourFailureCleanupAndRetry(t, script, binary)
	assertTourRejectsSymlinkAndProtectedRoots(t, script, binary, repoRoot)
	assertTourRejectsSyntheticSourceAncestor(t, script, binary)

	root := filepath.Join(t.TempDir(), "fixture with spaces")
	setupOut := runTourSetup(t, script, root, binary, false, true)
	if !strings.Contains(setupOut, "no integration operation has run") {
		t.Fatalf("setup did not state its no-effect boundary:\n%s", setupOut)
	}
	assertTourFixture(t, root)
	assertTourRequestsStrict(t, root)
	assertTourGuideContract(t, guide)
	readme, err := os.ReadFile(filepath.Join(repoRoot, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readmeText := string(readme)
	sectionStart := strings.Index(readmeText, "### Recursive Current-Workspace integration")
	if sectionStart < 0 {
		t.Fatal("README lost the recursive integration section")
	}
	sectionEnd := strings.Index(readmeText[sectionStart+4:], "\n### ")
	if sectionEnd < 0 {
		sectionEnd = len(readmeText)
	} else {
		sectionEnd += sectionStart + 4
	}
	if !strings.Contains(readmeText[sectionStart:sectionEnd], "[recursive workspace integration tour](docs/recursive-integration-tour.md)") {
		t.Fatal("README recursive integration section does not prominently link the committed tour")
	}

	refusal := exec.Command("bash", script, "--root", root, "--ajj", binary)
	refusalOut, err := refusal.CombinedOutput()
	if err == nil {
		t.Fatal("tour setup silently replaced an existing fixture without --force")
	}
	if !strings.Contains(string(refusalOut), "already exists") || !strings.Contains(string(refusalOut), "--force") {
		t.Fatalf("unsafe-reset refusal was not actionable: %s", refusalOut)
	}

	sentinel := filepath.Join(root, "must-disappear-on-explicit-reset")
	if err := os.WriteFile(sentinel, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTourSetup(t, script, root, binary, true, false)
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("explicit reset did not recreate fixture, sentinel stat error=%v", err)
	}
	assertTourFixture(t, root)
	assertTourRequestsStrict(t, root)

	buildParent := t.TempDir()
	buildRoot := filepath.Join(buildParent, "fixture-built-from-checkout")
	buildStaging := filepath.Join(buildParent, "staging")
	if err := os.Mkdir(buildStaging, 0o755); err != nil {
		t.Fatal(err)
	}
	buildSetup := exec.Command("bash", script, "--root", buildRoot)
	buildSetup.Env = append(os.Environ(), "TMPDIR="+buildStaging)
	buildOut, err := buildSetup.CombinedOutput()
	if err != nil {
		t.Fatalf("tour setup could not build Ajj by default: %v\n%s", err, buildOut)
	}
	if !strings.Contains(string(buildOut), "Building Ajj from") {
		t.Fatalf("default setup did not report the source build:\n%s", buildOut)
	}
	assertTourFixture(t, buildRoot)
	entries, err := os.ReadDir(buildStaging)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("successful default build leaked staging entries: %v", entries)
	}
}

func assertTourDefaultBuildValidatedBeforeRootMutation(t *testing.T, script string) {
	t.Helper()
	parent := t.TempDir()
	root := filepath.Join(parent, "must-remain-absent")
	staging := filepath.Join(parent, "staging")
	fakeBin := filepath.Join(parent, "fake-bin")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	bashPath := tourTestBashPath(t)
	fakeGo := fmt.Sprintf(`#!%s
set -euo pipefail
out=
while (($#)); do
  if [[ $1 == -o ]]; then out=$2; shift 2; continue; fi
  shift
done
[[ -n $out ]]
cat > "$out" <<'EOF'
#!%s
if [[ ${1:-} == capabilities ]]; then
  printf '%%s\n' '{"schema":"not-ajj-capabilities-v1"}'
  exit 0
fi
exit 2
EOF
chmod +x "$out"
`, bashPath, bashPath)
	if err := os.WriteFile(filepath.Join(fakeBin, "go"), []byte(fakeGo), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script, "--root", root)
	cmd.Env = append(os.Environ(), "PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"), "TMPDIR="+staging)
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "incompatible capabilities schema") {
		t.Fatalf("default build was not capability-validated before setup: err=%v output=%s", err, out)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("bad default build mutated requested root: %v", err)
	}
	entries, err := os.ReadDir(staging)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("bad default build leaked staging entries: %v", entries)
	}
}

func assertTourFailureCleanupAndRetry(t *testing.T, script, binary string) {
	t.Helper()
	parent := t.TempDir()
	root := filepath.Join(parent, "rebuild-after-failure")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "old-fixture-sentinel"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	fakeAjj := filepath.Join(parent, "capable-but-create-fails")
	fake := "#!" + tourTestBashPath(t) + `
set -euo pipefail
if [[ ${1:-} == capabilities && ${2:-} == --json ]]; then
  printf '%s\n' '{"schema":"ajj-capabilities-v1","integrate":{"minimumJjVersion":"0.41.0","executableStrategies":["single","provider-default","ordered-line"]}}'
  exit 0
fi
printf 'injected first create failure\n' >&2
exit 42
`
	if err := os.WriteFile(fakeAjj, []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	failed := exec.Command("bash", script, "--root", root, "--ajj", fakeAjj, "--force")
	out, err := failed.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "injected first create failure") {
		t.Fatalf("fixture-create injection did not fail as expected: err=%v output=%s", err, out)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("failed forced rebuild left partial replacement root: %v", err)
	}

	// Cleanup must make an ordinary retry possible without --force.
	runTourSetup(t, script, root, binary, false, false)
	assertTourFixture(t, root)
}

func assertTourRejectsSymlinkAndProtectedRoots(t *testing.T, script, binary, repoRoot string) {
	t.Helper()
	parent := t.TempDir()
	external := filepath.Join(parent, "external-target")
	if err := os.Mkdir(external, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(external, "must-survive")
	if err := os.WriteFile(sentinel, []byte("external"), 0o600); err != nil {
		t.Fatal(err)
	}
	liveLink := filepath.Join(parent, "live-link")
	if err := os.Symlink(external, liveLink); err != nil {
		t.Fatal(err)
	}
	assertTourRootRejected(t, script, binary, liveLink, "symbolic link")
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "external" {
		t.Fatalf("live symlink rejection changed external target: data=%q err=%v", data, err)
	}
	if info, err := os.Lstat(liveLink); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("live root symlink was removed or replaced: %v %v", info, err)
	}

	danglingTarget := filepath.Join(parent, "missing-target")
	danglingLink := filepath.Join(parent, "dangling-link")
	if err := os.Symlink(danglingTarget, danglingLink); err != nil {
		t.Fatal(err)
	}
	assertTourRootRejected(t, script, binary, danglingLink, "symbolic link")
	if _, err := os.Stat(danglingTarget); !os.IsNotExist(err) {
		t.Fatalf("dangling symlink target was created: %v", err)
	}
	if info, err := os.Lstat(danglingLink); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("dangling root symlink was removed or replaced: %v %v", info, err)
	}

	for _, protected := range []string{"/", "/tmp", "/home", os.Getenv("HOME"), repoRoot} {
		if protected == "" {
			continue
		}
		assertTourRootRejected(t, script, binary, protected, "unsafe fixture root")
	}
}

func assertTourRejectsSyntheticSourceAncestor(t *testing.T, script, binary string) {
	t.Helper()
	root := t.TempDir()
	checkout := filepath.Join(root, "checkout")
	scripts := filepath.Join(checkout, "scripts")
	if err := os.MkdirAll(scripts, 0o755); err != nil {
		t.Fatal(err)
	}
	copyScript := filepath.Join(scripts, "recursive-integration-tour.sh")
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copyScript, data, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(root, "must-survive")
	if err := os.WriteFile(sentinel, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertTourRootRejected(t, copyScript, binary, root, "unsafe fixture root")
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "safe" {
		t.Fatalf("ancestor guard changed root: %q %v", got, err)
	}
	if _, err := os.Stat(copyScript); err != nil {
		t.Fatalf("ancestor guard removed checkout: %v", err)
	}
}

func assertTourRootRejected(t *testing.T, script, binary, root, message string) {
	t.Helper()
	cmd := exec.Command("bash", script, "--root", root, "--ajj", binary, "--force")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), message) {
		t.Fatalf("unsafe root %q was not rejected with %q: err=%v output=%s", root, message, err, out)
	}
}

func tourTestBashPath(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func runTourSetup(t *testing.T, script, root, binary string, force, returnOutput bool) string {
	t.Helper()
	args := []string{script, "--root", root, "--ajj", binary}
	if force {
		args = append(args, "--force")
	}
	cmd := exec.Command("bash", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run tour setup: %v\n%s", err, out)
	}
	if returnOutput {
		return string(out)
	}
	return ""
}

func runTourShell(t *testing.T, root, body string) string {
	t.Helper()
	cmd := exec.Command("bash", "-c", `set -euo pipefail; source "$1/env.sh"; `+body, "tour-test", root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run tour helper %q: %v\n%s", body, err, out)
	}
	return strings.TrimSpace(string(out))
}

func assertTourFixture(t *testing.T, root string) {
	t.Helper()
	values := strings.Split(runTourShell(t, root, `printf '%s\n' "$TOUR_ROOT" "$MAIN" "$A" "$A1" "$A2" "$A3" "$B" "$INITIAL_MAIN_HEAD" "$INITIAL_B_HEAD"`), "\n")
	if len(values) != 9 {
		t.Fatalf("unexpected generated environment values: %q", values)
	}
	if values[0] != root {
		t.Fatalf("generated TOUR_ROOT=%q, want %q", values[0], root)
	}
	for i, path := range values[1:7] {
		if !strings.HasPrefix(path, root+string(os.PathSeparator)) {
			t.Fatalf("generated path escaped root: %q", path)
		}
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Fatalf("fixture workspace %d missing at %q: %v", i, path, err)
		}
	}
	for _, head := range values[7:] {
		if !revisionCommitIDRE.MatchString(head) {
			t.Fatalf("generated baseline is not an exact commit ID: %q", head)
		}
	}

	config, err := os.ReadFile(filepath.Join(root, "xdg", "ajj", "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"main_workspace: default", "  - A1", "  - A2", "  - A3", "  - B"} {
		if !bytes.Contains(config, []byte(want)) {
			t.Fatalf("tour config missing %q:\n%s", want, config)
		}
	}
	if _, err := os.Stat(filepath.Join(values[1], ".ajj", "integrations")); !os.IsNotExist(err) {
		t.Fatalf("setup created integration state before the tour: %v", err)
	}

	workspaceList := runTourShell(t, root, `jj -R "$MAIN" --color=never --no-pager workspace list`)
	for _, handle := range []string{"default:", "A:", "A1:", "A2:", "A3:", "B:"} {
		if !strings.Contains(workspaceList, handle) {
			t.Fatalf("workspace list missing %s:\n%s", handle, workspaceList)
		}
	}
	if got := runTourShell(t, root, `jj -R "$MAIN" --color=never --no-pager log -r 'A@- & ::A1@-' --no-graph -T 'commit_id ++ "\n"'`); got != "" {
		t.Fatalf("A's later target commit unexpectedly precedes A1: %s", got)
	}
	if got := runTourShell(t, root, `jj -R "$MAIN" --color=never --no-pager log -r '(A@- | A1@- | A2@- | A3@- | B@-) & ::default@' --no-graph -T 'commit_id ++ "\n"'`); got != "" {
		t.Fatalf("setup landed fake payloads in Main before integration: %s", got)
	}
	if got := runTourShell(t, root, `tour_verify_fixture_paths; tour_assert_main_unchanged; tour_assert_b_unchanged; printf ok`); got != "ok" {
		t.Fatalf("generated isolation helpers failed: %q", got)
	}
}

func assertTourRequestsStrict(t *testing.T, root string) {
	t.Helper()
	for _, strategy := range []string{integrationStrategyOrderedLine, integrationStrategyProviderDefault} {
		path := runTourShell(t, root, `tour_make_children_request `+strategy)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		request, _, err := parseIntegrationRequestV1(data)
		if err != nil {
			t.Fatalf("generated %s request failed strict parser: %v\n%s", strategy, err, data)
		}
		if request.Strategy != strategy || request.Target.ExpectedWorkspace != "A" || len(request.Payloads) != 3 {
			t.Fatalf("unexpected generated child request: %+v", request)
		}

		mainPath := runTourShell(t, root, `tour_make_main_request `+strategy)
		mainData, err := os.ReadFile(mainPath)
		if err != nil {
			t.Fatal(err)
		}
		mainRequest, _, err := parseIntegrationRequestV1(mainData)
		if err != nil {
			t.Fatalf("generated Main request failed strict parser: %v\n%s", err, mainData)
		}
		if mainRequest.Strategy != integrationStrategySingle || mainRequest.Target.ExpectedWorkspace != "default" || len(mainRequest.Payloads) != 1 || mainRequest.Payloads[0].Workspace != "A" {
			t.Fatalf("unexpected generated Main request: %+v", mainRequest)
		}
	}
}

func assertTourGuideContract(t *testing.T, guidePath string) {
	t.Helper()
	data, err := os.ReadFile(guidePath)
	if err != nil {
		t.Fatal(err)
	}
	guide := string(data)
	for _, required := range []string{
		"scripts/recursive-integration-tour.sh",
		"tour_make_children_request",
		"tour_make_main_request",
		"tour_assert_main_unchanged",
		"tour_assert_b_unchanged",
		`--repo "$A" integrate`,
		`--repo "$A" tidy --yes`,
		`--repo "$MAIN" integrate`,
		`--repo "$MAIN" tidy --yes`,
		`jj -R "$MAIN" --color=never --no-pager workspace list`,
		"## Stage 1 — Initial fixture graph",
		"## Stage 2 — After A <- A1/A2/A3",
		"## Stage 3 — After child tidy",
		"## Stage 4 — Before Main adoption",
		"## Stage 5 — After Main <- A",
		"## Stage 6 — Final tidy",
		"Short change and commit IDs",
		"A: target advanced after child creation",
		"A1: independent payload",
		"A2: independent payload",
		"A3: independent payload",
		"B: omitted independent payload",
		"default@ edeffc6d",
		"A@  3772fe30",
		"No tidy Workspaces selected.",
		"default@ 19590f9d",
		"target.beforeHeadCommit",
		"target.integratedTipCommit",
		"target.afterHeadCommit",
		`"schema": "ajj-integrate-request-v1"`,
		"provider-default",
		"Represented Elsewhere",
		"Agent walkthrough protocol",
		"Do not use modal/ask-question tools",
		".ajj/integrations",
	} {
		if !strings.Contains(guide, required) {
			t.Fatalf("tour guide drifted from script/CLI contract; missing %q", required)
		}
	}
	if got := strings.Count(guide, "```text"); got < 8 {
		t.Fatalf("tour guide must include representative text output at every major stage; found %d fences", got)
	}
	if got := strings.Count(guide, "```json"); got < 2 {
		t.Fatalf("tour guide must include both strict request bodies; found %d JSON fences", got)
	}
}
