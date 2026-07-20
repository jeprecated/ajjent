package main

import (
	"errors"
	"reflect"
	"sort"
	"strings"
)

const integrationMaxRepositoryEvidenceItems = 4096

type integrationOperationEvidenceV1 struct {
	OperationID        string   `json:"operationId"`
	ParentOperationIDs []string `json:"parentOperationIds"`
}

type integrationWorkspaceHeadEvidenceV1 struct {
	Workspace string `json:"workspace"`
	CommitID  string `json:"commitId"`
	ChangeID  string `json:"changeId"`
}

type integrationTargetCommitEvidenceV1 struct {
	CommitID        string   `json:"commitId"`
	ChangeID        string   `json:"changeId"`
	ParentCommitIDs []string `json:"parentCommitIds"`
}

type integrationRefEvidenceV1 struct {
	Name   string `json:"name"`
	Target string `json:"target"`
}

type integrationRepositoryViewV1 struct {
	Workspaces   []integrationWorkspaceHeadEvidenceV1 `json:"workspaces"`
	VisibleHeads []string                             `json:"visibleHeads"`
	Bookmarks    []integrationRefEvidenceV1           `json:"bookmarks"`
	Tags         []integrationRefEvidenceV1           `json:"tags"`
	Target       integrationTargetCommitEvidenceV1    `json:"target"`
}

func integrationOperationEvidence(repoPath, operationID string) (integrationOperationEvidenceV1, error) {
	out, err := integrationQuery(repoPath, operationID, "op", "log", "-n", "1", "--no-graph", "-T", `id ++ "\t" ++ parents.map(|p| p.id()).join(",") ++ "\n"`)
	if err != nil {
		return integrationOperationEvidenceV1{}, err
	}
	line := strings.TrimSpace(out)
	parts := strings.SplitN(line, "\t", 2)
	if len(parts) != 2 || !integrationFullOperationIDRE.MatchString(parts[0]) || parts[0] != operationID {
		return integrationOperationEvidenceV1{}, errors.New("Jujutsu returned invalid operation evidence")
	}
	parents := []string{}
	if parts[1] != "" {
		parents = strings.Split(parts[1], ",")
	}
	for _, parent := range parents {
		if !integrationFullOperationIDRE.MatchString(parent) {
			return integrationOperationEvidenceV1{}, errors.New("Jujutsu returned invalid operation parent evidence")
		}
	}
	return integrationOperationEvidenceV1{OperationID: parts[0], ParentOperationIDs: parents}, nil
}

func integrationRepositoryViewAtOperation(repoPath, operationID, targetHandle string) (integrationRepositoryViewV1, error) {
	workspaces, err := integrationWorkspaceHeadsAtOperation(repoPath, operationID)
	if err != nil {
		return integrationRepositoryViewV1{}, err
	}
	visible, err := integrationCommitIDsAtOperation(repoPath, operationID, "visible_heads()")
	if err != nil {
		return integrationRepositoryViewV1{}, err
	}
	sort.Strings(visible)
	bookmarks, err := integrationRefsAtOperation(repoPath, operationID, "bookmark")
	if err != nil {
		return integrationRepositoryViewV1{}, err
	}
	tags, err := integrationRefsAtOperation(repoPath, operationID, "tag")
	if err != nil {
		return integrationRepositoryViewV1{}, err
	}
	target, err := integrationTargetCommitEvidenceAtOperation(repoPath, operationID, targetHandle)
	if err != nil {
		return integrationRepositoryViewV1{}, err
	}
	view := integrationRepositoryViewV1{Workspaces: workspaces, VisibleHeads: visible, Bookmarks: bookmarks, Tags: tags, Target: target}
	if err := validateIntegrationRepositoryView(view); err != nil {
		return integrationRepositoryViewV1{}, err
	}
	return view, nil
}

func integrationWorkspaceHeadsAtOperation(repoPath, operationID string) ([]integrationWorkspaceHeadEvidenceV1, error) {
	out, err := integrationQuery(repoPath, operationID, "workspace", "list", "-T", `name ++ "\t" ++ target.commit_id() ++ "\t" ++ target.change_id() ++ "\n"`)
	if err != nil {
		return nil, err
	}
	items := []integrationWorkspaceHeadEvidenceV1{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 || validateWorkspaceHandle(parts[0]) != nil || !integrationCommitIDRE.MatchString(parts[1]) || !integrationFullChangeIDRE.MatchString(parts[2]) {
			return nil, errors.New("Jujutsu returned invalid workspace-head evidence")
		}
		items = append(items, integrationWorkspaceHeadEvidenceV1{Workspace: parts[0], CommitID: parts[1], ChangeID: parts[2]})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Workspace < items[j].Workspace })
	if len(items) == 0 || len(items) > integrationMaxRepositoryEvidenceItems {
		return nil, errors.New("workspace-head evidence count is invalid")
	}
	for i := 1; i < len(items); i++ {
		if items[i-1].Workspace == items[i].Workspace {
			return nil, errors.New("workspace-head evidence contains a duplicate")
		}
	}
	return items, nil
}

func integrationRefsAtOperation(repoPath, operationID, kind string) ([]integrationRefEvidenceV1, error) {
	if kind != "bookmark" && kind != "tag" {
		return nil, errors.New("invalid ref evidence kind")
	}
	args := integrationQueryArgs(repoPath, operationID, kind, "list")
	if kind == "bookmark" {
		args = append(args, "--all")
	}
	args = append(args, "-T", `name ++ "\t" ++ if(normal_target, normal_target.commit_id(), "conflict") ++ "\n"`)
	out, err := commandCaptureFn("jj", args...)
	if err != nil {
		return nil, err
	}
	items := []integrationRefEvidenceV1{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || (parts[1] != "conflict" && !integrationCommitIDRE.MatchString(parts[1])) {
			return nil, errors.New("Jujutsu returned invalid ref evidence")
		}
		items = append(items, integrationRefEvidenceV1{Name: parts[0], Target: parts[1]})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Name == items[j].Name {
			return items[i].Target < items[j].Target
		}
		return items[i].Name < items[j].Name
	})
	if len(items) > integrationMaxRepositoryEvidenceItems {
		return nil, errors.New("ref evidence count exceeds the limit")
	}
	return items, nil
}

func integrationTargetCommitEvidenceAtOperation(repoPath, operationID, targetHandle string) (integrationTargetCommitEvidenceV1, error) {
	out, err := integrationQuery(repoPath, operationID, "log", "-r", targetHandle+"@", "--no-graph", "-T", `commit_id ++ "\t" ++ change_id ++ "\t" ++ parents.map(|p| p.commit_id()).join(",") ++ "\n"`)
	if err != nil {
		return integrationTargetCommitEvidenceV1{}, err
	}
	parts := strings.SplitN(strings.TrimSpace(out), "\t", 3)
	if len(parts) != 3 || !integrationCommitIDRE.MatchString(parts[0]) || !integrationFullChangeIDRE.MatchString(parts[1]) {
		return integrationTargetCommitEvidenceV1{}, errors.New("Jujutsu returned invalid target commit evidence")
	}
	parents := []string{}
	if parts[2] != "" {
		parents = strings.Split(parts[2], ",")
	}
	for _, parent := range parents {
		if !integrationCommitIDRE.MatchString(parent) {
			return integrationTargetCommitEvidenceV1{}, errors.New("Jujutsu returned invalid target parent evidence")
		}
	}
	return integrationTargetCommitEvidenceV1{CommitID: parts[0], ChangeID: parts[1], ParentCommitIDs: parents}, nil
}

func validateIntegrationRepositoryView(view integrationRepositoryViewV1) error {
	if len(view.Workspaces) == 0 || len(view.Workspaces) > integrationMaxRepositoryEvidenceItems || len(view.VisibleHeads) > integrationMaxRepositoryEvidenceItems || len(view.Bookmarks) > integrationMaxRepositoryEvidenceItems || len(view.Tags) > integrationMaxRepositoryEvidenceItems {
		return errors.New("integration repository evidence count is invalid")
	}
	if !integrationCommitIDRE.MatchString(view.Target.CommitID) || !integrationFullChangeIDRE.MatchString(view.Target.ChangeID) {
		return errors.New("integration target evidence is invalid")
	}
	for _, id := range append(append([]string(nil), view.VisibleHeads...), view.Target.ParentCommitIDs...) {
		if !integrationCommitIDRE.MatchString(id) {
			return errors.New("integration repository commit evidence is invalid")
		}
	}
	for i, item := range view.Workspaces {
		if validateWorkspaceHandle(item.Workspace) != nil || !integrationCommitIDRE.MatchString(item.CommitID) || !integrationFullChangeIDRE.MatchString(item.ChangeID) || (i > 0 && view.Workspaces[i-1].Workspace >= item.Workspace) {
			return errors.New("integration workspace evidence is invalid")
		}
	}
	for _, refs := range [][]integrationRefEvidenceV1{view.Bookmarks, view.Tags} {
		for _, ref := range refs {
			if ref.Name == "" || (ref.Target != "conflict" && !integrationCommitIDRE.MatchString(ref.Target)) {
				return errors.New("integration ref evidence is invalid")
			}
		}
	}
	return nil
}

func integrationRepositoryViewsEqual(left, right integrationRepositoryViewV1) bool {
	return reflect.DeepEqual(left, right)
}

func validatePreparedStateAgainstRepositoryView(prepared integrationPreparedStateV1, view integrationRepositoryViewV1) error {
	if err := validateIntegrationRepositoryView(view); err != nil {
		return err
	}
	heads := make(map[string]string, len(view.Workspaces))
	for _, item := range view.Workspaces {
		heads[item.Workspace] = item.CommitID
	}
	if heads[prepared.Target.Workspace] != prepared.Target.HeadCommit || view.Target.CommitID != prepared.Target.HeadCommit {
		return errors.New("integration repository evidence does not bind the prepared target")
	}
	for _, payload := range prepared.Payloads {
		if heads[payload.Workspace] != payload.HeadCommit {
			return errors.New("integration repository evidence does not bind a prepared payload")
		}
	}
	return nil
}

func proveDetachedIntegrationTransaction(repoPath string, record integrationOperationRecord, request integrationRequestV1) error {
	if len(record.DetachedOperationIDs) == 0 || record.GraphOperationID != record.DetachedOperationIDs[len(record.DetachedOperationIDs)-1] {
		return errors.New("detached integration operation chain is incomplete")
	}
	expectedParent := record.BeforeOperationID
	seen := map[string]struct{}{}
	for _, operationID := range record.DetachedOperationIDs {
		if _, duplicate := seen[operationID]; duplicate {
			return errors.New("detached integration operation chain contains a duplicate")
		}
		seen[operationID] = struct{}{}
		evidence, err := integrationOperationEvidence(repoPath, operationID)
		if err != nil || len(evidence.ParentOperationIDs) != 1 || evidence.ParentOperationIDs[0] != expectedParent {
			return errors.New("detached integration operation chain ancestry is invalid")
		}
		expectedParent = operationID
	}
	before, err := integrationRepositoryViewAtOperation(repoPath, record.BeforeOperationID, request.Target.ExpectedWorkspace)
	if err != nil || !integrationRepositoryViewsEqual(before, record.BeforeRepositoryView) {
		return errors.New("detached integration before-state evidence is invalid")
	}
	staged, err := integrationRepositoryViewAtOperation(repoPath, record.GraphOperationID, request.Target.ExpectedWorkspace)
	if err != nil || !integrationRepositoryViewsEqual(staged, record.StagedRepositoryView) {
		return errors.New("detached integration staged repository evidence is invalid")
	}
	if err := validateModeledIntegrationTransition(before, staged, request); err != nil {
		return err
	}
	state, err := detachedTargetState(repoPath, record.GraphOperationID, request.Target.ExpectedWorkspace)
	if err != nil || record.StagedTargetState == nil || state != *record.StagedTargetState {
		return errors.New("detached integration staged target evidence is invalid")
	}
	mappings, err := proveIntegrationPayloadMappingsAtOperation(repoPath, record.GraphOperationID, request.Target.ExpectedWorkspace, record.PreparedState)
	if err != nil || !integrationPayloadMappingsEqual(mappings, record.StagedPayloadMappings) {
		return errors.New("detached integration staged mapping evidence is invalid")
	}
	if request.Strategy == integrationStrategyOrderedLine {
		if err := proveOrderedLineAtOperation(repoPath, record.GraphOperationID, record, request); err != nil {
			return err
		}
	}
	return nil
}

func proveOrderedLineAtOperation(repoPath, operationID string, record integrationOperationRecord, request integrationRequestV1) error {
	if len(record.PreparedState.Target.FrontierCommits) == 0 || len(record.PreparedState.Payloads) != len(request.Payloads) {
		return errors.New("ordered-line evidence is incomplete")
	}
	if err := provePreparedOrderedLineEvidence(repoPath, record, request); err != nil {
		return err
	}
	mappings, err := proveIntegrationPayloadMappingsAtOperation(repoPath, operationID, request.Target.ExpectedWorkspace, record.PreparedState)
	if err != nil {
		return errors.New("ordered-line mappings cannot be reproduced")
	}
	destination := append([]string(nil), record.PreparedState.Target.FrontierCommits...)
	finalTip := ""
	for i, payload := range record.PreparedState.Payloads {
		source := integrationPreparedChangeRevset(payload.Changes)
		if source == "" || len(payload.FrontierCommits) != 1 {
			return errors.New("ordered-line payload evidence is not singular")
		}
		anchored, err := orderedLineContributionAnchoredAtOperation(repoPath, operationID, source, destination)
		if err != nil || !anchored {
			return errors.New("ordered-line payload is not anchored to its exact ordered predecessor")
		}
		tips, err := integrationCommitIDsAtOperation(repoPath, operationID, "heads("+source+")")
		if err != nil || len(tips) != 1 {
			return errors.New("ordered-line payload has no exact landed tip")
		}
		inputTip := payload.FrontierCommits[0]
		landedTip := ""
		for _, mapping := range mappings[i] {
			if mapping.InputCommit == inputTip {
				landedTip = mapping.LandedCommit
				break
			}
		}
		if landedTip == "" || tips[0] != landedTip {
			return errors.New("ordered-line payload tip does not match its one-to-one mapping")
		}
		finalTip = landedTip
		destination = []string{finalTip}
	}
	if finalTip == "" {
		return errors.New("ordered-line has no final integrated tip")
	}
	state, err := detachedTargetState(repoPath, operationID, request.Target.ExpectedWorkspace)
	if err != nil || state.IntegratedTipCommit != finalTip || state.AfterHeadCommit == record.PreparedState.Target.HeadCommit {
		return errors.New("ordered-line target cursor does not bind the final line tip")
	}
	if err := proveFreshOrderedLineCursorAtOperation(repoPath, operationID, request.Target.ExpectedWorkspace, state.AfterHeadCommit, finalTip); err != nil {
		return errors.New("ordered-line target cursor is not the exact fresh cursor")
	}
	for _, payload := range request.Payloads {
		if err := proveFreshOrderedLineCursorAtOperation(repoPath, operationID, payload.Workspace, "", finalTip); err != nil {
			return errors.New("ordered-line selected payload cursor is not exactly reconciled")
		}
	}
	return nil
}

func proveFreshOrderedLineCursorAtOperation(repoPath, operationID, workspace, expectedHead, expectedParent string) error {
	heads, err := integrationCommitIDsAtOperation(repoPath, operationID, workspace+"@")
	if err != nil || len(heads) != 1 || (expectedHead != "" && heads[0] != expectedHead) {
		return errors.New("ordered-line cursor head is not exact")
	}
	parents, err := integrationCommitIDsAtOperation(repoPath, operationID, "parents("+workspace+"@)")
	if err != nil || len(parents) != 1 || parents[0] != expectedParent {
		return errors.New("ordered-line cursor parent is not exact")
	}
	empty, err := integrationRevisionMatchesAtOperation(repoPath, operationID, "empty() & "+workspace+"@")
	if err != nil || !empty {
		return errors.New("ordered-line cursor is not empty")
	}
	conflicted, err := integrationRevisionMatchesAtOperation(repoPath, operationID, "conflicts() & "+workspace+"@")
	if err != nil || conflicted {
		return errors.New("ordered-line cursor is conflicted")
	}
	mutable, err := integrationRevisionMatchesAtOperation(repoPath, operationID, "mutable() & "+workspace+"@")
	if err != nil || !mutable {
		return errors.New("ordered-line cursor is immutable")
	}
	description, err := integrationQuery(repoPath, operationID, "log", "-r", workspace+"@", "--no-graph", "-T", "description")
	if err != nil || description != "" {
		return errors.New("ordered-line cursor description is not exactly empty")
	}
	return nil
}

func provePreparedOrderedLineEvidence(repoPath string, record integrationOperationRecord, request integrationRequestV1) error {
	frontier, err := integrationCommitIDsAtOperation(repoPath, record.BeforeOperationID, "heads(::"+request.Target.ExpectedWorkspace+"@ & ~empty())")
	sort.Strings(frontier)
	if err != nil || !reflect.DeepEqual(frontier, record.PreparedState.Target.FrontierCommits) {
		return errors.New("ordered-line prepared target frontier cannot be reproduced")
	}
	baseWorkspace := request.Target.ExpectedWorkspace
	seen := map[string]struct{}{}
	for i, requested := range request.Payloads {
		changes, payloadFrontier, err := materializeIntegrationPayloadChangesAtOperation(repoPath, record.BeforeOperationID, baseWorkspace, requested.Workspace)
		if err != nil || i >= len(record.PreparedState.Payloads) {
			return errors.New("ordered-line prepared payload contribution cannot be reproduced")
		}
		prepared := record.PreparedState.Payloads[i]
		if !reflect.DeepEqual(changes, prepared.Changes) || !reflect.DeepEqual(payloadFrontier, prepared.FrontierCommits) {
			return errors.New("ordered-line prepared payload contribution differs from before-state graph")
		}
		for _, change := range changes {
			if _, duplicate := seen[change.ChangeID]; duplicate {
				return errors.New("ordered-line prepared payload contributions overlap")
			}
			seen[change.ChangeID] = struct{}{}
		}
		baseWorkspace = requested.Workspace
	}
	return nil
}

func proveTerminalIntegrationRecord(repoPath string, record integrationOperationRecord, request integrationRequestV1) error {
	if record.Receipt == nil || record.Receipt.BatchDisposition != integrationBatchSucceeded || record.CommitPointOperation == "" || record.CommitPointOperation != record.GraphOperationID {
		return errors.New("terminal integration record has no successful commit point")
	}
	if err := proveDetachedIntegrationTransaction(repoPath, record, request); err != nil {
		return err
	}
	state, err := detachedTargetState(repoPath, record.CommitPointOperation, request.Target.ExpectedWorkspace)
	if err != nil || record.TargetAdvancedState == nil || state != *record.TargetAdvancedState {
		return errors.New("terminal integration target evidence cannot be reproduced")
	}
	mappings, err := proveIntegrationPayloadMappingsAtOperation(repoPath, record.CommitPointOperation, request.Target.ExpectedWorkspace, record.PreparedState)
	if err != nil || !integrationPayloadMappingsEqual(mappings, record.StagedPayloadMappings) {
		return errors.New("terminal integration payload mappings cannot be reproduced")
	}
	for i, payload := range record.Receipt.Payloads {
		if i >= len(mappings) || !reflect.DeepEqual(payload.Changes, mappings[i]) {
			return errors.New("terminal receipt mappings do not match durable graph evidence")
		}
	}
	return nil
}

func validateModeledIntegrationTransition(before, staged integrationRepositoryViewV1, request integrationRequestV1) error {
	if !reflect.DeepEqual(before.Bookmarks, staged.Bookmarks) || !reflect.DeepEqual(before.Tags, staged.Tags) {
		return errors.New("detached integration changed unrelated refs")
	}
	allowed := map[string]struct{}{request.Target.ExpectedWorkspace: {}}
	for _, payload := range request.Payloads {
		allowed[payload.Workspace] = struct{}{}
	}
	beforeHeads := map[string]string{}
	stagedHeads := map[string]string{}
	for _, item := range before.Workspaces {
		beforeHeads[item.Workspace] = item.CommitID
	}
	for _, item := range staged.Workspaces {
		stagedHeads[item.Workspace] = item.CommitID
	}
	if len(beforeHeads) != len(stagedHeads) {
		return errors.New("detached integration changed workspace registrations")
	}
	oldAllowed := map[string]struct{}{}
	newAllowed := map[string]struct{}{}
	for handle, beforeID := range beforeHeads {
		afterID, ok := stagedHeads[handle]
		if !ok {
			return errors.New("detached integration changed workspace registrations")
		}
		if _, ok := allowed[handle]; ok {
			oldAllowed[beforeID] = struct{}{}
			newAllowed[afterID] = struct{}{}
		} else if request.Strategy == integrationStrategyOrderedLine && beforeID != afterID {
			return errors.New("ordered-line integration changed an omitted workspace head")
		} else if beforeID != afterID {
			var beforeChange, afterChange string
			for _, item := range before.Workspaces {
				if item.Workspace == handle {
					beforeChange = item.ChangeID
					break
				}
			}
			for _, item := range staged.Workspaces {
				if item.Workspace == handle {
					afterChange = item.ChangeID
					break
				}
			}
			if beforeChange == "" || beforeChange != afterChange {
				return errors.New("detached integration changed an unrelated workspace identity")
			}
			oldAllowed[beforeID] = struct{}{}
			newAllowed[afterID] = struct{}{}
		}
	}
	beforeVisible := make(map[string]struct{}, len(before.VisibleHeads))
	stagedVisible := make(map[string]struct{}, len(staged.VisibleHeads))
	for _, id := range before.VisibleHeads {
		beforeVisible[id] = struct{}{}
	}
	for _, id := range staged.VisibleHeads {
		stagedVisible[id] = struct{}{}
	}
	for id := range beforeVisible {
		if _, kept := stagedVisible[id]; !kept {
			if _, allowed := oldAllowed[id]; !allowed {
				return errors.New("detached integration removed an unrelated visible head")
			}
		}
	}
	for id := range stagedVisible {
		if _, existed := beforeVisible[id]; !existed {
			if _, allowed := newAllowed[id]; !allowed {
				return errors.New("detached integration added an unrelated visible head")
			}
		}
	}
	return nil
}
