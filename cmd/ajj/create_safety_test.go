package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Each synthetic repository has an inert HOME and explicit JJ config; even
// the legacy regression leg must never discover a user's trust/setup policy.
func setupSafeCreateRepo(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	jjConfig := filepath.Join(home, "jj.toml")
	if err := os.WriteFile(jjConfig, []byte("[user]\nname = \"Ajj Test\"\nemail = \"ajj-test@example.invalid\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JJ_CONFIG", jjConfig)
	return setupRealCreateRepo(t)
}

func safeCreateRequest(t *testing.T, repo string) []byte {
	t.Helper()
	var req createRequestV1
	if err := json.Unmarshal(machineCreateRequestWithBase(t, repo, jjCommitID(t, repo, "@-")), &req); err != nil {
		t.Fatal(err)
	}
	req.NoCleanup = true
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertSafeChildRetained(t *testing.T, repo, child string, edited bool) {
	t.Helper()
	refs, err := listWorkspaceRefs(repo)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ref := range refs {
		if ref.Handle == "A1" {
			found = true
		}
	}
	if !found || !exists(filepath.Join(child, ".jj")) {
		t.Fatal("child registration or metadata removed")
	}
	if edited {
		data, err := os.ReadFile(filepath.Join(child, "concurrent.txt"))
		if err != nil || string(data) != "concurrent work\n" {
			t.Fatalf("edited file lost: %q %v", data, err)
		}
	}
}

func TestNoCleanupProtocol(t *testing.T) {
	legacy := validCreateRequestJSON()
	for _, value := range []string{`null`, `"true"`, `0`, `{}`, `[]`, `true,"noCleanup":true`} {
		data := strings.Replace(legacy, `"schema":`, `"noCleanup":`+value+`,"schema":`, 1)
		if _, _, err := parseCreateRequestV1([]byte(data)); err == nil {
			t.Fatalf("accepted %s", data)
		}
	}
	for _, value := range []string{"true", "false"} {
		data := strings.Replace(legacy, `"schema":`, `"noCleanup":`+value+`,"schema":`, 1)
		r, digest, err := parseCreateRequestV1([]byte(data))
		_, oldDigest, _ := parseCreateRequestV1([]byte(legacy))
		if err != nil || r.NoCleanup != (value == "true") || digest == oldDigest {
			t.Fatalf("boolean not bound: %+v %v", r, err)
		}
	}
	for _, caps := range []any{integrationCapabilities(), capabilitiesV2()} {
		data, _ := json.Marshal(caps)
		if strings.Contains(string(data), "noCleanup") {
			t.Fatalf("legacy capabilities changed: %s", data)
		}
	}
	if !capabilitiesV3().Create.NoCleanup {
		t.Fatal("safe mode not advertised")
	}
	for _, schema := range []string{createReceiptSchemaV1, createReceiptSchemaV2} {
		r := validReadyCreateReceipt()
		r.Schema = schema
		if schema == createReceiptSchemaV2 {
			r.Child.WorkspaceRoot = "/tmp/child"
		}
		r.NoCleanup = true
		r = finalizeCreateReceipt(r)
		if err := validateCreateReceipt(r); err != nil {
			t.Fatal(err)
		}
		r.NoCleanup = false
		if err := validateCreateReceipt(r); err == nil {
			t.Fatal("mode not bound by evidence digest")
		}
	}
}

func TestNoCleanupReadyReplayAndRequestBinding(t *testing.T) {
	root, repo := setupSafeCreateRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "source-only.txt"), []byte("source work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	head := jjCaptureCommitID(t, repo, "@")
	request := safeCreateRequest(t, repo)
	first := runMachineCreateV2ForTest(t, repo, request)
	if first.Status != createStatusReady || !first.NoCleanup || first.Child.ParentCommit != jjCommitID(t, repo, "@-") {
		t.Fatalf("not ready: %+v", first)
	}
	child := filepath.Join(root, "proj", "A1")
	if exists(filepath.Join(child, "source-only.txt")) || jjCommitID(t, repo, "@") != head {
		t.Fatal("source changed or mutable content inherited")
	}
	op := currentCreateTestOperation(t, repo)
	second := runMachineCreateV2ForTest(t, repo, request)
	if first.EvidenceDigest != second.EvidenceDigest || op != currentCreateTestOperation(t, repo) {
		t.Fatalf("replay not idempotent: %+v", second)
	}
	for _, change := range []func(*createRequestV1){
		func(r *createRequestV1) { r.NoCleanup = false },
		func(r *createRequestV1) { r.RequestID = "changed-id" },
		func(r *createRequestV1) { r.Child.BaseCommit = r.Target.ExpectedHeadCommit },
		func(r *createRequestV1) { r.Child.Workspace = "A2" },
	} {
		var r createRequestV1
		_ = json.Unmarshal(request, &r)
		change(&r)
		data, _ := json.Marshal(r)
		got := runMachineCreateV2ForTest(t, repo, data)
		if got.Status != createStatusConflict || got.Error.Code != "create-evidence-conflict" {
			t.Fatalf("changed request accepted: %+v", got)
		}
	}
	if got := runMachineCreateV2ForTest(t, repo, append(request, '\n')); got.Status != createStatusConflict {
		t.Fatal("changed bytes accepted")
	}
	if exists(filepath.Join(root, "proj", "A2")) {
		t.Fatal("changed request created a second child")
	}
	data, err := os.ReadFile(filepath.Join(repo, "source-only.txt"))
	if err != nil || string(data) != "source work\n" || jjCommitID(t, repo, "@") != head {
		t.Fatal("source files/cursor changed")
	}
}

func TestNoCleanupPostAddReadbackFailure(t *testing.T) {
	for _, edited := range []bool{false, true} {
		t.Run(map[bool]string{false: "clean", true: "concurrent edits"}[edited], func(t *testing.T) {
			root, repo := setupSafeCreateRepo(t)
			request := safeCreateRequest(t, repo)
			child := filepath.Join(root, "proj", "A1")
			before := jjCommitID(t, repo, "@")
			old := commandCaptureFn
			failed := false
			commandCaptureFn = func(name string, args ...string) (string, error) {
				if !failed && strings.Contains(strings.Join(args, " "), child+" --ignore-working-copy log -r @-") {
					failed = true
					if edited {
						if err := os.WriteFile(filepath.Join(child, "concurrent.txt"), []byte("concurrent work\n"), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					return "", errors.New("injected parent readback exit 61")
				}
				return old(name, args...)
			}
			t.Cleanup(func() { commandCaptureFn = old })
			first := runMachineCreateV2ForTest(t, repo, request)
			if !failed || first.Status != createStatusConflict || first.Error.Code != "create-verification-failed" {
				t.Fatalf("dishonest failure: %+v injected=%t", first, failed)
			}
			assertSafeChildRetained(t, repo, child, edited)
			second := runMachineCreateV2ForTest(t, repo, request)
			want := createStatusReady
			if edited {
				want = createStatusConflict
			}
			if second.Status != want {
				t.Fatalf("unsafe reconciliation: %+v", second)
			}
			if jjCommitID(t, repo, "@") != before {
				t.Fatal("source cursor changed")
			}
		})
	}
}

func TestNoCleanupUnknownAddAndEvidenceFailures(t *testing.T) {
	for _, stage := range []string{"before-add", "partial-add", "registration-only", "pending-write", "ack-write", "head-read"} {
		t.Run(stage, func(t *testing.T) {
			root, repo := setupSafeCreateRepo(t)
			request := safeCreateRequest(t, repo)
			child := filepath.Join(root, "proj", "A1")
			oldRun, oldSave, oldCapture := createMachineCommandFn, saveCreateSafetyRecordFn, commandCaptureFn
			t.Cleanup(func() {
				createMachineCommandFn, saveCreateSafetyRecordFn, commandCaptureFn = oldRun, oldSave, oldCapture
			})
			adds := 0
			createMachineCommandFn = func(name string, args ...string) error {
				adds++
				if stage == "before-add" {
					return errors.New("add failed")
				}
				if err := oldRun(name, args...); err != nil {
					return err
				}
				if stage == "registration-only" {
					if err := os.Rename(child, filepath.Join(root, "retained-A1")); err != nil {
						t.Fatal(err)
					}
					return errors.New("partial registration")
				}
				if stage == "partial-add" {
					return errors.New("add completed but exit failed")
				}
				return nil
			}
			saveCreateSafetyRecordFn = func(path string, r createSafetyRecord) error {
				if stage == "pending-write" || (stage == "ack-write" && r.HeadCommit != "") {
					return errors.New("fsync failed")
				}
				return oldSave(path, r)
			}
			commandCaptureFn = func(name string, args ...string) (string, error) {
				if stage == "head-read" && adds > 0 && strings.Contains(strings.Join(args, " "), "log -r A1@") {
					return "", errors.New("head unavailable")
				}
				return oldCapture(name, args...)
			}
			first := runMachineCreateForTest(t, repo, request)
			if first.Status != createStatusConflict || first.Error.Code != "create-effects-unknown" {
				t.Fatalf("bad failure: %+v", first)
			}
			if stage == "pending-write" {
				if adds != 0 || exists(child) {
					t.Fatal("effect before durable intent")
				}
				return
			}
			createMachineCommandFn, saveCreateSafetyRecordFn, commandCaptureFn = oldRun, oldSave, oldCapture
			second := runMachineCreateForTest(t, repo, request)
			if second.Status != createStatusConflict || second.Error.Code != "create-effects-unknown" {
				t.Fatalf("unknown effect adopted: %+v", second)
			}
			if stage == "registration-only" {
				refs, err := listWorkspaceRefs(repo)
				if err != nil || len(refs) != 2 || exists(child) {
					t.Fatalf("partial registration lost: %+v %v", refs, err)
				}
			} else if stage != "before-add" {
				assertSafeChildRetained(t, repo, child, false)
			}
			legacy := strings.Replace(string(request), `"noCleanup":true,`, "", 1)
			if r := runMachineCreateForTest(t, repo, []byte(legacy)); r.Status != createStatusConflict || r.Error.Code != "create-evidence-conflict" {
				t.Fatalf("mode switch bypassed unknown effect: %+v", r)
			}
		})
	}
}

func TestNoCleanupDoesNotAdoptMatchingUnownedChild(t *testing.T) {
	_, repo := setupSafeCreateRepo(t)
	request := safeCreateRequest(t, repo)
	legacy := strings.Replace(string(request), `"noCleanup":true,`, "", 1)
	if r := runMachineCreateForTest(t, repo, []byte(legacy)); r.Status != createStatusReady {
		t.Fatal(r)
	}
	if r := runMachineCreateForTest(t, repo, request); r.Status != createStatusConflict || r.Error.Code != "create-evidence-conflict" {
		t.Fatalf("adopted unowned matching child: %+v", r)
	}
}

func TestNoCleanupParentMismatchAndMissingChild(t *testing.T) {
	root, repo := setupSafeCreateRepo(t)
	request := safeCreateRequest(t, repo)
	old := createMachineCommandFn
	createMachineCommandFn = func(name string, args ...string) error {
		if err := old(name, args...); err != nil {
			return err
		}
		return old("jj", "-R", args[len(args)-1], "new", "root()")
	}
	t.Cleanup(func() { createMachineCommandFn = old })
	r := runMachineCreateForTest(t, repo, request)
	if r.Status != createStatusConflict || r.Error.Code != "create-effects-unknown" {
		t.Fatalf("parent mismatch accepted: %+v", r)
	}
	assertSafeChildRetained(t, repo, filepath.Join(root, "proj", "A1"), false)
	if r := runMachineCreateForTest(t, repo, request); r.Status != createStatusConflict {
		t.Fatal("mismatch adopted")
	}
	// Simulate an external actor removing registration and relocating the directory;
	// ensure must never treat observed absence as proof no earlier effect occurred.
	runJJ(t, "-R", repo, "workspace", "forget", "A1")
	if err := os.Rename(filepath.Join(root, "proj", "A1"), filepath.Join(root, "retained-A1")); err != nil {
		t.Fatal(err)
	}
	if r := runMachineCreateForTest(t, repo, request); r.Status != createStatusConflict || r.Error.Code != "create-effects-unknown" {
		t.Fatalf("absence hid previous effect: %+v", r)
	}
	if exists(filepath.Join(root, "proj", "A1")) {
		t.Fatal("recreated a previously created child")
	}
}

func TestNoCleanupLock(t *testing.T) {
	root, repo := setupSafeCreateRepo(t)
	request := safeCreateRequest(t, repo)
	req, digest, err := parseCreateRequestV1(request)
	if err != nil {
		t.Fatal(err)
	}
	cfg := configForMachineCreateTest(t, repo, root)
	lock, err := openCreateSafety(repo, cfg, "proj", req, digest)
	if err != nil {
		t.Fatal(err)
	}
	if r := runMachineCreateForTest(t, repo, request); r.Status != createStatusConflict || exists(filepath.Join(root, "proj", "A1")) {
		t.Fatalf("concurrent effect: %+v", r)
	}
	lock.Close()

}

// This proxy runs the real JJ add, plants a real concurrent edit, and fails just
// one read-back with exit 61. Both commands are freshly built source binaries;
// the legacy leg reproduces the destructive installed-version regression.
func TestNoCleanupSourceBinaryRealJJRegression(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "ajj")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	realJJ, err := exec.LookPath("jj")
	if err != nil {
		t.Skip("jj unavailable")
	}
	t.Run("cross-process lock", func(t *testing.T) {
		root, repo := setupSafeCreateRepo(t)
		request := safeCreateRequest(t, repo)
		req, digest, err := parseCreateRequestV1(request)
		if err != nil {
			t.Fatal(err)
		}
		lock, err := openCreateSafety(repo, configForMachineCreateTest(t, repo, root), "proj", req, digest)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		cmd := exec.Command(binary, "create", "--repo", repo, "--request-json", "-", "--json")
		cmd.Stdin = strings.NewReader(string(request))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("binary lock check: %v %s", err, out)
		}
		var r createReceiptV1
		if err := json.Unmarshal(out, &r); err != nil {
			t.Fatal(err)
		}
		if r.Status != createStatusConflict || r.Error.Code != "create-evidence-conflict" || exists(filepath.Join(root, "proj", "A1")) {
			t.Fatalf("cross-process lock bypassed: %s", out)
		}
	})
	for _, safe := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy regression", true: "safe preservation"}[safe], func(t *testing.T) {
			root, repo := setupSafeCreateRepo(t)
			request := safeCreateRequest(t, repo)
			if !safe {
				request = []byte(strings.Replace(string(request), `"noCleanup":true,`, "", 1))
			}
			proxyDir := t.TempDir()
			proxy := `#!/bin/sh
case " $* " in
  *" workspace add "*)
    "$REAL_JJ" "$@" || exit $?
    printf 'concurrent work\n' > "$CHILD/concurrent.txt"
    : > "$FAULT/armed"
    exit 0 ;;
  *" log -r @- "*)
    if [ -f "$FAULT/armed" ]; then
      rm "$FAULT/armed"
      : > "$FAULT/fired"
      exit 61
    fi ;;
esac
exec "$REAL_JJ" "$@"
`
			if err := os.WriteFile(filepath.Join(proxyDir, "jj"), []byte(proxy), 0o700); err != nil {
				t.Fatal(err)
			}
			child := filepath.Join(root, "proj", "A1")
			before := jjCommitID(t, repo, "@")
			cmd := exec.Command(binary, "create", "--repo", repo, "--request-json", "-", "--json", "--receipt-schema", createReceiptSchemaV2)
			cmd.Stdin = strings.NewReader(string(request))
			cmd.Env = append(os.Environ(), "PATH="+proxyDir+":"+os.Getenv("PATH"), "REAL_JJ="+realJJ, "CHILD="+child, "FAULT="+proxyDir)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("binary: %v\n%s", err, out)
			}
			var r createReceiptV1
			if err := json.Unmarshal(out, &r); err != nil {
				t.Fatalf("receipt: %v\n%s", err, out)
			}
			if err := validateCreateReceiptV2(r); err != nil {
				t.Fatal(err)
			}
			if !exists(filepath.Join(proxyDir, "fired")) {
				t.Fatal("fault never fired")
			}
			if safe {
				if r.Status != createStatusConflict || r.Error.Code != "create-verification-failed" {
					t.Fatalf("unsafe receipt: %s", out)
				}
				assertSafeChildRetained(t, repo, child, true)
				if retry := runMachineCreateV2ForTest(t, repo, request); retry.Status != createStatusConflict {
					t.Fatalf("edited child reconciled as ready: %+v", retry)
				}
			} else if exists(child) || r.Status != createStatusNotCreated || r.Error.Code != "create-failed-before-effect" {
				t.Fatalf("legacy regression behavior changed: %s present=%t", out, exists(child))
			}
			if jjCommitID(t, repo, "@") != before {
				t.Fatal("source cursor changed")
			}
			t.Logf("safe=%t status=%s code=%s registration=%t destination=%t fault=exit61", safe, r.Status, r.Error.Code, r.Checks.RegistrationPresent, r.Checks.DestinationPresent)
		})
	}
}

func TestNoCleanupPreEffectRaceDoesNotAdopt(t *testing.T) {
	root, repo := setupSafeCreateRepo(t)
	request := safeCreateRequest(t, repo)
	req, _, _ := parseCreateRequestV1(request)
	old := commandCaptureFn
	fired := false
	commandCaptureFn = func(name string, args ...string) (string, error) {
		if !fired && strings.Join(args, " ") == "-R "+repo+" --color=never --no-pager status" {
			fired = true
			runJJ(t, "-R", repo, "workspace", "add", "--name", "A1", "--revision", req.baseCommit(), filepath.Join(root, "proj", "A1"))
			return "", errors.New("pre-effect check failed while another actor created a child")
		}
		return old(name, args...)
	}
	t.Cleanup(func() { commandCaptureFn = old })
	if r := runMachineCreateForTest(t, repo, request); !fired || r.Status != createStatusConflict || r.Error.Code != "create-evidence-conflict" {
		t.Fatalf("unowned child adopted after pre-effect failure: %+v", r)
	}
	assertSafeChildRetained(t, repo, filepath.Join(root, "proj", "A1"), false)
}

func TestNoCleanupCorruptEvidenceAndConfigDrift(t *testing.T) {
	for _, corruption := range []bool{false, true} {
		t.Run(map[bool]string{false: "config drift", true: "corrupt evidence"}[corruption], func(t *testing.T) {
			root, repo := setupSafeCreateRepo(t)
			request := safeCreateRequest(t, repo)
			if r := runMachineCreateForTest(t, repo, request); r.Status != createStatusReady {
				t.Fatal(r)
			}
			if corruption {
				shared, err := workspaceRepositoryDirectory(repo)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(shared, "ajj-create", "A1.json"), []byte(`{"schema":null}`), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				writeConfig(t, repo, "workspaces_root: "+root+"\nproject: moved\nmain_workspace: default\n")
			}
			if r := runMachineCreateForTest(t, repo, request); r.Status != createStatusConflict || r.Error.Code != "create-evidence-conflict" {
				t.Fatalf("evidence bypassed: %+v", r)
			}
			assertSafeChildRetained(t, repo, filepath.Join(root, "proj", "A1"), false)
			if exists(filepath.Join(root, "moved", "A1")) {
				t.Fatal("config drift created another child")
			}
		})
	}
}

func TestNoCleanupNeverTrustsDirenv(t *testing.T) {
	root, repo := setupSafeCreateRepo(t)
	writeConfig(t, repo, "workspaces_root: "+root+"\nproject: proj\nmain_workspace: default\ncreate:\n  direnv_allow: true\n")
	jjCaptureCommitID(t, repo, "@")
	request := safeCreateRequest(t, repo)
	old := createMachineCommandFn
	createMachineCommandFn = func(name string, args ...string) error {
		if name != "jj" || !strings.Contains(strings.Join(args, " "), "workspace add") {
			t.Fatalf("executed non-add command: %s %v", name, args)
		}
		return old(name, args...)
	}
	t.Cleanup(func() { createMachineCommandFn = old })
	if r := runMachineCreateForTest(t, repo, request); r.Status != createStatusReady {
		t.Fatalf("safe setup failed: %+v", r)
	}
}

func TestNoCleanupRejectsUntestedJJBeforeEffects(t *testing.T) {
	root, repo := setupSafeCreateRepo(t)
	request := safeCreateRequest(t, repo)
	oldVersion, oldIn := jjVersionFn, stdinReader
	t.Cleanup(func() { jjVersionFn, stdinReader = oldVersion, oldIn })
	for _, version := range []string{"jj 0.41.0", "jj 0.42.0", "jj 0.44.0", "jj 0.43.0-dev", "invalid"} {
		jjVersionFn = func() (string, error) { return version, nil }
		stdinReader = strings.NewReader(string(request))
		out, _, err := captureOutput(func() error { return runCreateMachine([]string{"--repo", repo, "--request-json", "-", "--json"}) })
		if err == nil || !strings.Contains(err.Error(), "requires tested jj version 0.43.0") || out != "" {
			t.Fatalf("accepted %q: %s %v", version, out, err)
		}
		shared, err := workspaceRepositoryDirectory(repo)
		if err != nil {
			t.Fatal(err)
		}
		if exists(filepath.Join(root, "proj", "A1")) || exists(filepath.Join(shared, "ajj-create")) {
			t.Fatal("unsupported JJ caused effects")
		}
	}
	caps := capabilitiesV3().Create
	if caps.MinimumJJVersion != "0.41.0" || len(caps.NoCleanupJJVersions) != 1 || caps.NoCleanupJJVersions[0] != "0.43.0" {
		t.Fatalf("wrong version constraints: %+v", caps)
	}
}

func TestNoCleanupBindsActualAddOperation(t *testing.T) {
	for _, foreign := range []string{"same-base-new", "unrelated-operation"} {
		t.Run(foreign, func(t *testing.T) {
			root, repo := setupSafeCreateRepo(t)
			request := safeCreateRequest(t, repo)
			req, _, _ := parseCreateRequestV1(request)
			old := createMachineCommandFn
			createMachineCommandFn = func(name string, args ...string) error {
				if err := old(name, args...); err != nil {
					return err
				}
				if foreign == "same-base-new" {
					return old("jj", "-R", args[len(args)-1], "new", req.baseCommit())
				}
				return old("jj", "-R", repo, "bookmark", "create", "foreign", "-r", req.baseCommit())
			}
			t.Cleanup(func() { createMachineCommandFn = old })
			if r := runMachineCreateForTest(t, repo, request); r.Status != createStatusConflict || r.Error.Code != "create-effects-unknown" {
				t.Fatalf("foreign operation adopted: %+v", r)
			}
			createMachineCommandFn = old
			if r := runMachineCreateForTest(t, repo, request); r.Status != createStatusConflict || r.Error.Code != "create-effects-unknown" {
				t.Fatalf("foreign matching cursor adopted on replay: %+v", r)
			}
			assertSafeChildRetained(t, repo, filepath.Join(root, "proj", "A1"), false)
		})
	}
}

func TestNoCleanupRootBaseAndUnknownBase(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown base", true: "root base"}[known], func(t *testing.T) {
			root, repo := setupSafeCreateRepo(t)
			request := safeCreateRequest(t, repo)
			var req createRequestV1
			_ = json.Unmarshal(request, &req)
			req.Child.BaseCommit = strings.Repeat("f", 40)
			if known {
				req.Child.BaseCommit = strings.Repeat("0", 40)
			}
			request, _ = json.Marshal(req)
			r := runMachineCreateV2ForTest(t, repo, request)
			if known {
				if r.Status != createStatusReady || r.Child.ParentCommit != req.Child.BaseCommit {
					t.Fatalf("root add proof failed: %+v", r)
				}
				if r := runMachineCreateV2ForTest(t, repo, request); r.Status != createStatusReady {
					t.Fatalf("root replay failed: %+v", r)
				}
			} else if r.Status != createStatusNotCreated || r.Error.Code != "create-failed-before-effect" || exists(filepath.Join(root, "proj", "A1")) {
				t.Fatalf("unknown base caused effect: %+v", r)
			}
		})
	}
}

func TestNoCleanupSetupPreservesIdenticalFilesAndRecovers(t *testing.T) {
	root, repo := setupSafeCreateRepo(t)
	writeConfig(t, repo, "workspaces_root: "+root+"\nproject: proj\nmain_workspace: default\nassimilated_paths:\n  - provider.txt\n")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("provider.txt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", repo, "commit", "-m", "inert provider setup config")
	if err := os.WriteFile(filepath.Join(repo, "provider.txt"), []byte("provider local data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := safeCreateRequest(t, repo)
	old := createMachineCommandFn
	createMachineCommandFn = func(name string, args ...string) error {
		if err := old(name, args...); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(args[len(args)-1], "provider.txt"), []byte("provider local data\n"), 0o600)
	}
	t.Cleanup(func() { createMachineCommandFn = old })
	r := runMachineCreateV2ForTest(t, repo, request)
	if r.Status != createStatusPartial || r.Error.Code != "setup-incomplete" {
		t.Fatalf("setup conflict not partial: %+v", r)
	}
	path := filepath.Join(root, "proj", "A1", "provider.txt")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatal("identical regular file replaced")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "provider local data\n" {
		t.Fatal("regular file contents lost")
	}
	// Explicit fixture/operator resolution, not provider cleanup.
	if err := os.Rename(path, filepath.Join(root, "retained-provider.txt")); err != nil {
		t.Fatal(err)
	}
	createMachineCommandFn = old
	if r := runMachineCreateV2ForTest(t, repo, request); r.Status != createStatusReady {
		t.Fatalf("setup retry not ready: %+v", r)
	}
	if target, err := os.Readlink(path); err != nil || target != filepath.Join(repo, "provider.txt") {
		t.Fatalf("setup link: %q %v", target, err)
	}
}

func TestNoCleanupSetupExclusiveCreation(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.WriteFile(source, []byte("source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mismatch := filepath.Join(root, "mismatch")
	if err := os.Symlink("other", mismatch); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureAssimilatedSymlinkMode(source, mismatch, true); err == nil {
		t.Fatal("mismatching symlink accepted")
	}
	if target, _ := os.Readlink(mismatch); target != "other" {
		t.Fatal("mismatching link replaced")
	}
	matching := filepath.Join(root, "matching")
	if err := os.Symlink(source, matching); err != nil {
		t.Fatal(err)
	}
	if created, err := ensureAssimilatedSymlinkMode(source, matching, true); err != nil || created {
		t.Fatalf("matching symlink not reused: %t %v", created, err)
	}
	for _, envrc := range []bool{false, true} {
		for i := 0; i < 30; i++ {
			dir := t.TempDir()
			path := filepath.Join(dir, "dest")
			if envrc {
				path = filepath.Join(dir, ".envrc")
			}
			start := make(chan struct{})
			writer := make(chan error, 1)
			go func() {
				<-start
				f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
				if err == nil {
					_, err = f.WriteString("concurrent content\n")
					err = errors.Join(err, f.Close())
				}
				writer <- err
			}()
			close(start)
			if envrc {
				_ = ensureEnvrcMode(dir, true)
			} else {
				_, _ = ensureAssimilatedSymlinkMode(source, path, true)
			}
			err := <-writer
			if err == nil {
				data, err := os.ReadFile(path)
				if err != nil || string(data) != "concurrent content\n" {
					t.Fatalf("concurrent destination overwritten: %q %v", data, err)
				}
			} else if !errors.Is(err, os.ErrExist) {
				t.Fatal(err)
			}
		}
	}
	data, err := os.ReadFile(source)
	if err != nil || string(data) != "source\n" {
		t.Fatal("source changed during setup")
	}
	// Existing envrc entries, including symlinks, are never followed/truncated.
	dir := t.TempDir()
	if err := os.Symlink(source, filepath.Join(dir, ".envrc")); err != nil {
		t.Fatal(err)
	}
	if err := ensureEnvrcMode(dir, true); err != nil {
		t.Fatal(err)
	}
	if target, _ := os.Readlink(filepath.Join(dir, ".envrc")); target != source {
		t.Fatal("envrc symlink replaced")
	}
	data, err = os.ReadFile(source)
	if err != nil || string(data) != "source\n" {
		t.Fatal("envrc symlink target overwritten")
	}
}
