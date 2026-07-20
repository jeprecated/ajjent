package main

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"reflect"
	"sort"
	"strings"
)

// Test seams around durable detached-operation and publish boundaries.
var integrationEffectPhaseHook = func(string) error { return nil }
var integrationCursorReconcileHook = func(string) error { return nil }
var integrationDetachedCommandHook = func(string) error { return nil }
var integrationPublishHook = func(string) error { return nil }
var integrationNoEffectProofHook = func(string) error { return nil }
var integrationBoundedCaptureFn = runIntegrationCommandCaptureBounded

// Kept as a compatibility test seam while detached-boundary tests migrate.
var integrationEffectCommandHook = func(string, string) error { return nil }

type integrationBoundaryInterruption struct{ err error }

type integrationFreshPublicationAuthority struct {
	Record integrationOperationRecord
}

func (e *integrationBoundaryInterruption) Error() string { return e.err.Error() }

func integrationQueryArgs(repoPath, operationID string, commandArgs ...string) []string {
	args := []string{"-R", repoPath, "--color=never", "--no-pager", "--ignore-working-copy"}
	if operationID != "" {
		args = append(args, "--at-op="+operationID)
	}
	return append(args, commandArgs...)
}

func integrationQuery(repoPath, operationID string, commandArgs ...string) (string, error) {
	return commandCaptureFn("jj", integrationQueryArgs(repoPath, operationID, commandArgs...)...)
}

func integrationRevisionMatches(repoPath, operationID, revset string) (bool, error) {
	ids, err := func() ([]string, error) {
		if operationID == "" {
			return integrationCommitIDs(repoPath, revset)
		}
		return integrationCommitIDsAtOperation(repoPath, operationID, revset)
	}()
	return len(ids) > 0, err
}

func integrationRevisionIsAncestor(repoPath, operationID, ancestor, descendant string) (bool, error) {
	if ancestor == "" || descendant == "" {
		return false, nil
	}
	return integrationRevisionMatches(repoPath, operationID, fmt.Sprintf("%s::%s", ancestor, descendant))
}

func materializeIntegrationPayloadChanges(repoPath, targetHandle, payloadHandle string) ([]integrationPreparedChangeV1, []string, error) {
	return materializeIntegrationPayloadChangesAtOperation(repoPath, "", targetHandle, payloadHandle)
}

func materializeIntegrationPayloadChangesAtOperation(repoPath, operationID, targetHandle, payloadHandle string) ([]integrationPreparedChangeV1, []string, error) {
	revset := fmt.Sprintf("(::%s@ & ~::%s@ & ~empty())", payloadHandle, targetHandle)
	out, err := integrationQuery(repoPath, operationID, "log", "-r", revset, "--no-graph", "-T", "change_id ++ \"\\t\" ++ commit_id ++ \"\\n\"")
	if err != nil {
		return nil, nil, err
	}
	changes := []integrationPreparedChangeV1{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 2 || !integrationFullChangeIDRE.MatchString(parts[0]) || !integrationCommitIDRE.MatchString(parts[1]) {
			return nil, nil, newIntegrationProtocolError(integrationErrorAssertionFailed, "payload change evidence is not exact")
		}
		changes = append(changes, integrationPreparedChangeV1{ChangeID: parts[0], CommitID: parts[1]})
	}
	var frontier []string
	if operationID == "" {
		frontier, err = integrationCommitIDs(repoPath, "heads("+revset+")")
	} else {
		frontier, err = integrationCommitIDsAtOperation(repoPath, operationID, "heads("+revset+")")
	}
	if err != nil {
		return nil, nil, err
	}
	return changes, frontier, nil
}

func integrationCommitIDs(repoPath, revset string) ([]string, error) {
	out, err := integrationQuery(repoPath, "", "log", "-r", revset, "--no-graph", "-T", "commit_id ++ \"\\n\"")
	if err != nil {
		return nil, err
	}
	return validateIntegrationCommitIDs(out)
}

func integrationCommitIDsAtOperation(repoPath, operationID, revset string) ([]string, error) {
	out, err := integrationQuery(repoPath, operationID, "log", "-r", revset, "--no-graph", "-T", "commit_id ++ \"\\n\"")
	if err != nil {
		return nil, err
	}
	return validateIntegrationCommitIDs(out)
}

func validateIntegrationCommitIDs(out string) ([]string, error) {
	ids := uniqueNonEmptyStrings(strings.Split(out, "\n"))
	for _, id := range ids {
		if !integrationCommitIDRE.MatchString(id) {
			return nil, errors.New("Jujutsu returned an invalid full commit id")
		}
	}
	return ids, nil
}

func integrationOperationFullID(repoPath, operationID string) (string, error) {
	out, err := integrationQuery(repoPath, operationID, "op", "log", "-n", "1", "--no-graph", "-T", "id ++ \"\\n\"")
	if err != nil {
		return "", err
	}
	full := strings.TrimSpace(out)
	if !integrationFullOperationIDRE.MatchString(full) {
		return "", errors.New("detached operation id is invalid")
	}
	return full, nil
}

func integrationWorkspaceHeadCommitAtOperation(repoPath, operationID, handle string) (string, error) {
	ids, err := integrationCommitIDsAtOperation(repoPath, operationID, handle+"@")
	if err != nil {
		return "", err
	}
	if len(ids) != 1 {
		return "", errors.New("Workspace does not resolve to one head in detached operation")
	}
	return ids[0], nil
}

func integrationRevisionMatchesAtOperation(repoPath, operationID, revset string) (bool, error) {
	ids, err := integrationCommitIDsAtOperation(repoPath, operationID, revset)
	return len(ids) > 0, err
}

func executePreparedIntegrationOperation(repoRoot string, cfg config, project string, target integrationTargetResolution, binding integrationStateBinding, record integrationOperationRecord, request integrationRequestV1) error {
	if err := revalidatePreparedIntegrationForEffect(repoRoot, cfg, project, target, record, request); err != nil {
		return emitIntegrationFailure(record.OperationID, record.RequestDigest, request, err, integrationBatchUnknownEffect, integrationNextActionOperatorReview)
	}
	var authority integrationFreshPublicationAuthority
	var err error
	if request.Strategy == integrationStrategyOrderedLine {
		authority, err = buildDetachedOrderedLineTransaction(repoRoot, cfg, project, target, binding, &record, request)
	} else {
		authority, err = buildDetachedStackTransaction(target.Path, cfg.Stack, binding, &record, request)
	}
	if err != nil {
		if boundary := new(integrationBoundaryInterruption); errors.As(err, &boundary) {
			return emitIntegrationFailure(record.OperationID, record.RequestDigest, request, err, integrationBatchUnknownEffect, integrationNextActionRecover)
		}
		code := integrationFailureCode(err)
		return proveAndCompleteNoEffect(repoRoot, cfg, project, target, binding, record, request, code)
	}
	if err := integrationEffectPhaseHook(integrationPhaseGraphRewritten); err != nil {
		return emitIntegrationFailure(record.OperationID, record.RequestDigest, request, err, integrationBatchUnknownEffect, integrationNextActionRecover)
	}
	return publishAndCompleteIntegration(repoRoot, cfg, project, target, binding, record, request, authority)
}

func buildDetachedOrderedLineTransaction(repoRoot string, cfg config, project string, target integrationTargetResolution, binding integrationStateBinding, record *integrationOperationRecord, request integrationRequestV1) (integrationFreshPublicationAuthority, error) {
	if len(record.PreparedState.Target.FrontierCommits) == 0 || len(record.PreparedState.Payloads) != len(request.Payloads) {
		return integrationFreshPublicationAuthority{}, errors.New("ordered-line prepared evidence is incomplete")
	}
	operationID := record.BeforeOperationID
	destination := append([]string(nil), record.PreparedState.Target.FrontierCommits...)
	finalTip := ""
	for _, payload := range record.PreparedState.Payloads {
		source := integrationPreparedChangeRevset(payload.Changes)
		if source == "" {
			return integrationFreshPublicationAuthority{}, errors.New("ordered-line payload contribution is empty")
		}
		anchored, err := orderedLineContributionAnchoredAtOperation(target.Path, operationID, source, destination)
		if err != nil {
			return integrationFreshPublicationAuthority{}, err
		}
		if !anchored {
			args := []string{"rebase", "-r", source}
			for _, commit := range destination {
				args = append(args, "-d", commit)
			}
			if err := stageDetachedCommand(target.Path, operationID, binding, record, args...); err != nil {
				return integrationFreshPublicationAuthority{}, err
			}
			operationID = record.GraphOperationID
		}
		conflicted, err := integrationRevisionMatchesAtOperation(target.Path, operationID, "conflicts() & ("+source+")")
		if err != nil {
			return integrationFreshPublicationAuthority{}, err
		}
		if conflicted {
			return integrationFreshPublicationAuthority{}, newIntegrationProtocolError(integrationErrorConflict, "ordered-line payload is conflicted")
		}
		tips, err := integrationCommitIDsAtOperation(target.Path, operationID, "heads("+source+")")
		if err != nil || len(tips) != 1 {
			return integrationFreshPublicationAuthority{}, errors.New("ordered-line payload does not resolve to one staged tip")
		}
		finalTip = tips[0]
		destination = []string{finalTip}
	}
	if finalTip == "" {
		return integrationFreshPublicationAuthority{}, errors.New("ordered-line has no integrated tip")
	}

	emptyAncestors := topEmptyMutableAncestorsRevset(request.Target.ExpectedWorkspace + "@")
	hasEmptyAncestors, err := integrationRevisionMatchesAtOperation(target.Path, operationID, emptyAncestors)
	if err != nil {
		return integrationFreshPublicationAuthority{}, err
	}
	if hasEmptyAncestors {
		if err := stageDetachedCommand(target.Path, operationID, binding, record, "abandon", "-r", emptyAncestors); err != nil {
			return integrationFreshPublicationAuthority{}, err
		}
		operationID = record.GraphOperationID
	}
	if err := stageDetachedCommand(target.Path, operationID, binding, record, "new", finalTip); err != nil {
		return integrationFreshPublicationAuthority{}, err
	}
	operationID = record.GraphOperationID

	workspacePaths, err := integrationSelectedWorkspacePaths(repoRoot, cfg, project, target.Handle, request)
	if err != nil {
		return integrationFreshPublicationAuthority{}, err
	}
	for _, payload := range request.Payloads {
		path, ok := workspacePaths[payload.Workspace]
		if !ok {
			return integrationFreshPublicationAuthority{}, errors.New("ordered-line payload Workspace path is unavailable")
		}
		if err := stageDetachedCommand(path, operationID, binding, record, "new", finalTip); err != nil {
			return integrationFreshPublicationAuthority{}, err
		}
		operationID = record.GraphOperationID
	}
	if err := cleanupDetachedGeneratedEmptyHeads(target.Path, binding, record); err != nil {
		return integrationFreshPublicationAuthority{}, err
	}
	operationID = record.GraphOperationID
	if err := proveOrderedLineAtOperation(target.Path, operationID, *record, request); err != nil {
		return integrationFreshPublicationAuthority{}, err
	}
	return sealDetachedIntegrationTransaction(target.Path, binding, record, request)
}

func integrationPreparedChangeRevset(changes []integrationPreparedChangeV1) string {
	terms := make([]string, 0, len(changes))
	for _, change := range changes {
		terms = append(terms, "change_id("+change.ChangeID+")")
	}
	if len(terms) == 0 {
		return ""
	}
	return "(" + strings.Join(terms, " | ") + ")"
}

func orderedLineContributionAnchoredAtOperation(repoPath, operationID, source string, destination []string) (bool, error) {
	outsideParents, err := integrationCommitIDsAtOperation(repoPath, operationID, "parents(roots("+source+")) & ~("+source+")")
	if err != nil {
		return false, err
	}
	return sameIntegrationCommitSet(outsideParents, destination), nil
}

func sameIntegrationCommitSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	leftCopy, rightCopy := append([]string(nil), left...), append([]string(nil), right...)
	sort.Strings(leftCopy)
	sort.Strings(rightCopy)
	return reflect.DeepEqual(leftCopy, rightCopy)
}

func integrationSelectedWorkspacePaths(repoRoot string, cfg config, project, currentHandle string, request integrationRequestV1) (map[string]string, error) {
	refs, err := listWorkspaceRefs(repoRoot)
	if err != nil {
		return nil, err
	}
	wanted := map[string]struct{}{}
	for _, payload := range request.Payloads {
		wanted[payload.Workspace] = struct{}{}
	}
	paths := make(map[string]string, len(wanted))
	for _, ref := range refs {
		if _, ok := wanted[ref.Handle]; !ok {
			continue
		}
		path := workspacePathForRef(repoRoot, cfg.WorkspacesRoot, project, ref, currentHandle)
		if !workspacePathExists(path) {
			return nil, errors.New("selected Workspace path is missing")
		}
		paths[ref.Handle] = path
	}
	if len(paths) != len(wanted) {
		return nil, errors.New("selected Workspace registration changed")
	}
	return paths, nil
}

func buildDetachedStackTransaction(repoPath string, stack stackConfig, binding integrationStateBinding, record *integrationOperationRecord, request integrationRequestV1) (integrationFreshPublicationAuthority, error) {
	probeChain := append([]string(nil), record.DetachedOperationIDs...)
	probeGraph := record.GraphOperationID
	cleanTidy, tidyConflicted, err := tryDetachedSingleTidyStack(repoPath, stack, binding, record, request)
	if err != nil {
		return integrationFreshPublicationAuthority{}, err
	}
	if !cleanTidy {
		restoreDetachedChain(record, probeChain, probeGraph)
		forcedShape := ""
		if tidyConflicted {
			forcedShape = "merge"
		}
		if err := stageDetachedGenericStack(repoPath, stack, forcedShape, binding, record, request); err != nil {
			return integrationFreshPublicationAuthority{}, err
		}
	}
	if err := finalizeDetachedStackTarget(repoPath, binding, record, request); err != nil {
		return integrationFreshPublicationAuthority{}, err
	}
	if err := stageDetachedPayloadCursors(repoPath, binding, record, request); err != nil {
		return integrationFreshPublicationAuthority{}, err
	}
	if err := cleanupDetachedGeneratedEmptyHeads(repoPath, binding, record); err != nil {
		return integrationFreshPublicationAuthority{}, err
	}
	return sealDetachedIntegrationTransaction(repoPath, binding, record, request)
}

func cleanupDetachedGeneratedEmptyHeads(repoPath string, binding integrationStateBinding, record *integrationOperationRecord) error {
	if record.GraphOperationID == "" {
		return nil
	}
	const disposable = `visible_heads() & empty() & description("") & mutable() & ~working_copies()`
	before, err := integrationChangeCommitPairsAtOperation(repoPath, record.BeforeOperationID, disposable)
	if err != nil {
		return err
	}
	staged, err := integrationChangeCommitPairsAtOperation(repoPath, record.GraphOperationID, disposable)
	if err != nil {
		return err
	}
	beforeCommits := make(map[string]struct{}, len(before))
	beforeChanges := make(map[string]struct{}, len(before))
	for _, item := range before {
		beforeCommits[item.CommitID] = struct{}{}
		beforeChanges[item.ChangeID] = struct{}{}
	}
	generated := make([]string, 0, len(staged))
	for _, item := range staged {
		if _, existed := beforeCommits[item.CommitID]; existed {
			continue
		}
		if _, evolvedUnrelated := beforeChanges[item.ChangeID]; evolvedUnrelated {
			// Leave an evolved pre-existing head for the full repository-view proof
			// to reject rather than concealing an unrelated graph mutation.
			continue
		}
		generated = append(generated, item.CommitID)
	}
	if len(generated) == 0 {
		return nil
	}
	sort.Strings(generated)
	return stageDetachedCommand(repoPath, record.GraphOperationID, binding, record, "abandon", "-r", "("+strings.Join(generated, " | ")+")")
}

// Each cleanup-evidence row is exactly: 32-byte change id, tab, 40-byte
// commit id, newline. This internal ceiling is derived from the existing item
// bound and is intentionally not advertised as a public stdout/stderr limit.
const integrationMaxCleanupEvidenceBytes = integrationMaxRepositoryEvidenceItems * (32 + 1 + 40 + 1)

var (
	errIntegrationEvidenceOutputLimit = errors.New("machine evidence command output exceeds the internal limit")
	errIntegrationEvidenceCommand     = errors.New("machine evidence command failed")
)

func runIntegrationCommandCaptureBounded(maxBytes int, name string, args ...string) (string, error) {
	if maxBytes <= 0 || uint64(maxBytes) >= uint64(math.MaxInt64) {
		return "", errIntegrationEvidenceOutputLimit
	}
	cmd := exec.Command(name, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", errIntegrationEvidenceCommand
	}
	// Child diagnostics are deliberately drained rather than retained. They are
	// not machine evidence and are not covered by the public JSON stdout bound.
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return "", errIntegrationEvidenceCommand
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, int64(maxBytes)+1))
	if readErr != nil || len(data) > maxBytes {
		_ = cmd.Process.Kill()
		_, _ = io.Copy(io.Discard, stdout)
		_ = cmd.Wait()
		if len(data) > maxBytes {
			return "", errIntegrationEvidenceOutputLimit
		}
		return "", errIntegrationEvidenceCommand
	}
	// The stdout pipe has reached EOF, so stderr has been drained concurrently
	// and Wait cannot deadlock on either child stream.
	if err := cmd.Wait(); err != nil {
		return "", errIntegrationEvidenceCommand
	}
	return string(data), nil
}

func integrationChangeCommitPairsAtOperation(repoPath, operationID, revset string) ([]integrationPreparedChangeV1, error) {
	args := integrationQueryArgs(repoPath, operationID, "log", "-r", revset, "--no-graph", "-T", `change_id ++ "\t" ++ commit_id ++ "\n"`)
	out, err := integrationBoundedCaptureFn(integrationMaxCleanupEvidenceBytes, "jj", args...)
	if err != nil {
		return nil, err
	}
	pairs := []integrationPreparedChangeV1{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 2 || !integrationFullChangeIDRE.MatchString(parts[0]) || !integrationCommitIDRE.MatchString(parts[1]) {
			return nil, errors.New("Jujutsu returned invalid disposable-head evidence")
		}
		pairs = append(pairs, integrationPreparedChangeV1{ChangeID: parts[0], CommitID: parts[1]})
		if len(pairs) > integrationMaxRepositoryEvidenceItems {
			return nil, errors.New("disposable-head evidence exceeds the bounded item limit")
		}
	}
	return pairs, nil
}

func sealDetachedIntegrationTransaction(repoPath string, binding integrationStateBinding, record *integrationOperationRecord, request integrationRequestV1) (integrationFreshPublicationAuthority, error) {
	mappings, err := proveIntegrationPayloadMappingsAtOperation(repoPath, record.GraphOperationID, request.Target.ExpectedWorkspace, record.PreparedState)
	if err != nil {
		return integrationFreshPublicationAuthority{}, err
	}
	record.StagedPayloadMappings = mappings
	state, err := detachedTargetState(repoPath, record.GraphOperationID, request.Target.ExpectedWorkspace)
	if err != nil {
		return integrationFreshPublicationAuthority{}, err
	}
	record.StagedTargetState = &state
	record.StagedRepositoryView, err = integrationRepositoryViewAtOperation(repoPath, record.GraphOperationID, request.Target.ExpectedWorkspace)
	if err != nil {
		return integrationFreshPublicationAuthority{}, err
	}
	record.PublishPending = true
	record.Phase = integrationPhaseGraphRewritten
	if err := proveDetachedIntegrationTransaction(repoPath, *record, request); err != nil {
		return integrationFreshPublicationAuthority{}, err
	}
	if err := writeIntegrationOperationRecordAtomic(binding.StateDir, *record); err != nil {
		return integrationFreshPublicationAuthority{}, err
	}
	stored, found, err := loadIntegrationOperationRecord(binding.StateDir, record.OperationID)
	if err != nil || !found || !reflect.DeepEqual(stored, *record) {
		return integrationFreshPublicationAuthority{}, errors.New("detached integration journal reread did not match fresh authority")
	}
	if err := proveDetachedIntegrationTransaction(repoPath, stored, request); err != nil {
		return integrationFreshPublicationAuthority{}, err
	}
	return integrationFreshPublicationAuthority{Record: stored}, nil
}

func tryDetachedSingleTidyStack(repoPath string, stack stackConfig, binding integrationStateBinding, record *integrationOperationRecord, request integrationRequestV1) (bool, bool, error) {
	conflictStrategy, err := resolveStackConflictStrategy(stack.ConflictStrategy)
	if err != nil {
		return false, false, err
	}
	if !eligibleForSingleInputTidyProbe(integrationRequestPayloadHandles(request), stack, conflictStrategy) || len(record.PreparedState.Payloads) != 1 || len(record.PreparedState.Payloads[0].Changes) != 1 {
		return false, false, nil
	}
	change := record.PreparedState.Payloads[0].Changes[0]
	cannotProbe, err := integrationRevisionMatches(repoPath, "", fmt.Sprintf("(immutable() | conflicts()) & commit_id(%s)", change.CommitID))
	if err != nil || cannotProbe {
		return false, false, err
	}
	payloadIsAncestor, err := integrationRevisionIsAncestor(repoPath, "", change.CommitID, request.Target.ExpectedWorkspace+"@")
	if err != nil {
		return false, false, err
	}
	targetIsAncestor, err := integrationRevisionIsAncestor(repoPath, "", request.Target.ExpectedWorkspace+"@", change.CommitID)
	if err != nil || payloadIsAncestor || targetIsAncestor {
		return false, false, err
	}
	if err := stageDetachedCommand(repoPath, record.BeforeOperationID, binding, record, "rebase", "-r", change.CommitID, "-B", request.Target.ExpectedWorkspace+"@"); err != nil {
		return false, false, err
	}
	conflicted, err := integrationRevisionMatchesAtOperation(repoPath, record.GraphOperationID, "conflicts() & ::"+request.Target.ExpectedWorkspace+"@")
	if err != nil {
		return false, false, err
	}
	return !conflicted, conflicted, nil
}

func restoreDetachedChain(record *integrationOperationRecord, operationIDs []string, graphOperationID string) {
	record.DetachedOperationIDs = append([]string(nil), operationIDs...)
	record.GraphOperationID = graphOperationID
	if graphOperationID == "" {
		record.Phase = integrationPhasePrepared
	}
}

func stageDetachedGenericStack(repoPath string, stack stackConfig, forcedShape string, binding integrationStateBinding, record *integrationOperationRecord, request integrationRequestV1) error {
	baseChain := append([]string(nil), record.DetachedOperationIDs...)
	baseGraph := record.GraphOperationID
	mode, modeReason, err := resolveStackRebaseMode(repoPath, stack.RebaseMode)
	if err != nil {
		return err
	}
	requestedShape := stack.Shape
	if forcedShape != "" {
		requestedShape = forcedShape
	}
	shape, shapeReason, destinations, err := resolveStackShape(repoPath, integrationRequestPayloadHandles(request), requestedShape)
	if err != nil {
		return err
	}
	conflicted, err := stageDetachedRebaseAttempt(repoPath, record.BeforeOperationID, binding, record, request, mode, modeReason, shape, shapeReason, destinations)
	if err != nil {
		return err
	}
	strategy, err := resolveStackConflictStrategy(stack.ConflictStrategy)
	if err != nil {
		return err
	}
	originalRequested := strings.TrimSpace(strings.ToLower(stack.Shape))
	if originalRequested == "" {
		originalRequested = "auto"
	}
	if forcedShape == "" && strategy == "prefer-clean" && conflicted && originalRequested == "auto" {
		alternative := "merge"
		if shape == "merge" {
			alternative = "linear"
		}
		altShape, altReason, altDestinations, altErr := resolveStackShape(repoPath, integrationRequestPayloadHandles(request), alternative)
		if altErr == nil {
			restoreDetachedChain(record, baseChain, baseGraph)
			altConflicted, stageErr := stageDetachedRebaseAttempt(repoPath, record.BeforeOperationID, binding, record, request, mode, modeReason, altShape, altReason, altDestinations)
			if stageErr != nil {
				return stageErr
			}
			conflicted = altConflicted
			if altConflicted && altShape == "linear" {
				restoreDetachedChain(record, baseChain, baseGraph)
				mergeShape, mergeReason, mergeDestinations, mergeErr := resolveStackShape(repoPath, integrationRequestPayloadHandles(request), "merge")
				if mergeErr != nil {
					return mergeErr
				}
				conflicted, err = stageDetachedRebaseAttempt(repoPath, record.BeforeOperationID, binding, record, request, mode, modeReason, mergeShape, mergeReason, mergeDestinations)
				if err != nil {
					return err
				}
			}
		}
	}
	if conflicted {
		return newIntegrationProtocolError(integrationErrorConflict, "detached Stack result is conflicted")
	}
	return nil
}

func stageDetachedRebaseAttempt(repoPath, baseOperationID string, binding integrationStateBinding, record *integrationOperationRecord, request integrationRequestV1, mode, _, shape, _ string, baseDestinations []string) (bool, error) {
	flag := "-b"
	destinations := append([]string(nil), baseDestinations...)
	if mode == "revision" {
		flag = "-r"
		parents, err := parentChangeIDs(repoPath)
		if err != nil {
			return false, err
		}
		preserved, err := parentChangeIDsToPreserve(repoPath, parents, baseDestinations)
		if err != nil {
			return false, err
		}
		destinations = append(preserved, destinations...)
	}
	destinations = uniqueNonEmptyStrings(destinations)
	if shape == "linear" && len(destinations) == 0 {
		return false, errors.New("detached Stack has no destination")
	}
	allDescendants := true
	for _, destination := range destinations {
		descendant, err := integrationRevisionIsAncestor(repoPath, "", request.Target.ExpectedWorkspace+"@", destination)
		if err != nil {
			return false, err
		}
		if !descendant {
			allDescendants = false
		}
	}
	if !allDescendants {
		args := []string{"rebase", flag, request.Target.ExpectedWorkspace + "@"}
		for _, destination := range destinations {
			args = append(args, "-d", destination)
		}
		if err := stageDetachedCommand(repoPath, baseOperationID, binding, record, args...); err != nil {
			return false, err
		}
	}
	operationID := record.GraphOperationID
	if operationID == "" {
		operationID = baseOperationID
	}
	return integrationRevisionMatchesAtOperation(repoPath, operationID, "conflicts() & "+request.Target.ExpectedWorkspace+"@")
}

func finalizeDetachedStackTarget(repoPath string, binding integrationStateBinding, record *integrationOperationRecord, request integrationRequestV1) error {
	operationID := record.GraphOperationID
	if operationID == "" {
		operationID = record.BeforeOperationID
	}
	emptyAncestors := topEmptyMutableAncestorsRevset(request.Target.ExpectedWorkspace + "@")
	hasEmptyAncestors, err := integrationRevisionMatchesAtOperation(repoPath, operationID, emptyAncestors)
	if err != nil {
		return err
	}
	if hasEmptyAncestors {
		if err := stageDetachedCommand(repoPath, operationID, binding, record, "abandon", "-r", emptyAncestors); err != nil {
			return err
		}
		operationID = record.GraphOperationID
	}
	landed, err := preparedChangesReachableFromTargetAtOperation(repoPath, operationID, request.Target.ExpectedWorkspace, record.PreparedState)
	if err != nil {
		return err
	}
	if !landed {
		tips, err := detachedPreparedFrontier(repoPath, operationID, record.PreparedState)
		if err != nil {
			return err
		}
		args := append([]string{"new"}, tips...)
		if err := stageDetachedCommand(repoPath, operationID, binding, record, args...); err != nil {
			return err
		}
		operationID = record.GraphOperationID
	}
	parents, err := integrationCommitIDsAtOperation(repoPath, operationID, "parents("+request.Target.ExpectedWorkspace+"@)")
	if err != nil {
		return err
	}
	if len(parents) > 1 {
		description, err := integrationQuery(repoPath, operationID, "log", "-r", request.Target.ExpectedWorkspace+"@", "--no-graph", "-T", "description")
		if err != nil {
			return err
		}
		if strings.TrimSpace(description) == "" {
			if err := stageDetachedCommand(repoPath, operationID, binding, record, "describe", "-r", request.Target.ExpectedWorkspace+"@", "-m", "chore: merge"); err != nil {
				return err
			}
			operationID = record.GraphOperationID
		}
		if err := stageDetachedCommand(repoPath, operationID, binding, record, "new", request.Target.ExpectedWorkspace+"@"); err != nil {
			return err
		}
	}
	conflicted, err := integrationRevisionMatchesAtOperation(repoPath, record.GraphOperationID, "conflicts() & ::"+request.Target.ExpectedWorkspace+"@")
	if err != nil {
		return err
	}
	if conflicted {
		return newIntegrationProtocolError(integrationErrorConflict, "detached Stack target is conflicted")
	}
	return nil
}

func detachedPreparedFrontier(repoPath, operationID string, prepared integrationPreparedStateV1) ([]string, error) {
	changeTerms := []string{}
	for _, payload := range prepared.Payloads {
		for _, change := range payload.Changes {
			changeTerms = append(changeTerms, "change_id("+change.ChangeID+")")
		}
	}
	if len(changeTerms) == 0 {
		return nil, errors.New("prepared integration has no changes")
	}
	return integrationCommitIDsAtOperation(repoPath, operationID, "heads("+strings.Join(changeTerms, " | ")+")")
}

func stageDetachedPayloadCursors(repoPath string, binding integrationStateBinding, record *integrationOperationRecord, request integrationRequestV1) error {
	for _, payload := range request.Payloads {
		operationID := record.GraphOperationID
		contains, err := integrationRevisionMatchesAtOperation(repoPath, operationID, request.Target.ExpectedWorkspace+"@ & ::"+payload.Workspace+"@")
		if err != nil {
			return err
		}
		if contains {
			continue
		}
		if err := stageDetachedCommand(repoPath, operationID, binding, record, "rebase", "-r", payload.Workspace+"@", "-d", request.Target.ExpectedWorkspace+"@"); err != nil {
			return err
		}
	}
	return nil
}

func stageDetachedCommand(repoPath, baseOperationID string, binding integrationStateBinding, record *integrationOperationRecord, commandArgs ...string) error {
	if err := integrationDetachedCommandHook("before-command"); err != nil {
		return &integrationBoundaryInterruption{err: err}
	}
	args := []string{"-R", repoPath, "--color=never", "--no-pager", "--at-op=" + baseOperationID, "--no-integrate-operation"}
	args = append(args, commandArgs...)
	out, err := commandCombinedCaptureFn("jj", args...)
	if err != nil {
		return err
	}
	shortID, err := detachedOperationID(out)
	if err != nil {
		return err
	}
	fullID, err := integrationOperationFullID(repoPath, shortID)
	if err != nil {
		return err
	}
	evidence, err := integrationOperationEvidence(repoPath, fullID)
	if err != nil || len(evidence.ParentOperationIDs) != 1 || evidence.ParentOperationIDs[0] != baseOperationID {
		return errors.New("detached operation does not have the exact expected parent")
	}
	if err := integrationDetachedCommandHook("after-command-before-record"); err != nil {
		return &integrationBoundaryInterruption{err: err}
	}
	if err := integrationEffectCommandHook("detached", "after-command"); err != nil {
		return &integrationBoundaryInterruption{err: err}
	}
	record.DetachedOperationIDs = append(record.DetachedOperationIDs, fullID)
	record.GraphOperationID = fullID
	record.Phase = integrationPhaseGraphRewritten
	if err := writeIntegrationOperationRecordAtomic(binding.StateDir, *record); err != nil {
		return err
	}
	if err := integrationDetachedCommandHook("after-record"); err != nil {
		return &integrationBoundaryInterruption{err: err}
	}
	return nil
}

func detachedTargetState(repoPath, operationID, targetHandle string) (integrationTargetAdvancedStateV1, error) {
	head, err := integrationWorkspaceHeadCommitAtOperation(repoPath, operationID, targetHandle)
	if err != nil {
		return integrationTargetAdvancedStateV1{}, err
	}
	parents, err := integrationCommitIDsAtOperation(repoPath, operationID, "parents("+targetHandle+"@)")
	if err != nil {
		return integrationTargetAdvancedStateV1{}, err
	}
	if len(parents) != 1 {
		return integrationTargetAdvancedStateV1{}, errors.New("staged integration target does not have one integrated tip")
	}
	return integrationTargetAdvancedStateV1{IntegratedTipCommit: parents[0], AfterHeadCommit: head}, nil
}

func publishAndCompleteIntegration(repoRoot string, cfg config, project string, target integrationTargetResolution, binding integrationStateBinding, record integrationOperationRecord, request integrationRequestV1, authority integrationFreshPublicationAuthority) error {
	if err := publishDetachedIntegration(target.Path, binding, &record, request, authority); err != nil {
		if boundary := new(integrationBoundaryInterruption); errors.As(err, &boundary) {
			return emitIntegrationFailure(record.OperationID, record.RequestDigest, request, err, integrationBatchUnknownEffect, integrationNextActionRecover)
		}
		return emitIntegrationFailure(record.OperationID, record.RequestDigest, request, err, integrationBatchUnknownEffect, integrationNextActionOperatorReview)
	}
	return finishPublishedIntegration(repoRoot, cfg, project, target, binding, record, request)
}

func publishDetachedIntegration(repoPath string, binding integrationStateBinding, record *integrationOperationRecord, request integrationRequestV1, authority integrationFreshPublicationAuthority) error {
	if record.GraphOperationID == "" || record.StagedTargetState == nil || !reflect.DeepEqual(authority.Record, *record) {
		return errors.New("detached integration graph has no fresh publication authority")
	}
	stored, found, err := loadIntegrationOperationRecord(binding.StateDir, record.OperationID)
	if err != nil || !found || !reflect.DeepEqual(stored, authority.Record) {
		return errors.New("detached integration journal no longer matches fresh publication authority")
	}
	if err := proveDetachedIntegrationTransaction(repoPath, *record, request); err != nil {
		return err
	}
	current, err := currentOperationFullID(repoPath)
	if err != nil || current != record.BeforeOperationID {
		return newIntegrationProtocolError(integrationErrorUnknownEffect, "repository operation changed before detached integration publish")
	}
	if _, err := proveIntegrationPayloadMappingsAtOperation(repoPath, record.GraphOperationID, request.Target.ExpectedWorkspace, record.PreparedState); err != nil {
		return err
	}
	record.PublishPending = true
	if err := writeIntegrationOperationRecordAtomic(binding.StateDir, *record); err != nil {
		return err
	}
	stored, found, err = loadIntegrationOperationRecord(binding.StateDir, record.OperationID)
	if err != nil || !found || !reflect.DeepEqual(stored, *record) {
		return errors.New("publish-pending journal reread did not match fresh publication authority")
	}
	if err := proveDetachedIntegrationTransaction(repoPath, stored, request); err != nil {
		return err
	}
	if err := integrationPublishHook("before-command"); err != nil {
		return &integrationBoundaryInterruption{err: err}
	}
	if err := commandToStderrFn("jj", "-R", repoPath, "--ignore-working-copy", "op", "integrate", record.GraphOperationID); err != nil {
		return err
	}
	if err := integrationPublishHook("after-command-before-record"); err != nil {
		return &integrationBoundaryInterruption{err: err}
	}
	if err := integrationEffectCommandHook("publish", "after-command"); err != nil {
		return &integrationBoundaryInterruption{err: err}
	}
	current, err = currentOperationFullID(repoPath)
	if err != nil || current != record.GraphOperationID {
		return newIntegrationProtocolError(integrationErrorUnknownEffect, "published operation does not exclusively own the current repository state")
	}
	if err := proveDetachedIntegrationTransaction(repoPath, *record, request); err != nil {
		return newIntegrationProtocolError(integrationErrorUnknownEffect, "published integration evidence is invalid")
	}
	record.PublishPending = false
	record.Phase = integrationPhaseTargetAdvanced
	record.CommitPointOperation = record.GraphOperationID
	state := *record.StagedTargetState
	record.TargetAdvancedState = &state
	if err := writeIntegrationOperationRecordAtomic(binding.StateDir, *record); err != nil {
		return err
	}
	if err := integrationEffectPhaseHook(integrationPhaseTargetAdvanced); err != nil {
		return &integrationBoundaryInterruption{err: err}
	}
	return integrationPublishHook("after-record")
}

func finishPublishedIntegration(repoRoot string, cfg config, project string, target integrationTargetResolution, binding integrationStateBinding, record integrationOperationRecord, request integrationRequestV1) error {
	current, err := currentOperationFullID(target.Path)
	if err != nil || current != record.GraphOperationID || record.TargetAdvancedState == nil {
		return emitIntegrationFailure(record.OperationID, record.RequestDigest, request, newIntegrationProtocolError(integrationErrorUnknownEffect, "published integration graph changed before receipt"), integrationBatchUnknownEffect, integrationNextActionOperatorReview)
	}
	if err := updateIntegratedWorkspaceFiles(repoRoot, cfg, project, target.Handle, request); err != nil {
		return emitIntegrationFailure(record.OperationID, record.RequestDigest, request, err, integrationBatchUnknownEffect, integrationNextActionRecover)
	}
	current, err = currentOperationFullID(target.Path)
	if err != nil || current != record.GraphOperationID {
		return emitIntegrationFailure(record.OperationID, record.RequestDigest, request, newIntegrationProtocolError(integrationErrorUnknownEffect, "workspace update changed the published integration operation"), integrationBatchUnknownEffect, integrationNextActionOperatorReview)
	}
	mappings, err := proveIntegrationPayloadMappings(target.Path, target.Handle, record.PreparedState)
	if err != nil || !integrationPayloadMappingsEqual(mappings, record.StagedPayloadMappings) {
		if err == nil {
			err = errors.New("published payload mappings differ from the staged detached graph")
		}
		return emitIntegrationFailure(record.OperationID, record.RequestDigest, request, err, integrationBatchUnknownEffect, integrationNextActionOperatorReview)
	}
	if err := verifyIntegrationTerminalGraph(target.Path, target.Handle, record); err != nil {
		return emitIntegrationFailure(record.OperationID, record.RequestDigest, request, err, integrationBatchUnknownEffect, integrationNextActionOperatorReview)
	}
	record.Phase = integrationPhaseCursorsReconciled
	if err := writeIntegrationOperationRecordAtomic(binding.StateDir, record); err != nil {
		return emitIntegrationFailure(record.OperationID, record.RequestDigest, request, err, integrationBatchUnknownEffect, integrationNextActionOperatorReview)
	}
	if err := integrationEffectPhaseHook(integrationPhaseCursorsReconciled); err != nil {
		return emitIntegrationFailure(record.OperationID, record.RequestDigest, request, err, integrationBatchUnknownEffect, integrationNextActionRecover)
	}
	return completeSuccessfulIntegration(binding, record, request, mappings)
}

func updateIntegratedWorkspaceFiles(repoRoot string, cfg config, project, currentHandle string, request integrationRequestV1) error {
	refs, err := listWorkspaceRefs(repoRoot)
	if err != nil {
		return err
	}
	byHandle := make(map[string]workspaceRef, len(refs))
	for _, ref := range refs {
		byHandle[ref.Handle] = ref
	}
	handles := []string{request.Target.ExpectedWorkspace}
	for _, payload := range request.Payloads {
		handles = append(handles, payload.Workspace)
	}
	for _, handle := range handles {
		if handle != request.Target.ExpectedWorkspace {
			if err := integrationCursorReconcileHook(handle); err != nil {
				return err
			}
		}
		ref, ok := byHandle[handle]
		if !ok {
			return errors.New("integrated Workspace is no longer registered")
		}
		path := workspacePathForRef(repoRoot, cfg.WorkspacesRoot, project, ref, currentHandle)
		if err := commandToStderrFn("jj", "-R", path, "workspace", "update-stale"); err != nil {
			return err
		}
	}
	return nil
}

func revalidatePreparedIntegrationForEffect(repoRoot string, cfg config, project string, target integrationTargetResolution, record integrationOperationRecord, request integrationRequestV1) error {
	current, err := currentOperationFullID(target.Path)
	if err != nil || current != record.BeforeOperationID {
		return newIntegrationProtocolError(integrationErrorUnknownEffect, "repository operation changed before integration effects")
	}
	if err := materializeIntegrationWorkspaceHeads(repoRoot, cfg, project, target.Handle, request); err != nil {
		return newIntegrationProtocolError(integrationErrorUnknownEffect, "workspace state changed before integration effects")
	}
	prepared, err := validateIntegrationAssertions(repoRoot, request)
	if err != nil || !integrationPreparedStatesEqual(prepared, record.PreparedState) {
		return newIntegrationProtocolError(integrationErrorUnknownEffect, "prepared target or payload state changed before integration effects")
	}
	after, err := currentOperationFullID(target.Path)
	if err != nil || after != record.BeforeOperationID {
		return newIntegrationProtocolError(integrationErrorUnknownEffect, "repository operation changed during integration revalidation")
	}
	return nil
}

func preparedChangesReachableFromTargetAtOperation(repoPath, operationID, targetHandle string, prepared integrationPreparedStateV1) (bool, error) {
	for _, payload := range prepared.Payloads {
		for _, change := range payload.Changes {
			commits, err := integrationCommitIDsAtOperation(repoPath, operationID, fmt.Sprintf("(change_id(%s) & ::%s@)", change.ChangeID, targetHandle))
			if err != nil {
				return false, err
			}
			if len(commits) != 1 {
				return false, nil
			}
		}
	}
	return true, nil
}

func preparedChangesReachableFromTarget(repoPath, targetHandle string, prepared integrationPreparedStateV1) (bool, error) {
	for _, payload := range prepared.Payloads {
		for _, change := range payload.Changes {
			commits, err := integrationCommitIDs(repoPath, fmt.Sprintf("(change_id(%s) & ::%s@)", change.ChangeID, targetHandle))
			if err != nil {
				return false, err
			}
			if len(commits) != 1 {
				return false, nil
			}
		}
	}
	return true, nil
}

func proveIntegrationPayloadMappingsAtOperation(repoPath, operationID, targetHandle string, prepared integrationPreparedStateV1) ([][]integrationReceiptChangeV1, error) {
	mappings := make([][]integrationReceiptChangeV1, len(prepared.Payloads))
	for i, payload := range prepared.Payloads {
		if len(payload.Changes) == 0 {
			return nil, errors.New("prepared payload has no exact change evidence")
		}
		for _, change := range payload.Changes {
			commits, err := integrationCommitIDsAtOperation(repoPath, operationID, fmt.Sprintf("(change_id(%s) & ::%s@)", change.ChangeID, targetHandle))
			if err != nil || len(commits) != 1 {
				return nil, errors.New("could not prove one landed payload change")
			}
			mappings[i] = append(mappings[i], integrationReceiptChangeV1{ChangeID: change.ChangeID, InputCommit: change.CommitID, LandedCommit: commits[0]})
		}
	}
	return mappings, nil
}

func integrationPayloadMappingsEqual(left, right [][]integrationReceiptChangeV1) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if len(left[i]) != len(right[i]) {
			return false
		}
		for j := range left[i] {
			if left[i][j] != right[i][j] {
				return false
			}
		}
	}
	return true
}

func proveIntegrationPayloadMappings(repoPath, targetHandle string, prepared integrationPreparedStateV1) ([][]integrationReceiptChangeV1, error) {
	mappings := make([][]integrationReceiptChangeV1, len(prepared.Payloads))
	for i, payload := range prepared.Payloads {
		if len(payload.Changes) == 0 {
			return nil, errors.New("prepared payload has no exact change evidence")
		}
		for _, change := range payload.Changes {
			commits, err := integrationCommitIDs(repoPath, fmt.Sprintf("(change_id(%s) & ::%s@)", change.ChangeID, targetHandle))
			if err != nil || len(commits) != 1 {
				return nil, errors.New("could not prove one landed payload change")
			}
			mappings[i] = append(mappings[i], integrationReceiptChangeV1{ChangeID: change.ChangeID, InputCommit: change.CommitID, LandedCommit: commits[0]})
		}
	}
	return mappings, nil
}

func verifyIntegrationTerminalGraph(repoPath, targetHandle string, record integrationOperationRecord) error {
	if record.TargetAdvancedState == nil || record.GraphOperationID == "" {
		return newIntegrationProtocolError(integrationErrorUnknownEffect, "terminal integration graph evidence is incomplete")
	}
	operationID, err := currentOperationFullID(repoPath)
	if err != nil || operationID != record.GraphOperationID || record.CommitPointOperation != record.GraphOperationID {
		return newIntegrationProtocolError(integrationErrorUnknownEffect, "repository operation changed before terminal integration receipt")
	}
	afterHead, err := integrationWorkspaceHeadCommit(repoPath, targetHandle)
	if err != nil || afterHead != record.TargetAdvancedState.AfterHeadCommit {
		return newIntegrationProtocolError(integrationErrorUnknownEffect, "target Workspace changed before terminal integration receipt")
	}
	landed, err := preparedChangesReachableFromTarget(repoPath, targetHandle, record.PreparedState)
	if err != nil || !landed {
		return newIntegrationProtocolError(integrationErrorUnknownEffect, "payload ancestry changed before terminal integration receipt")
	}
	return nil
}

func completeSuccessfulIntegration(binding integrationStateBinding, record integrationOperationRecord, request integrationRequestV1, mappings [][]integrationReceiptChangeV1) error {
	if record.TargetAdvancedState == nil || record.CommitPointOperation == "" || record.CommitPointOperation != record.GraphOperationID {
		return errors.New("integration success has no target advancement state")
	}
	if len(mappings) != len(record.PreparedState.Payloads) {
		return errors.New("integration success mapping count is inconsistent")
	}
	receipt := integrationReceiptV1{
		Schema: integrationReceiptSchemaV1, OperationID: record.OperationID, RequestDigest: record.RequestDigest,
		Strategy: request.Strategy, BatchDisposition: integrationBatchSucceeded,
		Target:       integrationReceiptTargetV1{Workspace: request.Target.ExpectedWorkspace, BeforeHeadCommit: request.Target.ExpectedHeadCommit, IntegratedTipCommit: record.TargetAdvancedState.IntegratedTipCommit, AfterHeadCommit: record.TargetAdvancedState.AfterHeadCommit},
		Payloads:     make([]integrationReceiptPayloadV1, 0, len(request.Payloads)),
		JJOperations: integrationJJOperationsV1{BeforeEffect: record.BeforeOperationID, CommitPoint: record.CommitPointOperation},
	}
	for i, payload := range request.Payloads {
		if len(mappings[i]) != len(record.PreparedState.Payloads[i].Changes) {
			return errors.New("integration success payload mapping is incomplete")
		}
		payloadReceipt := integrationReceiptPayloadV1{Workspace: payload.Workspace, InputHeadCommit: payload.ExpectedHeadCommit, Disposition: integrationPayloadLanded, Changes: mappings[i]}
		payloadReceipt.EvidenceDigest = integrationPayloadReceiptEvidenceDigest(payloadReceipt)
		receipt.Payloads = append(receipt.Payloads, payloadReceipt)
	}
	receipt.EvidenceDigest = integrationReceiptEvidenceDigest(receipt)
	record.Phase = integrationPhaseTerminal
	record.PublishPending = false
	record.Receipt = &receipt
	if err := writeIntegrationOperationRecordAtomic(binding.StateDir, record); err != nil {
		return emitIntegrationFailure(record.OperationID, record.RequestDigest, request, err, integrationBatchUnknownEffect, integrationNextActionOperatorReview)
	}
	return writeIntegrationJSON(stdoutWriter, receipt)
}

func integrationFailureCode(err error) string {
	var protocolErr *integrationProtocolError
	if errors.As(err, &protocolErr) && protocolErr.Code == integrationErrorConflict {
		return integrationErrorConflict
	}
	return integrationErrorInternal
}

func proveAndCompleteNoEffect(repoRoot string, cfg config, project string, target integrationTargetResolution, binding integrationStateBinding, record integrationOperationRecord, request integrationRequestV1, errorCode string) error {
	prove := func() error {
		current, err := currentOperationFullID(target.Path)
		if err != nil || current != record.BeforeOperationID {
			return errors.New("live repository operation no longer proves no effect")
		}
		view, err := integrationRepositoryViewAtOperation(target.Path, current, request.Target.ExpectedWorkspace)
		if err != nil || !integrationRepositoryViewsEqual(view, record.BeforeRepositoryView) {
			return errors.New("live repository view no longer proves no effect")
		}
		if err := materializeIntegrationWorkspaceHeads(repoRoot, cfg, project, target.Handle, request); err != nil {
			return err
		}
		prepared, err := validateIntegrationAssertions(repoRoot, request)
		if err != nil || !integrationPreparedStatesEqual(prepared, record.PreparedState) {
			return errors.New("live prepared state no longer proves no effect")
		}
		return nil
	}
	if err := prove(); err != nil {
		return emitIntegrationFailure(record.OperationID, record.RequestDigest, request, newIntegrationProtocolError(integrationErrorUnknownEffect, "could not prove that integration had no effect"), integrationBatchUnknownEffect, integrationNextActionOperatorReview)
	}
	if err := integrationNoEffectProofHook("between-proofs"); err != nil {
		return emitIntegrationFailure(record.OperationID, record.RequestDigest, request, newIntegrationProtocolError(integrationErrorUnknownEffect, "repository changed while proving that integration had no effect"), integrationBatchUnknownEffect, integrationNextActionOperatorReview)
	}
	if err := prove(); err != nil {
		return emitIntegrationFailure(record.OperationID, record.RequestDigest, request, newIntegrationProtocolError(integrationErrorUnknownEffect, "repository changed while proving that integration had no effect"), integrationBatchUnknownEffect, integrationNextActionOperatorReview)
	}
	if err := integrationNoEffectProofHook("before-terminal-record"); err != nil {
		return emitIntegrationFailure(record.OperationID, record.RequestDigest, request, newIntegrationProtocolError(integrationErrorUnknownEffect, "repository changed before recording that integration had no effect"), integrationBatchUnknownEffect, integrationNextActionOperatorReview)
	}
	current, err := currentOperationFullID(target.Path)
	if err != nil || current != record.BeforeOperationID {
		return emitIntegrationFailure(record.OperationID, record.RequestDigest, request, newIntegrationProtocolError(integrationErrorUnknownEffect, "repository changed before recording that integration had no effect"), integrationBatchUnknownEffect, integrationNextActionOperatorReview)
	}
	return completeFailedIntegration(target.Path, binding, record, request, errorCode)
}

func completeFailedIntegration(repoPath string, binding integrationStateBinding, record integrationOperationRecord, request integrationRequestV1, errorCode string) error {
	message := integrationPublicErrorSummaries[errorCode]
	receipt := integrationReceiptV1{
		Schema: integrationReceiptSchemaV1, OperationID: record.OperationID, RequestDigest: record.RequestDigest,
		Strategy: request.Strategy, BatchDisposition: integrationBatchFailed,
		Target:   integrationReceiptTargetV1{Workspace: request.Target.ExpectedWorkspace, BeforeHeadCommit: request.Target.ExpectedHeadCommit},
		Payloads: make([]integrationReceiptPayloadV1, 0, len(request.Payloads)), JJOperations: integrationJJOperationsV1{BeforeEffect: record.BeforeOperationID},
		Error: &integrationReceiptErrorV1{Code: errorCode, Message: message, NextAction: integrationNextActionRetryNewOperation},
	}
	for i, payload := range request.Payloads {
		changes := []integrationReceiptChangeV1{}
		for _, change := range record.PreparedState.Payloads[i].Changes {
			changes = append(changes, integrationReceiptChangeV1{ChangeID: change.ChangeID, InputCommit: change.CommitID})
		}
		payloadReceipt := integrationReceiptPayloadV1{Workspace: payload.Workspace, InputHeadCommit: payload.ExpectedHeadCommit, Disposition: integrationPayloadProvedNotLanded, Changes: changes}
		payloadReceipt.EvidenceDigest = integrationPayloadReceiptEvidenceDigest(payloadReceipt)
		receipt.Payloads = append(receipt.Payloads, payloadReceipt)
	}
	receipt.EvidenceDigest = integrationReceiptEvidenceDigest(receipt)
	// This final full operation read is the failed/proved-not-landed receipt's
	// linearization point. A direct jj operation after it is a later event;
	// provider state and Ajj's journal cannot form one atomic transaction.
	current, err := currentOperationFullID(repoPath)
	if err != nil || current != record.BeforeOperationID {
		return emitIntegrationFailure(record.OperationID, record.RequestDigest, request, newIntegrationProtocolError(integrationErrorUnknownEffect, "repository changed before persisting that integration had no effect"), integrationBatchUnknownEffect, integrationNextActionOperatorReview)
	}
	record.Phase = integrationPhaseTerminal
	record.PublishPending = false
	record.CommitPointOperation = ""
	record.TargetAdvancedState = nil
	record.Receipt = &receipt
	if err := writeIntegrationOperationRecordAtomic(binding.StateDir, record); err != nil {
		return emitIntegrationFailure(record.OperationID, record.RequestDigest, request, err, integrationBatchUnknownEffect, integrationNextActionOperatorReview)
	}
	if err := writeIntegrationJSON(stdoutWriter, receipt); err != nil {
		return err
	}
	return &integrationCommandFailure{message: errorCode + ": " + message}
}

func recoverIntegrationOperation(repoRoot string, cfg config, project string, target integrationTargetResolution, binding integrationStateBinding, record integrationOperationRecord, request integrationRequestV1) error {
	current, err := currentOperationFullID(target.Path)
	if err != nil {
		return emitIntegrationFailure(record.OperationID, record.RequestDigest, request, err, integrationBatchUnknownEffect, integrationNextActionOperatorReview)
	}
	if record.Phase == integrationPhasePrepared {
		return proveAndCompleteNoEffect(repoRoot, cfg, project, target, binding, record, request, integrationErrorOperationInterrupted)
	}
	if record.Phase == integrationPhaseGraphRewritten {
		if current == record.BeforeOperationID {
			// Detached operations are not live effects. Recovery deliberately refuses
			// to publish journaled prepublication work without fresh in-memory authority.
			return proveAndCompleteNoEffect(repoRoot, cfg, project, target, binding, record, request, integrationErrorOperationInterrupted)
		}
		if current == record.GraphOperationID && record.PublishPending {
			if err := proveDetachedIntegrationTransaction(target.Path, record, request); err != nil {
				return emitIntegrationFailure(record.OperationID, record.RequestDigest, request, newIntegrationProtocolError(integrationErrorUnknownEffect, "published detached transaction evidence is invalid"), integrationBatchUnknownEffect, integrationNextActionOperatorReview)
			}
			record.PublishPending = false
			record.Phase = integrationPhaseTargetAdvanced
			record.CommitPointOperation = record.GraphOperationID
			state := *record.StagedTargetState
			record.TargetAdvancedState = &state
			if err := writeIntegrationOperationRecordAtomic(binding.StateDir, record); err != nil {
				return emitIntegrationFailure(record.OperationID, record.RequestDigest, request, err, integrationBatchUnknownEffect, integrationNextActionOperatorReview)
			}
		} else {
			return emitIntegrationFailure(record.OperationID, record.RequestDigest, request, newIntegrationProtocolError(integrationErrorUnknownEffect, "repository operation contradicts detached publish evidence"), integrationBatchUnknownEffect, integrationNextActionOperatorReview)
		}
	}
	if record.Phase != integrationPhaseTargetAdvanced && record.Phase != integrationPhaseCursorsReconciled {
		return emitIntegrationFailure(record.OperationID, record.RequestDigest, request, newIntegrationProtocolError(integrationErrorUnknownEffect, "operation phase cannot be recovered"), integrationBatchUnknownEffect, integrationNextActionOperatorReview)
	}
	return finishPublishedIntegration(repoRoot, cfg, project, target, binding, record, request)
}

func integrationRequestPayloadHandles(request integrationRequestV1) []string {
	inputs := make([]string, 0, len(request.Payloads))
	for _, payload := range request.Payloads {
		inputs = append(inputs, payload.Workspace)
	}
	return inputs
}
