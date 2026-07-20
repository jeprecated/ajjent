package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func validCreateRequestJSON() string {
	return `{"schema":"ajj-create-request-v1","requestId":"placement-A1-001","target":{"expectedWorkspace":"default","expectedHeadCommit":"` + strings.Repeat("a", 40) + `"},"child":{"workspace":"A1"}}`
}
func TestParseCreateRequestV1StrictJSON(t *testing.T) {
	valid := validCreateRequestJSON()
	r, d, e := parseCreateRequestV1([]byte(valid))
	if e != nil {
		t.Fatal(e)
	}
	if r.Child.Workspace != "A1" || !integrationDigestRE.MatchString(d) {
		t.Fatalf("unexpected %+v %q", r, d)
	}
	cases := []struct{ name, input, want string }{{"empty", " ", "empty"}, {"duplicate", strings.Replace(valid, `"requestId":"placement-A1-001"`, `"requestId":"placement-A1-001","requestId":"x"`, 1), "duplicate"}, {"nested duplicate", strings.Replace(valid, `"workspace":"A1"`, `"workspace":"A1","workspace":"A2"`, 1), "duplicate"}, {"unknown", strings.TrimSuffix(valid, "}") + `,"path":"/tmp/x"}`, "unknown"}, {"nested unknown", strings.Replace(valid, `"workspace":"A1"`, `"workspace":"A1","path":"/tmp/x"`, 1), "unknown"}, {"trailing", valid + `{}`, "trailing"}, {"schema", strings.Replace(valid, createRequestSchemaV1, "bad", 1), "schema"}, {"id", strings.Replace(valid, "placement-A1-001", "bad id", 1), "requestId"}, {"commit", strings.Replace(valid, strings.Repeat("a", 40), "@", 1), "40-character"}, {"self", strings.Replace(valid, `"workspace":"A1"`, `"workspace":"default"`, 1), "target workspace"}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, e := parseCreateRequestV1([]byte(tc.input))
			if e == nil || !strings.Contains(e.Error(), tc.want) {
				t.Fatalf("got %v want %q", e, tc.want)
			}
		})
	}
}
func TestCapabilitiesV2AddsCreateWithoutChangingV1(t *testing.T) {
	before, _ := json.Marshal(integrationCapabilities())
	v2 := capabilitiesV2()
	if v2.Schema != ajjCapabilitiesSchemaV2 || v2.Create.RecoveryModel != createRecoveryModel || !v2.Create.Executable || strings.Join(v2.Create.NextActions, ",") != "retry-ensure,retry-create,operator-review" {
		t.Fatalf("bad v2: %+v", v2)
	}
	after, _ := json.Marshal(integrationCapabilities())
	if string(before) != string(after) || strings.Contains(string(after), `"create"`) {
		t.Fatalf("v1 changed: %s", after)
	}
	out, _, e := captureOutput(func() error { return runCapabilities([]string{"--json", "--schema", ajjCapabilitiesSchemaV2}) })
	if e != nil || !strings.Contains(out, `"recoveryModel":"state-reconciliation"`) {
		t.Fatalf("out=%s err=%v", out, e)
	}
}

func validReadyCreateReceipt() createReceiptV1 {
	head := strings.Repeat("a", 40)
	r := createReceiptV1{
		Schema: createReceiptSchemaV1, RequestID: "receipt-1", RequestDigest: "sha256:" + strings.Repeat("b", 64), Status: createStatusReady,
		Target: createReceiptTargetV1{Workspace: "default", ExpectedHeadCommit: head},
		Child:  createReceiptChildV1{Workspace: "A1", HeadCommit: strings.Repeat("c", 40), ParentCommit: head},
		Checks: createReceiptChecksV1{RegistrationPresent: true, DestinationPresent: true, RepositoryMatches: true, ParentMatches: true, FreshCursor: true, SetupComplete: true},
	}
	return finalizeCreateReceipt(r)
}

func TestValidateCreateReceiptV1RejectsContradictions(t *testing.T) {
	if err := validateCreateReceiptV1(validReadyCreateReceipt()); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		mutate     func(*createReceiptV1)
		keepDigest bool
	}{
		{"evidence digest", func(r *createReceiptV1) { r.Child.HeadCommit = strings.Repeat("d", 40) }, true},
		{"parent target mismatch", func(r *createReceiptV1) { r.Child.ParentCommit = strings.Repeat("d", 40) }, false},
		{"ready error", func(r *createReceiptV1) {
			r.Error = &createReceiptErrorV1{Code: "setup-incomplete", Message: "x", NextAction: createNextRetryEnsure}
		}, false},
		{"partial wrong action", func(r *createReceiptV1) {
			r.Status = createStatusPartial
			r.Checks.SetupComplete = false
			r.Error = &createReceiptErrorV1{Code: "setup-incomplete", Message: "x", NextAction: createNextRetryCreate}
		}, false},
		{"not-created extra evidence", func(r *createReceiptV1) {
			r.Status = createStatusNotCreated
			r.Checks.SetupComplete = false
			r.Error = &createReceiptErrorV1{Code: "create-failed-before-effect", Message: "x", NextAction: createNextRetryCreate}
		}, false},
		{"conflict complete setup", func(r *createReceiptV1) {
			r.Status = createStatusConflict
			r.Error = &createReceiptErrorV1{Code: "state-changed", Message: "x", NextAction: createNextOperatorReview}
		}, false},
		{"unknown conflict code", func(r *createReceiptV1) {
			r.Status = createStatusConflict
			r.Checks = createReceiptChecksV1{}
			r.Child.HeadCommit = ""
			r.Child.ParentCommit = ""
			r.Error = &createReceiptErrorV1{Code: "unknown", Message: "x", NextAction: createNextOperatorReview}
		}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := validReadyCreateReceipt()
			tc.mutate(&r)
			if !tc.keepDigest {
				r = finalizeCreateReceipt(r)
			}
			if err := validateCreateReceiptV1(r); err == nil {
				t.Fatalf("accepted contradictory receipt: %+v", r)
			}
		})
	}
}
