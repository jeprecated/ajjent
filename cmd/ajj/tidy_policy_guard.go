package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Policy edits and cleanup-rule edits intentionally do not move JJ's operation
// ID. Bind the reviewed intent, local identity, and matched config rule
// separately; never hold a policy lock across prompts.
type tidyPolicyState struct {
	Policy  string
	Rule    string
	Root    string
	Token   string
	Missing bool
}

type tidyPolicyReview struct {
	repo    string
	project string
	rules   []cleanupRule
	states  map[string]tidyPolicyState
}

func tidyPolicyChanged(err error) error {
	const message = "Tidy cleanup policy or identity changed after review; rerun Tidy and review the new state"
	if err != nil {
		return fmt.Errorf("%s: %w", message, err)
	}
	return errors.New(message)
}

// currentCleanupRules reloads the same merged config the command loaded, so
// revoking or flipping a rule during a confirmation window is detected drift.
// An unreadable or now-invalid config fails closed as drift, never as Keep.
func currentCleanupRules(repo string) ([]cleanupRule, error) {
	cfg, err := loadConfig(repo)
	if err != nil {
		return nil, err
	}
	return cfg.Cleanup.Rules, nil
}

func readTidyPolicyState(st workspacePolicyStore, rules []cleanupRule, info workspaceInfo) (tidyPolicyState, error) {
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
	policy, matchedRule, err := workspacePolicy(st, rules, info)
	if err != nil {
		return state, err
	}
	state.Policy = policy
	state.Rule = matchedRule
	var rootErr error
	state.Root, rootErr = canonicalExistingDirectory(info.Path)
	if rootErr != nil {
		return state, rootErr
	}
	state.Token, rootErr = readPolicyToken(filepath.Join(state.Root, ".jj", policyTokenFile))
	if rootErr != nil {
		return state, rootErr
	}
	return state, nil
}

func newTidyPolicyReview(repo, project string, rules []cleanupRule, infos []workspaceInfo) (*tidyPolicyReview, error) {
	review := &tidyPolicyReview{repo: repo, project: project, rules: rules, states: map[string]tidyPolicyState{}}
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
		state, err := readTidyPolicyState(st, rules, info)
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
	rules, err := currentCleanupRules(r.repo)
	if err != nil {
		return err
	}
	info.Policy = policy
	updated, err := newTidyPolicyReview(r.repo, r.project, rules, []workspaceInfo{info})
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
	rules, err := currentCleanupRules(r.repo)
	if err != nil {
		return tidyPolicyChanged(err)
	}
	for _, info := range targets {
		current, err := readTidyPolicyState(st, rules, info)
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
