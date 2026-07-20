package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const (
	// A record stores request bytes as base64 plus an optional receipt. Four
	// output envelopes leaves room for base64 growth and indented journal JSON
	// at every advertised request/receipt/count limit.
	integrationMaxRequestBytes       = 64 * 1024
	integrationMaxOutputBytes        = 1024 * 1024
	integrationMaxRecordBytes        = 4 * integrationMaxOutputBytes
	integrationMaxPayloads           = 256
	integrationMaxChangesPerPayload  = 8192
	integrationMaxReceiptChanges     = 8192
	integrationMaxDetachedOperations = 2*integrationMaxPayloads + 16
	integrationMaxErrorBytes         = 512
)

type integrationCommandFailure struct {
	message string
}

func (e *integrationCommandFailure) Error() string { return e.message }

type integrationLock struct {
	file *os.File
}

func (l *integrationLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	closeErr := l.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

type integrationPreparedTargetV1 struct {
	Workspace       string   `json:"workspace"`
	HeadCommit      string   `json:"headCommit"`
	FrontierCommits []string `json:"frontierCommits,omitempty"`
}

type integrationPreparedChangeV1 struct {
	ChangeID string `json:"changeId"`
	CommitID string `json:"commitId"`
}

type integrationPreparedPayloadV1 struct {
	Workspace       string                        `json:"workspace"`
	HeadCommit      string                        `json:"headCommit"`
	Changes         []integrationPreparedChangeV1 `json:"changes,omitempty"`
	FrontierCommits []string                      `json:"frontierCommits,omitempty"`
}

type integrationTargetAdvancedStateV1 struct {
	IntegratedTipCommit string `json:"integratedTipCommit"`
	AfterHeadCommit     string `json:"afterHeadCommit"`
}

var (
	integrationFullOperationIDRE = regexp.MustCompile(`^[0-9a-f]{128}$`)
	integrationFullChangeIDRE    = regexp.MustCompile(`^[a-z]{32}$`)
	integrationDigestRE          = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type integrationPreparedStateV1 struct {
	Target   integrationPreparedTargetV1    `json:"target"`
	Payloads []integrationPreparedPayloadV1 `json:"payloads"`
}

func runCapabilities(args []string) error {
	fs := flag.NewFlagSet("capabilities", flag.ContinueOnError)
	var jsonOutput bool
	var schema string
	fs.BoolVar(&jsonOutput, "json", false, "write the bounded machine-readable capability object")
	fs.StringVar(&schema, "schema", integrationCapabilitiesSchemaV1, "capability schema to emit")
	if handled, err := parseCommandFlags(fs, args, "ajj capabilities --json [--schema ajj-capabilities-v1|ajj-capabilities-v2]", "Report Ajj machine protocol capabilities."); handled || err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		return errors.New("capabilities does not accept positional arguments")
	}
	if !jsonOutput {
		return errors.New("capabilities requires --json")
	}
	switch strings.TrimSpace(schema) {
	case integrationCapabilitiesSchemaV1:
		return writeIntegrationJSON(stdoutWriter, integrationCapabilities())
	case ajjCapabilitiesSchemaV2:
		return writeIntegrationJSON(stdoutWriter, capabilitiesV2())
	default:
		return fmt.Errorf("unsupported capabilities schema %q", schema)
	}
}

func runIntegrate(args []string) error {
	fs := flag.NewFlagSet("integrate", flag.ContinueOnError)
	var repoRootOverride, requestSource, recoverOperationID string
	var jsonOutput bool
	fs.StringVar(&repoRootOverride, "repo", "", "Current Workspace root override")
	fs.StringVar(&requestSource, "request-json", "", "read one integration request from PATH or - for stdin")
	fs.StringVar(&recoverOperationID, "recover", "", "inspect an existing operation by id without starting effects")
	fs.BoolVar(&jsonOutput, "json", false, "write one bounded machine-readable result")
	if handled, err := parseCommandFlags(fs, args, "ajj integrate (--request-json PATH|- | --recover OPERATION-ID --json) [options]", "Prepare or recover an exact Current-Workspace integration operation. Integration effects are enabled by strategy implementation slices."); handled || err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		return errors.New("integrate does not accept positional arguments in machine mode")
	}
	requestSource = strings.TrimSpace(requestSource)
	recoverOperationID = strings.TrimSpace(recoverOperationID)
	if (requestSource == "") == (recoverOperationID == "") {
		return errors.New("provide exactly one of --request-json or --recover")
	}
	if recoverOperationID != "" {
		if !jsonOutput {
			return errors.New("integrate --recover requires --json")
		}
		if !integrationOperationIDRE.MatchString(recoverOperationID) {
			return errors.New("recover operation id must match " + integrationOperationIDRE.String())
		}
		return runIntegrationRecovery(repoRootOverride, recoverOperationID)
	}

	data, err := readIntegrationRequestSource(requestSource)
	if err != nil {
		return err
	}
	request, digest, err := parseIntegrationRequestV1(data)
	if err != nil {
		operationID, identified := identifyIntegrationOperationID(data)
		if !identified {
			return err
		}
		digest = integrationRequestDigest(data)
		return emitIntegrationFailure(operationID, digest, integrationRequestV1{}, err, integrationBatchFailed, integrationNextActionRetryNewOperation)
	}
	return prepareIntegrationOperation(repoRootOverride, data, digest, request)
}

func readIntegrationRequestSource(source string) ([]byte, error) {
	var reader io.Reader
	var closer io.Closer
	if source == "-" {
		reader = stdinReader
	} else {
		file, err := os.Open(expandPath(source))
		if err != nil {
			return nil, fmt.Errorf("open integration request: %w", err)
		}
		reader = file
		closer = file
	}
	if closer != nil {
		defer closer.Close()
	}
	limited := io.LimitReader(reader, integrationMaxRequestBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read integration request: %w", err)
	}
	if len(data) > integrationMaxRequestBytes {
		return nil, fmt.Errorf("integration request exceeds %d bytes", integrationMaxRequestBytes)
	}
	return data, nil
}

func identifyIntegrationOperationID(data []byte) (string, bool) {
	if err := validateSingleJSONValueWithoutDuplicateKeys(data); err != nil {
		return "", false
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return "", false
	}
	raw, ok := object["operationId"]
	if !ok {
		return "", false
	}
	var operationID string
	if err := json.Unmarshal(raw, &operationID); err != nil || !integrationOperationIDRE.MatchString(operationID) {
		return "", false
	}
	return operationID, true
}

func integrationRequestDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func prepareIntegrationOperation(repoRootOverride string, requestBytes []byte, requestDigest string, request integrationRequestV1) error {
	repoRoot, cfg, project, err := commandContext(repoRootOverride, "", "")
	if err != nil {
		return emitIntegrationFailure(request.OperationID, requestDigest, request, err, integrationBatchFailed, integrationNextActionRetryNewOperation)
	}
	refs, err := listWorkspaceRefs(repoRoot)
	if err != nil {
		return emitIntegrationFailure(request.OperationID, requestDigest, request, err, integrationBatchFailed, integrationNextActionRetryNewOperation)
	}
	// Invocation context is routing authority. Resolve it before applying the
	// request's target assertion so an existing operation id can be checked first.
	target, err := resolveCurrentIntegrationWorkspace(repoRoot, refs)
	if err != nil {
		return emitIntegrationFailure(request.OperationID, requestDigest, request, err, integrationBatchFailed, integrationNextActionRetryNewOperation)
	}
	mainPath, err := integrationMainWorkspacePath(repoRoot, cfg, project, refs, target.Handle)
	if err != nil {
		return emitIntegrationFailure(request.OperationID, requestDigest, request, err, integrationBatchFailed, integrationNextActionRetryNewOperation)
	}
	binding, err := integrationStatePaths(mainPath, target.Path)
	if err != nil {
		return emitIntegrationFailure(request.OperationID, requestDigest, request, err, integrationBatchFailed, integrationNextActionRetryNewOperation)
	}
	lock, err := acquireIntegrationLock(binding.StateDir)
	if err != nil {
		return emitIntegrationFailure(request.OperationID, requestDigest, request, err, integrationBatchUnknownEffect, integrationNextActionRecover)
	}
	defer lock.Close()
	lockedTarget, err := revalidateIntegrationBinding(repoRoot, cfg, project, target.Handle, binding)
	if err != nil {
		return emitIntegrationFailure(request.OperationID, requestDigest, request, err, integrationBatchFailed, integrationNextActionRetryNewOperation)
	}
	target = lockedTarget

	record, found, err := loadIntegrationOperationRecord(binding.StateDir, request.OperationID)
	if err != nil {
		return emitIntegrationFailure(request.OperationID, requestDigest, request, err, integrationBatchUnknownEffect, integrationNextActionOperatorReview)
	}
	if found {
		// Digest contradiction is deterministic and precedes all assertions from
		// the new request, including a different asserted target.
		if err := validateIntegrationOperationReuse(record, requestDigest); err != nil {
			return emitIntegrationFailure(request.OperationID, requestDigest, request, err, integrationBatchFailed, integrationNextActionOperatorReview)
		}
		if err := validateIntegrationOperationContext(record, binding); err != nil {
			return emitIntegrationFailure(request.OperationID, requestDigest, request, err, integrationBatchUnknownEffect, integrationNextActionOperatorReview)
		}
		storedRequest := integrationRequestFromRecord(record)
		if record.Receipt != nil && record.Phase == integrationPhaseTerminal {
			if record.Receipt.BatchDisposition == integrationBatchSucceeded {
				if err := proveTerminalIntegrationRecord(target.Path, record, storedRequest); err != nil {
					return emitIntegrationFailure(record.OperationID, record.RequestDigest, storedRequest, newIntegrationProtocolError(integrationErrorUnknownEffect, "terminal integration evidence cannot be reproduced"), integrationBatchUnknownEffect, integrationNextActionOperatorReview)
				}
			}
			return writeIntegrationJSON(stdoutWriter, record.Receipt)
		}
		if err := revalidateNonterminalIntegrationRecord(repoRoot, cfg, project, target.Handle, record, storedRequest); err != nil {
			return emitIntegrationFailure(record.OperationID, record.RequestDigest, storedRequest, err, integrationBatchUnknownEffect, integrationNextActionOperatorReview)
		}
		return emitIntegrationFailure(record.OperationID, record.RequestDigest, storedRequest, newIntegrationProtocolError(integrationErrorOperationInterrupted, "operation is nonterminal and requires recovery"), integrationBatchUnknownEffect, integrationNextActionRecover)
	}

	if target.Handle != request.Target.ExpectedWorkspace {
		return emitIntegrationFailure(request.OperationID, requestDigest, request, newIntegrationProtocolError(integrationErrorAssertionFailed, "Current Workspace does not match the request target assertion"), integrationBatchFailed, integrationNextActionRetryNewOperation)
	}
	if err := materializeIntegrationWorkspaceHeads(repoRoot, cfg, project, target.Handle, request); err != nil {
		return emitIntegrationFailure(request.OperationID, requestDigest, request, err, integrationBatchFailed, integrationNextActionRetryNewOperation)
	}
	beforeOperationID, err := currentOperationFullID(target.Path)
	if err != nil {
		return emitIntegrationFailure(request.OperationID, requestDigest, request, err, integrationBatchFailed, integrationNextActionRetryNewOperation)
	}
	prepared, err := validateIntegrationAssertions(repoRoot, request)
	if err != nil {
		return emitIntegrationFailure(request.OperationID, requestDigest, request, err, integrationBatchFailed, integrationNextActionRetryNewOperation)
	}
	beforeRepositoryView, err := integrationRepositoryViewAtOperation(target.Path, beforeOperationID, request.Target.ExpectedWorkspace)
	if err != nil {
		return emitIntegrationFailure(request.OperationID, requestDigest, request, err, integrationBatchFailed, integrationNextActionRetryNewOperation)
	}
	afterValidationOperationID, err := currentOperationFullID(target.Path)
	if err != nil {
		return emitIntegrationFailure(request.OperationID, requestDigest, request, err, integrationBatchFailed, integrationNextActionRetryNewOperation)
	}
	if err := validateIntegrationOperationUnchanged(beforeOperationID, afterValidationOperationID); err != nil {
		return emitIntegrationFailure(request.OperationID, requestDigest, request, err, integrationBatchFailed, integrationNextActionRetryNewOperation)
	}

	record = integrationOperationRecord{
		Schema:               integrationOperationRecordV1,
		OperationID:          request.OperationID,
		RequestDigest:        requestDigest,
		RequestBytes:         append([]byte(nil), requestBytes...),
		CanonicalProjectPath: binding.CanonicalProjectPath,
		CanonicalTargetPath:  binding.CanonicalTargetPath,
		Phase:                integrationPhasePrepared,
		BeforeOperationID:    beforeOperationID,
		PreparedState:        prepared,
		BeforeRepositoryView: beforeRepositoryView,
	}
	if err := writeIntegrationOperationRecordAtomic(binding.StateDir, record); err != nil {
		return emitIntegrationFailure(request.OperationID, requestDigest, request, err, integrationBatchUnknownEffect, integrationNextActionOperatorReview)
	}
	if err := integrationEffectPhaseHook(integrationPhasePrepared); err != nil {
		return emitIntegrationFailure(record.OperationID, record.RequestDigest, request, err, integrationBatchUnknownEffect, integrationNextActionRecover)
	}

	return executePreparedIntegrationOperation(repoRoot, cfg, project, target, binding, record, request)
}

func runIntegrationRecovery(repoRootOverride, operationID string) error {
	repoRoot, cfg, project, err := commandContext(repoRootOverride, "", "")
	if err != nil {
		return emitIntegrationFailure(operationID, "", integrationRequestV1{}, err, integrationBatchUnknownEffect, integrationNextActionOperatorReview)
	}
	refs, err := listWorkspaceRefs(repoRoot)
	if err != nil {
		return emitIntegrationFailure(operationID, "", integrationRequestV1{}, err, integrationBatchUnknownEffect, integrationNextActionOperatorReview)
	}
	target, err := resolveCurrentIntegrationWorkspace(repoRoot, refs)
	if err != nil {
		return emitIntegrationFailure(operationID, "", integrationRequestV1{}, err, integrationBatchUnknownEffect, integrationNextActionOperatorReview)
	}
	mainPath, err := integrationMainWorkspacePath(repoRoot, cfg, project, refs, target.Handle)
	if err != nil {
		return emitIntegrationFailure(operationID, "", integrationRequestV1{}, err, integrationBatchUnknownEffect, integrationNextActionOperatorReview)
	}
	binding, err := integrationStatePaths(mainPath, target.Path)
	if err != nil {
		return emitIntegrationFailure(operationID, "", integrationRequestV1{}, err, integrationBatchUnknownEffect, integrationNextActionOperatorReview)
	}
	lock, err := acquireIntegrationLock(binding.StateDir)
	if err != nil {
		return emitIntegrationFailure(operationID, "", integrationRequestV1{}, err, integrationBatchUnknownEffect, integrationNextActionRecover)
	}
	defer lock.Close()
	if _, err := revalidateIntegrationBinding(repoRoot, cfg, project, target.Handle, binding); err != nil {
		return emitIntegrationFailure(operationID, "", integrationRequestV1{}, err, integrationBatchUnknownEffect, integrationNextActionOperatorReview)
	}

	record, found, err := loadIntegrationOperationRecord(binding.StateDir, operationID)
	if err != nil {
		return emitIntegrationFailure(operationID, "", integrationRequestV1{}, err, integrationBatchUnknownEffect, integrationNextActionOperatorReview)
	}
	if !found {
		return emitIntegrationFailure(operationID, "", integrationRequestV1{}, newIntegrationProtocolError(integrationErrorInvalidRequest, "operation %q does not exist in the current Project", operationID), integrationBatchFailed, integrationNextActionNone)
	}
	if err := validateIntegrationOperationContext(record, binding); err != nil {
		return emitIntegrationFailure(operationID, record.RequestDigest, integrationRequestFromRecord(record), err, integrationBatchUnknownEffect, integrationNextActionOperatorReview)
	}
	request := integrationRequestFromRecord(record)
	if record.Receipt != nil && record.Phase == integrationPhaseTerminal {
		if record.Receipt.BatchDisposition == integrationBatchSucceeded {
			if err := proveTerminalIntegrationRecord(target.Path, record, request); err != nil {
				return emitIntegrationFailure(record.OperationID, record.RequestDigest, request, newIntegrationProtocolError(integrationErrorUnknownEffect, "terminal integration evidence cannot be reproduced"), integrationBatchUnknownEffect, integrationNextActionOperatorReview)
			}
		}
		return writeIntegrationJSON(stdoutWriter, record.Receipt)
	}
	return recoverIntegrationOperation(repoRoot, cfg, project, target, binding, record, request)
}

func resolveCurrentIntegrationWorkspace(repoRoot string, refs []workspaceRef) (integrationTargetResolution, error) {
	canonicalRepoRoot, err := canonicalExistingDirectory(repoRoot)
	if err != nil {
		return integrationTargetResolution{}, newIntegrationProtocolError(integrationErrorTargetResolution, "resolve current workspace root: %v", err)
	}
	matches := make([]integrationTargetResolution, 0, 1)
	for _, ref := range refs {
		root := cleanWorkspaceRoot(ref.Root)
		if root == "" {
			continue
		}
		canonicalRoot, err := canonicalExistingDirectory(root)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return integrationTargetResolution{}, newIntegrationProtocolError(integrationErrorTargetResolution, "resolve Workspace %q root: %v", ref.Handle, err)
		}
		if canonicalRoot == canonicalRepoRoot {
			matches = append(matches, integrationTargetResolution{Handle: ref.Handle, Path: canonicalRoot})
		}
	}
	if len(matches) != 1 {
		return integrationTargetResolution{}, newIntegrationProtocolError(integrationErrorTargetResolution, "current path must match exactly one registered Workspace root (matched %d)", len(matches))
	}
	return matches[0], nil
}

func revalidateIntegrationBinding(repoRoot string, cfg config, project, expectedWorkspace string, binding integrationStateBinding) (integrationTargetResolution, error) {
	refs, err := listWorkspaceRefs(repoRoot)
	if err != nil {
		return integrationTargetResolution{}, err
	}
	target, err := resolveIntegrationTargetWorkspace(repoRoot, refs, expectedWorkspace)
	if err != nil {
		return integrationTargetResolution{}, err
	}
	mainPath, err := integrationMainWorkspacePath(repoRoot, cfg, project, refs, target.Handle)
	if err != nil {
		return integrationTargetResolution{}, err
	}
	lockedBinding, err := integrationStatePaths(mainPath, target.Path)
	if err != nil {
		return integrationTargetResolution{}, err
	}
	if lockedBinding.CanonicalProjectPath != binding.CanonicalProjectPath || lockedBinding.CanonicalTargetPath != binding.CanonicalTargetPath || lockedBinding.StateDir != binding.StateDir {
		return integrationTargetResolution{}, newIntegrationProtocolError(integrationErrorAssertionFailed, "Project or Current Workspace binding changed while acquiring the integration lock")
	}
	return target, nil
}

func integrationMainWorkspacePath(repoRoot string, cfg config, project string, refs []workspaceRef, currentHandle string) (string, error) {
	for _, ref := range refs {
		if ref.Handle != cfg.MainWorkspace {
			continue
		}
		path := workspacePathForRef(repoRoot, cfg.WorkspacesRoot, project, ref, currentHandle)
		if !workspacePathExists(path) {
			return "", fmt.Errorf("configured Main Workspace %q path missing: %s", cfg.MainWorkspace, path)
		}
		return path, nil
	}
	return "", fmt.Errorf("configured Main Workspace %q not found", cfg.MainWorkspace)
}

func validateIntegrationAssertions(repoPath string, request integrationRequestV1) (integrationPreparedStateV1, error) {
	targetCommit, err := integrationWorkspaceHeadCommit(repoPath, request.Target.ExpectedWorkspace)
	if err != nil {
		return integrationPreparedStateV1{}, err
	}
	if targetCommit != request.Target.ExpectedHeadCommit {
		return integrationPreparedStateV1{}, newIntegrationProtocolError(integrationErrorAssertionFailed, "target Workspace %q head changed", request.Target.ExpectedWorkspace)
	}
	targetEmpty, err := integrationRevisionMatches(repoPath, "", "empty() & "+request.Target.ExpectedWorkspace+"@")
	if err != nil {
		return integrationPreparedStateV1{}, err
	}
	if !targetEmpty {
		return integrationPreparedStateV1{}, newIntegrationProtocolError(integrationErrorAssertionFailed, "target Workspace %q head is not empty", request.Target.ExpectedWorkspace)
	}
	description, err := integrationQuery(repoPath, "", "log", "-r", request.Target.ExpectedWorkspace+"@", "--no-graph", "-T", "description")
	if err != nil {
		return integrationPreparedStateV1{}, err
	}
	if description != "" {
		return integrationPreparedStateV1{}, newIntegrationProtocolError(integrationErrorAssertionFailed, "target Workspace %q head is described", request.Target.ExpectedWorkspace)
	}
	targetConflict, err := integrationRevisionMatches(repoPath, "", "conflicts() & "+request.Target.ExpectedWorkspace+"@")
	if err != nil {
		return integrationPreparedStateV1{}, err
	}
	if targetConflict {
		return integrationPreparedStateV1{}, newIntegrationProtocolError(integrationErrorAssertionFailed, "target Workspace %q head is conflicted", request.Target.ExpectedWorkspace)
	}

	prepared := integrationPreparedStateV1{
		Target:   integrationPreparedTargetV1{Workspace: request.Target.ExpectedWorkspace, HeadCommit: targetCommit},
		Payloads: make([]integrationPreparedPayloadV1, 0, len(request.Payloads)),
	}
	if request.Strategy == integrationStrategyOrderedLine {
		prepared.Target.FrontierCommits, err = integrationCommitIDs(repoPath, "heads(::"+request.Target.ExpectedWorkspace+"@ & ~empty())")
		if err != nil {
			return integrationPreparedStateV1{}, err
		}
		if len(prepared.Target.FrontierCommits) == 0 || len(prepared.Target.FrontierCommits) > integrationMaxChangesPerPayload {
			return integrationPreparedStateV1{}, newIntegrationProtocolError(integrationErrorAssertionFailed, "ordered-line target has no bounded non-empty frontier")
		}
		sort.Strings(prepared.Target.FrontierCommits)
		for i := 1; i < len(prepared.Target.FrontierCommits); i++ {
			if prepared.Target.FrontierCommits[i-1] == prepared.Target.FrontierCommits[i] {
				return integrationPreparedStateV1{}, newIntegrationProtocolError(integrationErrorAssertionFailed, "ordered-line target frontier is not unique")
			}
		}
	}
	totalChanges := 0
	for _, payload := range request.Payloads {
		commit, err := integrationWorkspaceHeadCommit(repoPath, payload.Workspace)
		if err != nil {
			return integrationPreparedStateV1{}, err
		}
		if commit != payload.ExpectedHeadCommit {
			return integrationPreparedStateV1{}, newIntegrationProtocolError(integrationErrorAssertionFailed, "payload Workspace %q head changed", payload.Workspace)
		}
		conflicted, err := integrationRevisionMatches(repoPath, "", "conflicts() & "+payload.Workspace+"@")
		if err != nil {
			return integrationPreparedStateV1{}, err
		}
		if conflicted {
			return integrationPreparedStateV1{}, newIntegrationProtocolError(integrationErrorAssertionFailed, "payload Workspace %q head is conflicted", payload.Workspace)
		}
		payloadEmpty, err := integrationRevisionMatches(repoPath, "", "empty() & "+payload.Workspace+"@")
		if err != nil {
			return integrationPreparedStateV1{}, err
		}
		payloadDescription, err := integrationQuery(repoPath, "", "log", "-r", payload.Workspace+"@", "--no-graph", "-T", "description")
		if err != nil {
			return integrationPreparedStateV1{}, err
		}
		if !payloadEmpty && strings.TrimSpace(payloadDescription) == "" {
			return integrationPreparedStateV1{}, newIntegrationProtocolError(integrationErrorAssertionFailed, "payload Workspace %q has in-progress undescribed changes", payload.Workspace)
		}
		baseWorkspace := request.Target.ExpectedWorkspace
		if request.Strategy == integrationStrategyOrderedLine && len(prepared.Payloads) > 0 {
			baseWorkspace = prepared.Payloads[len(prepared.Payloads)-1].Workspace
		}
		changes, frontier, err := materializeIntegrationPayloadChanges(repoPath, baseWorkspace, payload.Workspace)
		if err != nil {
			return integrationPreparedStateV1{}, err
		}
		if len(changes) == 0 || len(frontier) == 0 {
			return integrationPreparedStateV1{}, newIntegrationProtocolError(integrationErrorAssertionFailed, "payload Workspace %q has no unique non-empty changes relative to its ordered base", payload.Workspace)
		}
		if request.Strategy == integrationStrategyOrderedLine && len(frontier) != 1 {
			return integrationPreparedStateV1{}, newIntegrationProtocolError(integrationErrorAssertionFailed, "ordered-line payload does not have one exact contribution tip")
		}
		if len(changes) > integrationMaxChangesPerPayload || len(frontier) > integrationMaxChangesPerPayload {
			return integrationPreparedStateV1{}, newIntegrationProtocolError(integrationErrorInvalidRequest, "payload change evidence exceeds the protocol limit")
		}
		totalChanges += len(changes)
		if totalChanges > integrationMaxReceiptChanges {
			return integrationPreparedStateV1{}, newIntegrationProtocolError(integrationErrorInvalidRequest, "integration change evidence exceeds the protocol limit")
		}
		prepared.Payloads = append(prepared.Payloads, integrationPreparedPayloadV1{Workspace: payload.Workspace, HeadCommit: commit, Changes: changes, FrontierCommits: frontier})
	}
	return prepared, nil
}

func materializeIntegrationWorkspaceHeads(repoPath string, cfg config, project, currentHandle string, request integrationRequestV1) error {
	refs, err := listWorkspaceRefs(repoPath)
	if err != nil {
		return err
	}
	byHandle := make(map[string]workspaceRef, len(refs))
	for _, ref := range refs {
		byHandle[ref.Handle] = ref
	}
	handles := make([]string, 0, len(request.Payloads)+1)
	handles = append(handles, request.Target.ExpectedWorkspace)
	for _, payload := range request.Payloads {
		handles = append(handles, payload.Workspace)
	}
	for _, handle := range handles {
		ref, ok := byHandle[handle]
		if !ok {
			return newIntegrationProtocolError(integrationErrorAssertionFailed, "requested Workspace is not registered")
		}
		path := workspacePathForRef(repoPath, cfg.WorkspacesRoot, project, ref, currentHandle)
		if !workspacePathExists(path) {
			return newIntegrationProtocolError(integrationErrorAssertionFailed, "requested Workspace path is missing")
		}
		// The exact graph head is the request binding. Do not snapshot or update a
		// sibling working copy here: either action may rewrite that head after the
		// caller captured it. Callers must materialize filesystem state before
		// constructing the request; Ajj revalidates the resulting exact graph refs.
	}
	return nil
}

func revalidateNonterminalIntegrationRecord(repoPath string, cfg config, project, currentHandle string, record integrationOperationRecord, request integrationRequestV1) error {
	if err := materializeIntegrationWorkspaceHeads(repoPath, cfg, project, currentHandle, request); err != nil {
		return newIntegrationProtocolError(integrationErrorUnknownEffect, "recorded workspace state could not be materialized")
	}
	currentOperationID, err := currentOperationFullID(repoPath)
	if err != nil {
		return newIntegrationProtocolError(integrationErrorUnknownEffect, "recorded repository state could not be revalidated")
	}
	if currentOperationID != record.BeforeOperationID {
		return newIntegrationProtocolError(integrationErrorUnknownEffect, "recorded repository operation no longer matches current state")
	}
	prepared, err := validateIntegrationAssertions(repoPath, request)
	if err != nil || !integrationPreparedStatesEqual(prepared, record.PreparedState) {
		return newIntegrationProtocolError(integrationErrorUnknownEffect, "recorded target or payload state no longer matches current state")
	}
	afterOperationID, err := currentOperationFullID(repoPath)
	if err != nil || afterOperationID != record.BeforeOperationID {
		return newIntegrationProtocolError(integrationErrorUnknownEffect, "repository operation changed during recovery validation")
	}
	return nil
}

func integrationPreparedStatesEqual(left, right integrationPreparedStateV1) bool {
	if left.Target.Workspace != right.Target.Workspace || left.Target.HeadCommit != right.Target.HeadCommit || !reflect.DeepEqual(left.Target.FrontierCommits, right.Target.FrontierCommits) || len(left.Payloads) != len(right.Payloads) {
		return false
	}
	for i := range left.Payloads {
		leftPayload, rightPayload := left.Payloads[i], right.Payloads[i]
		if leftPayload.Workspace != rightPayload.Workspace || leftPayload.HeadCommit != rightPayload.HeadCommit || len(leftPayload.Changes) != len(rightPayload.Changes) || len(leftPayload.FrontierCommits) != len(rightPayload.FrontierCommits) {
			return false
		}
		for j := range leftPayload.Changes {
			if leftPayload.Changes[j] != rightPayload.Changes[j] {
				return false
			}
		}
		for j := range leftPayload.FrontierCommits {
			if leftPayload.FrontierCommits[j] != rightPayload.FrontierCommits[j] {
				return false
			}
		}
	}
	return true
}

func validateIntegrationOperationUnchanged(beforeOperationID, afterOperationID string) error {
	if strings.TrimSpace(beforeOperationID) == "" || strings.TrimSpace(afterOperationID) == "" || beforeOperationID != afterOperationID {
		return newIntegrationProtocolError(integrationErrorAssertionFailed, "repository operation changed while validating integration assertions")
	}
	return nil
}

func currentOperationFullID(repoPath string) (string, error) {
	out, err := integrationQuery(repoPath, "@", "op", "log", "-n", "1", "--no-graph", "-T", "id ++ \"\\n\"")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(out)
	if !integrationFullOperationIDRE.MatchString(id) {
		return "", errors.New("current full operation id is invalid")
	}
	return id, nil
}

func integrationWorkspaceHeadCommit(repoPath, handle string) (string, error) {
	out, err := integrationQuery(repoPath, "", "log", "-r", handle+"@", "--no-graph", "-T", "commit_id ++ \"\\n\"")
	if err != nil {
		return "", err
	}
	commits := uniqueNonEmptyStrings(strings.Split(out, "\n"))
	if len(commits) != 1 || !integrationCommitIDRE.MatchString(commits[0]) {
		return "", newIntegrationProtocolError(integrationErrorAssertionFailed, "Workspace %q does not resolve to one exact head commit", handle)
	}
	return commits[0], nil
}

func acquireIntegrationLock(stateDir string) (*integrationLock, error) {
	if err := ensureIntegrationStateDirectory(stateDir); err != nil {
		return nil, err
	}
	path := filepath.Join(stateDir, "integration.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open integration lock: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, newIntegrationProtocolError(integrationErrorOperationInProgress, "another Ajj integration process holds the Project lock")
		}
		return nil, fmt.Errorf("lock integration state: %w", err)
	}
	return &integrationLock{file: file}, nil
}

func ensureIntegrationStateDirectory(stateDir string) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("create integration state directory: %w", err)
	}
	// Persist both directory entries: `.ajj/integrations` in `.ajj`, and `.ajj`
	// itself in the configured Main Workspace when first created.
	if err := syncIntegrationDirectory(filepath.Dir(stateDir)); err != nil {
		return fmt.Errorf("sync integration state parent: %w", err)
	}
	if err := syncIntegrationDirectory(filepath.Dir(filepath.Dir(stateDir))); err != nil {
		return fmt.Errorf("sync integration state grandparent: %w", err)
	}
	return nil
}

func syncIntegrationDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func integrationOperationRecordPath(stateDir, operationID string) (string, error) {
	if !integrationOperationIDRE.MatchString(operationID) {
		return "", newIntegrationProtocolError(integrationErrorInvalidRequest, "operation id must match %s", integrationOperationIDRE.String())
	}
	return filepath.Join(stateDir, operationID+".json"), nil
}

func loadIntegrationOperationRecord(stateDir, operationID string) (integrationOperationRecord, bool, error) {
	path, err := integrationOperationRecordPath(stateDir, operationID)
	if err != nil {
		return integrationOperationRecord{}, false, err
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return integrationOperationRecord{}, false, nil
		}
		return integrationOperationRecord{}, false, fmt.Errorf("read integration operation record: %w", err)
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil {
		return integrationOperationRecord{}, false, fmt.Errorf("stat integration operation record: %w", err)
	} else if info.Size() > integrationMaxRecordBytes {
		return integrationOperationRecord{}, false, errors.New("integration operation record exceeds the size limit")
	}
	data, err := io.ReadAll(io.LimitReader(file, integrationMaxRecordBytes+1))
	if err != nil {
		return integrationOperationRecord{}, false, fmt.Errorf("read integration operation record: %w", err)
	}
	if len(data) > integrationMaxRecordBytes {
		return integrationOperationRecord{}, false, errors.New("integration operation record exceeds the size limit")
	}
	if err := validateSingleJSONValueWithoutDuplicateKeys(data); err != nil {
		return integrationOperationRecord{}, false, fmt.Errorf("parse integration operation record: %w", err)
	}
	if err := validateIntegrationOperationRecordJSONKeys(data); err != nil {
		return integrationOperationRecord{}, false, fmt.Errorf("parse integration operation record: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record integrationOperationRecord
	if err := decoder.Decode(&record); err != nil {
		return integrationOperationRecord{}, false, fmt.Errorf("parse integration operation record: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return integrationOperationRecord{}, false, fmt.Errorf("parse integration operation record: %w", err)
	}
	if err := validateIntegrationOperationRecordSemantic(record, operationID); err != nil {
		return integrationOperationRecord{}, false, err
	}
	return record, true, nil
}

func validateIntegrationOperationRecordJSONKeys(data []byte) error {
	top, err := decodeExactJSONObject(data, "integration operation record", []string{
		"schema", "operationId", "requestDigest", "requestBytes", "canonicalProjectPath", "canonicalTargetPath",
		"phase", "beforeOperationId", "commitPointOperationId", "preparedState", "beforeRepositoryView", "graphOperationId", "detachedOperationIds", "publishPending", "stagedTargetState", "stagedPayloadMappings", "stagedRepositoryView", "targetAdvancedState", "receipt",
	})
	if err != nil {
		return err
	}
	if raw := top["preparedState"]; len(raw) > 0 {
		prepared, err := decodeExactJSONObject(raw, "preparedState", []string{"target", "payloads"})
		if err != nil {
			return err
		}
		if rawTarget := prepared["target"]; len(rawTarget) > 0 {
			if _, err := decodeExactJSONObject(rawTarget, "preparedState.target", []string{"workspace", "headCommit", "frontierCommits"}); err != nil {
				return err
			}
		}
		var preparedPayloads []json.RawMessage
		if rawPayloads := prepared["payloads"]; len(rawPayloads) > 0 {
			if err := json.Unmarshal(rawPayloads, &preparedPayloads); err != nil {
				return fmt.Errorf("preparedState.payloads must be an array: %w", err)
			}
		}
		for i, rawPayload := range preparedPayloads {
			payload, err := decodeExactJSONObject(rawPayload, fmt.Sprintf("preparedState.payloads[%d]", i), []string{"workspace", "headCommit", "changes", "frontierCommits"})
			if err != nil {
				return err
			}
			if err := validateIntegrationJSONObjectArray(payload["changes"], fmt.Sprintf("preparedState.payloads[%d].changes", i), []string{"changeId", "commitId"}); err != nil {
				return err
			}
		}
	}
	for _, field := range []string{"beforeRepositoryView", "stagedRepositoryView"} {
		if raw := top[field]; len(raw) > 0 && string(raw) != "null" {
			if err := validateIntegrationRepositoryViewJSON(raw, field); err != nil {
				return err
			}
		}
	}
	if raw := top["stagedPayloadMappings"]; len(raw) > 0 {
		var payloadMappings []json.RawMessage
		if err := json.Unmarshal(raw, &payloadMappings); err != nil {
			return fmt.Errorf("stagedPayloadMappings must be an array: %w", err)
		}
		for i, rawMappings := range payloadMappings {
			if err := validateIntegrationJSONObjectArray(rawMappings, fmt.Sprintf("stagedPayloadMappings[%d]", i), []string{"changeId", "inputCommit", "landedCommit"}); err != nil {
				return err
			}
		}
	}
	for _, field := range []string{"stagedTargetState", "targetAdvancedState"} {
		if raw := top[field]; len(raw) > 0 && string(raw) != "null" {
			if _, err := decodeExactJSONObject(raw, field, []string{"integratedTipCommit", "afterHeadCommit"}); err != nil {
				return err
			}
		}
	}
	if raw := top["receipt"]; len(raw) > 0 && string(raw) != "null" {
		receipt, err := decodeExactJSONObject(raw, "receipt", []string{
			"schema", "operationId", "requestDigest", "strategy", "batchDisposition", "target", "payloads", "jjOperations", "evidenceDigest", "error",
		})
		if err != nil {
			return err
		}
		if rawTarget := receipt["target"]; len(rawTarget) > 0 {
			if _, err := decodeExactJSONObject(rawTarget, "receipt.target", []string{"workspace", "beforeHeadCommit", "integratedTipCommit", "afterHeadCommit"}); err != nil {
				return err
			}
		}
		var payloads []json.RawMessage
		if rawPayloads := receipt["payloads"]; len(rawPayloads) > 0 {
			if err := json.Unmarshal(rawPayloads, &payloads); err != nil {
				return fmt.Errorf("receipt.payloads must be an array: %w", err)
			}
		}
		for i, rawPayload := range payloads {
			payload, err := decodeExactJSONObject(rawPayload, fmt.Sprintf("receipt.payloads[%d]", i), []string{"workspace", "inputHeadCommit", "disposition", "changes", "evidenceDigest"})
			if err != nil {
				return err
			}
			if err := validateIntegrationJSONObjectArray(payload["changes"], fmt.Sprintf("receipt.payloads[%d].changes", i), []string{"changeId", "inputCommit", "landedCommit"}); err != nil {
				return err
			}
		}
		if rawOperations := receipt["jjOperations"]; len(rawOperations) > 0 {
			if _, err := decodeExactJSONObject(rawOperations, "receipt.jjOperations", []string{"beforeEffect", "commitPoint"}); err != nil {
				return err
			}
		}
		if rawError := receipt["error"]; len(rawError) > 0 && string(rawError) != "null" {
			if _, err := decodeExactJSONObject(rawError, "receipt.error", []string{"code", "message", "nextAction"}); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateIntegrationRepositoryViewJSON(data []byte, label string) error {
	view, err := decodeExactJSONObject(data, label, []string{"workspaces", "visibleHeads", "bookmarks", "tags", "target"})
	if err != nil {
		return err
	}
	if err := validateIntegrationJSONObjectArray(view["workspaces"], label+".workspaces", []string{"workspace", "commitId", "changeId"}); err != nil {
		return err
	}
	for _, field := range []string{"bookmarks", "tags"} {
		if err := validateIntegrationJSONObjectArray(view[field], label+"."+field, []string{"name", "target"}); err != nil {
			return err
		}
	}
	if rawTarget := view["target"]; len(rawTarget) > 0 {
		if _, err := decodeExactJSONObject(rawTarget, label+".target", []string{"commitId", "changeId", "parentCommitIds"}); err != nil {
			return err
		}
	}
	return nil
}

func validateIntegrationJSONObjectArray(data []byte, label string, allowed []string) error {
	if len(data) == 0 {
		return nil
	}
	var values []json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil {
		return fmt.Errorf("%s must be an array: %w", label, err)
	}
	for i, value := range values {
		if _, err := decodeExactJSONObject(value, fmt.Sprintf("%s[%d]", label, i), allowed); err != nil {
			return err
		}
	}
	return nil
}

func validateIntegrationOperationRecordSemantic(record integrationOperationRecord, expectedOperationID string) error {
	if record.Schema != integrationOperationRecordV1 || record.OperationID != expectedOperationID || !integrationOperationIDRE.MatchString(record.OperationID) {
		return errors.New("integration operation record identity is invalid")
	}
	if len(record.RequestBytes) == 0 || len(record.RequestBytes) > integrationMaxRequestBytes {
		return errors.New("integration operation record request bytes exceed the size limit")
	}
	request, digest, err := parseIntegrationRequestV1(record.RequestBytes)
	if err != nil || digest != record.RequestDigest || request.OperationID != record.OperationID || !integrationDigestRE.MatchString(record.RequestDigest) {
		return errors.New("integration operation record request binding is invalid")
	}
	if !filepath.IsAbs(record.CanonicalProjectPath) || !filepath.IsAbs(record.CanonicalTargetPath) || filepath.Clean(record.CanonicalProjectPath) != record.CanonicalProjectPath || filepath.Clean(record.CanonicalTargetPath) != record.CanonicalTargetPath {
		return errors.New("integration operation record path binding is invalid")
	}
	if !integrationFullOperationIDRE.MatchString(record.BeforeOperationID) {
		return errors.New("integration operation record before-operation id is invalid")
	}
	if record.CommitPointOperation != "" && !integrationFullOperationIDRE.MatchString(record.CommitPointOperation) {
		return errors.New("integration operation record commit-point id is invalid")
	}
	if record.GraphOperationID != "" && !integrationFullOperationIDRE.MatchString(record.GraphOperationID) {
		return errors.New("integration operation record graph-operation id is invalid")
	}
	if len(record.DetachedOperationIDs) > integrationMaxDetachedOperations {
		return errors.New("integration operation record detached-operation chain is too long")
	}
	for _, operationID := range record.DetachedOperationIDs {
		if !integrationFullOperationIDRE.MatchString(operationID) {
			return errors.New("integration operation record detached-operation id is invalid")
		}
	}
	if len(record.DetachedOperationIDs) > 0 && record.GraphOperationID != record.DetachedOperationIDs[len(record.DetachedOperationIDs)-1] {
		return errors.New("integration operation record graph-operation chain is inconsistent")
	}
	for _, state := range []*integrationTargetAdvancedStateV1{record.StagedTargetState, record.TargetAdvancedState} {
		if state != nil && (!integrationCommitIDRE.MatchString(state.IntegratedTipCommit) || !integrationCommitIDRE.MatchString(state.AfterHeadCommit)) {
			return errors.New("integration operation record target state is invalid")
		}
	}
	if len(record.StagedPayloadMappings) > 0 {
		if len(record.StagedPayloadMappings) != len(request.Payloads) {
			return errors.New("integration operation record staged payload mapping count is invalid")
		}
		totalMappings := 0
		for _, mappings := range record.StagedPayloadMappings {
			totalMappings += len(mappings)
			if len(mappings) > integrationMaxChangesPerPayload || totalMappings > integrationMaxReceiptChanges {
				return errors.New("integration operation record staged payload mappings exceed the limit")
			}
			for _, mapping := range mappings {
				if !integrationFullChangeIDRE.MatchString(mapping.ChangeID) || !integrationCommitIDRE.MatchString(mapping.InputCommit) || !integrationCommitIDRE.MatchString(mapping.LandedCommit) {
					return errors.New("integration operation record staged payload mapping is invalid")
				}
			}
		}
	}
	if record.PreparedState.Target.Workspace != request.Target.ExpectedWorkspace || record.PreparedState.Target.HeadCommit != request.Target.ExpectedHeadCommit || len(record.PreparedState.Payloads) != len(request.Payloads) {
		return errors.New("integration operation record prepared target is inconsistent with its request")
	}
	targetFrontier := record.PreparedState.Target.FrontierCommits
	if len(targetFrontier) > integrationMaxChangesPerPayload {
		return errors.New("integration operation record prepared target frontier exceeds the limit")
	}
	for i, commit := range targetFrontier {
		if !integrationCommitIDRE.MatchString(commit) {
			return errors.New("integration operation record prepared target frontier is invalid")
		}
		if i > 0 && targetFrontier[i-1] >= commit {
			return errors.New("integration operation record prepared target frontier is not uniquely ordered")
		}
	}
	if request.Strategy == integrationStrategyOrderedLine {
		if len(targetFrontier) == 0 {
			return errors.New("ordered-line integration operation record target frontier is empty")
		}
	} else if len(targetFrontier) != 0 {
		return errors.New("non-ordered integration operation record has ordered-line target frontier evidence")
	}
	totalPreparedChanges := 0
	seenPreparedChanges := map[string]struct{}{}
	for i, payload := range request.Payloads {
		prepared := record.PreparedState.Payloads[i]
		if prepared.Workspace != payload.Workspace || prepared.HeadCommit != payload.ExpectedHeadCommit {
			return errors.New("integration operation record prepared payloads are inconsistent with its request")
		}
		if len(prepared.Changes) == 0 || len(prepared.FrontierCommits) == 0 {
			return errors.New("integration operation record executable payload evidence is empty")
		}
		if request.Strategy == integrationStrategyOrderedLine && len(prepared.FrontierCommits) != 1 {
			return errors.New("ordered-line integration operation record payload frontier is not singular")
		}
		if len(prepared.Changes) > integrationMaxChangesPerPayload || len(prepared.FrontierCommits) > integrationMaxChangesPerPayload {
			return errors.New("integration operation record prepared payload evidence exceeds the limit")
		}
		totalPreparedChanges += len(prepared.Changes)
		if totalPreparedChanges > integrationMaxReceiptChanges {
			return errors.New("integration operation record prepared change evidence exceeds the total limit")
		}
		for _, change := range prepared.Changes {
			if !integrationFullChangeIDRE.MatchString(change.ChangeID) || !integrationCommitIDRE.MatchString(change.CommitID) {
				return errors.New("integration operation record prepared change evidence is invalid")
			}
			if request.Strategy == integrationStrategyOrderedLine {
				if _, duplicate := seenPreparedChanges[change.ChangeID]; duplicate {
					return errors.New("ordered-line prepared change evidence overlaps payloads")
				}
				seenPreparedChanges[change.ChangeID] = struct{}{}
			}
		}
		for _, commit := range prepared.FrontierCommits {
			if !integrationCommitIDRE.MatchString(commit) {
				return errors.New("integration operation record prepared frontier is invalid")
			}
		}
	}
	if err := validatePreparedStateAgainstRepositoryView(record.PreparedState, record.BeforeRepositoryView); err != nil {
		return fmt.Errorf("integration operation record before-state evidence is invalid: %w", err)
	}
	if len(record.StagedRepositoryView.Workspaces) != 0 {
		if err := validateIntegrationRepositoryView(record.StagedRepositoryView); err != nil {
			return fmt.Errorf("integration operation record staged repository evidence is invalid: %w", err)
		}
	}
	switch record.Phase {
	case integrationPhasePrepared:
		if record.Receipt != nil || record.GraphOperationID != "" || len(record.DetachedOperationIDs) != 0 || record.PublishPending || record.StagedTargetState != nil || len(record.StagedPayloadMappings) != 0 || record.CommitPointOperation != "" || record.TargetAdvancedState != nil || len(record.StagedRepositoryView.Workspaces) != 0 {
			return errors.New("prepared integration operation record is inconsistent")
		}
	case integrationPhaseGraphRewritten:
		if record.Receipt != nil || record.GraphOperationID == "" || len(record.DetachedOperationIDs) == 0 || record.CommitPointOperation != "" || record.TargetAdvancedState != nil || (record.PublishPending && (record.StagedTargetState == nil || len(record.StagedPayloadMappings) != len(request.Payloads) || len(record.StagedRepositoryView.Workspaces) == 0)) {
			return errors.New("graph-rewritten integration operation record is inconsistent")
		}
	case integrationPhaseTargetAdvanced, integrationPhaseCursorsReconciled:
		if record.PublishPending || record.CommitPointOperation == "" || record.TargetAdvancedState == nil || record.StagedTargetState == nil || len(record.StagedPayloadMappings) != len(request.Payloads) || len(record.StagedRepositoryView.Workspaces) == 0 || *record.TargetAdvancedState != *record.StagedTargetState || record.Receipt != nil {
			return errors.New("post-commit nonterminal integration operation record is inconsistent")
		}
	case integrationPhaseTerminal:
		if record.Receipt == nil || record.PublishPending {
			return errors.New("terminal integration operation record is inconsistent")
		}
		if err := validateIntegrationTerminalReceipt(*record.Receipt, record, request); err != nil {
			return err
		}
	default:
		return errors.New("integration operation record phase is invalid")
	}
	return nil
}

func validateIntegrationTerminalReceipt(receipt integrationReceiptV1, record integrationOperationRecord, request integrationRequestV1) error {
	if receipt.Schema != integrationReceiptSchemaV1 || receipt.OperationID != record.OperationID || receipt.RequestDigest != record.RequestDigest || receipt.Strategy != request.Strategy {
		return errors.New("terminal integration receipt identity is invalid")
	}
	if receipt.Target.Workspace != request.Target.ExpectedWorkspace || receipt.Target.BeforeHeadCommit != request.Target.ExpectedHeadCommit {
		return errors.New("terminal integration receipt target is inconsistent with its request")
	}
	if len(receipt.Payloads) != len(request.Payloads) || len(receipt.Payloads) > integrationMaxPayloads || receipt.JJOperations.BeforeEffect != record.BeforeOperationID || receipt.JJOperations.CommitPoint != record.CommitPointOperation {
		return errors.New("terminal integration receipt pre-state is inconsistent with its record")
	}
	if receipt.Target.IntegratedTipCommit != "" && !integrationCommitIDRE.MatchString(receipt.Target.IntegratedTipCommit) {
		return errors.New("terminal integration receipt optional integrated-tip commit is invalid")
	}
	if receipt.Target.AfterHeadCommit != "" && !integrationCommitIDRE.MatchString(receipt.Target.AfterHeadCommit) {
		return errors.New("terminal integration receipt optional after-head commit is invalid")
	}
	totalChanges := 0
	for _, payload := range receipt.Payloads {
		if len(payload.Changes) > integrationMaxChangesPerPayload {
			return errors.New("terminal integration receipt payload change count exceeds the limit")
		}
		totalChanges += len(payload.Changes)
		if totalChanges > integrationMaxReceiptChanges {
			return errors.New("terminal integration receipt total change count exceeds the limit")
		}
	}
	if err := validateIntegrationEncodedSize(receipt, integrationMaxOutputBytes, "terminal integration receipt"); err != nil {
		return err
	}
	if !integrationDigestRE.MatchString(receipt.EvidenceDigest) || receipt.EvidenceDigest != integrationReceiptEvidenceDigest(receipt) {
		return errors.New("terminal integration receipt evidence digest is invalid")
	}
	switch receipt.BatchDisposition {
	case integrationBatchSucceeded:
		if receipt.Error != nil || !integrationCommitIDRE.MatchString(receipt.Target.IntegratedTipCommit) || !integrationCommitIDRE.MatchString(receipt.Target.AfterHeadCommit) || record.CommitPointOperation == "" || record.GraphOperationID != record.CommitPointOperation || len(record.DetachedOperationIDs) == 0 || record.DetachedOperationIDs[len(record.DetachedOperationIDs)-1] != record.GraphOperationID || receipt.JJOperations.CommitPoint != record.CommitPointOperation || record.StagedTargetState == nil || record.TargetAdvancedState == nil || *record.StagedTargetState != *record.TargetAdvancedState || receipt.Target.IntegratedTipCommit != record.TargetAdvancedState.IntegratedTipCommit || receipt.Target.AfterHeadCommit != record.TargetAdvancedState.AfterHeadCommit {
			return errors.New("successful terminal integration receipt is incomplete")
		}
	case integrationBatchFailed:
		if receipt.Error == nil || receipt.Target.IntegratedTipCommit != "" || receipt.Target.AfterHeadCommit != "" || receipt.JJOperations.CommitPoint != "" || record.CommitPointOperation != "" || record.TargetAdvancedState != nil {
			return errors.New("failed terminal integration receipt is inconsistent")
		}
		if err := validateStoredFailedIntegrationError(*receipt.Error); err != nil {
			return err
		}
	case integrationBatchUnknownEffect:
		return errors.New("unknown-effect receipt cannot be stored as terminal integration evidence")
	default:
		return errors.New("terminal integration receipt batch disposition is invalid")
	}
	for i, expected := range request.Payloads {
		payload := receipt.Payloads[i]
		prepared := record.PreparedState.Payloads[i]
		if payload.Workspace != expected.Workspace || payload.InputHeadCommit != expected.ExpectedHeadCommit || !integrationDigestRE.MatchString(payload.EvidenceDigest) || payload.EvidenceDigest != integrationPayloadReceiptEvidenceDigest(payload) {
			return errors.New("terminal integration receipt payload binding is invalid")
		}
		if receipt.BatchDisposition == integrationBatchSucceeded && len(payload.Changes) != len(prepared.Changes) {
			return errors.New("successful terminal integration receipt payload mapping count is invalid")
		}
		if receipt.BatchDisposition == integrationBatchFailed && len(payload.Changes) != len(prepared.Changes) {
			return errors.New("failed terminal integration receipt payload proof count is invalid")
		}
		switch payload.Disposition {
		case integrationPayloadLanded:
			if receipt.BatchDisposition != integrationBatchSucceeded {
				return errors.New("landed payload appears in a non-success receipt")
			}
		case integrationPayloadProvedNotLanded:
			if receipt.BatchDisposition != integrationBatchFailed {
				return errors.New("proved-not-landed payload appears outside a failed receipt")
			}
		case integrationPayloadFailedBeforeEffect:
			return errors.New("failed-before-effect payload cannot be stored as terminal integration evidence")
		case integrationPayloadUnknownEffect:
			if receipt.BatchDisposition != integrationBatchUnknownEffect {
				return errors.New("unknown payload appears outside an unknown-effect receipt")
			}
		default:
			return errors.New("terminal integration receipt payload disposition is invalid")
		}
		for j, change := range payload.Changes {
			if !integrationFullChangeIDRE.MatchString(change.ChangeID) || !integrationCommitIDRE.MatchString(change.InputCommit) || (change.LandedCommit != "" && !integrationCommitIDRE.MatchString(change.LandedCommit)) {
				return errors.New("terminal integration receipt change evidence is invalid")
			}
			if payload.Disposition == integrationPayloadLanded && change.LandedCommit == "" {
				return errors.New("landed receipt change has no landed commit")
			}
			if receipt.BatchDisposition == integrationBatchFailed && (len(payload.Changes) != len(prepared.Changes) || j >= len(prepared.Changes) || change.ChangeID != prepared.Changes[j].ChangeID || change.InputCommit != prepared.Changes[j].CommitID || change.LandedCommit != "") {
				return errors.New("failed terminal integration receipt payload proof is inconsistent")
			}
			if receipt.BatchDisposition == integrationBatchSucceeded && (j >= len(prepared.Changes) || i >= len(record.StagedPayloadMappings) || j >= len(record.StagedPayloadMappings[i]) || change.ChangeID != prepared.Changes[j].ChangeID || change.InputCommit != prepared.Changes[j].CommitID || change != record.StagedPayloadMappings[i][j]) {
				return errors.New("successful terminal integration receipt payload mapping is inconsistent with staged graph evidence")
			}
		}
	}
	return nil
}

func validateStoredFailedIntegrationError(receiptError integrationReceiptErrorV1) error {
	expected, ok := integrationPublicErrorSummaries[receiptError.Code]
	if !ok || receiptError.Message != expected || !utf8.ValidString(receiptError.Message) || len([]byte(receiptError.Message)) > integrationMaxErrorBytes || receiptError.NextAction != integrationNextActionRetryNewOperation {
		return errors.New("failed terminal integration receipt error tuple is invalid")
	}
	switch receiptError.Code {
	case integrationErrorConflict, integrationErrorInternal, integrationErrorOperationInterrupted:
		return nil
	default:
		return errors.New("failed terminal integration receipt error tuple is invalid")
	}
}

func writeIntegrationOperationRecordAtomic(stateDir string, record integrationOperationRecord) error {
	if err := validateIntegrationOperationRecordSemantic(record, record.OperationID); err != nil {
		return fmt.Errorf("validate integration operation record before write: %w", err)
	}
	path, err := integrationOperationRecordPath(stateDir, record.OperationID)
	if err != nil {
		return err
	}
	if err := ensureIntegrationStateDirectory(stateDir); err != nil {
		return err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode integration operation record: %w", err)
	}
	data = append(data, '\n')
	if len(data) > integrationMaxRecordBytes {
		return fmt.Errorf("integration operation record exceeds %d bytes", integrationMaxRecordBytes)
	}
	tmp, err := os.CreateTemp(stateDir, ".operation-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary integration record: %w", err)
	}
	tmpPath := tmp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temporary integration record: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temporary integration record: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temporary integration record: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary integration record: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace integration operation record: %w", err)
	}
	removeTemp = false
	dir, err := os.Open(stateDir)
	if err != nil {
		return fmt.Errorf("open integration state directory for sync: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync integration state directory: %w", err)
	}
	return nil
}

func validateIntegrationOperationContext(record integrationOperationRecord, binding integrationStateBinding) error {
	if record.CanonicalProjectPath != binding.CanonicalProjectPath || record.CanonicalTargetPath != binding.CanonicalTargetPath {
		return newIntegrationProtocolError(integrationErrorAssertionFailed, "operation record belongs to a different Project or target Workspace")
	}
	return nil
}

func integrationRequestFromRecord(record integrationOperationRecord) integrationRequestV1 {
	request, _, err := parseIntegrationRequestV1(record.RequestBytes)
	if err != nil {
		return integrationRequestV1{OperationID: record.OperationID}
	}
	return request
}

func emitIntegrationFailure(operationID, requestDigest string, request integrationRequestV1, cause error, disposition, nextAction string) error {
	code := integrationErrorInternal
	var protocolErr *integrationProtocolError
	if errors.As(cause, &protocolErr) {
		code = protocolErr.Code
	}
	message := integrationPublicErrorMessage(cause)
	receipt := integrationReceiptV1{
		Schema:           integrationReceiptSchemaV1,
		OperationID:      operationID,
		RequestDigest:    requestDigest,
		Strategy:         request.Strategy,
		BatchDisposition: disposition,
		Target: integrationReceiptTargetV1{
			Workspace:        request.Target.ExpectedWorkspace,
			BeforeHeadCommit: request.Target.ExpectedHeadCommit,
		},
		Payloads:       make([]integrationReceiptPayloadV1, 0, len(request.Payloads)),
		JJOperations:   integrationJJOperationsV1{},
		EvidenceDigest: "",
		Error: &integrationReceiptErrorV1{
			Code:       code,
			Message:    message,
			NextAction: nextAction,
		},
	}
	payloadDisposition := integrationPayloadFailedBeforeEffect
	if disposition == integrationBatchUnknownEffect {
		payloadDisposition = integrationPayloadUnknownEffect
	}
	for _, payload := range request.Payloads {
		payloadReceipt := integrationReceiptPayloadV1{
			Workspace:       payload.Workspace,
			InputHeadCommit: payload.ExpectedHeadCommit,
			Disposition:     payloadDisposition,
			Changes:         []integrationReceiptChangeV1{},
		}
		payloadReceipt.EvidenceDigest = integrationPayloadReceiptEvidenceDigest(payloadReceipt)
		receipt.Payloads = append(receipt.Payloads, payloadReceipt)
	}
	receipt.EvidenceDigest = integrationReceiptEvidenceDigest(receipt)
	if err := validateIntegrationEncodedSize(receipt, integrationMaxOutputBytes, "integration failure receipt"); err != nil {
		receipt = integrationReceiptV1{
			Schema: integrationReceiptSchemaV1, OperationID: operationID, RequestDigest: requestDigest,
			BatchDisposition: integrationBatchUnknownEffect, Target: integrationReceiptTargetV1{},
			Payloads: []integrationReceiptPayloadV1{}, JJOperations: integrationJJOperationsV1{},
			Error: &integrationReceiptErrorV1{
				Code: integrationErrorInternal, Message: integrationPublicErrorSummaries[integrationErrorInternal], NextAction: integrationNextActionOperatorReview,
			},
		}
		receipt.EvidenceDigest = integrationReceiptEvidenceDigest(receipt)
		code = integrationErrorInternal
		message = integrationPublicErrorSummaries[integrationErrorInternal]
	}
	if err := writeIntegrationJSON(stdoutWriter, receipt); err != nil {
		return err
	}
	return &integrationCommandFailure{message: code + ": " + message}
}

var integrationPublicErrorSummaries = map[string]string{
	integrationErrorInvalidJSON:              "integration request is not valid strict JSON",
	integrationErrorInvalidRequest:           "integration request failed validation",
	integrationErrorOperationIDContradiction: "operation id is already bound to a different request",
	integrationErrorTargetResolution:         "could not resolve the exact Current Workspace",
	integrationErrorAssertionFailed:          "integration precondition failed",
	integrationErrorOperationInProgress:      "another integration operation is in progress",
	integrationErrorOperationInterrupted:     "integration operation is interrupted and requires recovery",
	integrationErrorConflict:                 "integration encountered a conflict",
	integrationErrorUnknownEffect:            "integration effect cannot be proved",
	integrationErrorInternal:                 "integration operation failed; inspect stderr and local operation state",
}

func integrationPublicErrorMessage(cause error) string {
	code := integrationErrorInternal
	var protocolErr *integrationProtocolError
	if errors.As(cause, &protocolErr) {
		code = protocolErr.Code
	} else {
		fmt.Fprintf(stderrWriter, "ajj integrate detail: %v\n", cause)
	}
	message, ok := integrationPublicErrorSummaries[code]
	if !ok {
		message = integrationPublicErrorSummaries[integrationErrorInternal]
	}
	return boundedIntegrationMessage(message)
}

func boundedIntegrationMessage(message string) string {
	message = strings.TrimSpace(strings.ReplaceAll(strings.ToValidUTF8(message, "�"), "\x00", ""))
	if len([]byte(message)) <= integrationMaxErrorBytes {
		return message
	}
	limit := integrationMaxErrorBytes - len("...")
	cut := 0
	for index := range message {
		if index > limit {
			break
		}
		cut = index
	}
	if cut == 0 {
		return "..."
	}
	return strings.TrimSpace(message[:cut]) + "..."
}

func integrationEvidenceDigest(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func integrationPayloadReceiptEvidenceDigest(payload integrationReceiptPayloadV1) string {
	copyPayload := payload
	copyPayload.EvidenceDigest = ""
	data, _ := json.Marshal(copyPayload)
	return integrationEvidenceDigest(string(data))
}

func integrationReceiptEvidenceDigest(receipt integrationReceiptV1) string {
	copyReceipt := receipt
	copyReceipt.EvidenceDigest = ""
	data, _ := json.Marshal(copyReceipt)
	return integrationEvidenceDigest(string(data))
}

func encodeIntegrationJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func validateIntegrationEncodedSize(value any, maximum int, label string) error {
	data, err := encodeIntegrationJSON(value)
	if err != nil {
		return fmt.Errorf("encode %s: %w", label, err)
	}
	if len(data) > maximum {
		return fmt.Errorf("%s exceeds %d bytes", label, maximum)
	}
	return nil
}

func writeIntegrationJSON(writer io.Writer, value any) error {
	data, err := encodeIntegrationJSON(value)
	if err != nil {
		return err
	}
	if len(data) > integrationMaxOutputBytes {
		return fmt.Errorf("integration JSON output exceeds %d bytes", integrationMaxOutputBytes)
	}
	_, err = writer.Write(data)
	return err
}
