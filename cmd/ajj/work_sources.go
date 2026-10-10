package main

import (
	"fmt"
	"sort"
	"strings"
)

// Bookmark Stack Sources (ADR 0016) let Stack incorporate lines of work that
// exist only as jj bookmarks, such as branches fetched from other machines.
// A bookmark source has no directory, Handle, cursor, or cleanup policy: Stack
// uses its exact target commit and never moves, tracks, pushes, or deletes it.

// bookmarkCleanupGuardRevset names commits that automatic empty-commit cleanup
// must not abandon: abandoning a bookmarked commit deletes or moves the bookmark.
const bookmarkCleanupGuardRevset = "(bookmarks() | remote_bookmarks())"

// bookmarkProtectedRevset is history still named by a local or remote bookmark.
// Forced Closing never abandons it; forgetting a bookmark is a separate choice.
const bookmarkProtectedRevset = "::" + bookmarkCleanupGuardRevset

const bookmarkListTemplate = `name ++ "\t" ++ if(remote, remote, "") ++ "\t" ++ if(present, "1", "0") ++ "\t" ++ if(conflict, "1", "0") ++ "\t" ++ if(tracked, "1", "0") ++ "\t" ++ if(normal_target, normal_target.commit_id(), "") ++ "\t" ++ if(normal_target, normal_target.description().first_line(), "") ++ "\n"`

// bookmarkRef is one `jj bookmark list --all-remotes` record: a local bookmark
// (Remote == "") or one remote's recorded position.
type bookmarkRef struct {
	Name        string
	Remote      string
	Present     bool
	Conflict    bool
	Tracked     bool
	Commit      string
	Description string
}

// label is the jj-native name a user types: `name` or `name@remote`.
func (b bookmarkRef) label() string {
	if b.Remote == "" {
		return b.Name
	}
	return b.Name + "@" + b.Remote
}

type bookmarkSource struct {
	Ref bookmarkRef
	// TrackedBy lists remotes tracking this local bookmark at the same commit.
	TrackedBy []string
	// Ahead reports relevant changes not represented in the Stack target.
	Ahead bool
	// Conflict reports conflicted commits among those changes.
	Conflict bool
	// InTrunk reports that every such change is already in trunk(): the target
	// is behind trunk rather than missing this line of work.
	InTrunk bool
}

func (s bookmarkSource) stackable() bool {
	return !s.Ref.Conflict && s.Ref.Commit != "" && s.Ahead
}

// inAll reports whether `--all` and the selector's All row include this
// source. Remote records and lines already in trunk need explicit selection,
// so fetching someone else's branches never widens an ordinary Stack.
func (s bookmarkSource) inAll() bool {
	return s.stackable() && s.Ref.Remote == "" && !s.InTrunk
}

func (s bookmarkSource) status() string {
	switch {
	case s.Ref.Conflict:
		return "ref-conflict"
	case s.Conflict:
		return "conflict"
	case s.InTrunk:
		return "in-trunk"
	default:
		return "unstacked"
	}
}

func (s bookmarkSource) markers() string {
	if s.Ref.Remote == "" {
		return strings.Join(append([]string{"bookmark"}, s.TrackedBy...), ",")
	}
	if s.Ref.Tracked {
		return "remote,tracked"
	}
	return "remote,untracked"
}

func listBookmarkRefs(repoPath string) ([]bookmarkRef, error) {
	out, err := commandCaptureFn("jj", "-R", repoPath, "--color=never", "--no-pager", "--ignore-working-copy", "bookmark", "list", "--all-remotes", "-T", bookmarkListTemplate)
	if err != nil {
		return nil, fmt.Errorf("list bookmarks: %w", err)
	}
	return parseBookmarkRefs(out)
}

func parseBookmarkRefs(out string) ([]bookmarkRef, error) {
	refs := []bookmarkRef{}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 7)
		if len(fields) != 7 || fields[0] == "" {
			return nil, fmt.Errorf("unexpected bookmark list line %q", sanitizeTidyPreview(line))
		}
		commit := fields[5]
		if commit != "" && !revisionCommitIDRE.MatchString(commit) {
			return nil, fmt.Errorf("unexpected commit id %q for bookmark %q", commit, fields[0])
		}
		refs = append(refs, bookmarkRef{
			Name:        fields[0],
			Remote:      fields[1],
			Present:     fields[2] == "1",
			Conflict:    fields[3] == "1",
			Tracked:     fields[4] == "1",
			Commit:      commit,
			Description: sanitizeTidyPreview(fields[6]),
		})
	}
	return refs, nil
}

// candidateBookmarkRefs drops records that are not separate lines of work:
// the colocated `@git` mirror, absent records, and remote records at the same
// commit as their local bookmark (shown as that bookmark's tracking marker).
func candidateBookmarkRefs(refs []bookmarkRef) []bookmarkSource {
	local := map[string]bookmarkRef{}
	for _, ref := range refs {
		if ref.Remote == "" && ref.Present {
			local[ref.Name] = ref
		}
	}
	trackedBy := map[string][]string{}
	sources := []bookmarkSource{}
	for _, ref := range refs {
		if ref.Remote == "git" || !ref.Present {
			continue
		}
		if ref.Remote != "" {
			if l, ok := local[ref.Name]; ok && !l.Conflict && !ref.Conflict && l.Commit == ref.Commit {
				if ref.Tracked {
					trackedBy[ref.Name] = append(trackedBy[ref.Name], ref.Remote)
				}
				continue
			}
		}
		sources = append(sources, bookmarkSource{Ref: ref})
	}
	for i := range sources {
		if sources[i].Ref.Remote == "" {
			sources[i].TrackedBy = trackedBy[sources[i].Ref.Name]
		}
	}
	sort.SliceStable(sources, func(i, j int) bool {
		if (sources[i].Ref.Remote == "") != (sources[j].Ref.Remote == "") {
			return sources[i].Ref.Remote == ""
		}
		return sources[i].Ref.label() < sources[j].Ref.label()
	})
	return sources
}

// discoverBookmarkSources returns every candidate bookmark source classified
// against the Stack target Workspace head, plus the raw records for argument
// diagnostics. Each classification is one batched query over all candidate
// commits: a commit has a property when it descends from a matching commit.
func discoverBookmarkSources(repoPath, targetHandle string) ([]bookmarkSource, []bookmarkRef, error) {
	refs, err := listBookmarkRefs(repoPath)
	if err != nil {
		return nil, nil, err
	}
	sources := candidateBookmarkRefs(refs)
	commits := []string{}
	for _, source := range sources {
		if source.Ref.Commit != "" {
			commits = append(commits, source.Ref.Commit)
		}
	}
	if len(commits) == 0 {
		return sources, refs, nil
	}
	candidates := revsetUnion(commits)
	outsideTarget := "~::" + targetHandle + "@ & " + workspaceRelevantRevset()
	query := func(marking string) (map[string]bool, error) {
		ids, err := revisionCommitIDs(repoPath, candidates+" & ("+marking+")::")
		if err != nil {
			return nil, fmt.Errorf("compare bookmarks with target Workspace %q: %w", targetHandle, err)
		}
		set := make(map[string]bool, len(ids))
		for _, id := range ids {
			set[id] = true
		}
		return set, nil
	}
	ahead, err := query(outsideTarget)
	if err != nil {
		return nil, nil, err
	}
	conflicted, err := query("conflicts() & " + outsideTarget)
	if err != nil {
		return nil, nil, err
	}
	beyondTrunk, err := query(outsideTarget + " & ~::trunk()")
	if err != nil {
		return nil, nil, err
	}
	for i := range sources {
		commit := sources[i].Ref.Commit
		sources[i].Ahead = ahead[commit]
		sources[i].Conflict = conflicted[commit]
		sources[i].InTrunk = ahead[commit] && !beyondTrunk[commit]
	}
	return sources, refs, nil
}

// findBookmarkSource resolves a Stack argument (`name` or `name@remote`)
// against every bookmark record, including represented and colocated ones,
// so it can explain why a named bookmark cannot be stacked.
func findBookmarkSource(sources []bookmarkSource, refs []bookmarkRef, label string) (bookmarkSource, error) {
	for _, source := range sources {
		if source.Ref.label() == label {
			if source.Ref.Conflict {
				return bookmarkSource{}, fmt.Errorf("bookmark %q is conflicted; resolve it with `jj bookmark set` before Stacking", label)
			}
			if !source.stackable() {
				return bookmarkSource{}, fmt.Errorf("bookmark %q is already represented in the target Workspace", label)
			}
			return source, nil
		}
	}
	for _, ref := range refs {
		if ref.label() != label {
			continue
		}
		switch {
		case ref.Remote == "git":
			return bookmarkSource{}, fmt.Errorf("%q is jj's colocated Git mirror, not a separate line of work; Stack %q instead", label, ref.Name)
		case !ref.Present:
			return bookmarkSource{}, fmt.Errorf("bookmark %q is deleted locally", label)
		default:
			return bookmarkSource{}, fmt.Errorf("bookmark %q is at the same commit as local bookmark %q; Stack %q instead", label, ref.Name, ref.Name)
		}
	}
	return bookmarkSource{}, fmt.Errorf("no Workspace or bookmark named %q", label)
}

func selectorItemsForBookmarkSources(sources []bookmarkSource) []selectorItem {
	items := []selectorItem{}
	for _, source := range sources {
		if !source.Ref.Conflict && !source.stackable() {
			continue
		}
		items = append(items, selectorItem{
			Kind:         selectorKindBookmark,
			Handle:       source.Ref.label(),
			Status:       source.status(),
			Markers:      source.markers(),
			Description:  source.Ref.Description,
			Disabled:     !source.stackable(),
			ExplicitOnly: !source.inAll(),
		})
	}
	return items
}

// stackInput is one Stack Input: a Workspace (Handle set) or a bookmark
// source (Commit set to its exact selected target).
type stackInput struct {
	Label  string
	Handle string
	Commit string
}

func workspaceStackInput(handle string) stackInput {
	return stackInput{Label: handle, Handle: handle}
}

func bookmarkStackInput(source bookmarkSource) stackInput {
	return stackInput{Label: source.Ref.label(), Commit: source.Ref.Commit}
}

func workspaceStackInputs(handles ...string) []stackInput {
	inputs := make([]stackInput, 0, len(handles))
	for _, handle := range handles {
		inputs = append(inputs, workspaceStackInput(handle))
	}
	return inputs
}

// payloadRevset is the commit Stack brings into the target. A bookmark's
// exact target is its payload, even when undescribed: it is not a cursor.
func (in stackInput) payloadRevset() string {
	if in.Handle == "" {
		return in.Commit
	}
	return stackInputPayloadRevset(in.Handle)
}

func uniqueStackInputs(inputs []stackInput) []stackInput {
	seen := map[stackInput]bool{}
	out := make([]stackInput, 0, len(inputs))
	for _, input := range inputs {
		if input.Label == "" || seen[input] {
			continue
		}
		seen[input] = true
		out = append(out, input)
	}
	return out
}

func stackInputLabels(inputs []stackInput) []string {
	labels := make([]string, 0, len(inputs))
	for _, input := range inputs {
		labels = append(labels, input.Label)
	}
	return labels
}

func stackInputWorkspaceHandles(inputs []stackInput) []string {
	handles := []string{}
	for _, input := range inputs {
		if input.Handle != "" {
			handles = append(handles, input.Handle)
		}
	}
	return handles
}

func stackInputBookmarks(inputs []stackInput) []stackInput {
	bookmarks := []stackInput{}
	for _, input := range inputs {
		if input.Handle == "" {
			bookmarks = append(bookmarks, input)
		}
	}
	return bookmarks
}

// verifyBookmarkInputsUnchanged confirms Stack left every selected local
// bookmark at the commit that was selected; remote records cannot move locally.
func verifyBookmarkInputsUnchanged(repoPath string, inputs []stackInput) error {
	bookmarks := stackInputBookmarks(inputs)
	if len(bookmarks) == 0 {
		return nil
	}
	refs, err := listBookmarkRefs(repoPath)
	if err != nil {
		return err
	}
	byLabel := map[string]bookmarkRef{}
	for _, ref := range refs {
		byLabel[ref.label()] = ref
	}
	for _, input := range bookmarks {
		ref, ok := byLabel[input.Label]
		if !ok || !ref.Present || ref.Conflict || ref.Commit != input.Commit {
			return fmt.Errorf("bookmark %q no longer points at selected commit %s after Stack; inspect the result or run `ajj undo`", input.Label, input.Commit)
		}
	}
	return nil
}
