package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateExplicitBaseProtocol(t *testing.T) {
	legacy := validCreateRequestJSON()
	base := strings.Repeat("b", 40)
	withBase := func(value string) string {
		return strings.Replace(legacy, `"workspace":"A1"`, `"workspace":"A1","baseCommit":`+value, 1)
	}
	for _, value := range []string{`""`, `null`, `42`, `"@-"`, `"bbbb"`, `"` + strings.Repeat("B", 40) + `"`, `"` + base + ` "`} {
		if _, _, err := parseCreateRequestV1([]byte(withBase(value))); err == nil {
			t.Fatalf("accepted invalid base %s", value)
		}
	}
	for _, field := range []string{`"baseCommit":"` + base + `","baseCommit":"` + base + `"`, `"BaseCommit":"` + base + `"`} {
		input := strings.Replace(legacy, `"workspace":"A1"`, `"workspace":"A1",`+field, 1)
		if _, _, err := parseCreateRequestV1([]byte(input)); err == nil {
			t.Fatalf("accepted duplicate or case-variant field: %s", input)
		}
	}
	old, oldDigest, err := parseCreateRequestV1([]byte(legacy))
	if err != nil || old.baseCommit() != old.Target.ExpectedHeadCommit {
		t.Fatalf("legacy base changed: %+v %v", old, err)
	}
	r, digest, err := parseCreateRequestV1([]byte(withBase(`"` + base + `"`)))
	if err != nil || r.baseCommit() != base || digest == oldDigest {
		t.Fatalf("explicit base not bound to request: %+v %s %v", r, digest, err)
	}
	encoded, err := json.Marshal(old)
	if err != nil || string(encoded) != legacy {
		t.Fatalf("legacy request encoding changed: %s %v", encoded, err)
	}
	for _, schema := range []string{createReceiptSchemaV1, createReceiptSchemaV2} {
		receipt := validReadyCreateReceipt()
		receipt.Schema = schema
		encoded, _ := json.Marshal(receipt)
		if strings.Contains(string(encoded), "baseCommit") {
			t.Fatalf("omitted base leaked into legacy receipt: %s", encoded)
		}
	}
	equalBase := withBase(`"` + old.Target.ExpectedHeadCommit + `"`)
	if _, _, err := parseCreateRequestV1([]byte(equalBase)); err != nil {
		t.Fatalf("explicit base equal to head rejected: %v", err)
	}
	out, _, err := captureOutput(func() error { return runCapabilities([]string{"--json", "--schema", ajjCapabilitiesSchemaV3}) })
	if err != nil || !strings.Contains(out, `"explicitBaseCommit":true`) {
		t.Fatalf("explicit base not advertised: %s %v", out, err)
	}
	for _, caps := range []any{integrationCapabilities(), capabilitiesV2()} {
		data, _ := json.Marshal(caps)
		if strings.Contains(string(data), "explicitBaseCommit") {
			t.Fatalf("legacy capabilities changed: %s", data)
		}
	}
}

func TestCreateReceiptExplicitBaseEvidence(t *testing.T) {
	for _, schema := range []string{createReceiptSchemaV1, createReceiptSchemaV2} {
		t.Run(schema, func(t *testing.T) {
			r := validReadyCreateReceipt()
			r.Schema = schema
			if schema == createReceiptSchemaV2 {
				r.Child.WorkspaceRoot = "/tmp/project/A1"
			}
			r.Child.BaseCommit = strings.Repeat("d", 40)
			r.Child.ParentCommit = r.Child.BaseCommit
			r = finalizeCreateReceipt(r)
			if err := validateCreateReceipt(r); err != nil {
				t.Fatal(err)
			}
			for _, base := range []string{"", "@-", r.Target.ExpectedHeadCommit} {
				bad := r
				bad.Child.BaseCommit = base
				bad = finalizeCreateReceipt(bad)
				if err := validateCreateReceipt(bad); err == nil {
					t.Fatalf("accepted contradictory base %q", base)
				}
			}
			r.Child.BaseCommit = strings.Repeat("e", 40)
			r.Child.ParentCommit = r.Child.BaseCommit
			if err := validateCreateReceipt(r); err == nil || !strings.Contains(err.Error(), "evidence digest") {
				t.Fatalf("base not covered by digest: %v", err)
			}
		})
	}
}

func machineCreateRequestWithBase(t *testing.T, repo, base string) []byte {
	t.Helper()
	var req createRequestV1
	if err := json.Unmarshal(machineCreateRequest(t, repo, "A1", "explicit-base"), &req); err != nil {
		t.Fatal(err)
	}
	req.Child.BaseCommit = base
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestMachineCreateExplicitBaseReadyReplayAndParentRewrite(t *testing.T) {
	for _, schema := range []string{createReceiptSchemaV1, createReceiptSchemaV2} {
		t.Run(schema, func(t *testing.T) {
			root, repo := setupRealCreateRepo(t)
			base := jjCommitID(t, repo, "@-")
			if err := os.WriteFile(filepath.Join(repo, "parent-only.txt"), []byte("in-progress work\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			head := jjCaptureCommitID(t, repo, "@")
			req := machineCreateRequestWithBase(t, repo, base)
			first := runMachineCreateWithReceiptSchemaForTest(t, repo, req, schema)
			if first.Status != createStatusReady || first.Child.BaseCommit != base || first.Child.ParentCommit != base || first.Target.ExpectedHeadCommit == base {
				t.Fatalf("wrong base: %+v", first)
			}
			op := currentCreateTestOperation(t, repo)
			second := runMachineCreateWithReceiptSchemaForTest(t, repo, req, schema)
			if second.EvidenceDigest != first.EvidenceDigest || currentCreateTestOperation(t, repo) != op {
				t.Fatalf("replay changed state: %+v", second)
			}
			child := filepath.Join(root, "proj", "A1")
			if exists(filepath.Join(child, "parent-only.txt")) || jjCommitID(t, repo, "@") != head {
				t.Fatal("creation inherited parent-only content or changed the target")
			}
			runJJ(t, "-R", repo, "describe", "-m", "rewrite mutable parent")
			if jjCommitID(t, child, "@") != first.Child.HeadCommit || jjCommitID(t, child, "@-") != base {
				t.Fatal("child followed mutable parent rewrite")
			}
			if r := runMachineCreateWithReceiptSchemaForTest(t, repo, req, schema); r.Status != createStatusConflict || r.Error.Code != "target-head-drift" {
				t.Fatalf("replay ignored target drift: %+v", r)
			}
		})
	}
}

func TestMachineCreateExplicitBaseRejectsWrongParentAndUnknownBase(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown base", true: "existing child on target head"}[existing], func(t *testing.T) {
			root, repo := setupRealCreateRepo(t)
			base := strings.Repeat("f", 40)
			if existing {
				base = jjCommitID(t, repo, "@-")
				if r := runMachineCreateForTest(t, repo, machineCreateRequest(t, repo, "A1", "legacy")); r.Status != createStatusReady {
					t.Fatalf("fixture: %+v", r)
				}
			}
			req := machineCreateRequestWithBase(t, repo, base)
			op := currentCreateTestOperation(t, repo)
			r := runMachineCreateForTest(t, repo, req)
			want := createStatusNotCreated
			if existing {
				want = createStatusConflict
			}
			if r.Status != want || r.Checks.ParentMatches || currentCreateTestOperation(t, repo) != op || exists(filepath.Join(root, "proj", "A1")) != existing {
				t.Fatalf("unexpected state/effect: %+v", r)
			}
		})
	}
}

func TestMachineCreateExplicitBasePreservesTargetRaceChecks(t *testing.T) {
	for _, boundary := range []string{"before request", "before effect", "during setup", "final proof"} {
		t.Run(boundary, func(t *testing.T) {
			root, repo := setupRealCreateRepo(t)
			req := machineCreateRequestWithBase(t, repo, jjCommitID(t, repo, "@-"))
			mutate := func() { runJJ(t, "-R", repo, "describe", "-m", "target drift") }
			switch boundary {
			case "before request":
				mutate()
			case "before effect":
				old := commandCaptureFn
				commandCaptureFn = func(name string, args ...string) (string, error) {
					if len(args) > 0 && args[len(args)-1] == "status" {
						mutate()
					}
					return old(name, args...)
				}
				t.Cleanup(func() { commandCaptureFn = old })
			case "during setup":
				old := createMaterializeSetupFn
				createMaterializeSetupFn = func(string, string, config, string) error { mutate(); return nil }
				t.Cleanup(func() { createMaterializeSetupFn = old })
			case "final proof":
				old := createReadyProofHook
				createReadyProofHook = func(string) error { mutate(); return nil }
				t.Cleanup(func() { createReadyProofHook = old })
			}
			r := runMachineCreateForTest(t, repo, req)
			want := createStatusConflict
			if boundary == "before effect" {
				want = createStatusNotCreated
			}
			if r.Status != want {
				t.Fatalf("target drift accepted: %+v", r)
			}
			if (boundary == "before request" || boundary == "before effect") && exists(filepath.Join(root, "proj", "A1")) {
				t.Fatal("created child after target drift")
			}
		})
	}
}

func TestMachineCreateExplicitBasePartialSetupRecovery(t *testing.T) {
	_, repo := setupRealCreateRepo(t)
	req := machineCreateRequestWithBase(t, repo, jjCommitID(t, repo, "@-"))
	old := createMaterializeSetupFn
	createMaterializeSetupFn = func(string, string, config, string) error { return errors.New("injected") }
	t.Cleanup(func() { createMaterializeSetupFn = old })
	first := runMachineCreateV2ForTest(t, repo, req)
	if first.Status != createStatusPartial || first.Child.BaseCommit != first.Child.ParentCommit {
		t.Fatalf("partial: %+v", first)
	}
	createMaterializeSetupFn = old
	op := currentCreateTestOperation(t, repo)
	r := runMachineCreateV2ForTest(t, repo, req)
	if r.Status != createStatusReady || r.Child.HeadCommit != first.Child.HeadCommit || currentCreateTestOperation(t, repo) != op {
		t.Fatalf("setup recovery changed graph: %+v", r)
	}
}
