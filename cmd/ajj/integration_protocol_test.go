package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseIntegrationRequestV1StrictJSON(t *testing.T) {
	commit := strings.Repeat("a", 40)
	valid := `{"schema":"ajj-integrate-request-v1","operationId":"op_123","target":{"expectedWorkspace":"alpha","expectedHeadCommit":"` + commit + `"},"strategy":"single","payloads":[{"workspace":"bravo","expectedHeadCommit":"` + commit + `"}]}`
	tests := []struct {
		name        string
		input       string
		wantCode    string
		wantMessage string
	}{
		{name: "valid", input: valid},
		{name: "empty", input: "  \n", wantCode: integrationErrorInvalidJSON, wantMessage: "empty"},
		{name: "duplicate top-level key", input: strings.Replace(valid, `"operationId":"op_123"`, `"operationId":"op_123","operationId":"op_456"`, 1), wantCode: integrationErrorInvalidJSON, wantMessage: `duplicate JSON object key "operationId"`},
		{name: "duplicate nested key", input: strings.Replace(valid, `"expectedWorkspace":"alpha"`, `"expectedWorkspace":"alpha","expectedWorkspace":"charlie"`, 1), wantCode: integrationErrorInvalidJSON, wantMessage: `duplicate JSON object key "expectedWorkspace"`},
		{name: "unknown field", input: strings.Replace(valid, `"strategy":"single"`, `"strategy":"single","repositoryIdentity":"not-supported"`, 1), wantCode: integrationErrorInvalidJSON, wantMessage: "unknown field"},
		{name: "case variant top-level field", input: strings.Replace(valid, `"schema":`, `"Schema":`, 1), wantCode: integrationErrorInvalidJSON, wantMessage: `unknown field "Schema"`},
		{name: "uppercase top-level field", input: strings.Replace(valid, `"schema":`, `"SCHEMA":`, 1), wantCode: integrationErrorInvalidJSON, wantMessage: `unknown field "SCHEMA"`},
		{name: "mixed canonical and case-alias top-level fields", input: strings.Replace(valid, `"schema":"ajj-integrate-request-v1"`, `"schema":"ajj-integrate-request-v1","Schema":"overwrite"`, 1), wantCode: integrationErrorInvalidJSON, wantMessage: `unknown field "Schema"`},
		{name: "case variant target field", input: strings.Replace(valid, `"expectedWorkspace":"alpha"`, `"ExpectedWorkspace":"alpha"`, 1), wantCode: integrationErrorInvalidJSON, wantMessage: `unknown field "ExpectedWorkspace"`},
		{name: "mixed canonical and case-alias target fields", input: strings.Replace(valid, `"expectedWorkspace":"alpha"`, `"expectedWorkspace":"alpha","ExpectedWorkspace":"charlie"`, 1), wantCode: integrationErrorInvalidJSON, wantMessage: `unknown field "ExpectedWorkspace"`},
		{name: "case variant payload field", input: strings.Replace(valid, `"workspace":"bravo"`, `"Workspace":"bravo"`, 1), wantCode: integrationErrorInvalidJSON, wantMessage: `unknown field "Workspace"`},
		{name: "mixed canonical and case-alias payload fields", input: strings.Replace(valid, `"workspace":"bravo"`, `"workspace":"bravo","Workspace":"charlie"`, 1), wantCode: integrationErrorInvalidJSON, wantMessage: `unknown field "Workspace"`},
		{name: "case variant nested commit field", input: strings.Replace(valid, `"expectedHeadCommit":`, `"ExpectedHeadCommit":`, 1), wantCode: integrationErrorInvalidJSON, wantMessage: `unknown field "ExpectedHeadCommit"`},
		{name: "trailing object", input: valid + `{}`, wantCode: integrationErrorInvalidJSON, wantMessage: "trailing JSON input"},
		{name: "trailing scalar", input: valid + ` true`, wantCode: integrationErrorInvalidJSON, wantMessage: "trailing JSON input"},
		{name: "malformed", input: `{"schema":`, wantCode: integrationErrorInvalidJSON},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, digest, err := parseIntegrationRequestV1([]byte(test.input))
			if test.wantCode == "" {
				if err != nil {
					t.Fatalf("parseIntegrationRequestV1 failed: %v", err)
				}
				if request.OperationID != "op_123" || !strings.HasPrefix(digest, "sha256:") || len(digest) != len("sha256:")+64 {
					t.Fatalf("unexpected request/digest: %+v %q", request, digest)
				}
				return
			}
			assertIntegrationProtocolError(t, err, test.wantCode, test.wantMessage)
		})
	}
}

func TestValidateIntegrationRequestV1Table(t *testing.T) {
	commit := strings.Repeat("b", 40)
	base := integrationRequestV1{
		Schema:      integrationRequestSchemaV1,
		OperationID: "op-valid_1",
		Target: integrationTargetAssertionV1{
			ExpectedWorkspace:  "alpha",
			ExpectedHeadCommit: commit,
		},
		Strategy: integrationStrategySingle,
		Payloads: []integrationPayloadAssertionV1{{Workspace: "bravo", ExpectedHeadCommit: commit}},
	}
	tests := []struct {
		name    string
		mutate  func(*integrationRequestV1)
		wantErr string
	}{
		{name: "valid"},
		{name: "wrong schema", mutate: func(r *integrationRequestV1) { r.Schema = "v2" }, wantErr: "schema"},
		{name: "empty operation id", mutate: func(r *integrationRequestV1) { r.OperationID = "" }, wantErr: "operationId"},
		{name: "operation id path separator", mutate: func(r *integrationRequestV1) { r.OperationID = "../escape" }, wantErr: "operationId"},
		{name: "operation id too long", mutate: func(r *integrationRequestV1) { r.OperationID = strings.Repeat("x", 129) }, wantErr: "operationId"},
		{name: "invalid target handle", mutate: func(r *integrationRequestV1) { r.Target.ExpectedWorkspace = "bad/name" }, wantErr: "expectedWorkspace"},
		{name: "short target commit", mutate: func(r *integrationRequestV1) { r.Target.ExpectedHeadCommit = "abc" }, wantErr: "target expectedHeadCommit"},
		{name: "uppercase target commit", mutate: func(r *integrationRequestV1) { r.Target.ExpectedHeadCommit = strings.Repeat("A", 40) }, wantErr: "target expectedHeadCommit"},
		{name: "unknown strategy", mutate: func(r *integrationRequestV1) { r.Strategy = "merge" }, wantErr: "unsupported strategy"},
		{name: "single has zero payloads", mutate: func(r *integrationRequestV1) { r.Payloads = nil }, wantErr: "exactly one"},
		{name: "single has two payloads", mutate: func(r *integrationRequestV1) {
			r.Payloads = append(r.Payloads, integrationPayloadAssertionV1{Workspace: "charlie", ExpectedHeadCommit: commit})
		}, wantErr: "exactly one"},
		{name: "provider default empty", mutate: func(r *integrationRequestV1) { r.Strategy = integrationStrategyProviderDefault; r.Payloads = nil }, wantErr: "at least one"},
		{name: "ordered line empty", mutate: func(r *integrationRequestV1) { r.Strategy = integrationStrategyOrderedLine; r.Payloads = nil }, wantErr: "at least one"},
		{name: "payload is target", mutate: func(r *integrationRequestV1) { r.Payloads[0].Workspace = "alpha" }, wantErr: "target workspace"},
		{name: "duplicate payload", mutate: func(r *integrationRequestV1) {
			r.Strategy = integrationStrategyProviderDefault
			r.Payloads = append(r.Payloads, r.Payloads[0])
		}, wantErr: "duplicate payload"},
		{name: "too many payloads", mutate: func(r *integrationRequestV1) {
			r.Strategy = integrationStrategyProviderDefault
			r.Payloads = make([]integrationPayloadAssertionV1, integrationMaxPayloads+1)
		}, wantErr: "maximum"},
		{name: "invalid payload handle", mutate: func(r *integrationRequestV1) { r.Payloads[0].Workspace = "bad/name" }, wantErr: "invalid payload"},
		{name: "invalid payload commit", mutate: func(r *integrationRequestV1) { r.Payloads[0].ExpectedHeadCommit = "abc" }, wantErr: "payload \"bravo\" expectedHeadCommit"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := base
			request.Payloads = append([]integrationPayloadAssertionV1(nil), base.Payloads...)
			if test.mutate != nil {
				test.mutate(&request)
			}
			err := validateIntegrationRequestV1(request)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("validateIntegrationRequestV1 failed: %v", err)
				}
				return
			}
			assertIntegrationProtocolError(t, err, integrationErrorInvalidRequest, test.wantErr)
		})
	}
}

func TestIntegrationRequestDigestBindsExactAcceptedBytes(t *testing.T) {
	commit := strings.Repeat("c", 40)
	compact := []byte(`{"schema":"ajj-integrate-request-v1","operationId":"op","target":{"expectedWorkspace":"alpha","expectedHeadCommit":"` + commit + `"},"strategy":"single","payloads":[{"workspace":"bravo","expectedHeadCommit":"` + commit + `"}]}`)
	pretty := append([]byte(nil), compact...)
	pretty = append(pretty[:1], append([]byte("\n  "), pretty[1:]...)...)
	_, compactDigest, err := parseIntegrationRequestV1(compact)
	if err != nil {
		t.Fatal(err)
	}
	_, prettyDigest, err := parseIntegrationRequestV1(pretty)
	if err != nil {
		t.Fatal(err)
	}
	if compactDigest == prettyDigest {
		t.Fatalf("expected exact-byte digests to differ, both were %q", compactDigest)
	}
}

func TestValidateIntegrationOperationReuse(t *testing.T) {
	record := integrationOperationRecord{OperationID: "op-1", RequestDigest: "sha256:first"}
	if err := validateIntegrationOperationReuse(record, record.RequestDigest); err != nil {
		t.Fatalf("same operation request should be idempotent: %v", err)
	}
	err := validateIntegrationOperationReuse(record, "sha256:different")
	assertIntegrationProtocolError(t, err, integrationErrorOperationIDContradiction, "already bound")
}

func TestResolveIntegrationTargetWorkspaceUsesExactRootOnly(t *testing.T) {
	projectRoot := t.TempDir()
	alpha := filepath.Join(projectRoot, "alpha")
	bravo := filepath.Join(projectRoot, "bravo")
	for _, path := range []string{alpha, bravo} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	refs := []workspaceRef{
		{Handle: "alpha", TargetChange: "same-change", Root: alpha},
		{Handle: "bravo", TargetChange: "same-change", Root: bravo},
	}

	resolved, err := resolveIntegrationTargetWorkspace(alpha, refs, "alpha")
	if err != nil {
		t.Fatalf("resolve exact current workspace: %v", err)
	}
	if resolved.Handle != "alpha" || resolved.Path != filepath.Clean(alpha) {
		t.Fatalf("unexpected resolution: %+v", resolved)
	}

	symlink := filepath.Join(projectRoot, "alpha-link")
	if err := os.Symlink(alpha, symlink); err != nil {
		t.Fatal(err)
	}
	resolved, err = resolveIntegrationTargetWorkspace(symlink, refs, "alpha")
	if err != nil || resolved.Handle != "alpha" {
		t.Fatalf("expected canonical symlink resolution to alpha, got %+v err=%v", resolved, err)
	}

	_, err = resolveIntegrationTargetWorkspace(alpha, refs, "bravo")
	assertIntegrationProtocolError(t, err, integrationErrorAssertionFailed, "request asserted \"bravo\"")
}

func TestResolveIntegrationTargetWorkspaceNeverFallsBackByChangeOrMain(t *testing.T) {
	root := t.TempDir()
	refs := []workspaceRef{
		{Handle: "default", TargetChange: "current-change"},
		{Handle: "alpha", TargetChange: "current-change"},
	}
	_, err := resolveIntegrationTargetWorkspace(root, refs, "alpha")
	assertIntegrationProtocolError(t, err, integrationErrorTargetResolution, "not an exact registered Workspace root")
}

func TestResolveIntegrationTargetWorkspaceRejectsAmbiguousRoot(t *testing.T) {
	root := t.TempDir()
	refs := []workspaceRef{{Handle: "alpha", Root: root}, {Handle: "bravo", Root: root}}
	_, err := resolveIntegrationTargetWorkspace(root, refs, "alpha")
	assertIntegrationProtocolError(t, err, integrationErrorTargetResolution, "matches multiple Workspaces")
}

func TestIntegrationStatePathsBindCanonicalProjectAndTarget(t *testing.T) {
	project := t.TempDir()
	target := t.TempDir()
	projectLink := filepath.Join(t.TempDir(), "project-link")
	if err := os.Symlink(project, projectLink); err != nil {
		t.Fatal(err)
	}
	binding, err := integrationStatePaths(projectLink, target)
	if err != nil {
		t.Fatal(err)
	}
	if binding.CanonicalProjectPath != filepath.Clean(project) || binding.CanonicalTargetPath != filepath.Clean(target) {
		t.Fatalf("unexpected canonical binding: %+v", binding)
	}
	if binding.StateDir != filepath.Join(project, ".ajj", "integrations") {
		t.Fatalf("unexpected state dir: %s", binding.StateDir)
	}
}

func TestIntegrationCapabilitiesFreezeProtocolSurface(t *testing.T) {
	capabilities := integrationCapabilities()
	if capabilities.Schema != integrationCapabilitiesSchemaV1 || capabilities.Integrate.TargetResolution != "current-workspace" {
		t.Fatalf("unexpected capabilities: %+v", capabilities)
	}
	if !capabilities.Integrate.ExactHeadAssertions || !capabilities.Integrate.Recovery || capabilities.Integrate.MinimumJJVersion != "0.41.0" {
		t.Fatalf("expected exact assertions, recovery, and jj 0.41 minimum capability: %+v", capabilities.Integrate)
	}
	encoded, err := json.Marshal(capabilities)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, want := range []string{integrationRequestSchemaV1, integrationReceiptSchemaV1, integrationStrategySingle, integrationStrategyProviderDefault, integrationStrategyOrderedLine} {
		if !strings.Contains(text, want) {
			t.Fatalf("capability response missing %q: %s", want, text)
		}
	}
	if strings.Contains(strings.ToLower(text), "repositoryidentity") || strings.Contains(strings.ToLower(text), "repositoryid") {
		t.Fatalf("capabilities must not introduce repository identity: %s", text)
	}
}

func TestIntegrationPhasesFreezeTargetAdvanceAsCommitPoint(t *testing.T) {
	got := []string{
		integrationPhasePrepared,
		integrationPhaseGraphRewritten,
		integrationPhaseTargetAdvanced,
		integrationPhaseCursorsReconciled,
		integrationPhaseTerminal,
	}
	want := []string{"prepared", "graph-rewritten", "target-advanced", "cursors-reconciled", "terminal"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected integration phases: %v", got)
	}
}

func assertIntegrationProtocolError(t *testing.T, err error, code, message string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected integration protocol error %q", code)
	}
	var protocolErr *integrationProtocolError
	if !errors.As(err, &protocolErr) {
		t.Fatalf("expected integrationProtocolError, got %T: %v", err, err)
	}
	if protocolErr.Code != code {
		t.Fatalf("expected code %q, got %q (%v)", code, protocolErr.Code, err)
	}
	if message != "" && !strings.Contains(protocolErr.Message, message) {
		t.Fatalf("expected message containing %q, got %q", message, protocolErr.Message)
	}
}
