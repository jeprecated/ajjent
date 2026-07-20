package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestRunIntegrateSingleLandsInCurrentWorkspaceThenMain(t *testing.T) {
	paths := setupRealStackRepo(t)
	ignoreIntegrationStateInFixture(t, paths.defaultPath)
	runJJ(t, "-R", paths.defaultPath, "commit", "-m", "project config")
	updateIntegrationFixtureWorkspaces(t, paths.defaultPath, paths.speedPath, paths.childPath)
	defaultBefore := jjFullCommitID(t, paths.defaultPath, "default@")
	speedBefore := jjFullCommitID(t, paths.defaultPath, "speed@")
	childHead := jjFullCommitID(t, paths.defaultPath, "agm-speed-transition@")
	childPayload := jjFullCommitID(t, paths.defaultPath, "agm-speed-transition@-")
	request := validIntegrationRequestBytes("single-speed", "speed", "agm-speed-transition", speedBefore, childHead)

	withIntegrationStdin(t, string(request))
	out, errOut, err := captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.speedPath, "--request-json", "-"})
	})
	if err != nil {
		t.Fatalf("single integration failed: %v\nstdout:%s\nstderr:%s", err, out, errOut)
	}
	receipt := decodeIntegrationReceipt(t, out)
	if receipt.BatchDisposition != integrationBatchSucceeded || receipt.Target.Workspace != "speed" || len(receipt.Payloads) != 1 || receipt.Payloads[0].Disposition != integrationPayloadLanded {
		t.Fatalf("unexpected single receipt: %+v", receipt)
	}
	if got := jjFullCommitID(t, paths.defaultPath, "default@"); got != defaultBefore {
		t.Fatalf("configured Main moved during A <- A1: before=%s after=%s", defaultBefore, got)
	}
	if got := jjFullCommitID(t, paths.defaultPath, "speed@"); got != receipt.Target.AfterHeadCommit {
		t.Fatalf("receipt target after head is stale: receipt=%s graph=%s", receipt.Target.AfterHeadCommit, got)
	}
	if got := jjRevsetCount(t, paths.defaultPath, childPayload+" & ::speed@"); got != 1 {
		t.Fatalf("child payload is not represented in speed: count=%d\n%s", got, errOut)
	}
	if len(receipt.Payloads[0].Changes) != 1 || receipt.Payloads[0].Changes[0].InputCommit != childPayload || receipt.Payloads[0].Changes[0].LandedCommit == "" {
		t.Fatalf("receipt did not map exact payload change: %+v", receipt.Payloads[0])
	}
	record, found, loadErr := loadIntegrationOperationRecord(filepath.Join(paths.defaultPath, ".ajj", "integrations"), "single-speed")
	if loadErr != nil || !found || len(record.DetachedOperationIDs) == 0 || record.GraphOperationID != record.DetachedOperationIDs[len(record.DetachedOperationIDs)-1] || record.CommitPointOperation != record.GraphOperationID || record.PublishPending {
		t.Fatalf("terminal journal does not bind the exact detached transaction: found=%v err=%v record=%+v", found, loadErr, record)
	}

	withIntegrationStdin(t, string(request))
	repeatOut, _, repeatErr := captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.speedPath, "--request-json", "-"})
	})
	if repeatErr != nil || repeatOut != out {
		t.Fatalf("terminal replay was not idempotent: err=%v\nfirst:%s\nrepeat:%s", repeatErr, out, repeatOut)
	}

	defaultBefore = jjFullCommitID(t, paths.defaultPath, "default@")
	speedAfter := jjFullCommitID(t, paths.defaultPath, "speed@")
	defaultRequest := validIntegrationRequestBytes("single-main", "default", "speed", defaultBefore, speedAfter)
	parsedMain, _, parseMainErr := parseIntegrationRequestV1(defaultRequest)
	if parseMainErr != nil {
		t.Fatal(parseMainErr)
	}
	if _, assertionErr := validateIntegrationAssertions(paths.defaultPath, parsedMain); assertionErr != nil {
		t.Fatalf("nested fixture assertions fail before integration: %v", assertionErr)
	}
	withIntegrationStdin(t, string(defaultRequest))
	mainOut, mainErrOut, mainErr := captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.defaultPath, "--request-json", "-"})
	})
	if mainErr != nil {
		t.Fatalf("Main <- A integration failed: %v\nstdout:%s\nstderr:%s", mainErr, mainOut, mainErrOut)
	}
	if got := jjRevsetCount(t, paths.defaultPath, childPayload+" & ::default@"); got != 1 {
		t.Fatalf("Main does not represent the nested child payload: %d", got)
	}
}

func TestRunIntegrateProviderDefaultMergePreservesPayloadCommits(t *testing.T) {
	paths := setupRealStackMergeRepo(t, false)
	ignoreIntegrationStateInFixture(t, paths.defaultPath)
	runJJ(t, "-R", paths.defaultPath, "commit", "-m", "ignore integration state")
	updateIntegrationFixtureWorkspaces(t, paths.defaultPath, paths.alphaPath, paths.bravoPath)
	targetBefore := jjFullCommitID(t, paths.defaultPath, "default@")
	alphaHead := jjFullCommitID(t, paths.defaultPath, "alpha@")
	bravoHead := jjFullCommitID(t, paths.defaultPath, "bravo@")
	alphaPayload := jjFullCommitID(t, paths.defaultPath, "alpha@-")
	bravoPayload := jjFullCommitID(t, paths.defaultPath, "bravo@-")
	request := integrationRequestBytesForStrategy("provider-merge", "default", []string{"alpha", "bravo"}, targetBefore, []string{alphaHead, bravoHead}, integrationStrategyProviderDefault)
	parsed, _, parseErr := parseIntegrationRequestV1(request)
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	if _, assertionErr := validateIntegrationAssertions(paths.defaultPath, parsed); assertionErr != nil {
		t.Fatalf("fixture assertions fail before command materialization: %v", assertionErr)
	}
	withIntegrationStdin(t, string(request))
	out, errOut, err := captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.defaultPath, "--request-json", "-"})
	})
	if err != nil {
		t.Fatalf("provider-default merge failed: %v\nstdout:%s\nstderr:%s", err, out, errOut)
	}
	receipt := decodeIntegrationReceipt(t, out)
	if receipt.BatchDisposition != integrationBatchSucceeded || len(receipt.Payloads) != 2 {
		t.Fatalf("unexpected merge receipt: %+v", receipt)
	}
	if got := jjFullCommitID(t, paths.defaultPath, "default@"); got != receipt.Target.AfterHeadCommit {
		t.Fatalf("merge receipt target after head is stale: receipt=%s graph=%s", receipt.Target.AfterHeadCommit, got)
	}
	wantInputs := []string{alphaPayload, bravoPayload}
	for i, payload := range receipt.Payloads {
		if len(payload.Changes) != 1 || payload.Changes[0].InputCommit != wantInputs[i] || payload.Changes[0].LandedCommit != wantInputs[i] {
			t.Fatalf("merge payload %d was not preserved exactly: %+v", i, payload)
		}
		if got := jjRevsetCount(t, paths.defaultPath, wantInputs[i]+" & ::default@"); got != 1 {
			t.Fatalf("merge payload %d is not represented in target", i)
		}
	}
}

func TestRunIntegrateSinglePreservesExistingCleanTargetWork(t *testing.T) {
	paths := setupRealTidySingleStackRepo(t, false)
	ignoreIntegrationStateInFixture(t, paths.defaultPath)
	runJJ(t, "-R", paths.defaultPath, "commit", "-m", "ignore integration state")
	updateIntegrationFixtureWorkspaces(t, paths.defaultPath, paths.alphaPath)
	targetBefore := jjFullCommitID(t, paths.defaultPath, "default@")
	mainChange := jjRev(t, paths.defaultPath, "default@-")
	alphaHead := jjFullCommitID(t, paths.defaultPath, "alpha@")
	alphaPayloadChange := jjRev(t, paths.defaultPath, "alpha@-")
	request := validIntegrationRequestBytes("existing-target", "default", "alpha", targetBefore, alphaHead)
	withIntegrationStdin(t, string(request))
	out, errOut, err := captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.defaultPath, "--request-json", "-"})
	})
	if err != nil {
		t.Fatalf("integration with existing target work failed: %v\nstdout:%s\nstderr:%s", err, out, errOut)
	}
	if got := jjRevsetCount(t, paths.defaultPath, "change_id("+mainChange+") & ::default@"); got != 1 {
		t.Fatalf("target lost existing target change %s", mainChange)
	}
	if got := jjRevsetCount(t, paths.defaultPath, "change_id("+alphaPayloadChange+") & ::default@"); got != 1 {
		t.Fatalf("target lost payload change %s", alphaPayloadChange)
	}
	receipt := decodeIntegrationReceipt(t, out)
	if len(receipt.Payloads) != 1 || len(receipt.Payloads[0].Changes) != 1 || receipt.Payloads[0].Changes[0].InputCommit == receipt.Payloads[0].Changes[0].LandedCommit {
		t.Fatalf("clean insertion receipt did not bind rewritten input and landed commits: %+v", receipt.Payloads)
	}
}

func TestRunIntegrateProviderConflictRollsBackAndProvesNotLanded(t *testing.T) {
	paths := setupRealStackMergeRepo(t, true)
	ignoreIntegrationStateInFixture(t, paths.defaultPath)
	runJJ(t, "-R", paths.defaultPath, "commit", "-m", "ignore integration state")
	updateIntegrationFixtureWorkspaces(t, paths.defaultPath, paths.alphaPath, paths.bravoPath)
	targetBefore := jjFullCommitID(t, paths.defaultPath, "default@")
	alphaHead := jjFullCommitID(t, paths.defaultPath, "alpha@")
	bravoHead := jjFullCommitID(t, paths.defaultPath, "bravo@")
	request := integrationRequestBytesForStrategy("conflict-rollback", "default", []string{"alpha", "bravo"}, targetBefore, []string{alphaHead, bravoHead}, integrationStrategyProviderDefault)
	withIntegrationStdin(t, string(request))
	out, errOut, err := captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.defaultPath, "--request-json", "-"})
	})
	if err == nil || !strings.Contains(err.Error(), integrationErrorConflict) {
		t.Fatalf("conflicted integration did not return conflict: %v\n%s\n%s", err, out, errOut)
	}
	receipt := decodeIntegrationReceipt(t, out)
	if receipt.BatchDisposition != integrationBatchFailed || receipt.Error == nil || receipt.Error.Code != integrationErrorConflict {
		t.Fatalf("unexpected conflict receipt: %+v", receipt)
	}
	for _, payload := range receipt.Payloads {
		if payload.Disposition != integrationPayloadProvedNotLanded {
			t.Fatalf("conflict payload was not proved not landed: %+v", payload)
		}
	}
	if got := jjFullCommitID(t, paths.defaultPath, "default@"); got != targetBefore {
		t.Fatalf("target head was not restored: before=%s after=%s", targetBefore, got)
	}
	if got := jjRevsetCount(t, paths.defaultPath, "conflicts() & default@"); got != 0 {
		t.Fatalf("conflict remained after rollback: %d", got)
	}
}

func TestConflictNoEffectProofRejectsInterleavedHumanOperation(t *testing.T) {
	paths := setupRealStackMergeRepo(t, true)
	ignoreIntegrationStateInFixture(t, paths.defaultPath)
	runJJ(t, "-R", paths.defaultPath, "commit", "-m", "ignore integration state")
	updateIntegrationFixtureWorkspaces(t, paths.defaultPath, paths.alphaPath, paths.bravoPath)
	targetBefore := jjFullCommitID(t, paths.defaultPath, "default@")
	request := integrationRequestBytesForStrategy(
		"conflict-interleave", "default", []string{"alpha", "bravo"}, targetBefore,
		[]string{jjFullCommitID(t, paths.defaultPath, "alpha@"), jjFullCommitID(t, paths.defaultPath, "bravo@")},
		integrationStrategyProviderDefault,
	)
	originalHook := integrationNoEffectProofHook
	integrationNoEffectProofHook = func(boundary string) error {
		if boundary == "between-proofs" {
			runJJ(t, "-R", paths.defaultPath, "--ignore-working-copy", "bookmark", "create", "human-interleave", "-r", "root()")
		}
		return nil
	}
	t.Cleanup(func() { integrationNoEffectProofHook = originalHook })
	withIntegrationStdin(t, string(request))
	out, _, err := captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.defaultPath, "--request-json", "-"})
	})
	if err == nil {
		t.Fatal("conflict plus interleaved human operation unexpectedly proved no effect")
	}
	receipt := decodeIntegrationReceipt(t, out)
	if receipt.BatchDisposition != integrationBatchUnknownEffect || receipt.Error == nil || receipt.Error.Code != integrationErrorUnknownEffect {
		t.Fatalf("interleaved no-effect proof did not fail closed: %+v", receipt)
	}
	record, found, loadErr := loadIntegrationOperationRecord(filepath.Join(paths.defaultPath, ".ajj", "integrations"), "conflict-interleave")
	if loadErr != nil || !found || record.Phase == integrationPhaseTerminal {
		t.Fatalf("unknown-effect interleave was incorrectly closed terminal: found=%v err=%v phase=%s", found, loadErr, record.Phase)
	}
}

func TestConflictNoEffectProofRejectsForeignOperationBeforeTerminalRecord(t *testing.T) {
	paths := setupRealStackMergeRepo(t, true)
	ignoreIntegrationStateInFixture(t, paths.defaultPath)
	runJJ(t, "-R", paths.defaultPath, "commit", "-m", "ignore integration state")
	updateIntegrationFixtureWorkspaces(t, paths.defaultPath, paths.alphaPath, paths.bravoPath)
	targetBefore := jjFullCommitID(t, paths.defaultPath, "default@")
	request := integrationRequestBytesForStrategy(
		"conflict-final-fence", "default", []string{"alpha", "bravo"}, targetBefore,
		[]string{jjFullCommitID(t, paths.defaultPath, "alpha@"), jjFullCommitID(t, paths.defaultPath, "bravo@")},
		integrationStrategyProviderDefault,
	)
	originalHook := integrationNoEffectProofHook
	integrationNoEffectProofHook = func(boundary string) error {
		if boundary == "before-terminal-record" {
			runJJ(t, "-R", paths.defaultPath, "--ignore-working-copy", "bookmark", "create", "final-proof-interleave", "-r", "root()")
		}
		return nil
	}
	t.Cleanup(func() { integrationNoEffectProofHook = originalHook })
	withIntegrationStdin(t, string(request))
	out, _, err := captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.defaultPath, "--request-json", "-"})
	})
	if err == nil {
		t.Fatal("foreign operation after final proof unexpectedly terminalized no-effect")
	}
	receipt := decodeIntegrationReceipt(t, out)
	if receipt.BatchDisposition != integrationBatchUnknownEffect || receipt.Error == nil || receipt.Error.Code != integrationErrorUnknownEffect {
		t.Fatalf("final no-effect fence did not fail closed: %+v", receipt)
	}
	record, found, loadErr := loadIntegrationOperationRecord(filepath.Join(paths.defaultPath, ".ajj", "integrations"), "conflict-final-fence")
	if loadErr != nil || !found || record.Phase == integrationPhaseTerminal {
		t.Fatalf("foreign operation was incorrectly recorded terminal: found=%v err=%v phase=%s", found, loadErr, record.Phase)
	}
}

func TestRunIntegrateRejectsInProgressPayloadHead(t *testing.T) {
	paths := setupRealStackRepo(t)
	if err := os.WriteFile(filepath.Join(paths.childPath, "in-progress.txt"), []byte("work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", paths.childPath, "file", "track", "root:in-progress.txt")
	targetHead := jjFullCommitID(t, paths.defaultPath, "speed@")
	payloadHead := jjFullCommitID(t, paths.defaultPath, "agm-speed-transition@")
	request := validIntegrationRequestBytes("dirty-payload", "speed", "agm-speed-transition", targetHead, payloadHead)
	withIntegrationStdin(t, string(request))
	out, _, err := captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.speedPath, "--request-json", "-"})
	})
	if err == nil {
		t.Fatal("in-progress payload unexpectedly integrated")
	}
	if receipt := decodeIntegrationReceipt(t, out); receipt.Error == nil || receipt.Error.Code != integrationErrorAssertionFailed || receipt.Payloads[0].Disposition != integrationPayloadFailedBeforeEffect {
		t.Fatalf("in-progress payload returned the wrong receipt: %+v", receipt)
	}
}

func TestIntegrationRecoveryBeforeAndAfterCommitPoint(t *testing.T) {
	for _, test := range []struct {
		name            string
		phase           string
		wantDisposition string
		wantError       bool
	}{
		{name: "prepared proves not landed", phase: integrationPhasePrepared, wantDisposition: integrationBatchFailed, wantError: true},
		{name: "graph rewrite remains unpublished and proves not landed", phase: integrationPhaseGraphRewritten, wantDisposition: integrationBatchFailed, wantError: true},
		{name: "after commit completes", phase: integrationPhaseTargetAdvanced, wantDisposition: integrationBatchSucceeded},
		{name: "after cursors completes", phase: integrationPhaseCursorsReconciled, wantDisposition: integrationBatchSucceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			paths := setupRealStackRepo(t)
			targetBefore := jjFullCommitID(t, paths.defaultPath, "speed@")
			payloadHead := jjFullCommitID(t, paths.defaultPath, "agm-speed-transition@")
			request := validIntegrationRequestBytes("recover-"+strings.ReplaceAll(test.phase, "-", "_"), "speed", "agm-speed-transition", targetBefore, payloadHead)
			originalHook := integrationEffectPhaseHook
			integrationEffectPhaseHook = func(phase string) error {
				if phase == test.phase {
					return fmt.Errorf("interrupt after %s", phase)
				}
				return nil
			}
			t.Cleanup(func() { integrationEffectPhaseHook = originalHook })
			withIntegrationStdin(t, string(request))
			out, _, err := captureOutput(func() error {
				return runIntegrate([]string{"--repo", paths.speedPath, "--request-json", "-"})
			})
			if err == nil || decodeIntegrationReceipt(t, out).Error == nil {
				t.Fatalf("injected interruption did not stop initial command: err=%v out=%s", err, out)
			}
			integrationEffectPhaseHook = func(string) error { return nil }
			recoverOut, _, recoverErr := captureOutput(func() error {
				return runIntegrate([]string{"--repo", paths.speedPath, "--recover", "recover-" + strings.ReplaceAll(test.phase, "-", "_"), "--json"})
			})
			receipt := decodeIntegrationReceipt(t, recoverOut)
			if receipt.BatchDisposition != test.wantDisposition {
				t.Fatalf("recovery disposition=%s want=%s err=%v receipt=%+v", receipt.BatchDisposition, test.wantDisposition, recoverErr, receipt)
			}
			if test.wantError && recoverErr == nil {
				t.Fatal("pre-commit recovery should return a failed command")
			}
			if !test.wantError && recoverErr != nil {
				t.Fatalf("post-commit recovery failed: %v", recoverErr)
			}
		})
	}
}

func TestDetachedCommandAdapterFailsClosedBeforeJournalAuthority(t *testing.T) {
	before := strings.Repeat("1", 128)
	result := strings.Repeat("2", 128)
	wrongParent := strings.Repeat("3", 128)
	validLine := "Operation left uncommitted because --no-integrate-operation was requested: " + result[:12] + "\n"
	baseRecord := integrationOperationRecord{BeforeOperationID: before}
	binding := integrationStateBinding{StateDir: filepath.Join(t.TempDir(), "integrations")}

	originalCombined := commandCombinedCaptureFn
	originalCapture := commandCaptureFn
	t.Cleanup(func() {
		commandCombinedCaptureFn = originalCombined
		commandCaptureFn = originalCapture
	})

	t.Run("nonzero command is never expanded", func(t *testing.T) {
		expansions := 0
		commandCombinedCaptureFn = func(string, ...string) (string, error) { return validLine, errors.New("command failed") }
		commandCaptureFn = func(string, ...string) (string, error) { expansions++; return result + "\n", nil }
		record := baseRecord
		if err := stageDetachedCommand("/repo", before, binding, &record, "describe", "-m", "x"); err == nil || expansions != 0 {
			t.Fatalf("nonzero detached command was not rejected before expansion: err=%v expansions=%d", err, expansions)
		}
	})

	t.Run("ambiguous prefix expansion fails closed", func(t *testing.T) {
		commandCombinedCaptureFn = func(_ string, args ...string) (string, error) {
			joined := strings.Join(args, " ")
			if !strings.Contains(joined, "--color=never") || !strings.Contains(joined, "--no-pager") {
				t.Fatalf("detached command lacks deterministic output flags: %v", args)
			}
			return validLine, nil
		}
		commandCaptureFn = func(string, ...string) (string, error) { return "", errors.New("ambiguous operation prefix") }
		record := baseRecord
		if err := stageDetachedCommand("/repo", before, binding, &record, "describe", "-m", "x"); err == nil {
			t.Fatal("ambiguous detached operation prefix was accepted")
		}
	})

	t.Run("invalid expanded operation id fails closed", func(t *testing.T) {
		commandCombinedCaptureFn = func(string, ...string) (string, error) { return validLine, nil }
		commandCaptureFn = func(string, ...string) (string, error) { return "not-an-operation-id\n", nil }
		record := baseRecord
		if err := stageDetachedCommand("/repo", before, binding, &record, "describe", "-m", "x"); err == nil {
			t.Fatal("invalid expanded detached operation id was accepted")
		}
	})

	t.Run("expanded operation parent mismatch fails closed", func(t *testing.T) {
		commandCombinedCaptureFn = func(string, ...string) (string, error) { return validLine, nil }
		commandCaptureFn = func(_ string, args ...string) (string, error) {
			joined := strings.Join(args, " ")
			if strings.Contains(joined, "parents.map") {
				return result + "\t" + wrongParent + "\n", nil
			}
			return result + "\n", nil
		}
		record := baseRecord
		if err := stageDetachedCommand("/repo", before, binding, &record, "describe", "-m", "x"); err == nil || !strings.Contains(err.Error(), "expected parent") {
			t.Fatalf("wrong detached parent was not rejected: %v", err)
		}
	})
}

func TestDetachedTransactionProofRejectsForgedOperation(t *testing.T) {
	paths := setupRealStackRepo(t)
	targetBefore := jjFullCommitID(t, paths.defaultPath, "speed@")
	payloadHead := jjFullCommitID(t, paths.defaultPath, "agm-speed-transition@")
	requestBytes := validIntegrationRequestBytes("forged-detached", "speed", "agm-speed-transition", targetBefore, payloadHead)
	originalHook := integrationEffectPhaseHook
	integrationEffectPhaseHook = func(phase string) error {
		if phase == integrationPhaseGraphRewritten {
			return errors.New("stop before publish")
		}
		return nil
	}
	t.Cleanup(func() { integrationEffectPhaseHook = originalHook })
	withIntegrationStdin(t, string(requestBytes))
	_, _, _ = captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.speedPath, "--request-json", "-"})
	})

	stateDir := filepath.Join(paths.defaultPath, ".ajj", "integrations")
	record, found, err := loadIntegrationOperationRecord(stateDir, "forged-detached")
	if err != nil || !found {
		t.Fatalf("load staged operation: found=%v err=%v", found, err)
	}
	request := integrationRequestFromRecord(record)
	out, err := commandCombinedCaptureFn("jj", "-R", paths.speedPath, "--at-op="+record.BeforeOperationID, "--no-integrate-operation", "bookmark", "create", "forged-ref", "-r", "root()")
	if err != nil {
		t.Fatal(err)
	}
	short, err := detachedOperationID(out)
	if err != nil {
		t.Fatal(err)
	}
	forged, err := integrationOperationFullID(paths.speedPath, short)
	if err != nil {
		t.Fatal(err)
	}
	record.DetachedOperationIDs = []string{forged}
	record.GraphOperationID = forged
	if err := proveDetachedIntegrationTransaction(paths.speedPath, record, request); err == nil {
		t.Fatal("forged detached operation unexpectedly passed exact transaction proof")
	}
}

func TestIntegrationOperationEvidenceReportsExactParent(t *testing.T) {
	root := t.TempDir()
	runJJ(t, "git", "init", "--colocate", root)
	before := currentOperationIDFullForTest(t, root)
	out, err := commandCombinedCaptureFn("jj", "-R", root, "--at-op="+before, "--no-integrate-operation", "describe", "-m", "detached")
	if err != nil {
		t.Fatal(err)
	}
	short, err := detachedOperationID(out)
	if err != nil {
		t.Fatal(err)
	}
	detached, err := integrationOperationFullID(root, short)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := integrationOperationEvidence(root, detached)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.OperationID != detached || len(evidence.ParentOperationIDs) != 1 || evidence.ParentOperationIDs[0] != before {
		t.Fatalf("unexpected operation evidence: %+v", evidence)
	}
}

func currentOperationIDFullForTest(t *testing.T, repo string) string {
	t.Helper()
	id, err := currentOperationFullID(repo)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestIntegrationRecoveryCompletesAfterCursorReconciliationFailure(t *testing.T) {
	paths := setupRealStackMergeRepo(t, false)
	ignoreIntegrationStateInFixture(t, paths.defaultPath)
	runJJ(t, "-R", paths.defaultPath, "commit", "-m", "ignore integration state")
	updateIntegrationFixtureWorkspaces(t, paths.defaultPath, paths.alphaPath, paths.bravoPath)
	targetBefore := jjFullCommitID(t, paths.defaultPath, "default@")
	alphaHead := jjFullCommitID(t, paths.defaultPath, "alpha@")
	bravoHead := jjFullCommitID(t, paths.defaultPath, "bravo@")
	request := integrationRequestBytesForStrategy("cursor-failure", "default", []string{"alpha", "bravo"}, targetBefore, []string{alphaHead, bravoHead}, integrationStrategyProviderDefault)
	originalHook := integrationCursorReconcileHook
	integrationCursorReconcileHook = func(workspace string) error {
		if workspace == "bravo" {
			return errors.New("injected cursor reconciliation failure")
		}
		return nil
	}
	t.Cleanup(func() { integrationCursorReconcileHook = originalHook })
	withIntegrationStdin(t, string(request))
	out, _, err := captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.defaultPath, "--request-json", "-"})
	})
	if err == nil || decodeIntegrationReceipt(t, out).Error == nil {
		t.Fatalf("cursor failure did not interrupt integration: err=%v out=%s", err, out)
	}
	integrationCursorReconcileHook = func(string) error { return nil }
	recoverOut, _, recoverErr := captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.defaultPath, "--recover", "cursor-failure", "--json"})
	})
	if recoverErr != nil {
		t.Fatalf("cursor recovery failed: %v\n%s", recoverErr, recoverOut)
	}
	if receipt := decodeIntegrationReceipt(t, recoverOut); receipt.BatchDisposition != integrationBatchSucceeded {
		t.Fatalf("cursor recovery did not preserve landed result: %+v", receipt)
	}
}

func ignoreIntegrationStateInFixture(t *testing.T, repoPath string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repoPath, ".gitignore"), []byte(".ajj/integrations/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", repoPath, "file", "track", "root:.gitignore")
}

func updateIntegrationFixtureWorkspaces(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		runJJ(t, "-R", path, "workspace", "update-stale")
	}
}

func TestRunIntegrateMapsMultiCommitPayload(t *testing.T) {
	paths := setupRealTidySingleStackRepo(t, false)
	ignoreIntegrationStateInFixture(t, paths.defaultPath)
	runJJ(t, "-R", paths.defaultPath, "commit", "-m", "ignore integration state")
	if err := os.WriteFile(filepath.Join(paths.alphaPath, "second.txt"), []byte("second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", paths.alphaPath, "file", "track", "root:second.txt")
	runJJ(t, "-R", paths.alphaPath, "commit", "-m", "alpha second payload")
	updateIntegrationFixtureWorkspaces(t, paths.defaultPath, paths.alphaPath)
	targetBefore := jjFullCommitID(t, paths.defaultPath, "default@")
	payloadHead := jjFullCommitID(t, paths.defaultPath, "alpha@")
	request := validIntegrationRequestBytes("multi-commit", "default", "alpha", targetBefore, payloadHead)
	withIntegrationStdin(t, string(request))
	out, errOut, err := captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.defaultPath, "--request-json", "-"})
	})
	if err != nil {
		t.Fatalf("multi-commit integration failed: %v\n%s\n%s", err, out, errOut)
	}
	receipt := decodeIntegrationReceipt(t, out)
	if len(receipt.Payloads) != 1 || len(receipt.Payloads[0].Changes) != 2 {
		t.Fatalf("multi-commit receipt mapping is incomplete: %+v", receipt.Payloads)
	}
	for _, change := range receipt.Payloads[0].Changes {
		if got := jjRevsetCount(t, paths.defaultPath, "change_id("+change.ChangeID+") & ::default@"); got != 1 {
			t.Fatalf("mapped payload change %s is not in target", change.ChangeID)
		}
	}
}

func TestRunIntegrateAcceptsImmutablePayloadWithoutRewritingIt(t *testing.T) {
	paths := setupRealTidySingleStackRepo(t, false)
	ignoreIntegrationStateInFixture(t, paths.defaultPath)
	runJJ(t, "-R", paths.defaultPath, "commit", "-m", "ignore integration state")
	runJJ(t, "-R", paths.defaultPath, "config", "set", "--repo", `revset-aliases."immutable_heads()"`, "alpha@-")
	updateIntegrationFixtureWorkspaces(t, paths.defaultPath, paths.alphaPath)
	targetBefore := jjFullCommitID(t, paths.defaultPath, "default@")
	payloadHead := jjFullCommitID(t, paths.defaultPath, "alpha@")
	payloadCommit := jjFullCommitID(t, paths.defaultPath, "alpha@-")
	request := validIntegrationRequestBytes("immutable-payload", "default", "alpha", targetBefore, payloadHead)
	withIntegrationStdin(t, string(request))
	out, errOut, err := captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.defaultPath, "--request-json", "-"})
	})
	if err != nil {
		t.Fatalf("immutable payload integration failed: %v\n%s\n%s", err, out, errOut)
	}
	receipt := decodeIntegrationReceipt(t, out)
	if len(receipt.Payloads[0].Changes) != 1 || receipt.Payloads[0].Changes[0].InputCommit != payloadCommit || receipt.Payloads[0].Changes[0].LandedCommit != payloadCommit {
		t.Fatalf("immutable payload was rewritten or mapped incorrectly: %+v", receipt.Payloads[0])
	}
}

func TestMachineSingleMatchesOrdinaryStackGraphShape(t *testing.T) {
	human := setupRealTidySingleStackRepo(t, false)
	ignoreIntegrationStateInFixture(t, human.defaultPath)
	runJJ(t, "-R", human.defaultPath, "commit", "-m", "ignore integration state")
	updateIntegrationFixtureWorkspaces(t, human.defaultPath, human.alphaPath)
	_, _, err := captureOutput(func() error {
		return runStack([]string{"alpha", "--repo", human.defaultPath, "--workspace", "default", "--yes"})
	})
	if err != nil {
		t.Fatalf("ordinary Stack failed: %v", err)
	}
	machine := setupRealTidySingleStackRepo(t, false)
	ignoreIntegrationStateInFixture(t, machine.defaultPath)
	runJJ(t, "-R", machine.defaultPath, "commit", "-m", "ignore integration state")
	updateIntegrationFixtureWorkspaces(t, machine.defaultPath, machine.alphaPath)
	targetBefore := jjFullCommitID(t, machine.defaultPath, "default@")
	payloadHead := jjFullCommitID(t, machine.defaultPath, "alpha@")
	request := validIntegrationRequestBytes("equivalent-single", "default", "alpha", targetBefore, payloadHead)
	withIntegrationStdin(t, string(request))
	_, _, err = captureOutput(func() error {
		return runIntegrate([]string{"--repo", machine.defaultPath, "--request-json", "-"})
	})
	if err != nil {
		t.Fatalf("machine integration failed: %v", err)
	}
	humanGraph := normalizedStackGraph(t, human.defaultPath, "default")
	machineGraph := normalizedStackGraph(t, machine.defaultPath, "default")
	if humanGraph != machineGraph {
		t.Fatalf("ordinary/machine normalized full graph mismatch:\n--- human ---\n%s\n--- machine ---\n%s", humanGraph, machineGraph)
	}
}

func normalizedStackGraph(t *testing.T, repoPath, target string) string {
	t.Helper()
	logOut, err := exec.Command("jj", "-R", repoPath, "--ignore-working-copy", "log", "-r", "::"+target+"@ & ~root()", "--no-graph", "-T", `description.first_line() ++ "\t" ++ parents.len() ++ "\t" ++ if(empty, "empty", "nonempty") ++ "\t" ++ if(conflict, "conflict", "clean") ++ "\n"`).CombinedOutput()
	if err != nil {
		t.Fatalf("normalized graph log failed: %v\n%s", err, logOut)
	}
	diffOut, err := exec.Command("jj", "-R", repoPath, "--ignore-working-copy", "diff", "--from", "root()", "--to", target+"@", "--git").CombinedOutput()
	if err != nil {
		t.Fatalf("normalized graph tree diff failed: %v\n%s", err, diffOut)
	}
	normalizedDiff := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(diffOut)), "\n") {
		if strings.HasPrefix(line, "+workspaces_root: ") {
			line = "+workspaces_root: <WORKSPACES>"
		}
		if strings.HasPrefix(line, "index ") {
			line = "index <NORMALIZED>"
		}
		normalizedDiff = append(normalizedDiff, line)
	}
	return strings.TrimSpace(string(logOut)) + "\nTREE\n" + strings.Join(normalizedDiff, "\n")
}

func TestMachineIntegrationAbandonsTopEmptyMutableAncestors(t *testing.T) {
	paths := setupRealTidySingleStackRepo(t, false)
	ignoreIntegrationStateInFixture(t, paths.defaultPath)
	runJJ(t, "-R", paths.defaultPath, "commit", "-m", "ignore integration state")
	runJJ(t, "-R", paths.defaultPath, "new", "@")
	updateIntegrationFixtureWorkspaces(t, paths.defaultPath, paths.alphaPath)
	if before := jjRevsetCount(t, paths.defaultPath, topEmptyMutableAncestorsRevset("default@")); before == 0 {
		t.Fatal("fixture did not create a top empty mutable ancestor")
	}
	request := validIntegrationRequestBytes(
		"empty-ancestor-cleanup", "default", "alpha",
		jjFullCommitID(t, paths.defaultPath, "default@"), jjFullCommitID(t, paths.defaultPath, "alpha@"),
	)
	withIntegrationStdin(t, string(request))
	out, _, err := captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.defaultPath, "--request-json", "-"})
	})
	if err != nil || decodeIntegrationReceipt(t, out).BatchDisposition != integrationBatchSucceeded {
		t.Fatalf("machine integration with empty ancestors failed: %v\n%s", err, out)
	}
	if after := jjRevsetCount(t, paths.defaultPath, topEmptyMutableAncestorsRevset("default@")); after != 0 {
		t.Fatalf("machine integration left %d top empty mutable ancestors", after)
	}
}

func TestIntegrationCommandBoundaryRecoveryAndForeignInterleaves(t *testing.T) {
	baselinePaths, baselineRequest := setupCommandBoundaryFixture(t, "boundary-baseline")
	commandCount := 0
	originalHook := integrationEffectCommandHook
	integrationEffectCommandHook = func(_, boundary string) error {
		if boundary == "after-command" {
			commandCount++
		}
		return nil
	}
	withIntegrationStdin(t, string(baselineRequest))
	_, _, baselineErr := captureOutput(func() error {
		return runIntegrate([]string{"--repo", baselinePaths.speedPath, "--request-json", "-"})
	})
	integrationEffectCommandHook = originalHook
	if baselineErr != nil || commandCount < 2 {
		t.Fatalf("baseline integration did not expose command boundaries: count=%d err=%v", commandCount, baselineErr)
	}

	for ordinal := 1; ordinal <= commandCount; ordinal++ {
		t.Run("recover-command-"+strconv.Itoa(ordinal), func(t *testing.T) {
			paths, request := setupCommandBoundaryFixture(t, "recover-command-"+strconv.Itoa(ordinal))
			seen := 0
			integrationEffectCommandHook = func(_, boundary string) error {
				if boundary == "after-command" {
					seen++
					if seen == ordinal {
						return errors.New("simulated process boundary")
					}
				}
				return nil
			}
			t.Cleanup(func() { integrationEffectCommandHook = originalHook })
			withIntegrationStdin(t, string(request))
			_, _, _ = captureOutput(func() error {
				return runIntegrate([]string{"--repo", paths.speedPath, "--request-json", "-"})
			})
			integrationEffectCommandHook = originalHook
			out, _, _ := captureOutput(func() error {
				return runIntegrate([]string{"--repo", paths.speedPath, "--recover", "recover-command-" + strconv.Itoa(ordinal), "--json"})
			})
			receipt := decodeIntegrationReceipt(t, out)
			assertDetachedBoundaryDisposition(t, receipt, ordinal, commandCount)
		})

		t.Run("foreign-command-"+strconv.Itoa(ordinal), func(t *testing.T) {
			paths, request := setupCommandBoundaryFixture(t, "foreign-command-"+strconv.Itoa(ordinal))
			seen := 0
			integrationEffectCommandHook = func(_, boundary string) error {
				if boundary == "after-command" {
					seen++
					if seen == ordinal {
						runJJ(t, "-R", paths.speedPath, "--ignore-working-copy", "bookmark", "create", "foreign-"+strconv.Itoa(ordinal), "-r", "root()")
						return errors.New("foreign interleave")
					}
				}
				return nil
			}
			t.Cleanup(func() { integrationEffectCommandHook = originalHook })
			withIntegrationStdin(t, string(request))
			_, _, _ = captureOutput(func() error {
				return runIntegrate([]string{"--repo", paths.speedPath, "--request-json", "-"})
			})
			integrationEffectCommandHook = originalHook
			out, _, err := captureOutput(func() error {
				return runIntegrate([]string{"--repo", paths.speedPath, "--recover", "foreign-command-" + strconv.Itoa(ordinal), "--json"})
			})
			if err == nil {
				t.Fatalf("foreign command boundary %d unexpectedly recovered", ordinal)
			}
			receipt := decodeIntegrationReceipt(t, out)
			if receipt.BatchDisposition != integrationBatchUnknownEffect || receipt.Error == nil || receipt.Error.Code != integrationErrorUnknownEffect {
				t.Fatalf("foreign command boundary %d was not unknown-effect: %+v", ordinal, receipt)
			}
		})
	}
}

func TestIntegrationProcessCrashAfterEachMutation(t *testing.T) {
	if os.Getenv("AJJ_EFFECT_CRASH_HELPER") != "" {
		ordinal, _ := strconv.Atoi(os.Getenv("AJJ_EFFECT_CRASH_ORDINAL"))
		seen := 0
		integrationEffectCommandHook = func(_, boundary string) error {
			if boundary == "after-command" {
				seen++
				if seen == ordinal {
					os.Exit(86)
				}
			}
			return nil
		}
		stdinReader = strings.NewReader(os.Getenv("AJJ_EFFECT_CRASH_REQUEST"))
		_ = runIntegrate([]string{"--repo", os.Getenv("AJJ_EFFECT_CRASH_REPO"), "--request-json", "-"})
		os.Exit(0)
	}

	baselinePaths, baselineRequest := setupCommandBoundaryFixture(t, "process-baseline")
	count := 0
	originalHook := integrationEffectCommandHook
	integrationEffectCommandHook = func(_, boundary string) error {
		if boundary == "after-command" {
			count++
		}
		return nil
	}
	withIntegrationStdin(t, string(baselineRequest))
	_, _, err := captureOutput(func() error { return runIntegrate([]string{"--repo", baselinePaths.speedPath, "--request-json", "-"}) })
	integrationEffectCommandHook = originalHook
	if err != nil || count < 2 {
		t.Fatalf("baseline command count failed: count=%d err=%v", count, err)
	}

	for ordinal := 1; ordinal <= count; ordinal++ {
		paths, request := setupCommandBoundaryFixture(t, "process-crash-"+strconv.Itoa(ordinal))
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(binary, "-test.run=^TestIntegrationProcessCrashAfterEachMutation$")
		cmd.Env = append(os.Environ(),
			"AJJ_EFFECT_CRASH_HELPER=1",
			"AJJ_EFFECT_CRASH_ORDINAL="+strconv.Itoa(ordinal),
			"AJJ_EFFECT_CRASH_REPO="+paths.speedPath,
			"AJJ_EFFECT_CRASH_REQUEST="+string(request),
		)
		if err := cmd.Run(); err == nil || cmd.ProcessState.ExitCode() != 86 {
			t.Fatalf("helper did not crash at mutation %d: %v", ordinal, err)
		}
		out, _, _ := captureOutput(func() error {
			return runIntegrate([]string{"--repo", paths.speedPath, "--recover", "process-crash-" + strconv.Itoa(ordinal), "--json"})
		})
		receipt := decodeIntegrationReceipt(t, out)
		assertDetachedBoundaryDisposition(t, receipt, ordinal, count)
	}
}

func TestProviderDefaultMergeProcessCrashAfterEachMutation(t *testing.T) {
	baselineRepo, baselineRequest := setupMergeCommandBoundaryFixture(t, "merge-process-baseline")
	count := 0
	originalHook := integrationEffectCommandHook
	integrationEffectCommandHook = func(_, boundary string) error {
		if boundary == "after-command" {
			count++
		}
		return nil
	}
	withIntegrationStdin(t, string(baselineRequest))
	_, _, baselineErr := captureOutput(func() error { return runIntegrate([]string{"--repo", baselineRepo, "--request-json", "-"}) })
	integrationEffectCommandHook = originalHook
	if baselineErr != nil || count < 3 {
		t.Fatalf("merge process baseline failed: count=%d err=%v", count, baselineErr)
	}
	for ordinal := 1; ordinal <= count; ordinal++ {
		repo, request := setupMergeCommandBoundaryFixture(t, "merge-process-"+strconv.Itoa(ordinal))
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(binary, "-test.run=^TestIntegrationProcessCrashAfterEachMutation$")
		cmd.Env = append(os.Environ(),
			"AJJ_EFFECT_CRASH_HELPER=1",
			"AJJ_EFFECT_CRASH_ORDINAL="+strconv.Itoa(ordinal),
			"AJJ_EFFECT_CRASH_REPO="+repo,
			"AJJ_EFFECT_CRASH_REQUEST="+string(request),
		)
		if err := cmd.Run(); err == nil || cmd.ProcessState.ExitCode() != 86 {
			t.Fatalf("merge helper did not crash at mutation %d: %v", ordinal, err)
		}
		out, _, _ := captureOutput(func() error {
			return runIntegrate([]string{"--repo", repo, "--recover", "merge-process-" + strconv.Itoa(ordinal), "--json"})
		})
		receipt := decodeIntegrationReceipt(t, out)
		assertDetachedBoundaryDisposition(t, receipt, ordinal, count)
	}
}

func TestProviderDefaultMergeMutationBoundaries(t *testing.T) {
	baselineRepo, baselineRequest := setupMergeCommandBoundaryFixture(t, "merge-boundary-baseline")
	count := 0
	originalHook := integrationEffectCommandHook
	integrationEffectCommandHook = func(_, boundary string) error {
		if boundary == "after-command" {
			count++
		}
		return nil
	}
	withIntegrationStdin(t, string(baselineRequest))
	_, _, baselineErr := captureOutput(func() error { return runIntegrate([]string{"--repo", baselineRepo, "--request-json", "-"}) })
	integrationEffectCommandHook = originalHook
	if baselineErr != nil || count < 3 {
		t.Fatalf("merge baseline did not expose all mutations: count=%d err=%v", count, baselineErr)
	}
	for ordinal := 1; ordinal <= count; ordinal++ {
		t.Run("recover-"+strconv.Itoa(ordinal), func(t *testing.T) {
			repo, request := setupMergeCommandBoundaryFixture(t, "merge-recover-"+strconv.Itoa(ordinal))
			seen := 0
			integrationEffectCommandHook = func(_, boundary string) error {
				if boundary == "after-command" {
					seen++
					if seen == ordinal {
						return errors.New("merge command boundary")
					}
				}
				return nil
			}
			t.Cleanup(func() { integrationEffectCommandHook = originalHook })
			withIntegrationStdin(t, string(request))
			_, _, _ = captureOutput(func() error { return runIntegrate([]string{"--repo", repo, "--request-json", "-"}) })
			integrationEffectCommandHook = originalHook
			out, _, _ := captureOutput(func() error {
				return runIntegrate([]string{"--repo", repo, "--recover", "merge-recover-" + strconv.Itoa(ordinal), "--json"})
			})
			receipt := decodeIntegrationReceipt(t, out)
			assertDetachedBoundaryDisposition(t, receipt, ordinal, count)
		})
		t.Run("foreign-"+strconv.Itoa(ordinal), func(t *testing.T) {
			repo, request := setupMergeCommandBoundaryFixture(t, "merge-foreign-"+strconv.Itoa(ordinal))
			seen := 0
			integrationEffectCommandHook = func(_, boundary string) error {
				if boundary == "after-command" {
					seen++
					if seen == ordinal {
						runJJ(t, "-R", repo, "--ignore-working-copy", "bookmark", "create", "merge-foreign-"+strconv.Itoa(ordinal), "-r", "root()")
						return errors.New("merge foreign interleave")
					}
				}
				return nil
			}
			t.Cleanup(func() { integrationEffectCommandHook = originalHook })
			withIntegrationStdin(t, string(request))
			_, _, _ = captureOutput(func() error { return runIntegrate([]string{"--repo", repo, "--request-json", "-"}) })
			integrationEffectCommandHook = originalHook
			out, _, err := captureOutput(func() error {
				return runIntegrate([]string{"--repo", repo, "--recover", "merge-foreign-" + strconv.Itoa(ordinal), "--json"})
			})
			if err == nil {
				t.Fatalf("merge foreign boundary %d unexpectedly recovered", ordinal)
			}
			if receipt := decodeIntegrationReceipt(t, out); receipt.BatchDisposition != integrationBatchUnknownEffect {
				t.Fatalf("merge foreign boundary %d did not become unknown: %+v", ordinal, receipt)
			}
		})
	}
}

func assertDetachedBoundaryDisposition(t *testing.T, receipt integrationReceiptV1, ordinal, commandCount int) {
	t.Helper()
	wantBatch := integrationBatchFailed
	wantPayload := integrationPayloadProvedNotLanded
	if ordinal == commandCount {
		wantBatch = integrationBatchSucceeded
		wantPayload = integrationPayloadLanded
	}
	if receipt.BatchDisposition != wantBatch {
		t.Fatalf("boundary %d/%d disposition=%s want=%s: %+v", ordinal, commandCount, receipt.BatchDisposition, wantBatch, receipt)
	}
	for _, payload := range receipt.Payloads {
		if payload.Disposition != wantPayload {
			t.Fatalf("boundary %d/%d payload disposition=%s want=%s: %+v", ordinal, commandCount, payload.Disposition, wantPayload, payload)
		}
	}
}

func setupMergeCommandBoundaryFixture(t *testing.T, operationID string) (string, []byte) {
	t.Helper()
	paths := setupRealStackMergeRepo(t, false)
	ignoreIntegrationStateInFixture(t, paths.defaultPath)
	runJJ(t, "-R", paths.defaultPath, "commit", "-m", "ignore integration state")
	updateIntegrationFixtureWorkspaces(t, paths.defaultPath, paths.alphaPath, paths.bravoPath)
	target := jjFullCommitID(t, paths.defaultPath, "default@")
	heads := []string{jjFullCommitID(t, paths.defaultPath, "alpha@"), jjFullCommitID(t, paths.defaultPath, "bravo@")}
	request := integrationRequestBytesForStrategy(operationID, "default", []string{"alpha", "bravo"}, target, heads, integrationStrategyProviderDefault)
	return paths.defaultPath, request
}

func setupCommandBoundaryFixture(t *testing.T, operationID string) (realStackRepoPaths, []byte) {
	t.Helper()
	paths := setupRealStackRepo(t)
	targetBefore := jjFullCommitID(t, paths.defaultPath, "speed@")
	payloadHead := jjFullCommitID(t, paths.defaultPath, "agm-speed-transition@")
	return paths, validIntegrationRequestBytes(operationID, "speed", "agm-speed-transition", targetBefore, payloadHead)
}

func TestIntegrationRecoveryRejectsForeignOperationAfterGraphRewrite(t *testing.T) {
	paths := setupRealStackRepo(t)
	targetBefore := jjFullCommitID(t, paths.defaultPath, "speed@")
	payloadHead := jjFullCommitID(t, paths.defaultPath, "agm-speed-transition@")
	request := validIntegrationRequestBytes("foreign-after-graph", "speed", "agm-speed-transition", targetBefore, payloadHead)
	originalHook := integrationEffectPhaseHook
	integrationEffectPhaseHook = func(phase string) error {
		if phase == integrationPhaseGraphRewritten {
			return errors.New("interrupt")
		}
		return nil
	}
	t.Cleanup(func() { integrationEffectPhaseHook = originalHook })
	withIntegrationStdin(t, string(request))
	_, _, _ = captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.speedPath, "--request-json", "-"})
	})
	integrationEffectPhaseHook = func(string) error { return nil }
	runJJ(t, "-R", paths.speedPath, "describe", "-m", "foreign")
	out, _, err := captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.speedPath, "--recover", "foreign-after-graph", "--json"})
	})
	if err == nil {
		t.Fatal("foreign operation was not rejected")
	}
	receipt := decodeIntegrationReceipt(t, out)
	if receipt.BatchDisposition != integrationBatchUnknownEffect || receipt.Error == nil || receipt.Error.Code != integrationErrorUnknownEffect {
		t.Fatalf("foreign operation did not produce unknown effect: %+v", receipt)
	}
}
