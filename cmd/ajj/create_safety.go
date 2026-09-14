package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Private, path-bound evidence, not an operation-ID recovery API. An intent
// without an acknowledged add and exact head never authorizes adoption/re-add.
type createSafetyRecord struct {
	Schema            string `json:"schema"`
	RequestDigest     string `json:"requestDigest"`
	RequestID         string `json:"requestId"`
	Repository        string `json:"repository"`
	Destination       string `json:"destination"`
	Workspace         string `json:"workspace"`
	HeadCommit        string `json:"headCommit,omitempty"`
	BeforeOperationID string `json:"beforeOperationId"`
	AddOperationID    string `json:"addOperationId,omitempty"`
}

type createSafety struct {
	lock   *integrationLock
	path   string
	record createSafetyRecord
	found  bool
}

var saveCreateSafetyRecordFn = saveCreateSafetyRecord

func openCreateSafety(repo string, cfg config, project string, req createRequestV1, digest string) (*createSafety, error) {
	shared, err := workspaceRepositoryDirectory(repo)
	if err != nil {
		return nil, err
	}
	stateDir := filepath.Join(shared, "ajj-create")
	lock, err := acquireIntegrationLock(stateDir)
	if err != nil {
		return nil, err
	}
	s := &createSafety{lock: lock, path: filepath.Join(stateDir, req.Child.Workspace+".json"), record: createSafetyRecord{
		Schema: "ajj-create-evidence-v1", RequestDigest: digest, RequestID: req.RequestID, Repository: shared,
		Destination: filepath.Join(cfg.WorkspacesRoot, project, req.Child.Workspace), Workspace: req.Child.Workspace,
	}}
	ok := false
	defer func() {
		if !ok {
			s.Close()
		}
	}()
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		record, err := loadCreateSafetyRecord(filepath.Join(stateDir, entry.Name()))
		if err != nil {
			return nil, err
		}
		if record.Workspace != req.Child.Workspace && record.RequestID != req.RequestID {
			continue
		}
		expected := s.record
		expected.HeadCommit = record.HeadCommit
		expected.BeforeOperationID, expected.AddOperationID = record.BeforeOperationID, record.AddOperationID
		if !req.NoCleanup || record != expected || entry.Name() != req.Child.Workspace+".json" {
			return nil, errors.New("creation evidence contradicts request")
		}
		if record.HeadCommit != "" {
			if err := validateCreateAddEvidence(repo, record, req.baseCommit()); err != nil {
				return nil, err
			}
		}
		s.record, s.found = record, true
	}
	ok = true
	return s, nil
}

func loadCreateSafetyRecord(path string) (createSafetyRecord, error) {
	var record createSafetyRecord
	f, err := os.Open(path)
	if err != nil {
		return record, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, createMaxRequestBytes+1))
	if err != nil || len(data) > createMaxRequestBytes {
		return record, errors.New("creation evidence unavailable")
	}
	if err := validateSingleJSONValueWithoutDuplicateKeys(data); err != nil {
		return record, err
	}
	if _, err := decodeExactJSONObject(data, "creation evidence", []string{"schema", "requestId", "requestDigest", "repository", "destination", "workspace", "headCommit", "beforeOperationId", "addOperationId"}); err != nil {
		return record, err
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return record, err
	}
	if !integrationFullOperationIDRE.MatchString(record.BeforeOperationID) || (record.HeadCommit == "") != (record.AddOperationID == "") || (record.AddOperationID != "" && !integrationFullOperationIDRE.MatchString(record.AddOperationID)) || record.Schema != "ajj-create-evidence-v1" || !integrationDigestRE.MatchString(record.RequestDigest) || !integrationOperationIDRE.MatchString(record.RequestID) || validateWorkspaceHandle(record.Workspace) != nil || !filepath.IsAbs(record.Repository) || !filepath.IsAbs(record.Destination) || (record.HeadCommit != "" && !revisionCommitIDRE.MatchString(record.HeadCommit)) {
		return record, errors.New("invalid creation evidence")
	}
	return record, nil
}

func (s *createSafety) Close() { _ = s.lock.Close() }

func (s *createSafety) create(repo string, req createRequestV1) error {
	// Persist the intent and containing directories before invoking JJ. Even a
	// failed add can leave registration/files behind; never remove or retry it.
	before, err := currentOperationFullID(repo)
	if err != nil {
		return err
	}
	s.record.BeforeOperationID = before
	if err := createChildAbsentAtOperation(repo, before, req.Child.Workspace); err != nil {
		return err
	}
	if err := saveCreateSafetyRecordFn(s.path, s.record); err != nil {
		return err
	}
	s.found = true
	if err := os.MkdirAll(filepath.Dir(s.record.Destination), 0o755); err != nil {
		return err
	}
	if err := createMachineCommandFn("jj", "-R", repo, "workspace", "add", "--name", req.Child.Workspace, "--revision", req.baseCommit(), s.record.Destination); err != nil {
		return err
	}
	addOperation, err := currentOperationFullID(repo)
	if err != nil {
		return err
	}
	head, err := integrationWorkspaceHeadCommitAtOperation(repo, addOperation, req.Child.Workspace)
	if err != nil {
		return err
	}
	ack := s.record
	ack.HeadCommit, ack.AddOperationID = head, addOperation
	if err := validateCreateAddEvidence(repo, ack, req.baseCommit()); err != nil {
		return err
	}
	if err := saveCreateSafetyRecordFn(s.path, ack); err != nil {
		return err
	}
	s.record = ack
	return nil
}

// JJ 0.43.0 adds a root-based placeholder, then the requested-base cursor.
// Bind that exact two-operation transition, not a later foreign fresh cursor.
// Operation descriptions are never authority, and other shapes fail closed.
func validateCreateAddEvidence(repo string, r createSafetyRecord, base string) error {
	op, err := integrationOperationEvidence(repo, r.AddOperationID)
	if err != nil {
		return err
	}
	if len(op.ParentOperationIDs) != 1 {
		return errors.New("ambiguous add operation")
	}
	registration, err := integrationOperationEvidence(repo, op.ParentOperationIDs[0])
	if err != nil {
		return err
	}
	if len(registration.ParentOperationIDs) != 1 || registration.ParentOperationIDs[0] != r.BeforeOperationID {
		return errors.New("add does not have the tested two-operation ancestry")
	}
	if err := createChildAbsentAtOperation(repo, r.BeforeOperationID, r.Workspace); err != nil {
		return err
	}
	before, err := integrationWorkspaceHeadsAtOperation(repo, r.BeforeOperationID)
	if err != nil {
		return err
	}
	for operation, parent := range map[string]string{registration.OperationID: strings.Repeat("0", 40), r.AddOperationID: base} {
		heads, err := integrationWorkspaceHeadsAtOperation(repo, operation)
		if err != nil {
			return err
		}
		if len(heads) != len(before)+1 {
			return errors.New("unexpected workspace registration transition")
		}
		for _, previous := range before {
			found := false
			for _, head := range heads {
				if head == previous {
					found = true
					break
				}
			}
			if !found {
				return errors.New("another Workspace changed during add")
			}
		}
		parents, err := integrationCommitIDsAtOperation(repo, operation, "parents("+r.Workspace+"@)")
		if err != nil {
			return err
		}
		if len(parents) != 1 || parents[0] != parent {
			return errors.New("unexpected add parent")
		}
		fresh, err := integrationRevisionMatchesAtOperation(repo, operation, r.Workspace+`@ & empty() & description("") & ~conflicts()`)
		if err != nil {
			return err
		}
		if !fresh {
			return errors.New("add cursor is not fresh")
		}
	}
	head, err := integrationWorkspaceHeadCommitAtOperation(repo, r.AddOperationID, r.Workspace)
	if err != nil {
		return err
	}
	if head != r.HeadCommit {
		return errors.New("add head does not match recorded evidence")
	}
	return nil
}

func createChildAbsentAtOperation(repo, operation, child string) error {
	heads, err := integrationWorkspaceHeadsAtOperation(repo, operation)
	if err != nil {
		return err
	}
	for _, head := range heads {
		if head.Workspace == child {
			return errors.New("child already present before add")
		}
	}
	return nil
}

// Atomic replacement with the same file/directory durability sequence used by
// integration records. Only private temporary metadata is removed on failure.
func saveCreateSafetyRecord(path string, record createSafetyRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".create-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncIntegrationDirectory(dir)
}
