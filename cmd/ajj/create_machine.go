package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var createMaterializeSetupFn = materializeAssimilatedFolders
var createReadyProofHook = func(string) error { return nil }
var createMachineCommandFn = func(name string, args ...string) error {
	_, err := runCommandCombinedCapture(name, args...)
	return err
}

type createObservedState struct {
	checks        createReceiptChecksV1
	headCommit    string
	parentCommit  string
	workspaceRoot string
	matchingCore  bool
	absent        bool
	conflictCode  string
}

func runCreateMachine(args []string) error {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	var repo, source, receiptSchema string
	var jsonOutput bool
	fs.StringVar(&repo, "repo", "", "Current Workspace root override")
	fs.StringVar(&source, "request-json", "", "read one create request from PATH or - for stdin")
	fs.StringVar(&receiptSchema, "receipt-schema", createReceiptSchemaV1, "machine receipt schema to emit")
	fs.BoolVar(&jsonOutput, "json", false, "write one bounded machine-readable result")
	if handled, err := parseCommandFlags(fs, args, "ajj create --repo PATH --request-json PATH|- --json [--receipt-schema ajj-create-receipt-v1|ajj-create-receipt-v2]", "Ensure or reconcile a Workspace from exact Current-Workspace state."); handled || err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		return errors.New("create machine mode does not accept positional arguments")
	}
	if strings.TrimSpace(source) == "" || !jsonOutput {
		return errors.New("create machine mode requires --request-json and --json")
	}
	receiptSchema = strings.TrimSpace(receiptSchema)
	if receiptSchema != createReceiptSchemaV1 && receiptSchema != createReceiptSchemaV2 {
		return fmt.Errorf("unsupported create receipt schema %q", receiptSchema)
	}
	data, err := readCreateRequestSource(source)
	if err != nil {
		return err
	}
	request, digest, err := parseCreateRequestV1(data)
	if err != nil {
		return err
	}
	return reconcileCreateRequest(repo, request, digest, receiptSchema)
}
func readCreateRequestSource(source string) ([]byte, error) {
	var r io.Reader
	var c io.Closer
	if source == "-" {
		r = stdinReader
	} else {
		f, err := os.Open(expandPath(source))
		if err != nil {
			return nil, errors.New("open create request failed")
		}
		r, c = f, f
	}
	if c != nil {
		defer c.Close()
	}
	data, err := io.ReadAll(io.LimitReader(r, createMaxRequestBytes+1))
	if err != nil {
		return nil, errors.New("read create request failed")
	}
	if len(data) > createMaxRequestBytes {
		return nil, fmt.Errorf("create request exceeds %d bytes", createMaxRequestBytes)
	}
	return data, nil
}

func reconcileCreateRequest(repoOverride string, req createRequestV1, digest, receiptSchema string) error {
	base := createReceiptV1{Schema: receiptSchema, RequestID: req.RequestID, RequestDigest: digest, Target: createReceiptTargetV1{Workspace: req.Target.ExpectedWorkspace, ExpectedHeadCommit: req.Target.ExpectedHeadCommit}, Child: createReceiptChildV1{Workspace: req.Child.Workspace}}
	repo, cfg, project, err := commandContext(repoOverride, "", "")
	if err != nil {
		return emitCreateState(base, createStatusConflict, createReceiptChecksV1{}, "target-resolution-failed", "Current Workspace or provider configuration does not match", createNextOperatorReview)
	}
	refs, err := listWorkspaceRefs(repo)
	if err != nil {
		return emitCreateState(base, createStatusConflict, createReceiptChecksV1{}, "target-resolution-failed", "Current Workspace registration could not be verified", createNextOperatorReview)
	}
	target, err := resolveIntegrationTargetWorkspace(repo, refs, req.Target.ExpectedWorkspace)
	if err != nil {
		return emitCreateState(base, createStatusConflict, createReceiptChecksV1{}, "target-assertion-failed", "Current Workspace does not match the request assertion", createNextOperatorReview)
	}
	head, err := integrationWorkspaceHeadCommit(repo, target.Handle)
	if err != nil || head != req.Target.ExpectedHeadCommit {
		return emitCreateState(base, createStatusConflict, createReceiptChecksV1{}, "target-head-drift", "Current Workspace head does not match the request assertion", createNextOperatorReview)
	}
	observed := inspectCreateState(repo, cfg, project, req)
	if observed.matchingCore {
		return reconcileAndEmitCreateState(base, repo, cfg, project, req, "", "")
	}
	if !observed.absent {
		return emitObservedCreateState(base, observed, createStatusConflict, observed.conflictCode, "Workspace registration or destination contradicts the request", createNextOperatorReview)
	}
	// Snapshot/check Current immediately before effect. Any filesystem drift changes
	// the exact head and is rejected by the following revalidation.
	if _, err := commandCaptureFn("jj", "-R", repo, "--color=never", "--no-pager", "status"); err != nil {
		return reconcileAndEmitCreateState(base, repo, cfg, project, req, "pre-effect-check-failed", "Workspace was not created because pre-effect validation failed")
	}
	refs, err = listWorkspaceRefs(repo)
	if err != nil {
		return reconcileAndEmitCreateState(base, repo, cfg, project, req, "pre-effect-check-failed", "Workspace was not created because pre-effect validation failed")
	}
	if _, err = resolveIntegrationTargetWorkspace(repo, refs, req.Target.ExpectedWorkspace); err != nil {
		return emitCreateTargetChangedState(base, repo, cfg, project, req)
	}
	head, err = integrationWorkspaceHeadCommit(repo, req.Target.ExpectedWorkspace)
	if err != nil || head != req.Target.ExpectedHeadCommit {
		return emitCreateTargetChangedState(base, repo, cfg, project, req)
	}
	_ = createWorkspaceInternal(repo, cfg, project, req.Child.Workspace, req.Target.ExpectedHeadCommit, cfg.Create.Envrc, false, false)
	return reconcileAndEmitCreateState(base, repo, cfg, project, req, "create-failed-before-effect", "Workspace was not created")
}

func reconcileAndEmitCreateState(base createReceiptV1, repo string, cfg config, project string, req createRequestV1, absentCode, absentMessage string) error {
	observed := inspectCreateState(repo, cfg, project, req)
	if observed.matchingCore {
		if !createTargetStillMatches(repo, req) {
			return emitObservedCreateState(base, observed, createStatusConflict, "target-head-drift", "Current Workspace changed during reconciliation", createNextOperatorReview)
		}
		setupErr := reconcileCreateProviderSetup(repo, cfg, project, req.Child.Workspace)
		observed, stable := inspectStableReadyCreateState(repo, cfg, project, req)
		if !stable {
			return emitObservedCreateState(base, observed, createStatusConflict, "state-changed", "Workspace state changed during reconciliation", createNextOperatorReview)
		}
		if setupErr != nil {
			return emitObservedCreateState(base, observed, createStatusPartial, "setup-incomplete", "Workspace exists but provider setup is incomplete", createNextRetryEnsure)
		}
		observed.checks.SetupComplete = true
		return emitObservedCreateState(base, observed, createStatusReady, "", "", "")
	}
	if observed.absent {
		if absentCode == "" {
			absentCode = "create-failed-before-effect"
			absentMessage = "Workspace was not created"
		}
		return emitObservedCreateState(base, observed, createStatusNotCreated, absentCode, absentMessage, createNextRetryCreate)
	}
	code := observed.conflictCode
	if code == "" {
		code = "create-state-conflict"
	}
	return emitObservedCreateState(base, observed, createStatusConflict, code, "Workspace registration or destination contradicts the request", createNextOperatorReview)
}

func inspectStableReadyCreateState(repo string, cfg config, project string, req createRequestV1) (createObservedState, bool) {
	beforeOperation, err := currentOperationFullID(repo)
	if err != nil {
		return createObservedState{}, false
	}
	observed := inspectCreateState(repo, cfg, project, req)
	if err := createReadyProofHook("after-final-child-inspection"); err != nil {
		return observed, false
	}
	if !observed.matchingCore || !createTargetStillMatches(repo, req) {
		return observed, false
	}
	// This final full operation read is the ready receipt's linearization point.
	// A direct jj operation after it is a later event; provider state and stdout
	// cannot participate in one cross-filesystem atomic transaction.
	afterOperation, err := currentOperationFullID(repo)
	return observed, err == nil && beforeOperation == afterOperation
}

func createTargetStillMatches(repo string, req createRequestV1) bool {
	refs, err := listWorkspaceRefs(repo)
	if err != nil {
		return false
	}
	if _, err := resolveIntegrationTargetWorkspace(repo, refs, req.Target.ExpectedWorkspace); err != nil {
		return false
	}
	head, err := integrationWorkspaceHeadCommit(repo, req.Target.ExpectedWorkspace)
	return err == nil && head == req.Target.ExpectedHeadCommit
}

func emitCreateTargetChangedState(base createReceiptV1, repo string, cfg config, project string, req createRequestV1) error {
	observed := inspectCreateState(repo, cfg, project, req)
	if observed.absent {
		return emitObservedCreateState(base, observed, createStatusNotCreated, "target-head-drift", "Workspace was not created because the target changed", createNextRetryCreate)
	}
	return emitObservedCreateState(base, observed, createStatusConflict, "target-head-drift", "Target or child Workspace state changed before creation", createNextOperatorReview)
}

func inspectCreateState(repo string, cfg config, project string, req createRequestV1) createObservedState {
	s := createObservedState{}
	dest := filepath.Join(cfg.WorkspacesRoot, project, req.Child.Workspace)
	entry, lstatErr := os.Lstat(dest)
	destinationOccupied := lstatErr == nil
	info, statErr := os.Stat(dest)
	s.checks.DestinationPresent = statErr == nil && info.IsDir()
	if lstatErr != nil && !errors.Is(lstatErr, os.ErrNotExist) {
		s.conflictCode = "destination-unavailable"
		return s
	}
	if destinationOccupied && entry.Mode()&os.ModeSymlink != 0 && statErr != nil {
		s.conflictCode = "destination-identity-mismatch"
		return s
	}
	refs, refsErr := listWorkspaceRefs(repo)
	var child *workspaceRef
	if refsErr == nil {
		for i := range refs {
			if refs[i].Handle == req.Child.Workspace {
				child = &refs[i]
				break
			}
		}
	}
	if refsErr != nil {
		s.conflictCode = "registration-unavailable"
		return s
	}
	s.checks.RegistrationPresent = child != nil
	if child == nil && !destinationOccupied {
		s.absent = true
		return s
	}
	if child == nil || !s.checks.DestinationPresent || statErr != nil {
		s.conflictCode = "registration-path-mismatch"
		return s
	}
	root, err := canonicalExistingDirectory(cleanWorkspaceRoot(child.Root))
	if err != nil {
		s.conflictCode = "registration-root-unavailable"
		return s
	}
	s.workspaceRoot = root
	canon, err := canonicalExistingDirectory(dest)
	if err != nil || root != canon {
		s.conflictCode = "destination-identity-mismatch"
		return s
	}
	main1, e1 := canonicalExistingDirectory(mainWorkspaceRoot(repo))
	main2, e2 := canonicalExistingDirectory(mainWorkspaceRoot(dest))
	s.checks.RepositoryMatches = e1 == nil && e2 == nil && main1 == main2
	if !s.checks.RepositoryMatches {
		s.conflictCode = "repository-mismatch"
		return s
	}
	head, err := integrationWorkspaceHeadCommit(dest, req.Child.Workspace)
	if err != nil {
		s.conflictCode = "child-head-unavailable"
		return s
	}
	parent, err := workspaceParentCommitID(dest)
	if err != nil {
		s.conflictCode = "child-parent-unavailable"
		return s
	}
	s.headCommit, s.parentCommit = head, parent
	s.checks.ParentMatches = parent == req.Target.ExpectedHeadCommit
	fresh, err := integrationCommitIDs(dest, `@ & empty() & description("") & ~conflicts() & mutable()`)
	s.checks.FreshCursor = err == nil && len(fresh) == 1 && fresh[0] == head
	if !s.checks.ParentMatches || !s.checks.FreshCursor {
		s.conflictCode = "child-graph-mismatch"
		return s
	}
	s.matchingCore = true
	return s
}
func reconcileCreateProviderSetup(repo string, cfg config, project, child string) error {
	dest := filepath.Join(cfg.WorkspacesRoot, project, child)
	if cfg.Create.Envrc {
		if err := ensureEnvrc(dest); err != nil {
			return err
		}
	}
	return createMaterializeSetupFn(mainWorkspaceRoot(repo), dest, cfg, project)
}
func emitObservedCreateState(base createReceiptV1, s createObservedState, status, code, message, next string) error {
	base.Child.HeadCommit, base.Child.ParentCommit = s.headCommit, s.parentCommit
	if base.Schema == createReceiptSchemaV2 && (status == createStatusReady || status == createStatusPartial) {
		base.Child.WorkspaceRoot = s.workspaceRoot
	}
	return emitCreateState(base, status, s.checks, code, message, next)
}
func emitCreateState(r createReceiptV1, status string, checks createReceiptChecksV1, code, message, next string) error {
	r.Status, r.Checks = status, checks
	if code != "" {
		r.Error = &createReceiptErrorV1{Code: code, Message: message, NextAction: next}
	}
	r = finalizeCreateReceipt(r)
	var validationErr error
	if r.Schema == createReceiptSchemaV2 {
		validationErr = validateCreateReceiptV2(r)
	} else {
		validationErr = validateCreateReceiptV1(r)
	}
	if validationErr != nil {
		return validationErr
	}
	data, err := encodeIntegrationJSON(r)
	if err != nil {
		return err
	}
	if len(data) > createMaxOutputBytes {
		return errors.New("create receipt exceeds machine output limit")
	}
	_, err = stdoutWriter.Write(data)
	return err
}
