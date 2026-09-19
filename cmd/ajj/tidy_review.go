package main

import (
	"fmt"
	"strings"
)

// Snapshot graph evidence once for the selector. Selection changes recompute
// reachability against the complete closing set without issuing JJ commands in
// the event loop. The existing operation guard rejects external graph drift.
func tidyGraphReview(repo string, infos []workspaceInfo) (func([]selectorItem, bool) (map[string]string, error), error) {
	work := map[string]map[string]bool{}
	for _, info := range infos {
		out, err := commandCaptureFn("jj", "-R", repo, "--ignore-working-copy", "--color=never", "--no-pager", "log", "--no-graph", "-r", "mutable() & ::"+info.Ref.Handle+"@ & "+workspaceRelevantRevset(), "-T", `commit_id ++ "\n"`)
		if err != nil {
			return nil, err
		}
		ids := map[string]bool{}
		for _, id := range strings.Fields(out) {
			ids[id] = true
		}
		work[info.Ref.Handle] = ids
	}
	return func(selected []selectorItem, force bool) (map[string]string, error) {
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
			return evidence, fmt.Errorf("Batch blocked: %s; uncheck a protector/target, or explicitly enable force. Selected rows cannot protect each other", strings.Join(blocked, ", "))
		}
		return evidence, nil
	}, nil
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
