package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// All membership queries and preview reads use one immutable operation. A
// bounded membership read must fail closed, never turn truncation into safety.
type tidyGraphEvidence struct {
	repo, operation string
	infos           []workspaceInfo
	work            map[string]map[string]bool
}

func tidyGraphReview(repo string, infos []workspaceInfo, operation string) (*tidyGraphEvidence, error) {
	if !integrationFullOperationIDRE.MatchString(operation) {
		return nil, fmt.Errorf("Tidy evidence requires a pinned operation")
	}
	review := &tidyGraphEvidence{repo: repo, operation: operation, infos: infos, work: map[string]map[string]bool{}}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for _, info := range infos {
		out, truncated, err := tidyReadCommandFn(ctx, repo, operation, 2*1024*1024, "log", "--no-graph", "-r", "mutable() & ::"+info.Ref.Handle+"@ & "+workspaceRelevantRevset(), "-T", `commit_id ++ "\n"`)
		if err != nil {
			return nil, fmt.Errorf("load pinned Tidy evidence: %w", err)
		}
		if truncated {
			return nil, fmt.Errorf("pinned Tidy evidence exceeds read limit; cannot establish batch safety")
		}
		ids := map[string]bool{}
		for _, id := range strings.Fields(out) {
			if !integrationCommitIDRE.MatchString(id) {
				return nil, fmt.Errorf("invalid commit ID in pinned Tidy evidence")
			}
			ids[id] = true
		}
		review.work[info.Ref.Handle] = ids
	}
	return review, nil
}

func (r *tidyGraphEvidence) review(selected []selectorItem, force bool) (map[string]string, error) {
	infos, work := r.infos, r.work

	closing := map[string]bool{}
	for _, item := range selected {
		closing[item.Handle] = true
	}
	evidence := map[string]string{}
	blocked := []string{}
	for _, info := range infos {
		handle := info.Ref.Handle
		if info.Missing {
			evidence[handle] = "forget registration only; preserve any leftover directory"
			continue
		}
		protected := map[string]bool{}
		names := []string{}
		fullProtectors := []string{}
		for _, other := range infos {
			if other.Ref.Handle == handle || closing[other.Ref.Handle] {
				continue
			}
			covered := 0
			for id := range work[handle] {
				if work[other.Ref.Handle][id] {
					protected[id] = true
					covered++
				}
			}
			if covered > 0 {
				names = append(names, other.Ref.Handle)
				if covered == len(work[handle]) {
					fullProtectors = append(fullProtectors, other.Ref.Handle)
				}
			}
		}
		unique := len(work[handle]) - len(protected)
		reason := "no relevant mutable changes"
		if unique > 0 {
			reason = fmt.Sprintf("%d unique mutable change(s) outside surviving Workspaces", unique)
		} else if len(fullProtectors) > 0 {
			reason = "represented in surviving: " + strings.Join(fullProtectors, ", ")
		} else if len(names) > 0 {
			reason = "represented across surviving (combined): " + strings.Join(names, ", ")
		}
		if info.Conflict {
			reason = "conflicts require force; " + reason
		}
		evidence[handle] = reason
		if closing[handle] && !force && (unique > 0 || info.Conflict) {
			blocked = append(blocked, handle)
		}
	}
	if len(blocked) > 0 {
		return evidence, fmt.Errorf("uncheck a row or press f to force; selected rows can't protect each other: %s", strings.Join(blocked, ", "))
	}
	return evidence, nil
}

// preselectSafeTidyBatch keeps the automatic (preselected) Tidy batch
// submittable. Rows that are individually represented elsewhere can still
// protect only each other; the batch review refuses that, so candidates are
// accepted greedily, in item order, only while the growing batch still passes
// the same review. Rejected rows are left unchecked (never silently closed) and
// returned so the caller can explain them. Force semantics are unchanged: the
// review accepts every candidate under force.
func preselectSafeTidyBatch(items []selectorItem, review func([]selectorItem, bool) (map[string]string, error), force bool) ([]selectorItem, []string) {
	items, left, _ := extendSafeTidyBatch(items, nil, review, force)
	return items, left
}

// extendSafeTidyBatch is preselectSafeTidyBatch on top of a fixed base: rows
// whose Handle is in fixed keep their check unconditionally (a preserved user
// selection is never changed), and every other checked row is accepted
// greedily, in item order, only while base plus accepted rows still pass the
// review. If the base is already blocked no addition can be judged safe, so
// every other row is left unchecked and the third result is true.
func extendSafeTidyBatch(items []selectorItem, fixed map[string]bool, review func([]selectorItem, bool) (map[string]string, error), force bool) ([]selectorItem, []string, bool) {
	if review == nil {
		return items, nil, false
	}
	accepted := []selectorItem{}
	for _, item := range items {
		if fixed[item.Handle] && item.Selected && !item.Disabled && !item.All {
			accepted = append(accepted, item)
		}
	}
	baseBlocked := false
	if len(accepted) > 0 {
		_, err := review(accepted, force)
		baseBlocked = err != nil
	}
	left := []string{}
	for i := range items {
		item := items[i]
		if !item.Selected || item.Disabled || item.All || fixed[item.Handle] {
			continue
		}
		if baseBlocked {
			items[i].Selected = false
			left = append(left, item.Handle)
			continue
		}
		trial := append(append([]selectorItem{}, accepted...), item)
		if _, err := review(trial, force); err != nil {
			items[i].Selected = false
			left = append(left, item.Handle)
			continue
		}
		accepted = trial
	}
	return items, left, baseBlocked
}

// restoreTidySelection re-applies a `u` refresh's preserved checks to rows
// rebuilt from a fresh review. A preserved Handle stays checked only while its
// row is still selectable; rows the user had left unchecked stay unchecked;
// newly recovered rows keep their startup default (eligible Disposable rows
// checked, Keep rows unchecked) and are then added with the startup greedy
// rule on top of the preserved checks, so they never block the batch. The
// returned notice names recovered rows left unchecked.
func restoreTidySelection(items []selectorItem, recovered, preserved []string, review func([]selectorItem, bool) (map[string]string, error), force bool) ([]selectorItem, string) {
	keep := map[string]bool{}
	for _, handle := range preserved {
		keep[handle] = true
	}
	fresh := map[string]bool{}
	for _, handle := range recovered {
		fresh[handle] = true
	}
	for i := range items {
		item := &items[i]
		switch {
		case item.Disabled || item.All:
			item.Selected = false
		case keep[item.Handle]:
			item.Selected = true
		case !fresh[item.Handle]:
			item.Selected = false
		}
	}
	items, left, baseBlocked := extendSafeTidyBatch(items, keep, review, force)
	if len(left) == 0 {
		return items, ""
	}
	if baseBlocked {
		return items, "Recovered rows left unchecked (the checked batch is already blocked): " + strings.Join(left, ", ")
	}
	return items, tidyLeftUncheckedNotice(left)
}

// safeTidyTargets applies the same greedy rule to non-interactive Tidy using
// the pinned graph evidence. The caller still revalidates the chosen set with
// the live jj-backed batch checks before anything is closed.
func (r *tidyGraphEvidence) safeTidyTargets(targets []workspaceInfo, force bool) ([]workspaceInfo, []workspaceInfo) {
	items := make([]selectorItem, 0, len(targets))
	for _, target := range targets {
		items = append(items, selectorItem{Handle: target.Ref.Handle, Selected: true})
	}
	items, _ = preselectSafeTidyBatch(items, r.review, force)
	kept, left := []workspaceInfo{}, []workspaceInfo{}
	for i, target := range targets {
		if items[i].Selected {
			kept = append(kept, target)
		} else {
			left = append(left, target)
		}
	}
	return kept, left
}

func (m *selectorModel) refreshTidyReview() {
	if m.opts.Tidy && m.opts.ReviewTidy != nil {
		evidence, err := m.opts.ReviewTidy(m.selectedItems(), m.opts.ForceEnabled)
		m.evidence = evidence
		m.problem = ""
		if err != nil {
			m.problem = err.Error()
		}
	}
}

func (m *selectorModel) toggleTidyPolicy() {
	if !m.opts.Tidy || m.opts.SetPolicy == nil {
		return
	}
	visible := m.visibleItems()
	if m.cursor < 0 || m.cursor >= len(visible) {
		return
	}
	m.notice = ""
	idx := visible[m.cursor]
	item := &m.opts.Items[idx]
	if itemHasMarker(*item, "current") || itemHasMarker(*item, "main") {
		m.notice = "Main/Current are unavailable in Tidy"
		return
	}
	policy := policyDisposable
	if item.Policy == policyDisposable {
		policy = policyKeep
	}
	if err := m.opts.SetPolicy(item.Handle, policy); err != nil {
		m.notice = err.Error()
		return
	}
	item.Policy = policy
	if policy == policyKeep {
		delete(m.selected, idx)
	}
	m.notice = item.Handle + ": " + policy + " persisted (also on cancel); Space selects separately"
}
