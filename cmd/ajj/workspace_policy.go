package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const policyKeep = "Keep"
const policyDisposable = "Disposable"
const policyTokenFile = "ajj-workspace-identity"

type workspacePolicyRecord struct {
	Root  string `json:"root"`
	Token string `json:"token"`
}
type workspacePolicyStore struct {
	Version    int                              `json:"version"`
	Disposable map[string]workspacePolicyRecord `json:"disposable"`
	Keep       map[string]workspacePolicyRecord `json:"keep,omitempty"`
}

func policyStorePath(repo, project string) (string, error) {
	if err := validateSlug("project", project); err != nil {
		return "", err
	}
	shared, err := workspaceRepositoryDirectory(repo)
	if err != nil {
		return "", err
	}
	return filepath.Join(shared, "ajj-policy", project, "policies.json"), nil
}

func readPolicyStore(path string) (workspacePolicyStore, error) {
	empty := workspacePolicyStore{Version: 1, Disposable: map[string]workspacePolicyRecord{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return empty, err
	}
	if !json.Valid(data) {
		return empty, errors.New("invalid Workspace policy store JSON")
	}
	var st workspacePolicyStore
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&st); err != nil {
		return empty, fmt.Errorf("invalid Workspace policy store: %w", err)
	}
	if st.Version != 1 || st.Disposable == nil {
		return empty, errors.New("unsupported or invalid Workspace policy store")
	}
	for handle, r := range st.Disposable {
		if validateWorkspaceHandle(handle) != nil || !filepath.IsAbs(r.Root) || !validPolicyToken(r.Token) {
			return empty, errors.New("invalid Workspace policy record")
		}
		if _, also := st.Keep[handle]; also {
			return empty, errors.New("Workspace has conflicting policy records")
		}
	}
	for handle, r := range st.Keep {
		if validateWorkspaceHandle(handle) != nil || !filepath.IsAbs(r.Root) || !validPolicyToken(r.Token) {
			return empty, errors.New("invalid Workspace policy record")
		}
	}
	return st, nil
}

func validPolicyToken(token string) bool {
	b, err := hex.DecodeString(token)
	return err == nil && len(b) == 32
}

func readPolicyToken(path string) (string, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("Workspace policy identity must be a regular file")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(b))
	if !validPolicyToken(token) {
		return "", errors.New("invalid Workspace policy identity")
	}
	return token, nil
}

// matchCleanupRule returns the first rule whose pattern matches the Handle.
// Rules are validated at config load; a defensively invalid pattern never matches.
func matchCleanupRule(rules []cleanupRule, handle string) (cleanupRule, bool) {
	for _, rule := range rules {
		if matched, err := path.Match(rule.Match, handle); err == nil && matched {
			return rule, true
		}
	}
	return cleanupRule{}, false
}

// Effective policy: explicit identity-bound records win over cleanup rules,
// rules are Handle-glob defaults for present valid registrations, and anything
// else stays Keep. Discovery never creates tokens or the store.
func workspacePolicy(st workspacePolicyStore, rules []cleanupRule, info workspaceInfo) (string, string, error) {
	if info.Missing {
		return policyKeep, "", nil
	}
	root, err := canonicalExistingDirectory(info.Path)
	if err != nil {
		return "", "", err
	}
	token, err := readPolicyToken(filepath.Join(root, ".jj", policyTokenFile))
	if err != nil {
		return "", "", err
	}
	if token != "" {
		if r, ok := st.Disposable[info.Ref.Handle]; ok && r.Root == root && r.Token == token {
			return policyDisposable, "", nil
		}
		if r, ok := st.Keep[info.Ref.Handle]; ok && r.Root == root && r.Token == token {
			return policyKeep, "", nil
		}
	}
	if rule, ok := matchCleanupRule(rules, info.Ref.Handle); ok {
		return rule.Policy, rule.Match, nil
	}
	return policyKeep, "", nil
}

func loadWorkspacePolicies(repo, project string, rules []cleanupRule, infos []workspaceInfo) error {
	path, err := policyStorePath(repo, project)
	if err != nil {
		return err
	}
	st, err := readPolicyStore(path)
	if err != nil {
		return err
	}
	for i := range infos {
		p, _, err := workspacePolicy(st, rules, infos[i])
		if err != nil {
			return fmt.Errorf("Workspace %q policy: %w", infos[i].Ref.Handle, err)
		}
		infos[i].Policy = p
	}
	return nil
}

// The store lives in shared JJ metadata, separate from NextIndex/Undo. A locked
// read-modify-atomic-rename preserves concurrent policy writes from all Workspaces.
func setWorkspacePolicies(repo, project string, targets []workspaceInfo, policy string) error {
	if policy != policyKeep && policy != policyDisposable {
		return errors.New("invalid Workspace policy")
	}
	path, err := policyStorePath(repo, project)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "policy.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	st, err := readPolicyStore(path)
	if err != nil {
		return err
	}
	refs, err := listWorkspaceRefs(repo)
	if err != nil {
		return err
	}
	registered := map[string]bool{}
	for _, ref := range refs {
		registered[ref.Handle] = true
	}
	for _, target := range targets {
		if !registered[target.Ref.Handle] {
			return fmt.Errorf("Workspace %q is not registered", target.Ref.Handle)
		}
		if policy == policyDisposable {
			if target.Main {
				return errors.New("Main Workspace cannot be marked Disposable")
			}
			if target.Missing {
				return fmt.Errorf("Workspace %q is missing: identity unavailable; keep it Keep and manually select forget-registration in Tidy", target.Ref.Handle)
			}
			if err := validateWorkspaceSnapshotTarget(repo, target, refs); err != nil {
				return err
			}
		}
	}
	if st.Keep == nil {
		st.Keep = map[string]workspacePolicyRecord{}
	}
	for _, target := range targets {
		// Explicit records override cleanup rules for exactly this identity.
		// Keep for a missing registration has no identity to bind, so it clears
		// any Disposable record instead; its effective policy is already Keep.
		if policy == policyKeep {
			delete(st.Disposable, target.Ref.Handle)
			if target.Main || target.Missing {
				delete(st.Keep, target.Ref.Handle)
				continue
			}
			if err := validateWorkspaceSnapshotTarget(repo, target, refs); err != nil {
				return err
			}
		} else {
			delete(st.Keep, target.Ref.Handle)
		}
		root, err := canonicalExistingDirectory(target.Path)
		if err != nil {
			return err
		}
		tokenPath := filepath.Join(root, ".jj", policyTokenFile)
		token, err := readPolicyToken(tokenPath)
		if err != nil {
			return err
		}
		if token == "" {
			b := make([]byte, 32)
			if _, err := rand.Read(b); err != nil {
				return err
			}
			token = hex.EncodeToString(b)
			f, err := os.OpenFile(tokenPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			_, writeErr := f.WriteString(token + "\n")
			syncErr := f.Sync()
			closeErr := f.Close()
			if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
				return err
			}
			if err := syncIntegrationDirectory(filepath.Dir(tokenPath)); err != nil {
				return err
			}
		}
		if policy == policyKeep {
			st.Keep[target.Ref.Handle] = workspacePolicyRecord{Root: root, Token: token}
		} else {
			st.Disposable[target.Ref.Handle] = workspacePolicyRecord{Root: root, Token: token}
		}
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".policy-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
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

func runWorkspacePolicy(args []string, policy string) error {
	args, err := normalizePositionalsLast(args, map[string]struct{}{"--repo": {}, "--project": {}, "--workspaces-root": {}})
	if err != nil {
		return err
	}
	command := strings.ToLower(policy)
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	var repo, project, root string
	fs.StringVar(&repo, "repo", "", "repo root override")
	fs.StringVar(&project, "project", "", "Project override")
	fs.StringVar(&root, "workspaces-root", "", "Workspaces root override")
	if handled, err := parseCommandFlags(fs, args, "ajj "+command+" <handle...> [options]", "Persist Workspace cleanup policy; Disposable opts into automatic Tidy, never bypassing safety."); handled || err != nil {
		return err
	}
	handles, err := canonicalUniqueWorkspaceHandles(fs.Args())
	if err != nil {
		return err
	}
	if len(handles) == 0 {
		return errors.New("provide at least one Workspace Handle")
	}
	repo, cfg, project, err := commandContext(repo, project, root)
	if err != nil {
		return err
	}
	// Policy needs registration/identity, not graph state or Current detection.
	// Avoid graph-loading fallbacks that could snapshot the caller's working copy.
	refs, err := listWorkspaceRefs(repo)
	if err != nil {
		return err
	}
	byHandle := map[string]workspaceRef{}
	for _, ref := range refs {
		byHandle[ref.Handle] = ref
	}
	targets := []workspaceInfo{}
	for _, handle := range handles {
		ref, ok := byHandle[handle]
		if !ok {
			return workspaceNotFoundError(handle)
		}
		path := workspacePathForRef(repo, cfg.WorkspacesRoot, project, ref, "")
		info := workspaceInfo{Ref: ref, Path: path, Main: handle == cfg.MainWorkspace, Missing: !workspacePathExists(path)}
		if _, err := os.Lstat(filepath.Join(path, ".jj")); errors.Is(err, os.ErrNotExist) {
			info.Missing = true
		}
		targets = append(targets, info)
	}
	if err := setWorkspacePolicies(repo, project, targets, policy); err != nil {
		return err
	}
	for _, target := range targets {
		fmt.Fprintf(stderrWriter, "%s: %s (persisted)\n", target.Ref.Handle, policy)
	}
	return nil
}
