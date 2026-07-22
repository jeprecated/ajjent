package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func machineCreateRequest(t *testing.T, repo, child, id string) []byte {
	return machineCreateRequestForTarget(t, repo, "default", child, id)
}

func machineCreateRequestForTarget(t *testing.T, repo, target, child, id string) []byte {
	t.Helper()
	head, e := integrationWorkspaceHeadCommit(repo, target)
	if e != nil {
		t.Fatal(e)
	}
	data, e := json.Marshal(createRequestV1{Schema: createRequestSchemaV1, RequestID: id, Target: createTargetAssertionV1{ExpectedWorkspace: target, ExpectedHeadCommit: head}, Child: createChildAssertionV1{Workspace: child}})
	if e != nil {
		t.Fatal(e)
	}
	return data
}
func runMachineCreateForTest(t *testing.T, repo string, request []byte) createReceiptV1 {
	t.Helper()
	oldIn, oldOut, oldErr := stdinReader, stdoutWriter, stderrWriter
	stdinReader = bytes.NewReader(request)
	var out, stderr bytes.Buffer
	stdoutWriter, stderrWriter = &out, &stderr
	t.Cleanup(func() { stdinReader, stdoutWriter, stderrWriter = oldIn, oldOut, oldErr })
	if e := runCreate([]string{"--repo", repo, "--request-json", "-", "--json"}); e != nil {
		t.Fatalf("machine create: %v", e)
	}
	if stderr.Len() != 0 {
		t.Fatalf("machine create wrote diagnostics: %q", stderr.String())
	}
	var r createReceiptV1
	if e := json.Unmarshal(out.Bytes(), &r); e != nil {
		t.Fatalf("receipt %q: %v", out.String(), e)
	}
	if strings.Contains(out.String(), repo) || strings.Contains(out.String(), filepath.Dir(repo)) {
		t.Fatalf("path leak: %s", out.String())
	}
	if err := validateCreateReceiptV1(r); err != nil {
		t.Fatalf("invalid receipt: %v\n%s", err, out.String())
	}
	return r
}
func TestMachineCreateReadyAndReplayAreStateIdempotent(t *testing.T) {
	root, repo := setupRealCreateRepo(t)
	req := machineCreateRequest(t, repo, "A1", "placement-A1")
	first := runMachineCreateForTest(t, repo, req)
	if first.Status != createStatusReady || !first.Checks.SetupComplete || first.Child.ParentCommit != first.Target.ExpectedHeadCommit {
		t.Fatalf("first %+v", first)
	}
	path := filepath.Join(root, "proj", "A1")
	op := currentCreateTestOperation(t, repo)
	head := jjCommitID(t, path, "@")
	second := runMachineCreateForTest(t, repo, req)
	if second.Status != createStatusReady || second.Child.HeadCommit != head {
		t.Fatalf("second %+v", second)
	}
	if got := currentCreateTestOperation(t, repo); got != op {
		t.Fatalf("operation changed %s -> %s", op, got)
	}
}
func TestMachineCreateTargetsCurrentParentAndLeavesMainUnchanged(t *testing.T) {
	root, repo := setupRealCreateRepo(t)
	cfg := configForMachineCreateTest(t, repo, root)
	mainHead, err := integrationWorkspaceHeadCommit(repo, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := createWorkspace(repo, cfg, "proj", "A", mainHead, false, false); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(root, "proj", "A")
	request := machineCreateRequestForTarget(t, parent, "A", "A1", "nested-A1")
	receipt := runMachineCreateForTest(t, parent, request)
	if receipt.Status != createStatusReady || receipt.Target.Workspace != "A" || receipt.Child.ParentCommit != receipt.Target.ExpectedHeadCommit {
		t.Fatalf("receipt: %+v", receipt)
	}
	if got, err := integrationWorkspaceHeadCommit(repo, "default"); err != nil || got != mainHead {
		t.Fatalf("Main moved: %s err=%v", got, err)
	}
}

func TestMachineCreateCanonicalizesInheritedRepoFD(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("inherited /proc/self/fd routing is Linux-specific")
	}
	root, repo := setupRealCreateRepo(t)
	cfg := configForMachineCreateTest(t, repo, root)
	mainHead, err := integrationWorkspaceHeadCommit(repo, "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := createWorkspace(repo, cfg, "proj", "A", mainHead, false, false); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(root, "proj", "A")
	request := machineCreateRequestForTarget(t, parent, "A", "A1", "nested-fd-A1")

	binary := filepath.Join(t.TempDir(), "ajj")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build ajj: %v\n%s", err, output)
	}

	// A wrong global fallback makes failure to find the configured Main's local
	// config observable: the child would be created in wrong-project and then
	// reported as a repository mismatch.
	xdg := filepath.Join(t.TempDir(), "xdg")
	if err := os.MkdirAll(filepath.Join(xdg, "ajj"), 0o755); err != nil {
		t.Fatal(err)
	}
	wrongGlobal := strings.Join([]string{
		"workspaces_root: " + root,
		"project: wrong-project",
		"main_workspace: default",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(xdg, "ajj", "config.yaml"), []byte(wrongGlobal), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)

	first := runMachineCreateViaInheritedRepoFD(t, binary, parent, request)
	if first.Status != createStatusReady || first.Target.Workspace != "A" || first.Child.ParentCommit != first.Target.ExpectedHeadCommit {
		t.Fatalf("first receipt: %+v", first)
	}
	child := filepath.Join(root, "proj", "A1")
	if !exists(child) || exists(filepath.Join(root, "wrong-project", "A1")) {
		t.Fatalf("machine create used the wrong Project: child=%t wrong=%t", exists(child), exists(filepath.Join(root, "wrong-project", "A1")))
	}
	operation := currentCreateTestOperation(t, repo)
	second := runMachineCreateViaInheritedRepoFD(t, binary, parent, request)
	if second.Status != createStatusReady || second.EvidenceDigest != first.EvidenceDigest {
		t.Fatalf("replay changed ready evidence: first=%+v second=%+v", first, second)
	}
	if got := currentCreateTestOperation(t, repo); got != operation {
		t.Fatalf("FD replay changed repository operation: %s -> %s", operation, got)
	}
	if got, err := integrationWorkspaceHeadCommit(repo, "default"); err != nil || got != mainHead {
		t.Fatalf("Main moved: %s err=%v", got, err)
	}
}

func runMachineCreateViaInheritedRepoFD(t *testing.T, binary, repo string, request []byte) createReceiptV1 {
	t.Helper()
	dir, err := os.Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	cmd := exec.Command(binary, "create", "--repo", "/proc/self/fd/3", "--request-json", "-", "--json")
	cmd.ExtraFiles = []*os.File{dir}
	cmd.Env = os.Environ()
	cmd.Stdin = bytes.NewReader(request)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("FD-routed machine create: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("FD-routed machine create wrote diagnostics: %q", stderr.String())
	}
	var receipt createReceiptV1
	if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil {
		t.Fatalf("decode FD-routed receipt %q: %v", stdout.String(), err)
	}
	if err := validateCreateReceiptV1(receipt); err != nil {
		t.Fatalf("invalid FD-routed receipt: %v\n%s", err, stdout.String())
	}
	if strings.Contains(stdout.String(), repo) || strings.Contains(stdout.String(), "/proc/self/fd") {
		t.Fatalf("FD-routed receipt leaked a path: %s", stdout.String())
	}
	return receipt
}

func TestMachineCreateSuppressesAssimilationDiagnostics(t *testing.T) {
	root, repo := setupRealCreateRepo(t)
	if err := os.Mkdir(filepath.Join(repo, "scratch"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, repo, strings.Join([]string{"workspaces_root: " + root, "project: proj", "main_workspace: default", "assimilated_paths:", "  - scratch", ""}, "\n"))
	_ = jjCaptureCommitID(t, repo, "@")
	if r := runMachineCreateForTest(t, repo, machineCreateRequest(t, repo, "A1", "quiet-setup")); r.Status != createStatusReady {
		t.Fatalf("got %+v error=%+v", r, r.Error)
	}
	if target, err := os.Readlink(filepath.Join(root, "proj", "A1", "scratch")); err != nil || target != filepath.Join(repo, "scratch") {
		t.Fatalf("assimilation missing: %q %v", target, err)
	}
}

func TestMachineCreateAcceptsExactHumanCreatedState(t *testing.T) {
	root, repo := setupRealCreateRepo(t)
	req := machineCreateRequest(t, repo, "A1", "lookalike")
	var parsed createRequestV1
	_ = json.Unmarshal(req, &parsed)
	if e := createWorkspace(repo, configForMachineCreateTest(t, repo, root), "proj", "A1", parsed.Target.ExpectedHeadCommit, false, false); e != nil {
		t.Fatal(e)
	}
	if r := runMachineCreateForTest(t, repo, req); r.Status != createStatusReady {
		t.Fatalf("got %+v", r)
	}
}
func TestMachineCreateReportsPartialAfterSetupFailure(t *testing.T) {
	root, repo := setupRealCreateRepo(t)
	writeConfig(t, repo, strings.Join([]string{"workspaces_root: " + root, "project: proj", "main_workspace: default", ""}, "\n"))
	req := machineCreateRequest(t, repo, "A1", "partial")
	old := createMaterializeSetupFn
	createMaterializeSetupFn = func(string, string, config, string) error { return errors.New("injected") }
	t.Cleanup(func() { createMaterializeSetupFn = old })
	r := runMachineCreateForTest(t, repo, req)
	if r.Status != createStatusPartial || !r.Checks.RegistrationPresent || r.Checks.SetupComplete {
		t.Fatalf("got %+v", r)
	}
	if r2 := runMachineCreateForTest(t, repo, req); r2.Status != createStatusPartial {
		t.Fatalf("replay %+v", r2)
	}
}
func TestMachineCreateNoResponseLookalikeReconcilesReady(t *testing.T) {
	_, repo := setupRealCreateRepo(t)
	req := machineCreateRequest(t, repo, "A1", "interrupted")
	old := createMachineCommandFn
	createMachineCommandFn = func(name string, args ...string) error {
		if err := old(name, args...); err != nil {
			return err
		}
		return errors.New("simulated lost response after effect")
	}
	t.Cleanup(func() { createMachineCommandFn = old })
	if r := runMachineCreateForTest(t, repo, req); r.Status != createStatusReady {
		t.Fatalf("got %+v", r)
	}
	createMachineCommandFn = old
	if r := runMachineCreateForTest(t, repo, req); r.Status != createStatusReady {
		t.Fatalf("replay got %+v", r)
	}
}

func TestMachineCreateFailureBeforeEffectIsNotCreated(t *testing.T) {
	_, repo := setupRealCreateRepo(t)
	req := machineCreateRequest(t, repo, "A1", "none")
	old := createMachineCommandFn
	createMachineCommandFn = func(string, ...string) error { return errors.New("injected") }
	t.Cleanup(func() { createMachineCommandFn = old })
	if r := runMachineCreateForTest(t, repo, req); r.Status != createStatusNotCreated || r.Checks.RegistrationPresent || r.Checks.DestinationPresent {
		t.Fatalf("got %+v", r)
	}
}
func TestMachineCreateContradictoryStatesConflict(t *testing.T) {
	t.Run("regular file destination", func(t *testing.T) {
		root, repo := setupRealCreateRepo(t)
		path := filepath.Join(root, "proj", "A1")
		if err := os.WriteFile(path, []byte("occupied"), 0o600); err != nil {
			t.Fatal(err)
		}
		r := runMachineCreateForTest(t, repo, machineCreateRequest(t, repo, "A1", "file"))
		if r.Status != createStatusConflict {
			t.Fatalf("got %+v", r)
		}
		if data, err := os.ReadFile(path); err != nil || string(data) != "occupied" {
			t.Fatalf("destination changed: %q %v", data, err)
		}
	})
	t.Run("dangling symlink destination", func(t *testing.T) {
		root, repo := setupRealCreateRepo(t)
		path := filepath.Join(root, "proj", "A1")
		if err := os.Symlink(filepath.Join(root, "missing"), path); err != nil {
			t.Fatal(err)
		}
		r := runMachineCreateForTest(t, repo, machineCreateRequest(t, repo, "A1", "dangling"))
		if r.Status != createStatusConflict {
			t.Fatalf("got %+v", r)
		}
		if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("symlink changed: %v %v", info, err)
		}
	})
	t.Run("path only", func(t *testing.T) {
		root, repo := setupRealCreateRepo(t)
		path := filepath.Join(root, "proj", "A1")
		_ = os.MkdirAll(path, 0755)
		r := runMachineCreateForTest(t, repo, machineCreateRequest(t, repo, "A1", "path"))
		if r.Status != createStatusConflict || !exists(path) {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("wrong parent", func(t *testing.T) {
		root, repo := setupRealCreateRepo(t)
		req := machineCreateRequest(t, repo, "A1", "parent")
		wrong := jjCommitID(t, repo, "@-")
		if e := createWorkspace(repo, configForMachineCreateTest(t, repo, root), "proj", "A1", wrong, false, false); e != nil {
			t.Fatal(e)
		}
		if r := runMachineCreateForTest(t, repo, req); r.Status != createStatusConflict {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("missing directory", func(t *testing.T) {
		root, repo := setupRealCreateRepo(t)
		req := machineCreateRequest(t, repo, "A1", "missing")
		var parsed createRequestV1
		_ = json.Unmarshal(req, &parsed)
		if e := createWorkspace(repo, configForMachineCreateTest(t, repo, root), "proj", "A1", parsed.Target.ExpectedHeadCommit, false, false); e != nil {
			t.Fatal(e)
		}
		_ = os.RemoveAll(filepath.Join(root, "proj", "A1"))
		if r := runMachineCreateForTest(t, repo, req); r.Status != createStatusConflict {
			t.Fatalf("got %+v", r)
		}
	})
}
func TestMachineCreatePostSetupMutationConflicts(t *testing.T) {
	root, repo := setupRealCreateRepo(t)
	req := machineCreateRequest(t, repo, "A1", "setup-race")
	var parsed createRequestV1
	_ = json.Unmarshal(req, &parsed)
	if err := createWorkspace(repo, configForMachineCreateTest(t, repo, root), "proj", "A1", parsed.Target.ExpectedHeadCommit, false, false); err != nil {
		t.Fatal(err)
	}
	old := createMaterializeSetupFn
	createMaterializeSetupFn = func(main, dest string, cfg config, project string) error {
		if err := old(main, dest, cfg, project); err != nil {
			return err
		}
		_, err := commandCaptureFn("jj", "-R", dest, "--color=never", "--no-pager", "describe", "-m", "concurrent mutation")
		return err
	}
	t.Cleanup(func() { createMaterializeSetupFn = old })
	if r := runMachineCreateForTest(t, repo, req); r.Status != createStatusConflict {
		t.Fatalf("got %+v", r)
	}
}

func TestMachineCreateFinalReadyProofRejectsConcurrentChildMutation(t *testing.T) {
	root, repo := setupRealCreateRepo(t)
	req := machineCreateRequest(t, repo, "A1", "final-ready-race")
	var parsed createRequestV1
	_ = json.Unmarshal(req, &parsed)
	if err := createWorkspace(repo, configForMachineCreateTest(t, repo, root), "proj", "A1", parsed.Target.ExpectedHeadCommit, false, false); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "proj", "A1")
	old := createReadyProofHook
	createReadyProofHook = func(boundary string) error {
		if boundary == "after-final-child-inspection" {
			_, err := commandCaptureFn("jj", "-R", dest, "--color=never", "--no-pager", "describe", "-m", "concurrent final mutation")
			return err
		}
		return nil
	}
	t.Cleanup(func() { createReadyProofHook = old })
	if r := runMachineCreateForTest(t, repo, req); r.Status != createStatusConflict {
		t.Fatalf("stale ready proof did not fail closed: %+v", r)
	}
}

func TestMachineCreateConcurrentActorDuringPreEffectFailureReconcilesReady(t *testing.T) {
	root, repo := setupRealCreateRepo(t)
	req := machineCreateRequest(t, repo, "A1", "concurrent")
	var parsed createRequestV1
	_ = json.Unmarshal(req, &parsed)
	cfg := configForMachineCreateTest(t, repo, root)
	old := commandCaptureFn
	created := false
	commandCaptureFn = func(name string, args ...string) (string, error) {
		if !created && len(args) > 0 && args[len(args)-1] == "status" {
			created = true
			if err := createWorkspaceInternal(repo, cfg, "proj", "A1", parsed.Target.ExpectedHeadCommit, false, false, false); err != nil {
				return "", err
			}
			return "", errors.New("simulated pre-effect response failure")
		}
		return old(name, args...)
	}
	t.Cleanup(func() { commandCaptureFn = old })
	if r := runMachineCreateForTest(t, repo, req); r.Status != createStatusReady {
		t.Fatalf("got %+v", r)
	}
}

func TestMachineCreateReceiptWriteLossReplaysProviderState(t *testing.T) {
	_, repo := setupRealCreateRepo(t)
	req := machineCreateRequest(t, repo, "A1", "write-loss")
	oldIn, oldOut, oldErr := stdinReader, stdoutWriter, stderrWriter
	stdinReader = bytes.NewReader(req)
	stdoutWriter = failingCreateWriter{}
	var stderr bytes.Buffer
	stderrWriter = &stderr
	err := runCreate([]string{"--repo", repo, "--request-json", "-", "--json"})
	stdinReader, stdoutWriter, stderrWriter = oldIn, oldOut, oldErr
	if err == nil {
		t.Fatal("expected receipt write failure")
	}
	if stderr.Len() != 0 {
		t.Fatalf("machine diagnostics: %q", stderr.String())
	}
	op := currentCreateTestOperation(t, repo)
	if r := runMachineCreateForTest(t, repo, req); r.Status != createStatusReady {
		t.Fatalf("got %+v", r)
	}
	if got := currentCreateTestOperation(t, repo); got != op {
		t.Fatalf("replay changed operation: %s -> %s", op, got)
	}
}

type failingCreateWriter struct{}

func (failingCreateWriter) Write([]byte) (int, error) { return 0, errors.New("simulated receipt loss") }

func TestMachineCreateTargetHeadDriftConflictsWithoutCreating(t *testing.T) {
	root, repo := setupRealCreateRepo(t)
	req := machineCreateRequest(t, repo, "A1", "drift")
	_ = os.WriteFile(filepath.Join(repo, "later"), []byte("later"), 0644)
	_ = jjCaptureCommitID(t, repo, "@")
	r := runMachineCreateForTest(t, repo, req)
	if r.Status != createStatusConflict || exists(filepath.Join(root, "proj", "A1")) {
		t.Fatalf("got %+v", r)
	}
}
func configForMachineCreateTest(t *testing.T, repo, root string) config {
	t.Helper()
	cfg, e := loadConfig(repo)
	if e != nil {
		t.Fatal(e)
	}
	cfg.WorkspacesRoot = root
	cfg.Project = "proj"
	return cfg
}
func currentCreateTestOperation(t *testing.T, repo string) string {
	t.Helper()
	out, e := integrationQuery(repo, "", "op", "log", "-n", "1", "--no-graph", "-T", `id ++ "\n"`)
	if e != nil {
		t.Fatal(e)
	}
	return strings.TrimSpace(out)
}
