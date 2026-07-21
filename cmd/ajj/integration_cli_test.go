package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRunRoutesCapabilitiesWithoutJJ(t *testing.T) {
	originalLookPath := lookPathFn
	lookPathFn = func(string) (string, error) { return "", errors.New("jj unavailable") }
	resetJJCheck()
	t.Cleanup(func() {
		lookPathFn = originalLookPath
		resetJJCheck()
	})
	out, _, err := captureOutput(func() error { return run([]string{"capabilities", "--json"}) })
	if err != nil {
		t.Fatalf("capabilities unexpectedly required jj: %v", err)
	}
	if !strings.Contains(out, integrationCapabilitiesSchemaV1) {
		t.Fatalf("capabilities command was not routed: %s", out)
	}
}

func TestRunCapabilitiesJSONReportsBoundedMachineSurface(t *testing.T) {
	out, errOut, err := captureOutput(func() error { return runCapabilities([]string{"--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	if errOut != "" {
		t.Fatalf("capabilities diagnostics must stay empty, got %q", errOut)
	}
	var capabilities ajjCapabilitiesV1
	if err := json.Unmarshal([]byte(out), &capabilities); err != nil {
		t.Fatalf("capabilities stdout is not one JSON object: %v\n%s", err, out)
	}
	if capabilities.Integrate.MaxRequestBytes != integrationMaxRequestBytes || capabilities.Integrate.MaxOutputBytes != integrationMaxOutputBytes || capabilities.Integrate.MaxPayloads != integrationMaxPayloads || capabilities.Integrate.MaxChangesPerPayload != integrationMaxChangesPerPayload || capabilities.Integrate.MaxReceiptChanges != integrationMaxReceiptChanges || capabilities.Integrate.MaxErrorMessageBytes != integrationMaxErrorBytes {
		t.Fatalf("capabilities omitted bounded limits: %+v", capabilities.Integrate)
	}
	if integrationMaxRecordBytes < integrationMaxOutputBytes+2*integrationMaxRequestBytes {
		t.Fatalf("record bound cannot contain the maximum request and receipt: record=%d request=%d output=%d", integrationMaxRecordBytes, integrationMaxRequestBytes, integrationMaxOutputBytes)
	}
	if capabilities.Integrate.PreparationOnly || strings.Join(capabilities.Integrate.ExecutableStrategies, ",") != "single,provider-default,ordered-line" || len(capabilities.Integrate.Strategies) != 3 {
		t.Fatalf("capabilities did not advertise the implemented strategy effects exactly: %+v", capabilities.Integrate)
	}
	if !strings.Contains(out, `"targetResolution":"current-workspace"`) || strings.Contains(strings.ToLower(out), "repositoryid") {
		t.Fatalf("unexpected capability protocol: %s", out)
	}
	if err := runCapabilities(nil); err == nil || !strings.Contains(err.Error(), "requires --json") {
		t.Fatalf("expected explicit --json requirement, got %v", err)
	}
}

func TestIntegrationLockRejectsContentionAndReleases(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "integrations")
	first, err := acquireIntegrationLock(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := acquireIntegrationLock(stateDir)
	if second != nil {
		_ = second.Close()
		t.Fatal("second lock unexpectedly succeeded")
	}
	assertIntegrationProtocolError(t, err, integrationErrorOperationInProgress, "holds the Project lock")
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := acquireIntegrationLock(stateDir)
	if err != nil {
		t.Fatalf("lock was not released: %v", err)
	}
	if err := third.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationCLILockContentionAndReleaseAfterProcessDeath(t *testing.T) {
	paths := setupRealStackRepo(t)
	stateDir := filepath.Join(paths.defaultPath, ".ajj", "integrations")
	cmd := exec.Command(os.Args[0], "-test.run=^TestIntegrationLockHelperProcess$")
	cmd.Env = append(os.Environ(), "AJJ_TEST_LOCK_STATE_DIR="+stateDir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "locked" {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("lock helper did not acquire lock: %q err=%v", scanner.Text(), scanner.Err())
	}

	targetCommit := jjFullCommitID(t, paths.defaultPath, "speed@")
	payloadCommit := jjFullCommitID(t, paths.defaultPath, "agm-speed-transition@")
	request := validIntegrationRequestBytes("process-lock-op", "speed", "agm-speed-transition", targetCommit, payloadCommit)
	withIntegrationStdin(t, string(request))
	out, _, err := captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.speedPath, "--request-json", "-"})
	})
	if err == nil {
		t.Fatal("CLI unexpectedly acquired a lock held by another process")
	}
	if got := decodeIntegrationReceipt(t, out); got.Error == nil || got.Error.Code != integrationErrorOperationInProgress {
		t.Fatalf("CLI lock contention returned the wrong result: %+v", got)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()

	withIntegrationStdin(t, string(request))
	out, _, err = captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.speedPath, "--request-json", "-"})
	})
	if err != nil || decodeIntegrationReceipt(t, out).BatchDisposition != integrationBatchSucceeded {
		t.Fatalf("kernel did not release flock for the integration retry: %v\n%s", err, out)
	}
}

func TestIntegrationLockHelperProcess(t *testing.T) {
	stateDir := os.Getenv("AJJ_TEST_LOCK_STATE_DIR")
	if stateDir == "" {
		return
	}
	lock, err := acquireIntegrationLock(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	fmt.Fprintln(os.Stdout, "locked")
	_ = os.Stdout.Sync()
	select {}
}

func TestIntegrationOperationRecordAtomicRoundTripAndCorruption(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "integrations")
	requestBytes := validIntegrationRequestBytes("record-op", "alpha", "bravo", strings.Repeat("a", 40), strings.Repeat("b", 40))
	record := integrationOperationRecord{
		Schema:               integrationOperationRecordV1,
		OperationID:          "record-op",
		RequestDigest:        integrationRequestDigest(requestBytes),
		RequestBytes:         requestBytes,
		CanonicalProjectPath: "/project",
		CanonicalTargetPath:  "/project/alpha",
		Phase:                integrationPhasePrepared,
		BeforeOperationID:    strings.Repeat("1", 128),
		PreparedState:        integrationTestPreparedState(),
		BeforeRepositoryView: integrationTestRepositoryView(strings.Repeat("a", 40)),
	}
	if err := writeIntegrationOperationRecordAtomic(stateDir, record); err != nil {
		t.Fatal(err)
	}
	loaded, found, err := loadIntegrationOperationRecord(stateDir, record.OperationID)
	if err != nil || !found {
		t.Fatalf("load atomic record: found=%v err=%v", found, err)
	}
	if loaded.RequestDigest != record.RequestDigest || !bytes.Equal(loaded.RequestBytes, requestBytes) || loaded.PreparedState.Target.HeadCommit != record.PreparedState.Target.HeadCommit {
		t.Fatalf("record did not preserve exact pre-state: %+v", loaded)
	}
	path, _ := integrationOperationRecordPath(stateDir, record.OperationID)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateIntegrationOperationContext(loaded, integrationStateBinding{CanonicalProjectPath: "/other", CanonicalTargetPath: loaded.CanonicalTargetPath}); err == nil {
		t.Fatal("wrong Project binding was accepted")
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("record mode = %o, want 600", info.Mode().Perm())
	}
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("atomic write left temporary file %s", entry.Name())
		}
	}

	baseRecordData := string(mustReadFile(t, path))
	for _, corrupt := range []string{
		`{`,
		`{"schema":"ajj-integration-operation-v1"}`,
		strings.Replace(baseRecordData, `"phase": "prepared"`, `"phase": "bogus"`, 1),
		strings.Replace(baseRecordData, `"schema": "ajj-integration-operation-v1"`, `"Schema": "ajj-integration-operation-v1"`, 1),
		strings.Replace(baseRecordData, `"workspace": "alpha"`, `"Workspace": "alpha"`, 1),
	} {
		if err := os.WriteFile(path, []byte(corrupt), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := loadIntegrationOperationRecord(stateDir, record.OperationID); err == nil {
			t.Fatalf("expected corrupt record rejection for %q", corrupt)
		}
	}
}

func TestIntegrationRecordReadAndStoredRequestAreBounded(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "integrations")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	operationID := "oversized-record"
	path, _ := integrationOperationRecordPath(stateDir, operationID)
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(integrationMaxRecordBytes + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadIntegrationOperationRecord(stateDir, operationID); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("oversized record was not rejected deterministically: %v", err)
	}

	requestBytes := validIntegrationRequestBytes("padded-request", "alpha", "bravo", strings.Repeat("a", 40), strings.Repeat("b", 40))
	requestBytes = append(requestBytes, bytes.Repeat([]byte(" \n"), integrationMaxRequestBytes/2)...)
	padded := integrationOperationRecord{
		Schema: integrationOperationRecordV1, OperationID: "padded-request", RequestDigest: integrationRequestDigest(requestBytes), RequestBytes: requestBytes,
		CanonicalProjectPath: "/project", CanonicalTargetPath: "/project/alpha", Phase: integrationPhasePrepared,
		BeforeOperationID: strings.Repeat("1", 128),
		PreparedState:     integrationTestPreparedState(),
	}
	writeForgedIntegrationRecord(t, stateDir, padded)
	if _, _, err := loadIntegrationOperationRecord(stateDir, padded.OperationID); err == nil || !strings.Contains(err.Error(), "request bytes exceed") {
		t.Fatalf("whitespace-padded oversized stored request was not rejected before parsing: %v", err)
	}
}

func TestIntegrationRecordRejectsForgedPreparedStateAndTerminalReceipt(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "integrations")
	requestBytes := validIntegrationRequestBytes("semantic-op", "alpha", "bravo", strings.Repeat("a", 40), strings.Repeat("b", 40))
	base := integrationOperationRecord{
		Schema:               integrationOperationRecordV1,
		OperationID:          "semantic-op",
		RequestDigest:        integrationRequestDigest(requestBytes),
		RequestBytes:         requestBytes,
		CanonicalProjectPath: "/project",
		CanonicalTargetPath:  "/project/alpha",
		Phase:                integrationPhasePrepared,
		BeforeOperationID:    strings.Repeat("1", 128),
		PreparedState:        integrationTestPreparedState(),
		BeforeRepositoryView: integrationTestRepositoryView(strings.Repeat("a", 40)),
	}

	forgedPrepared := cloneIntegrationRecord(t, base)
	forgedPrepared.PreparedState.Payloads[0].HeadCommit = strings.Repeat("c", 40)
	writeForgedIntegrationRecord(t, stateDir, forgedPrepared)
	if _, _, err := loadIntegrationOperationRecord(stateDir, base.OperationID); err == nil {
		t.Fatal("forged prepared payload state was accepted")
	}

	validTerminal := cloneIntegrationRecord(t, base)
	validTerminal.Phase = integrationPhaseTerminal
	setTestSuccessfulOperation(&validTerminal, strings.Repeat("2", 128), strings.Repeat("b", 40), strings.Repeat("d", 40))
	validTerminal.Receipt = successfulTestIntegrationReceipt(validTerminal)
	if err := writeIntegrationOperationRecordAtomic(stateDir, validTerminal); err != nil {
		t.Fatal(err)
	}
	if _, found, err := loadIntegrationOperationRecord(stateDir, base.OperationID); err != nil || !found {
		t.Fatalf("valid terminal record was rejected: found=%v err=%v", found, err)
	}

	for index, mutate := range []func(*integrationOperationRecord){
		func(record *integrationOperationRecord) { record.Receipt.Target.Workspace = "charlie" },
		func(record *integrationOperationRecord) { record.Receipt.Payloads[0].Workspace = "charlie" },
		func(record *integrationOperationRecord) {
			record.Receipt.EvidenceDigest = integrationEvidenceDigest("forged")
		},
		func(record *integrationOperationRecord) {
			record.Receipt.JJOperations.CommitPoint = strings.Repeat("3", 128)
		},
		func(record *integrationOperationRecord) {
			record.Receipt.Payloads[0].Changes = nil
			record.Receipt.Payloads[0].EvidenceDigest = integrationPayloadReceiptEvidenceDigest(record.Receipt.Payloads[0])
		},
		func(record *integrationOperationRecord) {
			record.Receipt.Payloads[0].Changes[0].ChangeID = strings.Repeat("d", 32)
			record.Receipt.Payloads[0].EvidenceDigest = integrationPayloadReceiptEvidenceDigest(record.Receipt.Payloads[0])
		},
		func(record *integrationOperationRecord) {
			record.Receipt.Payloads[0].Changes[0].LandedCommit = strings.Repeat("e", 40)
			record.Receipt.Payloads[0].EvidenceDigest = integrationPayloadReceiptEvidenceDigest(record.Receipt.Payloads[0])
		},
		func(record *integrationOperationRecord) { record.TargetAdvancedState = nil },
		func(record *integrationOperationRecord) { record.PayloadCursors = nil },
		func(record *integrationOperationRecord) { record.PayloadCursors[0].Workspace = "charlie" },
		func(record *integrationOperationRecord) { record.PayloadCursors[0].HeadCommit = "not-a-commit" },
	} {
		forged := cloneIntegrationRecord(t, validTerminal)
		mutate(&forged)
		if index != 2 {
			forged.Receipt.EvidenceDigest = integrationReceiptEvidenceDigest(*forged.Receipt)
		}
		writeForgedIntegrationRecord(t, stateDir, forged)
		if _, _, err := loadIntegrationOperationRecord(stateDir, base.OperationID); err == nil {
			t.Fatal("forged terminal receipt was accepted")
		}
	}

	validFailed := cloneIntegrationRecord(t, base)
	validFailed.Phase = integrationPhaseTerminal
	validFailed.Receipt = failedTestIntegrationReceipt(validFailed)
	if err := writeIntegrationOperationRecordAtomic(stateDir, validFailed); err != nil {
		t.Fatalf("valid failed terminal record was rejected: %v", err)
	}
	if _, found, err := loadIntegrationOperationRecord(stateDir, base.OperationID); err != nil || !found {
		t.Fatalf("valid failed terminal record could not be loaded: found=%v err=%v", found, err)
	}

	for name, mutate := range map[string]func(*integrationOperationRecord){
		"impossible code/action": func(record *integrationOperationRecord) {
			record.Receipt.Error.Code = integrationErrorAssertionFailed
			record.Receipt.Error.Message = integrationPublicErrorSummaries[integrationErrorAssertionFailed]
			record.Receipt.Error.NextAction = integrationNextActionNone
		},
		"wrong action": func(record *integrationOperationRecord) {
			record.Receipt.Error.NextAction = integrationNextActionOperatorReview
		},
		"transient payload disposition": func(record *integrationOperationRecord) {
			record.Receipt.Payloads[0].Disposition = integrationPayloadFailedBeforeEffect
			record.Receipt.Payloads[0].EvidenceDigest = integrationPayloadReceiptEvidenceDigest(record.Receipt.Payloads[0])
		},
	} {
		t.Run(name, func(t *testing.T) {
			forged := cloneIntegrationRecord(t, validFailed)
			mutate(&forged)
			forged.Receipt.EvidenceDigest = integrationReceiptEvidenceDigest(*forged.Receipt)
			writeForgedIntegrationRecord(t, stateDir, forged)
			if _, _, err := loadIntegrationOperationRecord(stateDir, base.OperationID); err == nil {
				t.Fatal("impossible stored terminal receipt tuple was accepted")
			}
		})
	}

	forgedError := cloneIntegrationRecord(t, base)
	forgedError.Phase = integrationPhaseTerminal
	forgedError.Receipt = failedTestIntegrationReceipt(forgedError)
	forgedError.Receipt.Error.Message = "/tmp/request-controlled-secret"
	forgedError.Receipt.EvidenceDigest = integrationReceiptEvidenceDigest(*forgedError.Receipt)
	writeForgedIntegrationRecord(t, stateDir, forgedError)
	if _, _, err := loadIntegrationOperationRecord(stateDir, base.OperationID); err == nil {
		t.Fatal("terminal receipt with path-bearing arbitrary error text was accepted")
	}

	failedOptional := cloneIntegrationRecord(t, base)
	failedOptional.Phase = integrationPhaseTerminal
	failedOptional.Receipt = failedTestIntegrationReceipt(failedOptional)
	failedOptional.Receipt.Target.IntegratedTipCommit = "/tmp/not-a-commit"
	failedOptional.Receipt.EvidenceDigest = integrationReceiptEvidenceDigest(*failedOptional.Receipt)
	writeForgedIntegrationRecord(t, stateDir, failedOptional)
	if _, _, err := loadIntegrationOperationRecord(stateDir, base.OperationID); err == nil {
		t.Fatal("failed receipt accepted a path-bearing optional target commit")
	}

	unknownOptional := cloneIntegrationRecord(t, base)
	unknownOptional.Phase = integrationPhaseTerminal
	unknownOptional.Receipt = failedTestIntegrationReceipt(unknownOptional)
	unknownOptional.Receipt.BatchDisposition = integrationBatchUnknownEffect
	unknownOptional.Receipt.Payloads[0].Disposition = integrationPayloadUnknownEffect
	unknownOptional.Receipt.Payloads[0].EvidenceDigest = integrationPayloadReceiptEvidenceDigest(unknownOptional.Receipt.Payloads[0])
	unknownOptional.Receipt.Error = &integrationReceiptErrorV1{
		Code: integrationErrorUnknownEffect, Message: integrationPublicErrorSummaries[integrationErrorUnknownEffect], NextAction: integrationNextActionOperatorReview,
	}
	unknownOptional.Receipt.Target.AfterHeadCommit = "../../not-a-commit"
	unknownOptional.Receipt.EvidenceDigest = integrationReceiptEvidenceDigest(*unknownOptional.Receipt)
	writeForgedIntegrationRecord(t, stateDir, unknownOptional)
	if _, _, err := loadIntegrationOperationRecord(stateDir, base.OperationID); err == nil {
		t.Fatal("unknown-effect receipt accepted a path-bearing optional target commit")
	}

	tooManyChanges := cloneIntegrationRecord(t, validTerminal)
	tooManyChanges.Receipt.Payloads[0].Changes = make([]integrationReceiptChangeV1, integrationMaxChangesPerPayload+1)
	tooManyChanges.Receipt.Payloads[0].EvidenceDigest = integrationPayloadReceiptEvidenceDigest(tooManyChanges.Receipt.Payloads[0])
	tooManyChanges.Receipt.EvidenceDigest = integrationReceiptEvidenceDigest(*tooManyChanges.Receipt)
	request, _, err := parseIntegrationRequestV1(tooManyChanges.RequestBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateIntegrationTerminalReceipt(*tooManyChanges.Receipt, tooManyChanges, request); err == nil || !strings.Contains(err.Error(), "change count") {
		t.Fatalf("terminal receipt change-count bound was not enforced: %v", err)
	}
}

func TestRunIntegrateMalformedRequestDoesNotPolluteStdoutBeforeIdentification(t *testing.T) {
	withIntegrationStdin(t, `{"schema":`)
	out, _, err := captureOutput(func() error { return runIntegrate([]string{"--request-json", "-"}) })
	if err == nil {
		t.Fatal("expected malformed request error")
	}
	if out != "" {
		t.Fatalf("malformed unidentified request polluted stdout: %q", out)
	}
}

func TestRunIntegrateIdentifiedInvalidRequestReturnsBoundedJSON(t *testing.T) {
	request := `{"schema":"ajj-integrate-request-v1","operationId":"known-op","unknown":true}`
	withIntegrationStdin(t, request)
	out, _, err := captureOutput(func() error { return runIntegrate([]string{"--request-json", "-"}) })
	if err == nil {
		t.Fatal("expected invalid request error")
	}
	receipt := decodeIntegrationReceipt(t, out)
	if receipt.OperationID != "known-op" || receipt.Error == nil || receipt.Error.Code != integrationErrorInvalidJSON {
		t.Fatalf("unexpected invalid-request receipt: %+v", receipt)
	}
	if strings.Contains(out, "/") || len(receipt.Error.Message) > integrationMaxErrorBytes {
		t.Fatalf("failure output was unbounded or contained a path: %s", out)
	}
}

func TestIntegrationMachineErrorsAreStablePathFreeAndUTF8Bounded(t *testing.T) {
	commit := strings.Repeat("a", 40)
	requests := []struct {
		input      string
		secret     string
		expectCode string
	}{
		{
			input:      `{"schema":"ajj-integrate-request-v1","operationId":"bad-key","/tmp/secret-🙂":true}`,
			secret:     "/tmp/secret-🙂",
			expectCode: integrationErrorInvalidJSON,
		},
		{
			input: fmt.Sprintf(`{"schema":"%s","operationId":"bad-strategy","target":{"expectedWorkspace":"alpha","expectedHeadCommit":"%s"},"strategy":"/tmp/private-🙂","payloads":[{"workspace":"bravo","expectedHeadCommit":"%s"}]}`,
				integrationRequestSchemaV1, commit, commit),
			secret:     "/tmp/private-🙂",
			expectCode: integrationErrorInvalidRequest,
		},
	}
	for _, test := range requests {
		withIntegrationStdin(t, test.input)
		out, _, err := captureOutput(func() error { return runIntegrate([]string{"--request-json", "-"}) })
		if err == nil {
			t.Fatal("adversarial request unexpectedly succeeded")
		}
		receipt := decodeIntegrationReceipt(t, out)
		if receipt.Error == nil || receipt.Error.Code != test.expectCode || receipt.Error.Message != integrationPublicErrorSummaries[test.expectCode] {
			t.Fatalf("unstable machine error: %+v", receipt)
		}
		if strings.Contains(out, test.secret) || strings.Contains(receipt.Error.Message, "/") || len([]byte(receipt.Error.Message)) > integrationMaxErrorBytes || !utf8.ValidString(receipt.Error.Message) {
			t.Fatalf("machine error leaked request data or exceeded bounds: %s", out)
		}
	}

	bounded := boundedIntegrationMessage(strings.Repeat("界", integrationMaxErrorBytes))
	if !utf8.ValidString(bounded) || len([]byte(bounded)) > integrationMaxErrorBytes || !strings.HasSuffix(bounded, "...") {
		t.Fatalf("UTF-8 truncation was not safe or bounded: bytes=%d valid=%v", len([]byte(bounded)), utf8.ValidString(bounded))
	}
}

func TestRunIntegratePreparesDurableOperationAndRecoveryNeverReexecutes(t *testing.T) {
	paths := setupRealStackRepo(t)
	prepareOrderedSpeedTarget(t, paths)
	targetCommit := jjFullCommitID(t, paths.defaultPath, "speed@")
	payloadCommit := jjFullCommitID(t, paths.defaultPath, "agm-speed-transition@")
	requestBytes := integrationRequestBytesForStrategy("prepare-op", "speed", []string{"agm-speed-transition"}, targetCommit, []string{payloadCommit}, integrationStrategyOrderedLine)

	originalPhaseHook := integrationEffectPhaseHook
	integrationEffectPhaseHook = func(phase string) error {
		if phase == integrationPhasePrepared {
			return errors.New("stop after durable preparation")
		}
		return nil
	}
	t.Cleanup(func() { integrationEffectPhaseHook = originalPhaseHook })
	withIntegrationStdin(t, string(requestBytes))
	out, errOut, err := captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.speedPath, "--request-json", "-"})
	})
	if err == nil {
		t.Fatalf("expected injected prepared interruption, got nil\nstdout:%s\nstderr:%s", out, errOut)
	}
	receipt := decodeIntegrationReceipt(t, out)
	if receipt.OperationID != "prepare-op" || receipt.RequestDigest != integrationRequestDigest(requestBytes) || receipt.BatchDisposition != integrationBatchUnknownEffect || receipt.Error == nil || receipt.Error.NextAction != integrationNextActionRecover {
		t.Fatalf("unexpected prepared receipt: %+v", receipt)
	}
	if strings.Contains(out, paths.defaultPath) || strings.Contains(out, paths.childPath) {
		t.Fatalf("receipt leaked filesystem paths: %s", out)
	}

	stateDir := filepath.Join(paths.defaultPath, ".ajj", "integrations")
	record, found, err := loadIntegrationOperationRecord(stateDir, "prepare-op")
	if err != nil || !found {
		t.Fatalf("prepared record missing: found=%v err=%v", found, err)
	}
	if record.CanonicalProjectPath != filepath.Clean(paths.defaultPath) || record.CanonicalTargetPath != filepath.Clean(paths.speedPath) || !bytes.Equal(record.RequestBytes, requestBytes) {
		t.Fatalf("prepared record omitted exact bindings: %+v", record)
	}
	if record.PreparedState.Target.HeadCommit != targetCommit || len(record.PreparedState.Payloads) != 1 || record.PreparedState.Payloads[0].HeadCommit != payloadCommit || !integrationFullOperationIDRE.MatchString(record.BeforeOperationID) {
		t.Fatalf("prepared record omitted exact graph pre-state: %+v", record)
	}

	withIntegrationStdin(t, string(requestBytes))
	repeatOut, _, repeatErr := captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.speedPath, "--request-json", "-"})
	})
	if repeatErr == nil || !strings.Contains(repeatErr.Error(), integrationErrorOperationInterrupted) {
		t.Fatalf("same nonterminal request must require recovery, got %v", repeatErr)
	}
	if decodeIntegrationReceipt(t, repeatOut).Error.NextAction != integrationNextActionRecover {
		t.Fatalf("same nonterminal request did not direct recovery: %s", repeatOut)
	}

	recoverOut, _, recoverErr := captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.speedPath, "--recover", "prepare-op", "--json"})
	})
	if recoverErr == nil || !strings.Contains(recoverErr.Error(), integrationErrorOperationInterrupted) {
		t.Fatalf("inspect-only recovery unexpectedly succeeded or reexecuted: %v", recoverErr)
	}
	if recovered := decodeIntegrationReceipt(t, recoverOut); recovered.Error == nil || recovered.Error.Code != integrationErrorOperationInterrupted || recovered.Error.Message != integrationPublicErrorSummaries[integrationErrorOperationInterrupted] {
		t.Fatalf("recovery did not report inspect-only state: %+v", recovered)
	}
	wrongTargetOut, _, wrongTargetErr := captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.defaultPath, "--recover", "prepare-op", "--json"})
	})
	if wrongTargetErr == nil {
		t.Fatal("recovery from another Current Workspace unexpectedly succeeded")
	}
	if wrongTarget := decodeIntegrationReceipt(t, wrongTargetOut); wrongTarget.Error == nil || wrongTarget.Error.Code != integrationErrorAssertionFailed {
		t.Fatalf("wrong-target recovery did not fail closed: %+v", wrongTarget)
	}

	prettyDifferent := append([]byte("\n"), requestBytes...)
	withIntegrationStdin(t, string(prettyDifferent))
	contradictionOut, _, contradictionErr := captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.speedPath, "--request-json", "-"})
	})
	if contradictionErr == nil || !strings.Contains(contradictionErr.Error(), integrationErrorOperationIDContradiction) {
		t.Fatalf("different exact bytes did not contradict operation id: %v", contradictionErr)
	}
	if got := decodeIntegrationReceipt(t, contradictionOut); got.Error == nil || got.Error.Code != integrationErrorOperationIDContradiction {
		t.Fatalf("unexpected contradiction receipt: %+v", got)
	}

	differentTarget := integrationRequestBytesForStrategy("prepare-op", "default", []string{"agm-speed-transition"}, targetCommit, []string{payloadCommit}, integrationStrategyOrderedLine)
	withIntegrationStdin(t, string(differentTarget))
	differentTargetOut, _, differentTargetErr := captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.speedPath, "--request-json", "-"})
	})
	if differentTargetErr == nil || !strings.Contains(differentTargetErr.Error(), integrationErrorOperationIDContradiction) {
		t.Fatalf("different target assertion did not deterministically contradict operation id: %v", differentTargetErr)
	}
	if got := decodeIntegrationReceipt(t, differentTargetOut); got.Error == nil || got.Error.Code != integrationErrorOperationIDContradiction {
		t.Fatalf("different-target contradiction returned the wrong result: %+v", got)
	}
}

func TestIntegrationRecoveryFailsClosedAfterForeignJJOperation(t *testing.T) {
	paths := setupRealStackRepo(t)
	prepareOrderedSpeedTarget(t, paths)
	targetCommit := jjFullCommitID(t, paths.defaultPath, "speed@")
	payloadCommit := jjFullCommitID(t, paths.defaultPath, "agm-speed-transition@")
	request := integrationRequestBytesForStrategy("foreign-op", "speed", []string{"agm-speed-transition"}, targetCommit, []string{payloadCommit}, integrationStrategyOrderedLine)
	originalPhaseHook := integrationEffectPhaseHook
	integrationEffectPhaseHook = func(phase string) error {
		if phase == integrationPhasePrepared {
			return errors.New("stop after durable preparation")
		}
		return nil
	}
	t.Cleanup(func() { integrationEffectPhaseHook = originalPhaseHook })
	withIntegrationStdin(t, string(request))
	_, _, _ = captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.speedPath, "--request-json", "-"})
	})
	integrationEffectPhaseHook = originalPhaseHook
	runJJ(t, "-R", paths.speedPath, "describe", "-m", "foreign operation")

	out, _, err := captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.speedPath, "--recover", "foreign-op", "--json"})
	})
	if err == nil {
		t.Fatal("recovery unexpectedly accepted foreign Jujutsu operation drift")
	}
	receipt := decodeIntegrationReceipt(t, out)
	if receipt.BatchDisposition != integrationBatchUnknownEffect || receipt.Error == nil || receipt.Error.Code != integrationErrorUnknownEffect || receipt.Error.NextAction != integrationNextActionOperatorReview {
		t.Fatalf("foreign operation did not fail closed: %+v", receipt)
	}
	withIntegrationStdin(t, string(request))
	reuseOut, _, reuseErr := captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.speedPath, "--request-json", "-"})
	})
	if reuseErr == nil {
		t.Fatal("same-request reuse ignored foreign Jujutsu operation drift")
	}
	if reused := decodeIntegrationReceipt(t, reuseOut); reused.Error == nil || reused.Error.Code != integrationErrorUnknownEffect || reused.Error.NextAction != integrationNextActionOperatorReview {
		t.Fatalf("same-request drift did not fail closed: %+v", reused)
	}
}

func TestRunIntegrateAcceptsNonEmptyUndescribedTargetFromRealJJState(t *testing.T) {
	paths := setupRealStackRepo(t)
	if err := os.WriteFile(filepath.Join(paths.speedPath, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", paths.speedPath, "file", "track", "root:dirty.txt")
	targetCommit := jjFullCommitID(t, paths.speedPath, "speed@")
	payloadCommit := jjFullCommitID(t, paths.defaultPath, "agm-speed-transition@")
	request := validIntegrationRequestBytes("dirty-target", "speed", "agm-speed-transition", targetCommit, payloadCommit)
	withIntegrationStdin(t, string(request))
	out, _, err := captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.speedPath, "--request-json", "-"})
	})
	if err != nil {
		t.Fatalf("non-empty undescribed target integration failed: %v\n%s", err, out)
	}
	receipt := decodeIntegrationReceipt(t, out)
	if receipt.BatchDisposition != integrationBatchSucceeded || receipt.Target.PreservedCommit != targetCommit || jjRevsetCount(t, paths.defaultPath, targetCommit+" & ::speed@") != 1 {
		t.Fatalf("non-empty undescribed target was not preserved exactly: %+v", receipt.Target)
	}
}

func TestRunIntegrateAcceptsNonEmptyDescribedTargetWithoutExternalCursorMutation(t *testing.T) {
	paths := setupRealStackRepo(t)
	ignoreIntegrationStateInFixture(t, paths.defaultPath)
	runJJ(t, "-R", paths.defaultPath, "commit", "-m", "ignore integration state")
	updateIntegrationFixtureWorkspaces(t, paths.defaultPath, paths.speedPath, paths.childPath)
	if err := os.WriteFile(filepath.Join(paths.speedPath, "current-a.txt"), []byte("materialized A work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", paths.speedPath, "file", "track", "root:current-a.txt")
	runJJ(t, "-R", paths.speedPath, "describe", "-m", "A materialized current work")
	mainBefore := jjFullCommitID(t, paths.defaultPath, "default@")
	targetCommit := jjFullCommitID(t, paths.speedPath, "speed@")
	payloadCommit := jjFullCommitID(t, paths.defaultPath, "agm-speed-transition@")
	request := validIntegrationRequestBytes("described-materialized-target", "speed", "agm-speed-transition", targetCommit, payloadCommit)
	withIntegrationStdin(t, string(request))
	out, _, err := captureOutput(func() error { return runIntegrate([]string{"--repo", paths.speedPath, "--request-json", "-"}) })
	if err != nil {
		t.Fatalf("non-empty described target integration failed: %v\n%s", err, out)
	}
	receipt := decodeIntegrationReceipt(t, out)
	if receipt.BatchDisposition != integrationBatchSucceeded || receipt.Target.PreservedCommit != targetCommit || receipt.Target.BeforeHeadCommit != targetCommit {
		t.Fatalf("described target receipt lacks exact preservation: %+v", receipt.Target)
	}
	if jjRevsetCount(t, paths.defaultPath, targetCommit+" & ::speed@") != 1 {
		t.Fatalf("asserted target commit is not exact result ancestry: %s", targetCommit)
	}
	if got := jjFullCommitID(t, paths.defaultPath, "default@"); got != mainBefore {
		t.Fatalf("configured Main moved: before=%s after=%s", mainBefore, got)
	}
	if got := jjRevsetCount(t, paths.defaultPath, `empty() & description("") & speed@`); got != 1 {
		t.Fatalf("target did not finish on one fresh empty cursor: %d", got)
	}
	replayOut, _, replayErr := captureOutput(func() error {
		withIntegrationStdin(t, string(request))
		return runIntegrate([]string{"--repo", paths.speedPath, "--request-json", "-"})
	})
	if replayErr != nil || replayOut != out {
		t.Fatalf("terminal replay changed exact receipt: err=%v\nfirst=%s\nreplay=%s", replayErr, out, replayOut)
	}
}

func TestRunIntegrateReturnsTerminalReceiptIdempotently(t *testing.T) {
	paths := setupRealStackRepo(t)
	targetCommit := jjFullCommitID(t, paths.defaultPath, "speed@")
	payloadCommit := jjFullCommitID(t, paths.defaultPath, "agm-speed-transition@")
	requestBytes := validIntegrationRequestBytes("terminal-op", "speed", "agm-speed-transition", targetCommit, payloadCommit)
	withIntegrationStdin(t, string(requestBytes))
	firstOut, _, firstErr := captureOutput(func() error { return runIntegrate([]string{"--repo", paths.speedPath, "--request-json", "-"}) })
	if firstErr != nil {
		t.Fatalf("initial terminal integration failed: %v", firstErr)
	}
	first := decodeIntegrationReceipt(t, firstOut)
	if first.BatchDisposition != integrationBatchSucceeded {
		t.Fatalf("initial integration was not terminal success: %+v", first)
	}

	withIntegrationStdin(t, string(requestBytes))
	out, _, err := captureOutput(func() error { return runIntegrate([]string{"--repo", paths.speedPath, "--request-json", "-"}) })
	if err != nil {
		t.Fatalf("terminal idempotent request failed: %v", err)
	}
	if got := decodeIntegrationReceipt(t, out); got.BatchDisposition != integrationBatchSucceeded || got.JJOperations.CommitPoint != first.JJOperations.CommitPoint || out != firstOut {
		t.Fatalf("did not return the proved terminal receipt: %+v", got)
	}
}

func TestOversizedStoredTerminalReceiptReturnsBoundedMachineError(t *testing.T) {
	paths := setupRealStackRepo(t)
	targetCommit := jjFullCommitID(t, paths.defaultPath, "speed@")
	payloadCommit := jjFullCommitID(t, paths.defaultPath, "agm-speed-transition@")
	request := validIntegrationRequestBytes("oversized-receipt", "speed", "agm-speed-transition", targetCommit, payloadCommit)
	withIntegrationStdin(t, string(request))
	_, _, _ = captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.speedPath, "--request-json", "-"})
	})

	stateDir := filepath.Join(paths.defaultPath, ".ajj", "integrations")
	record, found, err := loadIntegrationOperationRecord(stateDir, "oversized-receipt")
	if err != nil || !found {
		t.Fatalf("load prepared oversized-receipt record: found=%v err=%v", found, err)
	}
	record.Phase = integrationPhaseTerminal
	setTestSuccessfulOperation(&record, strings.Repeat("2", 128), payloadCommit, targetCommit)
	record.Receipt = successfulTestIntegrationReceipt(record)
	changes := make([]integrationReceiptChangeV1, integrationMaxReceiptChanges+1)
	for i := range changes {
		changes[i] = integrationReceiptChangeV1{
			ChangeID: strings.Repeat("a", 32), InputCommit: payloadCommit, LandedCommit: payloadCommit,
		}
	}
	record.Receipt.Payloads[0].Changes = changes
	record.Receipt.Payloads[0].EvidenceDigest = integrationPayloadReceiptEvidenceDigest(record.Receipt.Payloads[0])
	record.Receipt.EvidenceDigest = integrationReceiptEvidenceDigest(*record.Receipt)
	encodedReceipt, err := encodeIntegrationJSON(*record.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	if len(encodedReceipt) > integrationMaxRecordBytes {
		t.Fatalf("over-limit receipt fixture exceeds the bounded journal: %d bytes", len(encodedReceipt))
	}
	writeForgedIntegrationRecord(t, stateDir, record)
	recordPath, _ := integrationOperationRecordPath(stateDir, record.OperationID)
	info, err := os.Stat(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > integrationMaxRecordBytes {
		t.Fatalf("fixture must exercise receipt validation inside the record read bound: size=%d", info.Size())
	}

	out, _, recoverErr := captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.speedPath, "--recover", "oversized-receipt", "--json"})
	})
	if recoverErr == nil {
		t.Fatal("oversized stored receipt unexpectedly emitted as terminal success")
	}
	if len(out) == 0 || len(out) > integrationMaxOutputBytes {
		t.Fatalf("oversized stored receipt did not yield bounded JSON: bytes=%d", len(out))
	}
	failure := decodeIntegrationReceipt(t, out)
	if failure.Error == nil || failure.Error.Code != integrationErrorInternal || failure.Error.Message != integrationPublicErrorSummaries[integrationErrorInternal] {
		t.Fatalf("oversized stored receipt returned the wrong bounded failure: %+v", failure)
	}

	malformed := cloneIntegrationRecord(t, record)
	malformed.Receipt.Payloads[0].Changes = nil
	malformed.Receipt.Payloads[0].EvidenceDigest = integrationPayloadReceiptEvidenceDigest(malformed.Receipt.Payloads[0])
	malformed.Receipt.Target.AfterHeadCommit = "/tmp/not-a-commit"
	malformed.Receipt.EvidenceDigest = integrationReceiptEvidenceDigest(*malformed.Receipt)
	writeForgedIntegrationRecord(t, stateDir, malformed)
	malformedOut, _, malformedErr := captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.speedPath, "--recover", "oversized-receipt", "--json"})
	})
	if malformedErr == nil || len(malformedOut) == 0 || len(malformedOut) > integrationMaxOutputBytes {
		t.Fatalf("malformed stored receipt did not return bounded machine JSON: err=%v bytes=%d", malformedErr, len(malformedOut))
	}
	if strings.Contains(malformedOut, "/tmp/not-a-commit") || strings.Contains(malformedOut, stateDir) {
		t.Fatalf("malformed stored receipt leaked a path in machine JSON: %s", malformedOut)
	}
	if malformedFailure := decodeIntegrationReceipt(t, malformedOut); malformedFailure.Error == nil || malformedFailure.Error.Code != integrationErrorInternal {
		t.Fatalf("malformed stored receipt returned the wrong failure: %+v", malformedFailure)
	}
}

func TestRunIntegrateRejectsWrongTargetAndChangedHeadBeforeRecord(t *testing.T) {
	paths := setupRealStackRepo(t)
	targetCommit := jjFullCommitID(t, paths.defaultPath, "speed@")
	payloadCommit := jjFullCommitID(t, paths.defaultPath, "agm-speed-transition@")
	cases := []struct {
		name         string
		operationID  string
		target       string
		targetCommit string
	}{
		{name: "wrong current target", operationID: "wrong-target", target: "default", targetCommit: targetCommit},
		{name: "changed target head", operationID: "changed-head", target: "speed", targetCommit: strings.Repeat("0", 40)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			request := validIntegrationRequestBytes(test.operationID, test.target, "agm-speed-transition", test.targetCommit, payloadCommit)
			withIntegrationStdin(t, string(request))
			out, _, err := captureOutput(func() error { return runIntegrate([]string{"--repo", paths.speedPath, "--request-json", "-"}) })
			if err == nil {
				t.Fatal("expected assertion failure")
			}
			receipt := decodeIntegrationReceipt(t, out)
			if receipt.Error == nil || receipt.Error.Code != integrationErrorAssertionFailed || receipt.Error.Message != integrationPublicErrorSummaries[integrationErrorAssertionFailed] {
				t.Fatalf("unexpected assertion receipt: %+v", receipt)
			}
			path, _ := integrationOperationRecordPath(filepath.Join(paths.defaultPath, ".ajj", "integrations"), test.operationID)
			if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("assertion failure persisted an operation record: %v", statErr)
			}
		})
	}
}

func TestValidateIntegrationAssertionsRejectsConflictedTarget(t *testing.T) {
	commit := strings.Repeat("a", 40)
	request := integrationRequestV1{
		Schema: integrationRequestSchemaV1, OperationID: "op", Strategy: integrationStrategySingle,
		Target:   integrationTargetAssertionV1{ExpectedWorkspace: "alpha", ExpectedHeadCommit: commit},
		Payloads: []integrationPayloadAssertionV1{{Workspace: "bravo", ExpectedHeadCommit: commit}},
	}
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "op log"):
			return strings.Repeat("f", 128) + "\n", nil
		case strings.Contains(joined, " -T change_id"):
			return strings.Repeat("a", 32), nil
		case strings.Contains(joined, "empty() & alpha@"):
			return commit + "\n", nil
		case strings.Contains(joined, " -T description"):
			return "", nil
		case strings.Contains(joined, "conflicts() & alpha@"):
			return commit + "\n", nil
		case strings.Contains(joined, "mutable() & alpha@"):
			return commit + "\n", nil
		case strings.Contains(joined, "commit_id"):
			return commit + "\n", nil
		default:
			return "", nil
		}
	})
	_, err := validateIntegrationAssertions("/repo", request)
	assertIntegrationProtocolError(t, err, integrationErrorAssertionFailed, "target Workspace \"alpha\" head is conflicted")
}

func TestValidateIntegrationAssertionsRejectsConflictedHeadsAndOperationChanges(t *testing.T) {
	commit := strings.Repeat("a", 40)
	request := integrationRequestV1{
		Schema: integrationRequestSchemaV1, OperationID: "op", Strategy: integrationStrategySingle,
		Target:   integrationTargetAssertionV1{ExpectedWorkspace: "alpha", ExpectedHeadCommit: commit},
		Payloads: []integrationPayloadAssertionV1{{Workspace: "bravo", ExpectedHeadCommit: commit}},
	}
	withCommandCapture(t, func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "op log"):
			return strings.Repeat("f", 128) + "\n", nil
		case strings.Contains(joined, " -T change_id"):
			return strings.Repeat("a", 32), nil
		case strings.Contains(joined, "empty() & alpha@"):
			return commit + "\n", nil
		case strings.Contains(joined, " -T description"):
			return "", nil
		case strings.Contains(joined, "conflicts() & bravo@"):
			return commit + "\n", nil
		case strings.Contains(joined, "conflicts() & alpha@"):
			return "", nil
		case strings.Contains(joined, "mutable() & alpha@"):
			return commit + "\n", nil
		case strings.Contains(joined, "commit_id"):
			return commit + "\n", nil
		default:
			return "", nil
		}
	})
	_, err := validateIntegrationAssertions("/repo", request)
	assertIntegrationProtocolError(t, err, integrationErrorAssertionFailed, "payload Workspace \"bravo\" head is conflicted")
	assertIntegrationProtocolError(t, validateIntegrationOperationUnchanged("op-1", "op-2"), integrationErrorAssertionFailed, "operation changed")
}

func validIntegrationRequestBytes(operationID, target, payload, targetCommit, payloadCommit string) []byte {
	return integrationRequestBytesForStrategy(operationID, target, []string{payload}, targetCommit, []string{payloadCommit}, integrationStrategySingle)
}

func integrationRequestBytesForStrategy(operationID, target string, payloads []string, targetCommit string, payloadCommits []string, strategy string) []byte {
	items := make([]string, len(payloads))
	for i := range payloads {
		items[i] = fmt.Sprintf(`{"workspace":"%s","expectedHeadCommit":"%s"}`, payloads[i], payloadCommits[i])
	}
	return []byte(fmt.Sprintf(`{"schema":"%s","operationId":"%s","target":{"expectedWorkspace":"%s","expectedHeadCommit":"%s"},"strategy":"%s","payloads":[%s]}`,
		integrationRequestSchemaV1, operationID, target, targetCommit, strategy, strings.Join(items, ",")))
}

func withIntegrationStdin(t *testing.T, input string) {
	t.Helper()
	original := stdinReader
	stdinReader = strings.NewReader(input)
	t.Cleanup(func() { stdinReader = original })
}

func decodeIntegrationReceipt(t *testing.T, output string) integrationReceiptV1 {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(output))
	var receipt integrationReceiptV1
	if err := decoder.Decode(&receipt); err != nil {
		t.Fatalf("decode integration receipt: %v\n%s", err, output)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatalf("integration stdout contained trailing data: %v\n%s", err, output)
	}
	return receipt
}

func jjFullCommitID(t *testing.T, repoPath, revset string) string {
	t.Helper()
	cmd := exec.Command("jj", "-R", repoPath, "--color=never", "--no-pager", "--ignore-working-copy", "log", "-r", revset, "--no-graph", "-T", "commit_id ++ \"\\n\"")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("resolve full commit id for %s: %v\n%s", revset, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeForgedIntegrationRecord(t *testing.T, stateDir string, record integrationOperationRecord) {
	t.Helper()
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path, err := integrationOperationRecordPath(stateDir, record.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func cloneIntegrationRecord(t *testing.T, record integrationOperationRecord) integrationOperationRecord {
	t.Helper()
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var clone integrationOperationRecord
	if err := json.Unmarshal(data, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func integrationTestPreparedState() integrationPreparedStateV1 {
	return integrationPreparedStateV1{
		Target:     integrationPreparedTargetV1{Workspace: "alpha", HeadCommit: strings.Repeat("a", 40), HeadChangeID: strings.Repeat("a", 32)},
		StackShape: "linear",
		Payloads: []integrationPreparedPayloadV1{{
			Workspace: "bravo", HeadCommit: strings.Repeat("b", 40),
			Changes:         []integrationPreparedChangeV1{{ChangeID: strings.Repeat("c", 32), CommitID: strings.Repeat("b", 40)}},
			FrontierCommits: []string{strings.Repeat("b", 40)},
		}},
	}
}

func integrationTestRepositoryView(targetCommit string) integrationRepositoryViewV1 {
	return integrationRepositoryViewV1{
		Workspaces: []integrationWorkspaceHeadEvidenceV1{
			{Workspace: "alpha", CommitID: targetCommit, ChangeID: strings.Repeat("a", 32)},
			{Workspace: "bravo", CommitID: strings.Repeat("b", 40), ChangeID: strings.Repeat("b", 32)},
		},
		VisibleHeads: []string{targetCommit, strings.Repeat("b", 40)},
		Target: integrationTargetCommitEvidenceV1{
			CommitID:        targetCommit,
			ChangeID:        strings.Repeat("a", 32),
			ParentCommitIDs: []string{strings.Repeat("e", 40)},
		},
	}
}

func setTestSuccessfulOperation(record *integrationOperationRecord, operationID, integratedTip, afterHead string) {
	record.CommitPointOperation = operationID
	record.GraphOperationID = operationID
	record.DetachedOperationIDs = []string{operationID}
	record.PreparedState.StackShape = "linear"
	state := &integrationTargetAdvancedStateV1{IntegratedTipCommit: integratedTip, AfterHeadCommit: afterHead}
	record.StagedTargetState = state
	record.StagedRepositoryView = integrationTestRepositoryView(afterHead)
	record.StagedRepositoryView.Workspaces[0].CommitID = afterHead
	for i, payload := range record.PreparedState.Payloads {
		cursorHead := strings.Repeat(string(rune('f'-i)), 40)
		record.PayloadCursors = append(record.PayloadCursors, integrationPayloadCursorV1{Workspace: payload.Workspace, HeadCommit: cursorHead})
		for j := range record.StagedRepositoryView.Workspaces {
			if record.StagedRepositoryView.Workspaces[j].Workspace == payload.Workspace {
				record.StagedRepositoryView.Workspaces[j].CommitID = cursorHead
			}
		}
	}
	copyState := *state
	record.TargetAdvancedState = &copyState
	record.StagedPayloadMappings = make([][]integrationReceiptChangeV1, len(record.PreparedState.Payloads))
	for i, payload := range record.PreparedState.Payloads {
		for _, change := range payload.Changes {
			record.StagedPayloadMappings[i] = append(record.StagedPayloadMappings[i], integrationReceiptChangeV1{ChangeID: change.ChangeID, InputCommit: change.CommitID, LandedCommit: change.CommitID})
		}
	}
}

func successfulTestIntegrationReceipt(record integrationOperationRecord) *integrationReceiptV1 {
	payload := record.PreparedState.Payloads[0]
	receipt := &integrationReceiptV1{
		Schema: integrationReceiptSchemaV1, OperationID: record.OperationID, RequestDigest: record.RequestDigest,
		Strategy: integrationStrategySingle, BatchDisposition: integrationBatchSucceeded,
		Target: integrationReceiptTargetV1{
			Workspace: record.PreparedState.Target.Workspace, BeforeHeadCommit: record.PreparedState.Target.HeadCommit,
			BeforeHeadChangeID: record.PreparedState.Target.HeadChangeID, PreservationDisposition: integrationTargetPreservedExact,
			PreservedCommit: record.PreparedState.Target.HeadCommit, IntegratedTipCommit: payload.HeadCommit, AfterHeadCommit: strings.Repeat("d", 40),
		},
		Payloads: []integrationReceiptPayloadV1{{
			Workspace: payload.Workspace, InputHeadCommit: payload.HeadCommit, Disposition: integrationPayloadLanded,
			Changes: []integrationReceiptChangeV1{{ChangeID: payload.Changes[0].ChangeID, InputCommit: payload.Changes[0].CommitID, LandedCommit: payload.Changes[0].CommitID}},
		}},
		JJOperations: integrationJJOperationsV1{BeforeEffect: record.BeforeOperationID, CommitPoint: record.CommitPointOperation},
	}
	receipt.Payloads[0].EvidenceDigest = integrationPayloadReceiptEvidenceDigest(receipt.Payloads[0])
	receipt.EvidenceDigest = integrationReceiptEvidenceDigest(*receipt)
	return receipt
}

func failedTestIntegrationReceipt(record integrationOperationRecord) *integrationReceiptV1 {
	payload := record.PreparedState.Payloads[0]
	receipt := &integrationReceiptV1{
		Schema: integrationReceiptSchemaV1, OperationID: record.OperationID, RequestDigest: record.RequestDigest,
		Strategy: integrationStrategySingle, BatchDisposition: integrationBatchFailed,
		Target: integrationReceiptTargetV1{
			Workspace: record.PreparedState.Target.Workspace, BeforeHeadCommit: record.PreparedState.Target.HeadCommit,
			BeforeHeadChangeID: record.PreparedState.Target.HeadChangeID, PreservationDisposition: integrationTargetProvedUnchanged,
			PreservedCommit: record.PreparedState.Target.HeadCommit,
		},
		Payloads: []integrationReceiptPayloadV1{{
			Workspace: payload.Workspace, InputHeadCommit: payload.HeadCommit, Disposition: integrationPayloadProvedNotLanded,
			Changes: []integrationReceiptChangeV1{{ChangeID: payload.Changes[0].ChangeID, InputCommit: payload.Changes[0].CommitID}},
		}},
		JJOperations: integrationJJOperationsV1{BeforeEffect: record.BeforeOperationID},
		Error: &integrationReceiptErrorV1{
			Code: integrationErrorOperationInterrupted, Message: integrationPublicErrorSummaries[integrationErrorOperationInterrupted], NextAction: integrationNextActionRetryNewOperation,
		},
	}
	receipt.Payloads[0].EvidenceDigest = integrationPayloadReceiptEvidenceDigest(receipt.Payloads[0])
	receipt.EvidenceDigest = integrationReceiptEvidenceDigest(*receipt)
	return receipt
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
