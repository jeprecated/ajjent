package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestADRIntegrationJSONExamplesUseStrictProtocolValidators(t *testing.T) {
	adrPath := filepath.Join("..", "..", "docs", "adr", "0010-add-workspace-relative-integration-protocol.md")
	data, err := os.ReadFile(adrPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	requestBytes := extractADRJSONExample(t, text, "### Request schema")
	request, requestDigest, err := parseIntegrationRequestV1(requestBytes)
	if err != nil {
		t.Fatalf("ADR request example fails the actual strict decoder: %v", err)
	}

	receiptBytes := extractADRJSONExample(t, text, "### Receipt schema")
	wrappedReceipt := append([]byte(`{"receipt":`), receiptBytes...)
	wrappedReceipt = append(wrappedReceipt, '}')
	if err := validateIntegrationOperationRecordJSONKeys(wrappedReceipt); err != nil {
		t.Fatalf("ADR receipt example fails the actual strict JSON-key validation: %v", err)
	}
	var receipt integrationReceiptV1
	if err := json.Unmarshal(receiptBytes, &receipt); err != nil {
		t.Fatalf("decode ADR receipt example: %v", err)
	}
	if len(receipt.Payloads) != 1 || len(receipt.Payloads[0].Changes) != 1 {
		t.Fatalf("ADR receipt example must contain one complete mapped payload: %+v", receipt)
	}
	mapping := receipt.Payloads[0].Changes[0]
	state := integrationTargetAdvancedStateV1{
		IntegratedTipCommit: receipt.Target.IntegratedTipCommit,
		AfterHeadCommit:     receipt.Target.AfterHeadCommit,
	}
	record := integrationOperationRecord{
		OperationID:          request.OperationID,
		RequestDigest:        requestDigest,
		BeforeOperationID:    receipt.JJOperations.BeforeEffect,
		CommitPointOperation: receipt.JJOperations.CommitPoint,
		GraphOperationID:     receipt.JJOperations.CommitPoint,
		DetachedOperationIDs: []string{receipt.JJOperations.CommitPoint},
		PreparedState: integrationPreparedStateV1{
			Target: integrationPreparedTargetV1{Workspace: request.Target.ExpectedWorkspace, HeadCommit: request.Target.ExpectedHeadCommit},
			Payloads: []integrationPreparedPayloadV1{{
				Workspace:  request.Payloads[0].Workspace,
				HeadCommit: request.Payloads[0].ExpectedHeadCommit,
				Changes:    []integrationPreparedChangeV1{{ChangeID: mapping.ChangeID, CommitID: mapping.InputCommit}},
			}},
		},
		StagedTargetState:     &state,
		TargetAdvancedState:   &state,
		StagedPayloadMappings: [][]integrationReceiptChangeV1{{mapping}},
	}
	if receipt.RequestDigest != requestDigest {
		t.Fatalf("ADR receipt requestDigest does not bind the exact fenced request bytes: got=%q want=%q", receipt.RequestDigest, requestDigest)
	}
	if got := integrationPayloadReceiptEvidenceDigest(receipt.Payloads[0]); receipt.Payloads[0].EvidenceDigest != got {
		t.Fatalf("ADR payload evidenceDigest drifted: got=%q want=%q", receipt.Payloads[0].EvidenceDigest, got)
	}
	if got := integrationReceiptEvidenceDigest(receipt); receipt.EvidenceDigest != got {
		t.Fatalf("ADR receipt evidenceDigest drifted: got=%q want=%q", receipt.EvidenceDigest, got)
	}
	if err := validateIntegrationTerminalReceipt(receipt, record, request); err != nil {
		t.Fatalf("ADR receipt example fails the actual semantic validator: %v", err)
	}
}

func extractADRJSONExample(t *testing.T, text, heading string) []byte {
	t.Helper()
	start := strings.Index(text, heading)
	if start < 0 {
		t.Fatalf("missing ADR heading %q", heading)
	}
	text = text[start+len(heading):]
	fence := strings.Index(text, "```json\n")
	if fence < 0 {
		t.Fatalf("missing JSON fence after %q", heading)
	}
	text = text[fence+len("```json\n"):]
	end := strings.Index(text, "\n```")
	if end < 0 {
		t.Fatalf("unterminated JSON fence after %q", heading)
	}
	return []byte(text[:end])
}

func TestIntegrationQueryArgsAlwaysDisableColorAndPager(t *testing.T) {
	withoutOperation := integrationQueryArgs("/repo", "", "log", "-r", "@")
	wantWithout := []string{"-R", "/repo", "--color=never", "--no-pager", "--ignore-working-copy", "log", "-r", "@"}
	if !reflect.DeepEqual(withoutOperation, wantWithout) {
		t.Fatalf("current-operation query flags drifted: got=%v want=%v", withoutOperation, wantWithout)
	}
	withOperation := integrationQueryArgs("/repo", "abc123", "workspace", "list")
	wantWith := []string{"-R", "/repo", "--color=never", "--no-pager", "--ignore-working-copy", "--at-op=abc123", "workspace", "list"}
	if !reflect.DeepEqual(withOperation, wantWith) {
		t.Fatalf("historical-operation query flags drifted: got=%v want=%v", withOperation, wantWith)
	}
}

func TestIntegrationCleanupEvidenceForcedColorAndPager(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))
	repo := filepath.Join(root, "repo")
	runJJ(t, "git", "init", "--colocate", repo)
	runJJ(t, "-R", repo, "config", "set", "--repo", "ui.color", "always")
	runJJ(t, "-R", repo, "config", "set", "--repo", "ui.paginate", "auto")
	runJJ(t, "-R", repo, "config", "set", "--repo", "ui.pager", `["false"]`)
	operationID, err := currentOperationFullID(repo)
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := integrationChangeCommitPairsAtOperation(repo, operationID, "visible_heads()")
	if err != nil {
		t.Fatalf("forced color/pager corrupted bounded cleanup evidence: %v", err)
	}
	if len(pairs) == 0 || len(pairs) > integrationMaxRepositoryEvidenceItems {
		t.Fatalf("unexpected cleanup evidence count: %d", len(pairs))
	}
	for _, pair := range pairs {
		if !integrationFullChangeIDRE.MatchString(pair.ChangeID) || !integrationCommitIDRE.MatchString(pair.CommitID) {
			t.Fatalf("cleanup evidence contains presentation bytes: %+v", pair)
		}
	}
}

func TestIntegrationCleanupEvidenceRowAndItemCeiling(t *testing.T) {
	row := strings.Repeat("a", 32) + "\t" + strings.Repeat("b", 40) + "\n"
	if len(row) != 74 {
		t.Fatalf("cleanup evidence row width=%d want=32+1+40+1=74", len(row))
	}
	if integrationMaxCleanupEvidenceBytes != integrationMaxRepositoryEvidenceItems*len(row) || integrationMaxCleanupEvidenceBytes != 303104 {
		t.Fatalf("cleanup byte derivation drifted: bytes=%d items=%d row=%d", integrationMaxCleanupEvidenceBytes, integrationMaxRepositoryEvidenceItems, len(row))
	}
	original := integrationBoundedCaptureFn
	t.Cleanup(func() { integrationBoundedCaptureFn = original })
	integrationBoundedCaptureFn = func(maxBytes int, _ string, _ ...string) (string, error) {
		out := strings.Repeat(row, integrationMaxRepositoryEvidenceItems)
		if len(out) != maxBytes {
			t.Fatalf("exact row fixture bytes=%d limit=%d", len(out), maxBytes)
		}
		return out, nil
	}
	pairs, err := integrationChangeCommitPairsAtOperation("/repo", strings.Repeat("1", 128), "visible_heads()")
	if err != nil || len(pairs) != integrationMaxRepositoryEvidenceItems {
		t.Fatalf("exact 4096-row evidence rejected: count=%d err=%v", len(pairs), err)
	}
}

func TestRunIntegrationCommandCaptureBounded(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("invalid limits fail before execution", func(t *testing.T) {
		for _, limit := range []int{0, -1} {
			out, err := runIntegrationCommandCaptureBounded(limit, "/must/not/run")
			if out != "" || !errors.Is(err, errIntegrationEvidenceOutputLimit) {
				t.Fatalf("invalid limit %d was accepted: out=%q err=%v", limit, out, err)
			}
		}
		if strconv.IntSize == 64 {
			maxInt := int(^uint(0) >> 1)
			out, err := runIntegrationCommandCaptureBounded(maxInt, "/must/not/run")
			if out != "" || !errors.Is(err, errIntegrationEvidenceOutputLimit) {
				t.Fatalf("overflow-prone limit was accepted: out=%q err=%v", out, err)
			}
		}
	})

	t.Run("exact limit", func(t *testing.T) {
		t.Setenv("AJJ_BOUNDED_CAPTURE_HELPER", "1")
		t.Setenv("AJJ_BOUNDED_CAPTURE_BYTES", strconv.Itoa(integrationMaxCleanupEvidenceBytes))
		t.Setenv("AJJ_BOUNDED_CAPTURE_STDERR_BYTES", strconv.Itoa(1<<20))
		t.Setenv("AJJ_BOUNDED_CAPTURE_EXIT", "0")
		out, err := runIntegrationCommandCaptureBounded(integrationMaxCleanupEvidenceBytes, binary, "-test.run=^TestIntegrationBoundedCaptureHelperProcess$")
		if err != nil || len(out) != integrationMaxCleanupEvidenceBytes {
			t.Fatalf("exact-limit output rejected: bytes=%d err=%v", len(out), err)
		}
	})

	t.Run("over limit has no partial output", func(t *testing.T) {
		t.Setenv("AJJ_BOUNDED_CAPTURE_HELPER", "1")
		t.Setenv("AJJ_BOUNDED_CAPTURE_BYTES", strconv.Itoa(integrationMaxCleanupEvidenceBytes+1))
		t.Setenv("AJJ_BOUNDED_CAPTURE_EXIT", "0")
		out, err := runIntegrationCommandCaptureBounded(integrationMaxCleanupEvidenceBytes, binary, "-test.run=^TestIntegrationBoundedCaptureHelperProcess$")
		if out != "" || !errors.Is(err, errIntegrationEvidenceOutputLimit) || strings.Contains(err.Error(), string(filepath.Separator)) {
			t.Fatalf("over-limit capture was not bounded/path-free: bytes=%d err=%v", len(out), err)
		}
	})

	t.Run("nonzero command has no partial output", func(t *testing.T) {
		t.Setenv("AJJ_BOUNDED_CAPTURE_HELPER", "1")
		t.Setenv("AJJ_BOUNDED_CAPTURE_BYTES", "64")
		t.Setenv("AJJ_BOUNDED_CAPTURE_EXIT", "7")
		out, err := runIntegrationCommandCaptureBounded(128, binary, "-test.run=^TestIntegrationBoundedCaptureHelperProcess$")
		if out != "" || !errors.Is(err, errIntegrationEvidenceCommand) || strings.Contains(err.Error(), string(filepath.Separator)) {
			t.Fatalf("nonzero capture exposed output or path: bytes=%d err=%v", len(out), err)
		}
	})
}

func TestIntegrationBoundedCaptureHelperProcess(t *testing.T) {
	if os.Getenv("AJJ_BOUNDED_CAPTURE_HELPER") != "1" {
		return
	}
	count, err := strconv.Atoi(os.Getenv("AJJ_BOUNDED_CAPTURE_BYTES"))
	if err != nil || count < 0 {
		os.Exit(9)
	}
	_, _ = os.Stdout.Write([]byte(strings.Repeat("x", count)))
	stderrCount, _ := strconv.Atoi(os.Getenv("AJJ_BOUNDED_CAPTURE_STDERR_BYTES"))
	if stderrCount > 0 {
		_, _ = os.Stderr.Write([]byte(strings.Repeat("e", stderrCount)))
	}
	exitCode, _ := strconv.Atoi(os.Getenv("AJJ_BOUNDED_CAPTURE_EXIT"))
	os.Exit(exitCode)
}

func TestIntegrationChangeCommitPairsRejectsCaptureFailureWithoutPartialEvidence(t *testing.T) {
	original := integrationBoundedCaptureFn
	t.Cleanup(func() { integrationBoundedCaptureFn = original })
	integrationBoundedCaptureFn = func(int, string, ...string) (string, error) {
		return strings.Repeat("a", 32) + "\t" + strings.Repeat("b", 40) + "\n", errIntegrationEvidenceOutputLimit
	}
	pairs, err := integrationChangeCommitPairsAtOperation("/secret/repo", strings.Repeat("1", 128), "visible_heads()")
	if pairs != nil || !errors.Is(err, errIntegrationEvidenceOutputLimit) || strings.Contains(err.Error(), "/secret/repo") {
		t.Fatalf("partial evidence survived bounded capture failure: pairs=%v err=%v", pairs, err)
	}
}
