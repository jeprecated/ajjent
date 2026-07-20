package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestRunIntegrateOrderedLineAnchorsIndependentPayloadsInRequestOrder(t *testing.T) {
	paths := setupRealStackMergeRepo(t, false)
	ignoreIntegrationStateInFixture(t, paths.defaultPath)
	// Advance the target after both payload workspaces were created. Ordered-line
	// must anchor on this newer target frontier rather than either old payload base.
	if err := os.WriteFile(filepath.Join(paths.defaultPath, "target-newer.txt"), []byte("target newer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", paths.defaultPath, "file", "track", "root:target-newer.txt")
	runJJ(t, "-R", paths.defaultPath, "commit", "-m", "target newer than children")
	updateIntegrationFixtureWorkspaces(t, paths.alphaPath, paths.bravoPath)
	// Give the first payload multiple commits.
	if err := os.WriteFile(filepath.Join(paths.alphaPath, "alpha-second.txt"), []byte("second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", paths.alphaPath, "file", "track", "root:alpha-second.txt")
	runJJ(t, "-R", paths.alphaPath, "commit", "-m", "feat: alpha second")
	updateIntegrationFixtureWorkspaces(t, paths.defaultPath, paths.alphaPath, paths.bravoPath)

	before := jjFullCommitID(t, paths.defaultPath, "default@")
	heads := []string{jjFullCommitID(t, paths.defaultPath, "alpha@"), jjFullCommitID(t, paths.defaultPath, "bravo@")}
	request := integrationRequestBytesForStrategy("ordered-independent", "default", []string{"alpha", "bravo"}, before, heads, integrationStrategyOrderedLine)
	assertOrderedFixturePrepares(t, paths.defaultPath, request)
	withIntegrationStdin(t, string(request))
	out, errOut, err := captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.defaultPath, "--request-json", "-"})
	})
	if err != nil {
		t.Fatalf("ordered-line integration failed: %v\nstdout:%s\nstderr:%s", err, out, errOut)
	}
	receipt := decodeIntegrationReceipt(t, out)
	if receipt.BatchDisposition != integrationBatchSucceeded || receipt.Strategy != integrationStrategyOrderedLine || receipt.Target.BeforeHeadCommit != before || receipt.Target.IntegratedTipCommit == "" || receipt.Target.AfterHeadCommit == "" || receipt.Target.IntegratedTipCommit == receipt.Target.AfterHeadCommit {
		t.Fatalf("ordered-line target receipt is incomplete: %+v", receipt)
	}
	if len(receipt.Payloads) != 2 || len(receipt.Payloads[0].Changes) != 2 || len(receipt.Payloads[1].Changes) != 1 {
		t.Fatalf("ordered-line mappings are incomplete: %+v", receipt.Payloads)
	}
	alphaTip := receipt.Payloads[0].Changes[0].LandedCommit
	for _, mapping := range receipt.Payloads[0].Changes {
		if jjRevsetCount(t, paths.defaultPath, mapping.LandedCommit+" & ::"+receipt.Payloads[1].Changes[0].LandedCommit) != 1 {
			t.Fatalf("alpha contribution %s is not below bravo in request order", mapping.LandedCommit)
		}
		alphaTip = mapping.LandedCommit
	}
	bravoTip := receipt.Payloads[1].Changes[0].LandedCommit
	if jjRevsetCount(t, paths.defaultPath, "change_id("+receipt.Payloads[0].Changes[0].ChangeID+") & ::"+bravoTip) != 1 {
		t.Fatalf("first requested payload is not below second: alphaTip=%s bravoTip=%s", alphaTip, bravoTip)
	}
	if got := jjFullCommitID(t, paths.defaultPath, "default@"); got != receipt.Target.AfterHeadCommit {
		t.Fatalf("target head=%s receipt=%s", got, receipt.Target.AfterHeadCommit)
	}
	for _, handle := range []string{"default", "alpha", "bravo"} {
		if got := jjRevsetCount(t, paths.defaultPath, "parents("+handle+"@) & commit_id("+receipt.Target.IntegratedTipCommit+")"); got != 1 {
			t.Fatalf("%s cursor is not based on integrated tip %s", handle, receipt.Target.IntegratedTipCommit)
		}
		if got := jjRevsetCount(t, paths.defaultPath, "empty() & "+handle+"@"); got != 1 {
			t.Fatalf("%s cursor is not empty", handle)
		}
	}
}

func TestRunIntegrateOrderedLineLaterPayloadMultiCommitPreservesExactOrderAndMappings(t *testing.T) {
	paths := setupRealStackMergeRepo(t, false)
	ignoreIntegrationStateInFixture(t, paths.defaultPath)
	runJJ(t, "-R", paths.defaultPath, "commit", "-m", "ignore integration state")
	runJJ(t, "-R", paths.bravoPath, "workspace", "update-stale")
	if err := os.WriteFile(filepath.Join(paths.bravoPath, "bravo-second.txt"), []byte("second bravo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", paths.bravoPath, "file", "track", "root:bravo-second.txt")
	runJJ(t, "-R", paths.bravoPath, "commit", "-m", "feat: bravo second")
	updateIntegrationFixtureWorkspaces(t, paths.defaultPath, paths.alphaPath, paths.bravoPath)

	before := jjFullCommitID(t, paths.defaultPath, "default@")
	heads := []string{jjFullCommitID(t, paths.defaultPath, "alpha@"), jjFullCommitID(t, paths.defaultPath, "bravo@")}
	requestBytes := integrationRequestBytesForStrategy("ordered-later-multicommit", "default", []string{"alpha", "bravo"}, before, heads, integrationStrategyOrderedLine)
	request, _, err := parseIntegrationRequestV1(requestBytes)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := validateIntegrationAssertions(paths.defaultPath, request)
	if err != nil {
		t.Fatalf("later multi-commit fixture does not prepare: %v", err)
	}
	if len(prepared.Payloads) != 2 || len(prepared.Payloads[1].Changes) != 2 {
		t.Fatalf("fixture did not materialize two later-payload commits: %+v", prepared.Payloads)
	}

	withIntegrationStdin(t, string(requestBytes))
	out, errOut, runErr := captureOutput(func() error { return runIntegrate([]string{"--repo", paths.defaultPath, "--request-json", "-"}) })
	if runErr != nil {
		t.Fatalf("later multi-commit ordered line failed: %v\n%s\n%s", runErr, out, errOut)
	}
	receipt := decodeIntegrationReceipt(t, out)
	if receipt.BatchDisposition != integrationBatchSucceeded || len(receipt.Payloads) != 2 || len(receipt.Payloads[1].Changes) != 2 {
		t.Fatalf("later multi-commit receipt is incomplete: %+v", receipt)
	}
	for payloadIndex := range prepared.Payloads {
		if len(receipt.Payloads[payloadIndex].Changes) != len(prepared.Payloads[payloadIndex].Changes) {
			t.Fatalf("payload %d mapping count changed: prepared=%+v receipt=%+v", payloadIndex, prepared.Payloads[payloadIndex], receipt.Payloads[payloadIndex])
		}
		for changeIndex, preparedChange := range prepared.Payloads[payloadIndex].Changes {
			mapping := receipt.Payloads[payloadIndex].Changes[changeIndex]
			if mapping.ChangeID != preparedChange.ChangeID || mapping.InputCommit != preparedChange.CommitID || mapping.LandedCommit == "" {
				t.Fatalf("payload %d change %d lost one-to-one ordered mapping: prepared=%+v mapping=%+v", payloadIndex, changeIndex, preparedChange, mapping)
			}
		}
	}
	firstSource := integrationPreparedChangeRevset(prepared.Payloads[0].Changes)
	laterSource := integrationPreparedChangeRevset(prepared.Payloads[1].Changes)
	if got := jjRevsetCount(t, paths.defaultPath, "heads("+firstSource+") & ::heads("+laterSource+")"); got != 1 {
		t.Fatalf("later multi-commit payload is not structurally after the first payload: count=%d", got)
	}
	laterRoots, err := integrationCommitIDs(paths.defaultPath, "roots("+laterSource+")")
	if err != nil {
		t.Fatal(err)
	}
	laterTips, err := integrationCommitIDs(paths.defaultPath, "heads("+laterSource+")")
	if err != nil {
		t.Fatal(err)
	}
	if len(laterRoots) != 1 || len(laterTips) != 1 || laterRoots[0] == laterTips[0] || jjRevsetCount(t, paths.defaultPath, laterRoots[0]+" & ::"+laterTips[0]) != 1 {
		t.Fatalf("later payload multi-commit ancestry changed: roots=%v tips=%v", laterRoots, laterTips)
	}
}

func TestRunIntegrateOrderedLineSupportsMultipleTargetFrontierCommits(t *testing.T) {
	paths := setupRealStackMergeRepo(t, false)
	ignoreIntegrationStateInFixture(t, paths.defaultPath)
	runJJ(t, "-R", paths.defaultPath, "commit", "-m", "ignore integration state")
	updateIntegrationFixtureWorkspaces(t, paths.defaultPath, paths.alphaPath, paths.bravoPath)
	base := jjFullCommitID(t, paths.defaultPath, "default@-")
	root := filepath.Dir(paths.defaultPath)
	leftPath := filepath.Join(root, "target-left")
	rightPath := filepath.Join(root, "target-right")
	runJJ(t, "-R", paths.defaultPath, "workspace", "add", "--revision", base, "--name", "target-left", leftPath)
	if err := os.WriteFile(filepath.Join(leftPath, "target-left.txt"), []byte("left\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", leftPath, "file", "track", "root:target-left.txt")
	runJJ(t, "-R", leftPath, "commit", "-m", "target left")
	left := jjFullCommitID(t, paths.defaultPath, "target-left@-")

	runJJ(t, "-R", paths.defaultPath, "workspace", "add", "--revision", base, "--name", "target-right", rightPath)
	if err := os.WriteFile(filepath.Join(rightPath, "target-right.txt"), []byte("right\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", rightPath, "file", "track", "root:target-right.txt")
	runJJ(t, "-R", rightPath, "commit", "-m", "target right")
	right := jjFullCommitID(t, paths.defaultPath, "target-right@-")
	runJJ(t, "-R", paths.defaultPath, "workspace", "update-stale")
	runJJ(t, "-R", paths.defaultPath, "new", left, right)
	updateIntegrationFixtureWorkspaces(t, paths.alphaPath, paths.bravoPath)

	before := jjFullCommitID(t, paths.defaultPath, "default@")
	heads := []string{jjFullCommitID(t, paths.defaultPath, "alpha@"), jjFullCommitID(t, paths.defaultPath, "bravo@")}
	requestBytes := integrationRequestBytesForStrategy("ordered-multiple-frontier", "default", []string{"alpha", "bravo"}, before, heads, integrationStrategyOrderedLine)
	request, _, err := parseIntegrationRequestV1(requestBytes)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := validateIntegrationAssertions(paths.defaultPath, request)
	if err != nil {
		t.Fatalf("multiple-frontier fixture does not prepare: %v", err)
	}
	if len(prepared.Target.FrontierCommits) != 2 || !sameIntegrationCommitSet(prepared.Target.FrontierCommits, []string{left, right}) {
		t.Fatalf("target frontier is not the exact two deterministic tips: got=%v want=%v", prepared.Target.FrontierCommits, []string{left, right})
	}
	for i := 1; i < len(prepared.Target.FrontierCommits); i++ {
		if prepared.Target.FrontierCommits[i-1] >= prepared.Target.FrontierCommits[i] {
			t.Fatalf("target frontier evidence is not canonically ordered: %v", prepared.Target.FrontierCommits)
		}
	}

	withIntegrationStdin(t, string(requestBytes))
	out, errOut, runErr := captureOutput(func() error { return runIntegrate([]string{"--repo", paths.defaultPath, "--request-json", "-"}) })
	if runErr != nil {
		t.Fatalf("multiple-frontier ordered line failed: %v\n%s\n%s", runErr, out, errOut)
	}
	receipt := decodeIntegrationReceipt(t, out)
	if receipt.BatchDisposition != integrationBatchSucceeded {
		t.Fatalf("multiple-frontier ordered line did not succeed: %+v", receipt)
	}
	firstSource := integrationPreparedChangeRevset(prepared.Payloads[0].Changes)
	outsideParents, err := integrationCommitIDs(paths.defaultPath, "parents(roots("+firstSource+")) & ~("+firstSource+")")
	if err != nil || !sameIntegrationCommitSet(outsideParents, prepared.Target.FrontierCommits) {
		t.Fatalf("first contribution is not exactly anchored to target frontier: parents=%v frontier=%v err=%v", outsideParents, prepared.Target.FrontierCommits, err)
	}
}

func TestRunIntegrateOrderedLineSupportsDependentChildChain(t *testing.T) {
	paths := setupRealTidySingleStackRepo(t, false)
	ignoreIntegrationStateInFixture(t, paths.defaultPath)
	runJJ(t, "-R", paths.defaultPath, "commit", "-m", "ignore integration state")
	paths.bravoPath = filepath.Join(filepath.Dir(paths.defaultPath), "bravo")
	runJJ(t, "-R", paths.defaultPath, "workspace", "add", "--revision", "alpha@-", "--name", "bravo", paths.bravoPath)
	if err := os.WriteFile(filepath.Join(paths.bravoPath, "bravo-child.txt"), []byte("dependent child\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", paths.bravoPath, "file", "track", "root:bravo-child.txt")
	runJJ(t, "-R", paths.bravoPath, "commit", "-m", "feat: dependent bravo")
	updateIntegrationFixtureWorkspaces(t, paths.defaultPath, paths.alphaPath, paths.bravoPath)
	request := integrationRequestBytesForStrategy("ordered-dependent", "default", []string{"alpha", "bravo"}, jjFullCommitID(t, paths.defaultPath, "default@"), []string{jjFullCommitID(t, paths.defaultPath, "alpha@"), jjFullCommitID(t, paths.defaultPath, "bravo@")}, integrationStrategyOrderedLine)
	assertOrderedFixturePrepares(t, paths.defaultPath, request)
	withIntegrationStdin(t, string(request))
	out, errOut, err := captureOutput(func() error { return runIntegrate([]string{"--repo", paths.defaultPath, "--request-json", "-"}) })
	if err != nil {
		t.Fatalf("dependent ordered line failed: %v\n%s\n%s", err, out, errOut)
	}
	receipt := decodeIntegrationReceipt(t, out)
	alpha := receipt.Payloads[0].Changes[0].LandedCommit
	bravo := receipt.Payloads[1].Changes[0].LandedCommit
	if jjRevsetCount(t, paths.defaultPath, alpha+" & ::"+bravo) != 1 {
		t.Fatalf("dependent child order was not preserved: alpha=%s bravo=%s", alpha, bravo)
	}
}

func TestRunIntegrateOrderedLineLeavesOmittedWorkspaceUnchanged(t *testing.T) {
	paths := setupRealStackMergeRepo(t, false)
	ignoreIntegrationStateInFixture(t, paths.defaultPath)
	runJJ(t, "-R", paths.defaultPath, "commit", "-m", "ignore integration state")
	root := filepath.Dir(paths.defaultPath)
	omittedPath := filepath.Join(root, "omitted")
	runJJ(t, "-R", paths.defaultPath, "workspace", "add", "--revision", "root()", "--name", "omitted", omittedPath)
	if err := os.WriteFile(filepath.Join(omittedPath, "omitted.txt"), []byte("omitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", omittedPath, "file", "track", "root:omitted.txt")
	runJJ(t, "-R", omittedPath, "commit", "-m", "omitted payload")
	updateIntegrationFixtureWorkspaces(t, paths.defaultPath, paths.alphaPath, paths.bravoPath, omittedPath)
	omittedBefore := jjFullCommitID(t, paths.defaultPath, "omitted@")
	request := integrationRequestBytesForStrategy("ordered-omitted", "default", []string{"alpha", "bravo"}, jjFullCommitID(t, paths.defaultPath, "default@"), []string{jjFullCommitID(t, paths.defaultPath, "alpha@"), jjFullCommitID(t, paths.defaultPath, "bravo@")}, integrationStrategyOrderedLine)
	assertOrderedFixturePrepares(t, paths.defaultPath, request)
	withIntegrationStdin(t, string(request))
	out, errOut, err := captureOutput(func() error { return runIntegrate([]string{"--repo", paths.defaultPath, "--request-json", "-"}) })
	if err != nil {
		t.Fatalf("ordered-line with omitted Workspace failed: %v\n%s\n%s", err, out, errOut)
	}
	if got := jjFullCommitID(t, paths.defaultPath, "omitted@"); got != omittedBefore {
		t.Fatalf("omitted Workspace moved: before=%s after=%s", omittedBefore, got)
	}
}

func TestRunIntegrateOrderedLineFirstPayloadConflictProvesNoEffect(t *testing.T) {
	paths := setupRealTidySingleStackRepo(t, true)
	ignoreIntegrationStateInFixture(t, paths.defaultPath)
	runJJ(t, "-R", paths.defaultPath, "commit", "-m", "ignore integration state")
	updateIntegrationFixtureWorkspaces(t, paths.defaultPath, paths.alphaPath)
	before := currentOperationIDFullForTest(t, paths.defaultPath)
	request := integrationRequestBytesForStrategy("ordered-first-conflict", "default", []string{"alpha"}, jjFullCommitID(t, paths.defaultPath, "default@"), []string{jjFullCommitID(t, paths.defaultPath, "alpha@")}, integrationStrategyOrderedLine)
	assertOrderedFixturePrepares(t, paths.defaultPath, request)
	withIntegrationStdin(t, string(request))
	out, _, err := captureOutput(func() error { return runIntegrate([]string{"--repo", paths.defaultPath, "--request-json", "-"}) })
	if err == nil {
		t.Fatal("first ordered payload conflict unexpectedly succeeded")
	}
	receipt := decodeIntegrationReceipt(t, out)
	if receipt.BatchDisposition != integrationBatchFailed || receipt.Error == nil || receipt.Error.Code != integrationErrorConflict || receipt.Payloads[0].Disposition != integrationPayloadProvedNotLanded || currentOperationIDFullForTest(t, paths.defaultPath) != before {
		t.Fatalf("first ordered conflict did not prove no effect: %+v", receipt)
	}
}

func TestRunIntegrateOrderedLineConflictProvesExactNoEffect(t *testing.T) {
	paths := setupRealStackMergeRepo(t, true)
	ignoreIntegrationStateInFixture(t, paths.defaultPath)
	runJJ(t, "-R", paths.defaultPath, "commit", "-m", "ignore integration state")
	updateIntegrationFixtureWorkspaces(t, paths.defaultPath, paths.alphaPath, paths.bravoPath)
	beforeOp := currentOperationIDFullForTest(t, paths.defaultPath)
	beforeHeads, err := integrationWorkspaceHeadsAtOperation(paths.defaultPath, beforeOp)
	if err != nil {
		t.Fatal(err)
	}
	request := integrationRequestBytesForStrategy("ordered-conflict", "default", []string{"alpha", "bravo"}, jjFullCommitID(t, paths.defaultPath, "default@"), []string{jjFullCommitID(t, paths.defaultPath, "alpha@"), jjFullCommitID(t, paths.defaultPath, "bravo@")}, integrationStrategyOrderedLine)
	assertOrderedFixturePrepares(t, paths.defaultPath, request)
	withIntegrationStdin(t, string(request))
	out, _, runErr := captureOutput(func() error { return runIntegrate([]string{"--repo", paths.defaultPath, "--request-json", "-"}) })
	if runErr == nil {
		t.Fatal("conflicted ordered-line unexpectedly succeeded")
	}
	receipt := decodeIntegrationReceipt(t, out)
	if receipt.BatchDisposition != integrationBatchFailed || receipt.Error == nil || receipt.Error.Code != integrationErrorConflict {
		t.Fatalf("ordered conflict did not fail with exact disposition: %+v", receipt)
	}
	for _, payload := range receipt.Payloads {
		if payload.Disposition != integrationPayloadProvedNotLanded {
			t.Fatalf("ordered conflict payload not proved-not-landed: %+v", payload)
		}
	}
	afterOp := currentOperationIDFullForTest(t, paths.defaultPath)
	afterHeads, err := integrationWorkspaceHeadsAtOperation(paths.defaultPath, afterOp)
	if err != nil {
		t.Fatal(err)
	}
	if beforeOp != afterOp || !reflect.DeepEqual(beforeHeads, afterHeads) {
		t.Fatalf("ordered conflict changed live state: op %s -> %s, heads %v -> %v", beforeOp, afterOp, beforeHeads, afterHeads)
	}
}

func TestOrderedLineRecoveryRejectsCoherentDescribedTargetCursorForgery(t *testing.T) {
	repo, requestBytes := setupOrderedLineBoundaryFixture(t, "ordered-described-forgery")
	originalPhase := integrationEffectPhaseHook
	integrationEffectPhaseHook = func(phase string) error {
		if phase == integrationPhaseGraphRewritten {
			return errors.New("stop before publication")
		}
		return nil
	}
	t.Cleanup(func() { integrationEffectPhaseHook = originalPhase })
	withIntegrationStdin(t, string(requestBytes))
	_, _, _ = captureOutput(func() error { return runIntegrate([]string{"--repo", repo, "--request-json", "-"}) })

	stateDir := filepath.Join(repo, ".ajj", "integrations")
	record, found, err := loadIntegrationOperationRecord(stateDir, "ordered-described-forgery")
	if err != nil || !found || record.GraphOperationID == "" {
		t.Fatalf("load valid staged ordered-line record: found=%v err=%v record=%+v", found, err, record)
	}
	request := integrationRequestFromRecord(record)
	out, err := commandCombinedCaptureFn("jj", "-R", repo, "--ignore-working-copy", "--at-op="+record.GraphOperationID, "--no-integrate-operation", "describe", "-r", request.Target.ExpectedWorkspace+"@", "-m", "forged described cursor")
	if err != nil {
		t.Fatal(err)
	}
	shortID, err := detachedOperationID(out)
	if err != nil {
		t.Fatal(err)
	}
	forgedOperation, err := integrationOperationFullID(repo, shortID)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := integrationOperationEvidence(repo, forgedOperation)
	if err != nil || len(evidence.ParentOperationIDs) != 1 || evidence.ParentOperationIDs[0] != record.GraphOperationID {
		t.Fatalf("forgery is not a valid sole-parent detached operation: %+v err=%v", evidence, err)
	}
	record.DetachedOperationIDs = append(record.DetachedOperationIDs, forgedOperation)
	record.GraphOperationID = forgedOperation
	stagedState, err := detachedTargetState(repo, forgedOperation, request.Target.ExpectedWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	record.StagedTargetState = &stagedState
	record.StagedRepositoryView, err = integrationRepositoryViewAtOperation(repo, forgedOperation, request.Target.ExpectedWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	record.StagedPayloadMappings, err = proveIntegrationPayloadMappingsAtOperation(repo, forgedOperation, request.Target.ExpectedWorkspace, record.PreparedState)
	if err != nil {
		t.Fatal(err)
	}
	record.PublishPending = true
	if err := writeIntegrationOperationRecordAtomic(stateDir, record); err != nil {
		t.Fatalf("coherent forged journal should pass structural validation before graph proof: %v", err)
	}
	runJJ(t, "-R", repo, "--ignore-working-copy", "op", "integrate", forgedOperation)
	if got := currentOperationIDFullForTest(t, repo); got != forgedOperation {
		t.Fatalf("forged operation did not become the exact live operation: got=%s want=%s", got, forgedOperation)
	}

	integrationEffectPhaseHook = func(string) error { return nil }
	recoverOut, _, recoverErr := captureOutput(func() error {
		return runIntegrate([]string{"--repo", repo, "--recover", "ordered-described-forgery", "--json"})
	})
	if recoverErr == nil {
		t.Fatal("coherent described target-cursor forgery unexpectedly recovered as landed")
	}
	receipt := decodeIntegrationReceipt(t, recoverOut)
	if receipt.BatchDisposition != integrationBatchUnknownEffect || receipt.Error == nil || receipt.Error.Code != integrationErrorUnknownEffect || receipt.Error.NextAction != integrationNextActionOperatorReview {
		t.Fatalf("coherent described forgery did not fail closed: %+v", receipt)
	}
	if len(recoverOut) > integrationMaxOutputBytes || strings.Contains(recoverOut, repo) {
		t.Fatalf("forgery error is unbounded or path-bearing: length=%d output=%q", len(recoverOut), recoverOut)
	}
	if got := currentOperationIDFullForTest(t, repo); got != forgedOperation {
		t.Fatalf("recovery mutated the foreign graph: got=%s want=%s", got, forgedOperation)
	}
}

func TestOrderedLinePreparedTargetFrontierIsStrategyExclusiveAndDeeplyValidated(t *testing.T) {
	commitA := strings.Repeat("1", 40)
	commitB := strings.Repeat("2", 40)
	ordered := orderedLineSemanticTestRecord("ordered-frontier-semantic", []string{commitA, commitB})
	if err := validateIntegrationOperationRecordSemantic(ordered, ordered.OperationID); err != nil {
		t.Fatalf("valid ordered-line frontier rejected: %v", err)
	}

	for name, mutate := range map[string]func(*integrationOperationRecord){
		"missing": func(record *integrationOperationRecord) { record.PreparedState.Target.FrontierCommits = nil },
		"duplicate": func(record *integrationOperationRecord) {
			record.PreparedState.Target.FrontierCommits = []string{commitA, commitA}
		},
		"unordered": func(record *integrationOperationRecord) {
			record.PreparedState.Target.FrontierCommits = []string{commitB, commitA}
		},
		"malformed": func(record *integrationOperationRecord) {
			record.PreparedState.Target.FrontierCommits = []string{"not-a-commit"}
		},
		"oversized": func(record *integrationOperationRecord) {
			record.PreparedState.Target.FrontierCommits = make([]string, integrationMaxChangesPerPayload+1)
			for i := range record.PreparedState.Target.FrontierCommits {
				record.PreparedState.Target.FrontierCommits[i] = strings.Repeat("3", 40)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			forged := cloneIntegrationRecord(t, ordered)
			mutate(&forged)
			if err := validateIntegrationOperationRecordSemantic(forged, forged.OperationID); err == nil {
				t.Fatalf("%s ordered-line target frontier was accepted: %+v", name, forged.PreparedState.Target.FrontierCommits)
			}
		})
	}

	for _, strategy := range []string{integrationStrategySingle, integrationStrategyProviderDefault} {
		t.Run(strategy, func(t *testing.T) {
			record := semanticTestRecordForStrategy(strategy+"-foreign-frontier", strategy)
			record.PreparedState.Target.FrontierCommits = []string{commitA}
			if err := validateIntegrationOperationRecordSemantic(record, record.OperationID); err == nil {
				t.Fatalf("%s accepted ordered-line-only target frontier", strategy)
			}
			record.PreparedState.Target.FrontierCommits = nil
			if err := validateIntegrationOperationRecordSemantic(record, record.OperationID); err != nil {
				t.Fatalf("%s rejected absent ordered-line-only target frontier: %v", strategy, err)
			}
		})
	}
}

func TestOrderedLineSuccessfulTerminalRequestIsIdempotent(t *testing.T) {
	repo, request := setupOrderedLineBoundaryFixture(t, "ordered-terminal-idempotent")
	withIntegrationStdin(t, string(request))
	firstOut, firstErrOut, firstErr := captureOutput(func() error { return runIntegrate([]string{"--repo", repo, "--request-json", "-"}) })
	if firstErr != nil {
		t.Fatalf("first ordered request failed: %v\n%s\n%s", firstErr, firstOut, firstErrOut)
	}
	firstReceipt := decodeIntegrationReceipt(t, firstOut)
	firstOperation := currentOperationIDFullForTest(t, repo)
	firstHeads, err := integrationWorkspaceHeadsAtOperation(repo, firstOperation)
	if err != nil {
		t.Fatal(err)
	}

	withIntegrationStdin(t, string(request))
	secondOut, secondErrOut, secondErr := captureOutput(func() error { return runIntegrate([]string{"--repo", repo, "--request-json", "-"}) })
	if secondErr != nil {
		t.Fatalf("repeated ordered request failed: %v\n%s\n%s", secondErr, secondOut, secondErrOut)
	}
	secondReceipt := decodeIntegrationReceipt(t, secondOut)
	secondOperation := currentOperationIDFullForTest(t, repo)
	secondHeads, err := integrationWorkspaceHeadsAtOperation(repo, secondOperation)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(firstReceipt, secondReceipt) || firstOperation != secondOperation || !reflect.DeepEqual(firstHeads, secondHeads) {
		t.Fatalf("repeated ordered request was not exact terminal replay: receiptsEqual=%v op=%s->%s heads=%v->%v", reflect.DeepEqual(firstReceipt, secondReceipt), firstOperation, secondOperation, firstHeads, secondHeads)
	}
}

func TestOrderedLineForgedEvidenceAndForeignInterleaveFailClosed(t *testing.T) {
	paths := setupRealStackMergeRepo(t, false)
	ignoreIntegrationStateInFixture(t, paths.defaultPath)
	runJJ(t, "-R", paths.defaultPath, "commit", "-m", "ignore integration state")
	updateIntegrationFixtureWorkspaces(t, paths.defaultPath, paths.alphaPath, paths.bravoPath)
	requestBytes := integrationRequestBytesForStrategy("ordered-forged", "default", []string{"alpha", "bravo"}, jjFullCommitID(t, paths.defaultPath, "default@"), []string{jjFullCommitID(t, paths.defaultPath, "alpha@"), jjFullCommitID(t, paths.defaultPath, "bravo@")}, integrationStrategyOrderedLine)
	originalPhase := integrationEffectPhaseHook
	integrationEffectPhaseHook = func(phase string) error {
		if phase == integrationPhaseGraphRewritten {
			return errors.New("stop before publication")
		}
		return nil
	}
	t.Cleanup(func() { integrationEffectPhaseHook = originalPhase })
	withIntegrationStdin(t, string(requestBytes))
	_, _, _ = captureOutput(func() error { return runIntegrate([]string{"--repo", paths.defaultPath, "--request-json", "-"}) })
	record, found, err := loadIntegrationOperationRecord(filepath.Join(paths.defaultPath, ".ajj", "integrations"), "ordered-forged")
	if err != nil || !found {
		t.Fatalf("load ordered staged record: found=%v err=%v", found, err)
	}
	request := integrationRequestFromRecord(record)
	forged := record
	forged.PreparedState.Target.FrontierCommits = []string{forged.PreparedState.Payloads[0].Changes[0].CommitID}
	if err := proveDetachedIntegrationTransaction(paths.defaultPath, forged, request); err == nil {
		t.Fatal("forged ordered target frontier passed exact ordered proof")
	}
	integrationEffectPhaseHook = func(string) error { return nil }
	runJJ(t, "-R", paths.defaultPath, "--ignore-working-copy", "bookmark", "create", "ordered-foreign", "-r", "root()")
	out, _, recoverErr := captureOutput(func() error {
		return runIntegrate([]string{"--repo", paths.defaultPath, "--recover", "ordered-forged", "--json"})
	})
	if recoverErr == nil {
		t.Fatal("foreign live operation unexpectedly recovered ordered prepublication work")
	}
	if receipt := decodeIntegrationReceipt(t, out); receipt.BatchDisposition != integrationBatchUnknownEffect || receipt.Error == nil || receipt.Error.Code != integrationErrorUnknownEffect {
		t.Fatalf("foreign ordered state did not fail closed: %+v", receipt)
	}
}

func TestRunIntegrateOrderedLineNestedTargetThenMain(t *testing.T) {
	paths := setupRealStackRepo(t)
	ignoreIntegrationStateInFixture(t, paths.defaultPath)
	runJJ(t, "-R", paths.defaultPath, "commit", "-m", "project state")
	updateIntegrationFixtureWorkspaces(t, paths.speedPath, paths.childPath)
	if err := os.WriteFile(filepath.Join(paths.speedPath, "speed-base.txt"), []byte("speed base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", paths.speedPath, "file", "track", "root:speed-base.txt")
	runJJ(t, "-R", paths.speedPath, "commit", "-m", "speed target advance")
	updateIntegrationFixtureWorkspaces(t, paths.defaultPath, paths.speedPath, paths.childPath)
	mainBefore := jjFullCommitID(t, paths.defaultPath, "default@")
	request := integrationRequestBytesForStrategy("ordered-nested-a", "speed", []string{"agm-speed-transition"}, jjFullCommitID(t, paths.defaultPath, "speed@"), []string{jjFullCommitID(t, paths.defaultPath, "agm-speed-transition@")}, integrationStrategyOrderedLine)
	assertOrderedFixturePrepares(t, paths.defaultPath, request)
	withIntegrationStdin(t, string(request))
	out, errOut, err := captureOutput(func() error { return runIntegrate([]string{"--repo", paths.speedPath, "--request-json", "-"}) })
	if err != nil {
		t.Fatalf("nested ordered A <- A1 failed: %v\n%s\n%s", err, out, errOut)
	}
	if got := jjFullCommitID(t, paths.defaultPath, "default@"); got != mainBefore {
		t.Fatalf("configured Main moved during nested ordered integration: %s -> %s", mainBefore, got)
	}
	speedAfter := jjFullCommitID(t, paths.defaultPath, "speed@")
	mainRequest := validIntegrationRequestBytes("ordered-nested-main", "default", "speed", mainBefore, speedAfter)
	withIntegrationStdin(t, string(mainRequest))
	mainOut, mainErrOut, mainErr := captureOutput(func() error { return runIntegrate([]string{"--repo", paths.defaultPath, "--request-json", "-"}) })
	if mainErr != nil {
		t.Fatalf("later Main <- A failed: %v\n%s\n%s", mainErr, mainOut, mainErrOut)
	}
	childChange := decodeIntegrationReceipt(t, out).Payloads[0].Changes[0].ChangeID
	if jjRevsetCount(t, paths.defaultPath, "change_id("+childChange+") & ::default@") != 1 {
		t.Fatalf("later Main integration did not retain nested child change %s", childChange)
	}
}

func TestOrderedLineRecoveryAfterCursorFileInterruptionDoesNotReplay(t *testing.T) {
	repo, request := setupOrderedLineBoundaryFixture(t, "ordered-cursor-recover")
	original := integrationCursorReconcileHook
	integrationCursorReconcileHook = func(workspace string) error {
		if workspace == "alpha" {
			return errors.New("stop during ordered cursor reconciliation")
		}
		return nil
	}
	t.Cleanup(func() { integrationCursorReconcileHook = original })
	withIntegrationStdin(t, string(request))
	out, _, err := captureOutput(func() error { return runIntegrate([]string{"--repo", repo, "--request-json", "-"}) })
	if err == nil || decodeIntegrationReceipt(t, out).Error == nil {
		t.Fatalf("ordered cursor interruption did not stop after publication: err=%v out=%s", err, out)
	}
	publishedOperation := currentOperationIDFullForTest(t, repo)
	integrationCursorReconcileHook = original
	recoverOut, _, recoverErr := captureOutput(func() error {
		return runIntegrate([]string{"--repo", repo, "--recover", "ordered-cursor-recover", "--json"})
	})
	if recoverErr != nil {
		t.Fatalf("ordered cursor recovery failed: %v\n%s", recoverErr, recoverOut)
	}
	receipt := decodeIntegrationReceipt(t, recoverOut)
	if receipt.BatchDisposition != integrationBatchSucceeded || receipt.JJOperations.CommitPoint != publishedOperation || currentOperationIDFullForTest(t, repo) != publishedOperation {
		t.Fatalf("ordered cursor recovery replayed or lost commit point: %+v", receipt)
	}
}

func TestOrderedLineRecoveryAtEveryMutationBoundary(t *testing.T) {
	baselineRepo, baselineRequest := setupOrderedLineBoundaryFixture(t, "ordered-boundary-baseline")
	count := 0
	original := integrationEffectCommandHook
	integrationEffectCommandHook = func(_, boundary string) error {
		if boundary == "after-command" {
			count++
		}
		return nil
	}
	withIntegrationStdin(t, string(baselineRequest))
	_, _, baselineErr := captureOutput(func() error { return runIntegrate([]string{"--repo", baselineRepo, "--request-json", "-"}) })
	integrationEffectCommandHook = original
	if baselineErr != nil || count < 5 {
		t.Fatalf("ordered baseline did not expose detached/publication/cursor boundaries: count=%d err=%v", count, baselineErr)
	}
	for ordinal := 1; ordinal <= count; ordinal++ {
		t.Run(strconv.Itoa(ordinal), func(t *testing.T) {
			repo, request := setupOrderedLineBoundaryFixture(t, "ordered-recover-"+strconv.Itoa(ordinal))
			beforeOperation := currentOperationIDFullForTest(t, repo)
			beforeHeads, beforeHeadsErr := integrationWorkspaceHeadsAtOperation(repo, beforeOperation)
			if beforeHeadsErr != nil {
				t.Fatal(beforeHeadsErr)
			}
			seen := 0
			integrationEffectCommandHook = func(_, boundary string) error {
				if boundary == "after-command" {
					seen++
					if seen == ordinal {
						return errors.New("ordered boundary")
					}
				}
				return nil
			}
			t.Cleanup(func() { integrationEffectCommandHook = original })
			withIntegrationStdin(t, string(request))
			_, _, _ = captureOutput(func() error { return runIntegrate([]string{"--repo", repo, "--request-json", "-"}) })
			integrationEffectCommandHook = original
			out, _, _ := captureOutput(func() error {
				return runIntegrate([]string{"--repo", repo, "--recover", "ordered-recover-" + strconv.Itoa(ordinal), "--json"})
			})
			receipt := decodeIntegrationReceipt(t, out)
			assertDetachedBoundaryDisposition(t, receipt, ordinal, count)
			if ordinal < count {
				afterOperation := currentOperationIDFullForTest(t, repo)
				afterHeads, headsErr := integrationWorkspaceHeadsAtOperation(repo, afterOperation)
				if headsErr != nil {
					t.Fatal(headsErr)
				}
				if afterOperation != beforeOperation || !reflect.DeepEqual(afterHeads, beforeHeads) {
					t.Fatalf("prepublish boundary %d changed live state: op %s -> %s heads %v -> %v", ordinal, beforeOperation, afterOperation, beforeHeads, afterHeads)
				}
			} else if receipt.Target.IntegratedTipCommit == "" || receipt.Target.AfterHeadCommit == "" {
				t.Fatalf("postpublish boundary did not recover exact target evidence: %+v", receipt.Target)
			}
		})
	}
}

func prepareOrderedSpeedTarget(t *testing.T, paths realStackRepoPaths) {
	t.Helper()
	updateIntegrationFixtureWorkspaces(t, paths.defaultPath, paths.speedPath, paths.childPath)
	if err := os.WriteFile(filepath.Join(paths.speedPath, "ordered-target-base.txt"), []byte("ordered target base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runJJ(t, "-R", paths.speedPath, "file", "track", "root:ordered-target-base.txt")
	runJJ(t, "-R", paths.speedPath, "commit", "-m", "ordered target base")
	updateIntegrationFixtureWorkspaces(t, paths.defaultPath, paths.speedPath, paths.childPath)
}

func assertOrderedFixturePrepares(t *testing.T, repo string, requestBytes []byte) {
	t.Helper()
	request, _, err := parseIntegrationRequestV1(requestBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateIntegrationAssertions(repo, request); err != nil {
		t.Fatalf("ordered-line fixture does not prepare: %v", err)
	}
}

func TestOrderedLineProcessCrashAfterEveryMutation(t *testing.T) {
	baselineRepo, baselineRequest := setupOrderedLineBoundaryFixture(t, "ordered-process-baseline")
	count := 0
	original := integrationEffectCommandHook
	integrationEffectCommandHook = func(_, boundary string) error {
		if boundary == "after-command" {
			count++
		}
		return nil
	}
	withIntegrationStdin(t, string(baselineRequest))
	_, _, baselineErr := captureOutput(func() error { return runIntegrate([]string{"--repo", baselineRepo, "--request-json", "-"}) })
	integrationEffectCommandHook = original
	if baselineErr != nil || count < 5 {
		t.Fatalf("ordered process baseline failed: count=%d err=%v", count, baselineErr)
	}
	for ordinal := 1; ordinal <= count; ordinal++ {
		repo, request := setupOrderedLineBoundaryFixture(t, "ordered-process-"+strconv.Itoa(ordinal))
		before := currentOperationIDFullForTest(t, repo)
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
			t.Fatalf("ordered helper did not crash at mutation %d: %v", ordinal, err)
		}
		out, _, _ := captureOutput(func() error {
			return runIntegrate([]string{"--repo", repo, "--recover", "ordered-process-" + strconv.Itoa(ordinal), "--json"})
		})
		receipt := decodeIntegrationReceipt(t, out)
		assertDetachedBoundaryDisposition(t, receipt, ordinal, count)
		if ordinal < count && currentOperationIDFullForTest(t, repo) != before {
			t.Fatalf("ordered prepublication process crash %d changed live operation", ordinal)
		}
	}
}

func setupOrderedLineBoundaryFixture(t *testing.T, operationID string) (string, []byte) {
	t.Helper()
	paths := setupRealStackMergeRepo(t, false)
	ignoreIntegrationStateInFixture(t, paths.defaultPath)
	runJJ(t, "-R", paths.defaultPath, "commit", "-m", "ignore integration state")
	updateIntegrationFixtureWorkspaces(t, paths.defaultPath, paths.alphaPath, paths.bravoPath)
	request := integrationRequestBytesForStrategy(operationID, "default", []string{"alpha", "bravo"}, jjFullCommitID(t, paths.defaultPath, "default@"), []string{jjFullCommitID(t, paths.defaultPath, "alpha@"), jjFullCommitID(t, paths.defaultPath, "bravo@")}, integrationStrategyOrderedLine)
	return paths.defaultPath, request
}

func semanticTestRecordForStrategy(operationID, strategy string) integrationOperationRecord {
	targetCommit := strings.Repeat("a", 40)
	payloadCommit := strings.Repeat("b", 40)
	requestBytes := integrationRequestBytesForStrategy(operationID, "alpha", []string{"bravo"}, targetCommit, []string{payloadCommit}, strategy)
	return integrationOperationRecord{
		Schema: integrationOperationRecordV1, OperationID: operationID,
		RequestDigest: integrationRequestDigest(requestBytes), RequestBytes: requestBytes,
		CanonicalProjectPath: "/project", CanonicalTargetPath: "/project/alpha",
		Phase: integrationPhasePrepared, BeforeOperationID: strings.Repeat("1", 128),
		PreparedState: integrationTestPreparedState(), BeforeRepositoryView: integrationTestRepositoryView(targetCommit),
	}
}

func orderedLineSemanticTestRecord(operationID string, frontier []string) integrationOperationRecord {
	record := semanticTestRecordForStrategy(operationID, integrationStrategyOrderedLine)
	record.PreparedState.Target.FrontierCommits = append([]string(nil), frontier...)
	return record
}

func TestOrderedLineRequestRejectsTargetPayloadAndCapabilitiesAdvertiseExecution(t *testing.T) {
	request := integrationRequestV1{Schema: integrationRequestSchemaV1, OperationID: "ordered-target", Target: integrationTargetAssertionV1{ExpectedWorkspace: "alpha", ExpectedHeadCommit: strings.Repeat("a", 40)}, Strategy: integrationStrategyOrderedLine, Payloads: []integrationPayloadAssertionV1{{Workspace: "alpha", ExpectedHeadCommit: strings.Repeat("b", 40)}}}
	if err := validateIntegrationRequestV1(request); err == nil || !strings.Contains(err.Error(), "target workspace") {
		t.Fatalf("ordered-line accepted target as payload: %v", err)
	}
	if got := strings.Join(integrationCapabilities().Integrate.ExecutableStrategies, ","); got != "single,provider-default,ordered-line" {
		t.Fatalf("ordered-line is not executable in capabilities: %s", got)
	}
}
