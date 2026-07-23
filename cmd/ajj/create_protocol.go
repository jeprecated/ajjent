package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

const (
	createRequestSchemaV1      = "ajj-create-request-v1"
	createReceiptSchemaV1      = "ajj-create-receipt-v1"
	createReceiptSchemaV2      = "ajj-create-receipt-v2"
	ajjCapabilitiesSchemaV2    = "ajj-capabilities-v2"
	ajjCapabilitiesSchemaV3    = "ajj-capabilities-v3"
	createRecoveryModel        = "state-reconciliation"
	createStatusReady          = "ready"
	createStatusPartial        = "partial"
	createStatusNotCreated     = "not-created"
	createStatusConflict       = "conflict"
	createNextRetryEnsure      = "retry-ensure"
	createNextRetryCreate      = "retry-create"
	createNextOperatorReview   = "operator-review"
	createMaxRequestBytes      = 64 * 1024
	createMaxOutputBytes       = 64 * 1024
	createMaxErrorMessageBytes = 512
)

type createRequestV1 struct {
	Schema    string                  `json:"schema"`
	RequestID string                  `json:"requestId"`
	Target    createTargetAssertionV1 `json:"target"`
	Child     createChildAssertionV1  `json:"child"`
}
type createTargetAssertionV1 struct {
	ExpectedWorkspace  string `json:"expectedWorkspace"`
	ExpectedHeadCommit string `json:"expectedHeadCommit"`
}
type createChildAssertionV1 struct {
	Workspace string `json:"workspace"`
}
type createReceiptV1 struct {
	Schema         string                `json:"schema"`
	RequestID      string                `json:"requestId"`
	RequestDigest  string                `json:"requestDigest"`
	Status         string                `json:"status"`
	Target         createReceiptTargetV1 `json:"target"`
	Child          createReceiptChildV1  `json:"child"`
	Checks         createReceiptChecksV1 `json:"checks"`
	EvidenceDigest string                `json:"evidenceDigest"`
	Error          *createReceiptErrorV1 `json:"error,omitempty"`
}
type createReceiptTargetV1 struct {
	Workspace          string `json:"workspace"`
	ExpectedHeadCommit string `json:"expectedHeadCommit"`
}
type createReceiptChildV1 struct {
	Workspace     string `json:"workspace"`
	WorkspaceRoot string `json:"workspaceRoot,omitempty"`
	HeadCommit    string `json:"headCommit,omitempty"`
	ParentCommit  string `json:"parentCommit,omitempty"`
}
type createReceiptChecksV1 struct {
	RegistrationPresent bool `json:"registrationPresent"`
	DestinationPresent  bool `json:"destinationPresent"`
	RepositoryMatches   bool `json:"repositoryMatches"`
	ParentMatches       bool `json:"parentMatches"`
	FreshCursor         bool `json:"freshCursor"`
	SetupComplete       bool `json:"setupComplete"`
}
type createReceiptErrorV1 struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	NextAction string `json:"nextAction"`
}
type ajjCapabilitiesV2 struct {
	Schema    string                    `json:"schema"`
	Integrate integrationCapabilitiesV1 `json:"integrate"`
	Create    createCapabilitiesV1      `json:"create"`
}
type ajjCapabilitiesV3 struct {
	Schema    string                    `json:"schema"`
	Integrate integrationCapabilitiesV1 `json:"integrate"`
	Create    createCapabilitiesV2      `json:"create"`
}
type createCapabilitiesV1 struct {
	RequestSchema        string   `json:"requestSchema"`
	ReceiptSchema        string   `json:"receiptSchema"`
	Executable           bool     `json:"executable"`
	MinimumJJVersion     string   `json:"minimumJjVersion"`
	TargetResolution     string   `json:"targetResolution"`
	ExactHeadAssertion   bool     `json:"exactHeadAssertion"`
	RecoveryModel        string   `json:"recoveryModel"`
	Statuses             []string `json:"statuses"`
	NextActions          []string `json:"nextActions"`
	RequestIDPattern     string   `json:"requestIdPattern"`
	MaxRequestBytes      int      `json:"maxRequestBytes"`
	MaxOutputBytes       int      `json:"maxOutputBytes"`
	MaxErrorMessageBytes int      `json:"maxErrorMessageBytes"`
}
type createCapabilitiesV2 struct {
	RequestSchema        string   `json:"requestSchema"`
	ReceiptSchemas       []string `json:"receiptSchemas"`
	Executable           bool     `json:"executable"`
	MinimumJJVersion     string   `json:"minimumJjVersion"`
	TargetResolution     string   `json:"targetResolution"`
	ExactHeadAssertion   bool     `json:"exactHeadAssertion"`
	RecoveryModel        string   `json:"recoveryModel"`
	Statuses             []string `json:"statuses"`
	NextActions          []string `json:"nextActions"`
	RequestIDPattern     string   `json:"requestIdPattern"`
	MaxRequestBytes      int      `json:"maxRequestBytes"`
	MaxOutputBytes       int      `json:"maxOutputBytes"`
	MaxErrorMessageBytes int      `json:"maxErrorMessageBytes"`
}

func createCapabilityBase() createCapabilitiesV1 {
	return createCapabilitiesV1{RequestSchema: createRequestSchemaV1, ReceiptSchema: createReceiptSchemaV1, Executable: true, MinimumJJVersion: jjMinVersion, TargetResolution: "current-workspace", ExactHeadAssertion: true, RecoveryModel: createRecoveryModel, Statuses: []string{createStatusReady, createStatusPartial, createStatusNotCreated, createStatusConflict}, NextActions: []string{createNextRetryEnsure, createNextRetryCreate, createNextOperatorReview}, RequestIDPattern: integrationOperationIDRE.String(), MaxRequestBytes: createMaxRequestBytes, MaxOutputBytes: createMaxOutputBytes, MaxErrorMessageBytes: createMaxErrorMessageBytes}
}
func capabilitiesV2() ajjCapabilitiesV2 {
	v1 := integrationCapabilities()
	return ajjCapabilitiesV2{Schema: ajjCapabilitiesSchemaV2, Integrate: v1.Integrate, Create: createCapabilityBase()}
}
func capabilitiesV3() ajjCapabilitiesV3 {
	v1 := integrationCapabilities()
	create := createCapabilityBase()
	return ajjCapabilitiesV3{Schema: ajjCapabilitiesSchemaV3, Integrate: v1.Integrate, Create: createCapabilitiesV2{RequestSchema: create.RequestSchema, ReceiptSchemas: []string{createReceiptSchemaV1, createReceiptSchemaV2}, Executable: create.Executable, MinimumJJVersion: create.MinimumJJVersion, TargetResolution: create.TargetResolution, ExactHeadAssertion: create.ExactHeadAssertion, RecoveryModel: create.RecoveryModel, Statuses: create.Statuses, NextActions: create.NextActions, RequestIDPattern: create.RequestIDPattern, MaxRequestBytes: create.MaxRequestBytes, MaxOutputBytes: create.MaxOutputBytes, MaxErrorMessageBytes: create.MaxErrorMessageBytes}}
}
func parseCreateRequestV1(data []byte) (createRequestV1, string, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return createRequestV1{}, "", fmt.Errorf("create request is empty")
	}
	if err := validateSingleJSONValueWithoutDuplicateKeys(data); err != nil {
		return createRequestV1{}, "", fmt.Errorf("invalid create request JSON: %w", err)
	}
	if err := validateCreateRequestJSONKeys(data); err != nil {
		return createRequestV1{}, "", fmt.Errorf("invalid create request JSON: %w", err)
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	var r createRequestV1
	if err := d.Decode(&r); err != nil {
		return r, "", fmt.Errorf("invalid create request JSON: %w", err)
	}
	if err := requireJSONEOF(d); err != nil {
		return r, "", fmt.Errorf("invalid create request JSON: %w", err)
	}
	if err := validateCreateRequestV1(r); err != nil {
		return r, "", err
	}
	sum := sha256.Sum256(data)
	return r, "sha256:" + hex.EncodeToString(sum[:]), nil
}
func validateCreateRequestV1(r createRequestV1) error {
	if r.Schema != createRequestSchemaV1 {
		return fmt.Errorf("schema must be %q", createRequestSchemaV1)
	}
	if !integrationOperationIDRE.MatchString(r.RequestID) {
		return fmt.Errorf("requestId must match %s", integrationOperationIDRE.String())
	}
	if err := validateWorkspaceHandle(r.Target.ExpectedWorkspace); err != nil {
		return fmt.Errorf("invalid target expectedWorkspace: %w", err)
	}
	if !integrationCommitIDRE.MatchString(r.Target.ExpectedHeadCommit) {
		return fmt.Errorf("target expectedHeadCommit must be a full 40-character lowercase hexadecimal commit id")
	}
	if err := validateWorkspaceHandle(r.Child.Workspace); err != nil {
		return fmt.Errorf("invalid child workspace: %w", err)
	}
	if r.Child.Workspace == r.Target.ExpectedWorkspace {
		return fmt.Errorf("child workspace %q is the target workspace", r.Child.Workspace)
	}
	return nil
}
func validateCreateRequestJSONKeys(data []byte) error {
	top, err := decodeExactJSONObject(data, "request", []string{"schema", "requestId", "target", "child"})
	if err != nil {
		return err
	}
	if raw, ok := top["target"]; ok {
		if _, err := decodeExactJSONObject(raw, "target", []string{"expectedWorkspace", "expectedHeadCommit"}); err != nil {
			return err
		}
	}
	if raw, ok := top["child"]; ok {
		if _, err := decodeExactJSONObject(raw, "child", []string{"workspace"}); err != nil {
			return err
		}
	}
	return nil
}
func validateCreateReceiptV1(r createReceiptV1) error {
	if r.Schema != createReceiptSchemaV1 {
		return fmt.Errorf("receipt schema must be %q", createReceiptSchemaV1)
	}
	if r.Child.WorkspaceRoot != "" {
		return fmt.Errorf("v1 receipt must not contain a Workspace root")
	}
	return validateCreateReceipt(r)
}
func validateCreateReceiptV2(r createReceiptV1) error {
	if r.Schema != createReceiptSchemaV2 {
		return fmt.Errorf("receipt schema must be %q", createReceiptSchemaV2)
	}
	return validateCreateReceipt(r)
}
func validateCreateReceipt(r createReceiptV1) error {
	if r.Schema != createReceiptSchemaV1 && r.Schema != createReceiptSchemaV2 {
		return fmt.Errorf("receipt schema is unsupported")
	}
	if !integrationOperationIDRE.MatchString(r.RequestID) {
		return fmt.Errorf("receipt requestId is invalid")
	}
	if !integrationDigestRE.MatchString(r.RequestDigest) || !integrationDigestRE.MatchString(r.EvidenceDigest) {
		return fmt.Errorf("receipt digest is invalid")
	}
	if err := validateWorkspaceHandle(r.Target.Workspace); err != nil {
		return fmt.Errorf("receipt target is invalid")
	}
	if !integrationCommitIDRE.MatchString(r.Target.ExpectedHeadCommit) {
		return fmt.Errorf("receipt target head is invalid")
	}
	if err := validateWorkspaceHandle(r.Child.Workspace); err != nil {
		return fmt.Errorf("receipt child is invalid")
	}
	if r.Child.HeadCommit != "" && !integrationCommitIDRE.MatchString(r.Child.HeadCommit) {
		return fmt.Errorf("receipt child head is invalid")
	}
	if r.Child.ParentCommit != "" && !integrationCommitIDRE.MatchString(r.Child.ParentCommit) {
		return fmt.Errorf("receipt child parent is invalid")
	}
	if r.Target.Workspace == r.Child.Workspace {
		return fmt.Errorf("receipt child is the target Workspace")
	}
	rootRequired := r.Schema == createReceiptSchemaV2 && (r.Status == createStatusReady || r.Status == createStatusPartial)
	if rootRequired {
		if !filepath.IsAbs(r.Child.WorkspaceRoot) || filepath.Clean(r.Child.WorkspaceRoot) != r.Child.WorkspaceRoot || strings.ContainsRune(r.Child.WorkspaceRoot, '\x00') || len(r.Child.WorkspaceRoot) > 4096 {
			return fmt.Errorf("v2 receipt Workspace root is invalid")
		}
	} else if r.Child.WorkspaceRoot != "" {
		return fmt.Errorf("receipt status must not contain a Workspace root")
	}
	if r.Checks.RepositoryMatches && (!r.Checks.RegistrationPresent || !r.Checks.DestinationPresent) {
		return fmt.Errorf("receipt repository evidence is inconsistent")
	}
	if (r.Checks.ParentMatches || r.Checks.FreshCursor) && !r.Checks.RepositoryMatches {
		return fmt.Errorf("receipt graph evidence is inconsistent")
	}
	if r.Checks.ParentMatches && r.Child.ParentCommit != r.Target.ExpectedHeadCommit {
		return fmt.Errorf("receipt child parent does not match the target assertion")
	}
	if r.Checks.FreshCursor && r.Child.HeadCommit == "" {
		return fmt.Errorf("receipt fresh cursor lacks a child head")
	}
	if r.Checks.SetupComplete && r.Status != createStatusReady {
		return fmt.Errorf("only ready may report complete setup")
	}
	switch r.Status {
	case createStatusReady:
		if r.Error != nil || !exactCreateCoreChecks(r.Checks) || !r.Checks.SetupComplete || r.Child.HeadCommit == "" || r.Child.ParentCommit == "" {
			return fmt.Errorf("ready receipt lacks exact provider evidence")
		}
	case createStatusPartial:
		if !exactCreateCoreChecks(r.Checks) || r.Checks.SetupComplete || r.Child.HeadCommit == "" || r.Child.ParentCommit == "" || !validCreateReceiptError(r.Error, map[string]string{"setup-incomplete": createNextRetryEnsure}) {
			return fmt.Errorf("partial receipt evidence is inconsistent")
		}
	case createStatusNotCreated:
		if r.Checks != (createReceiptChecksV1{}) || r.Child.HeadCommit != "" || r.Child.ParentCommit != "" || !validCreateReceiptError(r.Error, map[string]string{"pre-effect-check-failed": createNextRetryCreate, "target-head-drift": createNextRetryCreate, "create-failed-before-effect": createNextRetryCreate}) {
			return fmt.Errorf("not-created receipt evidence is inconsistent")
		}
	case createStatusConflict:
		if !validCreateReceiptError(r.Error, createConflictErrorActions()) {
			return fmt.Errorf("conflict receipt evidence is inconsistent")
		}
	default:
		return fmt.Errorf("receipt status is invalid")
	}
	if r.Error != nil && (r.Error.Message == "" || len(r.Error.Message) > createMaxErrorMessageBytes) {
		return fmt.Errorf("receipt error message is empty or too large")
	}
	if r.EvidenceDigest != createReceiptEvidenceDigest(r) {
		return fmt.Errorf("receipt evidence digest does not match the receipt")
	}
	return nil
}

func exactCreateCoreChecks(c createReceiptChecksV1) bool {
	return c.RegistrationPresent && c.DestinationPresent && c.RepositoryMatches && c.ParentMatches && c.FreshCursor
}

func validCreateReceiptError(e *createReceiptErrorV1, allowed map[string]string) bool {
	if e == nil {
		return false
	}
	action, ok := allowed[e.Code]
	return ok && e.NextAction == action
}

func createConflictErrorActions() map[string]string {
	return map[string]string{
		"target-resolution-failed":      createNextOperatorReview,
		"target-assertion-failed":       createNextOperatorReview,
		"target-head-drift":             createNextOperatorReview,
		"state-changed":                 createNextOperatorReview,
		"destination-unavailable":       createNextOperatorReview,
		"registration-unavailable":      createNextOperatorReview,
		"registration-path-mismatch":    createNextOperatorReview,
		"registration-root-unavailable": createNextOperatorReview,
		"destination-identity-mismatch": createNextOperatorReview,
		"repository-mismatch":           createNextOperatorReview,
		"child-head-unavailable":        createNextOperatorReview,
		"child-parent-unavailable":      createNextOperatorReview,
		"child-graph-mismatch":          createNextOperatorReview,
		"create-state-conflict":         createNextOperatorReview,
	}
}

func createReceiptEvidenceDigest(r createReceiptV1) string {
	copy := r
	copy.EvidenceDigest = ""
	data, _ := json.Marshal(copy)
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func finalizeCreateReceipt(r createReceiptV1) createReceiptV1 {
	r.EvidenceDigest = createReceiptEvidenceDigest(r)
	return r
}
