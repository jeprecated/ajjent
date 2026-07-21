package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	integrationRequestSchemaV1      = "ajj-integrate-request-v1"
	integrationReceiptSchemaV1      = "ajj-integrate-receipt-v1"
	integrationCapabilitiesSchemaV1 = "ajj-capabilities-v1"
	integrationOperationRecordV1    = "ajj-integration-operation-v1"

	integrationStrategySingle          = "single"
	integrationStrategyProviderDefault = "provider-default"
	integrationStrategyOrderedLine     = "ordered-line"

	integrationPhasePrepared          = "prepared"
	integrationPhaseGraphRewritten    = "graph-rewritten"
	integrationPhaseTargetAdvanced    = "target-advanced"
	integrationPhaseCursorsReconciled = "cursors-reconciled"
	integrationPhaseTerminal          = "terminal"

	integrationBatchSucceeded     = "succeeded"
	integrationBatchFailed        = "failed"
	integrationBatchUnknownEffect = "unknown-effect"

	integrationPayloadLanded             = "landed"
	integrationPayloadProvedNotLanded    = "proved-not-landed"
	integrationPayloadUnknownEffect      = "unknown-effect"
	integrationPayloadFailedBeforeEffect = "failed-before-effect"

	integrationTargetPreservedExact  = "preserved-exact-ancestor"
	integrationTargetProvedUnchanged = "proved-unchanged"

	integrationErrorInvalidJSON              = "invalid-json"
	integrationErrorInvalidRequest           = "invalid-request"
	integrationErrorOperationIDContradiction = "operation-id-contradiction"
	integrationErrorTargetResolution         = "target-resolution-failed"
	integrationErrorAssertionFailed          = "assertion-failed"
	integrationErrorOperationInProgress      = "operation-in-progress"
	integrationErrorOperationInterrupted     = "operation-interrupted"
	integrationErrorConflict                 = "conflict"
	integrationErrorUnknownEffect            = "unknown-effect"
	integrationErrorInternal                 = "internal-error"

	integrationNextActionRecover           = "recover"
	integrationNextActionRetryNewOperation = "retry-new-operation"
	integrationNextActionOperatorReview    = "operator-review"
	integrationNextActionNone              = "none"
)

var (
	integrationOperationIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	integrationCommitIDRE    = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

type integrationRequestV1 struct {
	Schema      string                          `json:"schema"`
	OperationID string                          `json:"operationId"`
	Target      integrationTargetAssertionV1    `json:"target"`
	Strategy    string                          `json:"strategy"`
	Payloads    []integrationPayloadAssertionV1 `json:"payloads"`
}

type integrationTargetAssertionV1 struct {
	ExpectedWorkspace  string `json:"expectedWorkspace"`
	ExpectedHeadCommit string `json:"expectedHeadCommit"`
}

type integrationPayloadAssertionV1 struct {
	Workspace          string `json:"workspace"`
	ExpectedHeadCommit string `json:"expectedHeadCommit"`
}

type integrationReceiptV1 struct {
	Schema           string                        `json:"schema"`
	OperationID      string                        `json:"operationId"`
	RequestDigest    string                        `json:"requestDigest"`
	Strategy         string                        `json:"strategy"`
	BatchDisposition string                        `json:"batchDisposition"`
	Target           integrationReceiptTargetV1    `json:"target"`
	Payloads         []integrationReceiptPayloadV1 `json:"payloads"`
	JJOperations     integrationJJOperationsV1     `json:"jjOperations"`
	EvidenceDigest   string                        `json:"evidenceDigest"`
	Error            *integrationReceiptErrorV1    `json:"error,omitempty"`
}

type integrationReceiptTargetV1 struct {
	Workspace               string `json:"workspace"`
	BeforeHeadCommit        string `json:"beforeHeadCommit"`
	BeforeHeadChangeID      string `json:"beforeHeadChangeId,omitempty"`
	PreservationDisposition string `json:"preservationDisposition,omitempty"`
	PreservedCommit         string `json:"preservedCommit,omitempty"`
	IntegratedTipCommit     string `json:"integratedTipCommit,omitempty"`
	AfterHeadCommit         string `json:"afterHeadCommit,omitempty"`
}

type integrationReceiptPayloadV1 struct {
	Workspace       string                       `json:"workspace"`
	InputHeadCommit string                       `json:"inputHeadCommit"`
	Disposition     string                       `json:"disposition"`
	Changes         []integrationReceiptChangeV1 `json:"changes"`
	EvidenceDigest  string                       `json:"evidenceDigest"`
}

type integrationReceiptChangeV1 struct {
	ChangeID     string `json:"changeId"`
	InputCommit  string `json:"inputCommit"`
	LandedCommit string `json:"landedCommit,omitempty"`
}

type integrationJJOperationsV1 struct {
	BeforeEffect string `json:"beforeEffect"`
	CommitPoint  string `json:"commitPoint,omitempty"`
}

type integrationReceiptErrorV1 struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	NextAction string `json:"nextAction"`
}

type ajjCapabilitiesV1 struct {
	Schema    string                    `json:"schema"`
	Integrate integrationCapabilitiesV1 `json:"integrate"`
}

type integrationCapabilitiesV1 struct {
	RequestSchema            string   `json:"requestSchema"`
	ReceiptSchema            string   `json:"receiptSchema"`
	Strategies               []string `json:"strategies"`
	ExecutableStrategies     []string `json:"executableStrategies"`
	PreparationOnly          bool     `json:"preparationOnly"`
	MinimumJJVersion         string   `json:"minimumJjVersion"`
	TargetResolution         string   `json:"targetResolution"`
	ExactHeadAssertions      bool     `json:"exactHeadAssertions"`
	TargetHeadPolicy         string   `json:"targetHeadPolicy"`
	SupportsNonEmptyTarget   bool     `json:"supportsNonEmptyTarget"`
	SupportsDescribedTarget  bool     `json:"supportsDescribedTarget"`
	SupportsImmutableTarget  bool     `json:"supportsImmutableTarget"`
	SupportsConflictedTarget bool     `json:"supportsConflictedTarget"`
	Recovery                 bool     `json:"recovery"`
	PerPayloadDispositions   []string `json:"perPayloadDispositions"`
	BatchDispositions        []string `json:"batchDispositions"`
	OperationIDPattern       string   `json:"operationIdPattern"`
	MaxRequestBytes          int      `json:"maxRequestBytes"`
	MaxOutputBytes           int      `json:"maxOutputBytes"`
	MaxPayloads              int      `json:"maxPayloads"`
	MaxChangesPerPayload     int      `json:"maxChangesPerPayload"`
	MaxReceiptChanges        int      `json:"maxReceiptChanges"`
	MaxErrorMessageBytes     int      `json:"maxErrorMessageBytes"`
}

type integrationOperationRecord struct {
	Schema                string                            `json:"schema"`
	OperationID           string                            `json:"operationId"`
	RequestDigest         string                            `json:"requestDigest"`
	RequestBytes          []byte                            `json:"requestBytes"`
	CanonicalProjectPath  string                            `json:"canonicalProjectPath"`
	CanonicalTargetPath   string                            `json:"canonicalTargetPath"`
	Phase                 string                            `json:"phase"`
	BeforeOperationID     string                            `json:"beforeOperationId"`
	CommitPointOperation  string                            `json:"commitPointOperationId,omitempty"`
	PreparedState         integrationPreparedStateV1        `json:"preparedState"`
	BeforeRepositoryView  integrationRepositoryViewV1       `json:"beforeRepositoryView"`
	GraphOperationID      string                            `json:"graphOperationId,omitempty"`
	DetachedOperationIDs  []string                          `json:"detachedOperationIds,omitempty"`
	GeneratedCommitIDs    []string                          `json:"generatedCommitIds,omitempty"`
	PayloadCursors        []integrationPayloadCursorV1      `json:"payloadCursors,omitempty"`
	PublishPending        bool                              `json:"publishPending,omitempty"`
	StagedTargetState     *integrationTargetAdvancedStateV1 `json:"stagedTargetState,omitempty"`
	StagedPayloadMappings [][]integrationReceiptChangeV1    `json:"stagedPayloadMappings,omitempty"`
	StagedRepositoryView  integrationRepositoryViewV1       `json:"stagedRepositoryView,omitempty"`
	TargetAdvancedState   *integrationTargetAdvancedStateV1 `json:"targetAdvancedState,omitempty"`
	Receipt               *integrationReceiptV1             `json:"receipt,omitempty"`
}

type integrationTargetResolution struct {
	Handle string
	Path   string
}

type integrationStateBinding struct {
	CanonicalProjectPath string
	CanonicalTargetPath  string
	StateDir             string
}

type integrationProtocolError struct {
	Code    string
	Message string
}

func (e *integrationProtocolError) Error() string {
	return e.Code + ": " + e.Message
}

func newIntegrationProtocolError(code, format string, args ...any) error {
	return &integrationProtocolError{Code: code, Message: fmt.Sprintf(format, args...)}
}

func integrationCapabilities() ajjCapabilitiesV1 {
	return ajjCapabilitiesV1{
		Schema: integrationCapabilitiesSchemaV1,
		Integrate: integrationCapabilitiesV1{
			RequestSchema:            integrationRequestSchemaV1,
			ReceiptSchema:            integrationReceiptSchemaV1,
			Strategies:               []string{integrationStrategySingle, integrationStrategyProviderDefault, integrationStrategyOrderedLine},
			ExecutableStrategies:     []string{integrationStrategySingle, integrationStrategyProviderDefault, integrationStrategyOrderedLine},
			PreparationOnly:          false,
			MinimumJJVersion:         jjMinVersion,
			TargetResolution:         "current-workspace",
			ExactHeadAssertions:      true,
			TargetHeadPolicy:         "preserve-materialized-current",
			SupportsNonEmptyTarget:   true,
			SupportsDescribedTarget:  true,
			SupportsImmutableTarget:  true,
			SupportsConflictedTarget: false,
			Recovery:                 true,
			PerPayloadDispositions: []string{
				integrationPayloadLanded,
				integrationPayloadProvedNotLanded,
				integrationPayloadUnknownEffect,
				integrationPayloadFailedBeforeEffect,
			},
			BatchDispositions:    []string{integrationBatchSucceeded, integrationBatchFailed, integrationBatchUnknownEffect},
			OperationIDPattern:   integrationOperationIDRE.String(),
			MaxRequestBytes:      integrationMaxRequestBytes,
			MaxOutputBytes:       integrationMaxOutputBytes,
			MaxPayloads:          integrationMaxPayloads,
			MaxChangesPerPayload: integrationMaxChangesPerPayload,
			MaxReceiptChanges:    integrationMaxReceiptChanges,
			MaxErrorMessageBytes: integrationMaxErrorBytes,
		},
	}
}

func parseIntegrationRequestV1(data []byte) (integrationRequestV1, string, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return integrationRequestV1{}, "", newIntegrationProtocolError(integrationErrorInvalidJSON, "request is empty")
	}
	if err := validateSingleJSONValueWithoutDuplicateKeys(data); err != nil {
		return integrationRequestV1{}, "", newIntegrationProtocolError(integrationErrorInvalidJSON, "%v", err)
	}
	if err := validateIntegrationRequestJSONKeys(data); err != nil {
		return integrationRequestV1{}, "", newIntegrationProtocolError(integrationErrorInvalidJSON, "%v", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var request integrationRequestV1
	if err := decoder.Decode(&request); err != nil {
		return integrationRequestV1{}, "", newIntegrationProtocolError(integrationErrorInvalidJSON, "%v", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return integrationRequestV1{}, "", newIntegrationProtocolError(integrationErrorInvalidJSON, "%v", err)
	}
	if err := validateIntegrationRequestV1(request); err != nil {
		return integrationRequestV1{}, "", err
	}

	sum := sha256.Sum256(data)
	return request, "sha256:" + hex.EncodeToString(sum[:]), nil
}

func validateIntegrationRequestV1(request integrationRequestV1) error {
	if request.Schema != integrationRequestSchemaV1 {
		return newIntegrationProtocolError(integrationErrorInvalidRequest, "schema must be %q", integrationRequestSchemaV1)
	}
	if !integrationOperationIDRE.MatchString(request.OperationID) {
		return newIntegrationProtocolError(integrationErrorInvalidRequest, "operationId must match %s", integrationOperationIDRE.String())
	}
	if err := validateWorkspaceHandle(request.Target.ExpectedWorkspace); err != nil {
		return newIntegrationProtocolError(integrationErrorInvalidRequest, "invalid target expectedWorkspace: %v", err)
	}
	if !integrationCommitIDRE.MatchString(request.Target.ExpectedHeadCommit) {
		return newIntegrationProtocolError(integrationErrorInvalidRequest, "target expectedHeadCommit must be a full 40-character lowercase hexadecimal commit id")
	}

	switch request.Strategy {
	case integrationStrategySingle:
		if len(request.Payloads) != 1 {
			return newIntegrationProtocolError(integrationErrorInvalidRequest, "single strategy requires exactly one payload")
		}
	case integrationStrategyProviderDefault, integrationStrategyOrderedLine:
		if len(request.Payloads) == 0 {
			return newIntegrationProtocolError(integrationErrorInvalidRequest, "%s strategy requires at least one payload", request.Strategy)
		}
	default:
		return newIntegrationProtocolError(integrationErrorInvalidRequest, "unsupported strategy %q", request.Strategy)
	}

	if len(request.Payloads) > integrationMaxPayloads {
		return newIntegrationProtocolError(integrationErrorInvalidRequest, "payloads exceeds the maximum of %d", integrationMaxPayloads)
	}

	seen := make(map[string]struct{}, len(request.Payloads))
	for i, payload := range request.Payloads {
		if err := validateWorkspaceHandle(payload.Workspace); err != nil {
			return newIntegrationProtocolError(integrationErrorInvalidRequest, "invalid payload %d workspace: %v", i+1, err)
		}
		if payload.Workspace == request.Target.ExpectedWorkspace {
			return newIntegrationProtocolError(integrationErrorInvalidRequest, "payload workspace %q is the target workspace", payload.Workspace)
		}
		if _, ok := seen[payload.Workspace]; ok {
			return newIntegrationProtocolError(integrationErrorInvalidRequest, "duplicate payload workspace %q", payload.Workspace)
		}
		seen[payload.Workspace] = struct{}{}
		if !integrationCommitIDRE.MatchString(payload.ExpectedHeadCommit) {
			return newIntegrationProtocolError(integrationErrorInvalidRequest, "payload %q expectedHeadCommit must be a full 40-character lowercase hexadecimal commit id", payload.Workspace)
		}
	}
	return nil
}

func validateIntegrationOperationReuse(record integrationOperationRecord, requestDigest string) error {
	if record.RequestDigest == requestDigest {
		return nil
	}
	return newIntegrationProtocolError(
		integrationErrorOperationIDContradiction,
		"operationId %q is already bound to a different request digest",
		record.OperationID,
	)
}

func validateIntegrationRequestJSONKeys(data []byte) error {
	top, err := decodeExactJSONObject(data, "request", []string{"schema", "operationId", "target", "strategy", "payloads"})
	if err != nil {
		return err
	}
	if raw, ok := top["target"]; ok {
		if _, err := decodeExactJSONObject(raw, "target", []string{"expectedWorkspace", "expectedHeadCommit"}); err != nil {
			return err
		}
	}
	if raw, ok := top["payloads"]; ok {
		var payloads []json.RawMessage
		if err := json.Unmarshal(raw, &payloads); err != nil {
			return fmt.Errorf("payloads must be a JSON array: %w", err)
		}
		for i, payload := range payloads {
			if _, err := decodeExactJSONObject(payload, fmt.Sprintf("payload %d", i+1), []string{"workspace", "expectedHeadCommit"}); err != nil {
				return err
			}
		}
	}
	return nil
}

func decodeExactJSONObject(data []byte, label string, allowed []string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return nil, fmt.Errorf("%s must be a JSON object: %w", label, err)
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = struct{}{}
	}
	unknown := make([]string, 0)
	for key := range object {
		if _, ok := allowedSet[key]; !ok {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("%s contains unknown field %q", label, unknown[0])
	}
	return object, nil
}

func validateSingleJSONValueWithoutDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := consumeJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err == nil {
		return errors.New("request contains trailing JSON input")
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("read trailing JSON input: %w", err)
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}

	switch delim {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate JSON object key %q", key)
			}
			seen[key] = struct{}{}
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return errors.New("JSON object is not terminated")
		}
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return errors.New("JSON array is not terminated")
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return fmt.Errorf("read trailing JSON input: %w", err)
	}
	return errors.New("request contains trailing JSON input")
}

func resolveIntegrationTargetWorkspace(repoRoot string, refs []workspaceRef, expectedWorkspace string) (integrationTargetResolution, error) {
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

	if len(matches) == 0 {
		return integrationTargetResolution{}, newIntegrationProtocolError(integrationErrorTargetResolution, "current path %s is not an exact registered Workspace root", canonicalRepoRoot)
	}
	if len(matches) > 1 {
		handles := make([]string, 0, len(matches))
		for _, match := range matches {
			handles = append(handles, match.Handle)
		}
		return integrationTargetResolution{}, newIntegrationProtocolError(integrationErrorTargetResolution, "current path %s matches multiple Workspaces: %s", canonicalRepoRoot, strings.Join(handles, ", "))
	}
	if matches[0].Handle != expectedWorkspace {
		return integrationTargetResolution{}, newIntegrationProtocolError(
			integrationErrorAssertionFailed,
			"Current Workspace is %q, request asserted %q",
			matches[0].Handle,
			expectedWorkspace,
		)
	}
	return matches[0], nil
}

func integrationStatePaths(mainWorkspacePath, targetWorkspacePath string) (integrationStateBinding, error) {
	canonicalProjectPath, err := canonicalExistingDirectory(mainWorkspacePath)
	if err != nil {
		return integrationStateBinding{}, fmt.Errorf("resolve configured Main Workspace state root: %w", err)
	}
	canonicalTargetPath, err := canonicalExistingDirectory(targetWorkspacePath)
	if err != nil {
		return integrationStateBinding{}, fmt.Errorf("resolve integration target Workspace root: %w", err)
	}
	return integrationStateBinding{
		CanonicalProjectPath: canonicalProjectPath,
		CanonicalTargetPath:  canonicalTargetPath,
		StateDir:             filepath.Join(canonicalProjectPath, ".ajj", "integrations"),
	}, nil
}

func canonicalExistingDirectory(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", errors.New("path is empty")
	}
	absolute, err := filepath.Abs(trimmed)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", canonical)
	}
	return filepath.Clean(canonical), nil
}
