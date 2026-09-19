package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Policy edits intentionally do not move JJ's operation ID. Bind the reviewed
// intent and local identity separately; never hold a policy lock across prompts.
type tidyPolicyState struct {
	Policy  string
	Root    string
	Token   string
	Missing bool
}

type tidyPolicyReview struct {
	repo    string
	project string
	states  map[string]tidyPolicyState
}

func tidyPolicyChanged(err error) error {
	const message = "Tidy cleanup policy or identity changed after review; rerun Tidy and review the new state"
	if err != nil {
		return fmt.Errorf("%s: %w", message, err)
	}
	return errors.New(message)
}

func readTidyPolicyState(st workspacePolicyStore, info workspaceInfo) (tidyPolicyState, error) {
	state := tidyPolicyState{Policy: policyKeep, Root: info.Path, Missing: !workspacePathExists(info.Path)}
	if !state.Missing {
		_, err := os.Lstat(filepath.Join(info.Path, ".jj"))
		if errors.Is(err, os.ErrNotExist) {
			state.Missing = true
		} else if err != nil {
			return state, err
		}
	}
	if state.Missing {
		return state, nil
	}
	var err error
	state.Root, err = canonicalExistingDirectory(info.Path)
	if err != nil {
		return state, err
	}
	state.Token, err = readPolicyToken(filepath.Join(state.Root, ".jj", policyTokenFile))
	if err != nil {
		return state, err
	}
	if record, ok := st.Disposable[info.Ref.Handle]; ok && record.Root == state.Root && state.Token != "" && record.Token == state.Token {
		state.Policy = policyDisposable
	}
	return state, nil
}

func newTidyPolicyReview(repo, project string, infos []workspaceInfo) (*tidyPolicyReview, error) {
	review := &tidyPolicyReview{repo: repo, project: project, states: map[string]tidyPolicyState{}}
	if len(infos) == 0 {
		return review, nil
	}
	path, err := policyStorePath(repo, project)
	if err != nil {
		return nil, err
	}
	st, err := readPolicyStore(path)
	if err != nil {
		return nil, err
	}
	for _, info := range infos {
		if info.Main || info.Current {
			continue
		}
		state, err := readTidyPolicyState(st, info)
		if err != nil {
			return nil, err
		}
		// Do not silently adopt a concurrent revocation between loading the rows and
		// establishing their review (especially for automatically checked targets).
		if state.Policy != emptyDefault(info.Policy, policyKeep) || state.Missing != info.Missing {
			return nil, tidyPolicyChanged(nil)
		}
		review.states[info.Ref.Handle] = state
	}
	return review, nil
}

// The TUI's explicit p action is itself a policy review. Accept its successful
// persisted state before submission, but do not refresh other rows' baselines.
func (r *tidyPolicyReview) setPolicy(info workspaceInfo, policy string) error {
	if err := r.revalidate([]workspaceInfo{info}); err != nil {
		return err
	}
	if err := setWorkspacePolicies(r.repo, r.project, []workspaceInfo{info}, policy); err != nil {
		return err
	}
	info.Policy = policy
	updated, err := newTidyPolicyReview(r.repo, r.project, []workspaceInfo{info})
	if err != nil {
		return err
	}
	r.states[info.Ref.Handle] = updated.states[info.Ref.Handle]
	return nil
}

func (r *tidyPolicyReview) revalidate(targets []workspaceInfo) error {
	path, err := policyStorePath(r.repo, r.project)
	if err != nil {
		return tidyPolicyChanged(err)
	}
	st, err := readPolicyStore(path)
	if err != nil {
		return tidyPolicyChanged(err)
	}
	for _, info := range targets {
		current, err := readTidyPolicyState(st, info)
		if err != nil {
			return tidyPolicyChanged(err)
		}
		reviewed, ok := r.states[info.Ref.Handle]
		if !ok || current != reviewed {
			return tidyPolicyChanged(fmt.Errorf("Workspace %q", info.Ref.Handle))
		}
	}
	return nil
}
