package main

import (
	"fmt"
	"regexp"
	"strings"
)

// User-initiated stale recovery inside Tidy. A stale Workspace is never
// selectable or closable. The user may recover it explicitly, with the TUI's
// `u` key or --update-stale; Ajj then runs `jj workspace update-stale` and
// rebuilds the complete review (snapshots, reviewed operation, Workspaces,
// policies, graph evidence) exactly as a fresh `ajj tidy` would. Nothing
// from the earlier review is reused except the selector's UI state.

// tidyUIState is the selector state a `u` refresh carries into the rebuilt
// review. Checks and the cursor are by Handle because rows are rebuilt.
type tidyUIState struct {
	Selected    []string
	Cursor      string
	Filter      string
	Force       bool
	PreviewOpen bool
}

// tidyRefreshRequest ends a Tidy selector run when the user presses `u`. It
// is returned as the selector's error so that nothing after selection (target
// resolution, confirmation, Closing) runs against the old review.
type tidyRefreshRequest struct {
	// Stale lists the stale rows shown (the filter applies) when u was pressed.
	Stale []string
	State tidyUIState
}

func (r *tidyRefreshRequest) Error() string {
	return "Tidy refresh requested to update stale Workspaces: " + strings.Join(r.Stale, ", ")
}

// tidyResume is what a rebuilt review receives from the previous pass.
type tidyResume struct {
	State tidyUIState
	// Recovered lists requested Workspaces that are no longer stale in the
	// rebuilt review and may get the startup preselection.
	Recovered []string
	// Review holds updated Workspaces that are no longer stale but must not
	// be selected automatically (e.g. jj kept their on-disk edits in a
	// divergent commit), with the reason.
	Review map[string]string
	Notice string
}

// requestStaleUpdate records a `u` press: every stale row currently shown is
// to be updated and the whole review rebuilt. The selector never runs jj; it
// only ends with the request and the UI state to restore.
func (m *selectorModel) requestStaleUpdate() {
	stale := []string{}
	for _, idx := range m.visibleItems() {
		if item := m.opts.Items[idx]; item.Stale {
			stale = append(stale, item.Handle)
		}
	}
	if len(stale) == 0 {
		m.notice = "No stale Workspace shown; nothing to update"
		return
	}
	m.refresh = &tidyRefreshRequest{Stale: stale, State: m.uiState()}
}

func (m selectorModel) uiState() tidyUIState {
	state := tidyUIState{Filter: m.filter, Force: m.opts.ForceEnabled, PreviewOpen: m.previewOpen}
	for _, item := range m.selectedItems() {
		state.Selected = append(state.Selected, item.Handle)
	}
	if visible := m.visibleItems(); m.cursor >= 0 && m.cursor < len(visible) {
		state.Cursor = m.opts.Items[visible[m.cursor]].Handle
	}
	return state
}

func (m selectorModel) hasStaleRows() bool {
	for _, item := range m.opts.Items {
		if item.Stale {
			return true
		}
	}
	return false
}

// refreshTidyAfterStaleUpdate serves a `u` refresh between two selector runs:
// update the requested Workspaces that the previous review found stale, then
// rebuild the whole review. The returned operation replaces the previous one
// for every later check, including final pre-close revalidation.
func refreshTidyAfterStaleUpdate(repoRoot string, cfg config, project string, infos []workspaceInfo, request *tidyRefreshRequest) (*tidyResume, []workspaceInfo, string, error) {
	byHandle := mapInfosByHandle(infos)
	targets := []workspaceInfo{}
	for _, handle := range request.Stale {
		if info, ok := byHandle[handle]; ok && info.Stale {
			targets = append(targets, info)
		}
	}
	styles := cliStylesForWriter(stderrWriter)
	if len(targets) > 0 {
		// Between selector runs: the previous selector has cleared its lines.
		fmt.Fprintln(stderrWriter, styles.Muted.Render("Updating stale Workspaces (jj workspace update-stale): "+workspaceHandleList(targets)))
	}
	reports, err := updateStaleWorkspaces(repoRoot, targets)
	if err != nil {
		return nil, nil, "", err
	}
	refreshed, reviewedOperation, err := loadTidyReview(repoRoot, cfg, project)
	if err != nil {
		return nil, nil, "", err
	}
	outcome := staleUpdateOutcome(repoRoot, reviewedOperation, targets, reports, refreshed)
	// jj's own report stays in the scrollback above the reopened selector.
	for _, detail := range outcome.details {
		fmt.Fprintln(stderrWriter, styles.Muted.Render("  "+detail))
	}
	notices := []string{outcome.reviewNotice()}
	if len(outcome.stillStale) > 0 {
		notices = append(notices, "Still stale: "+strings.Join(outcome.stillStale, "; "))
	}
	if len(outcome.updated) > 0 {
		notices = append(notices, "Updated stale: "+strings.Join(outcome.updated, ", "))
	}
	return &tidyResume{State: request.State, Recovered: outcome.recovered, Review: outcome.review, Notice: joinNotices(notices...)}, refreshed, reviewedOperation, nil
}

// updateStaleTidyCandidates is --update-stale: after the initial snapshot,
// recover stale Disposable non-Main non-Current present candidates, then
// rebuild the whole review. Stale Keep rows are left for the TUI's u. The
// returned map holds recovered Workspaces that must not be selected or
// closed automatically, with the reason.
func updateStaleTidyCandidates(repoRoot string, cfg config, project string, infos []workspaceInfo, reviewedOperation string) ([]workspaceInfo, string, map[string]string, error) {
	targets := []workspaceInfo{}
	for _, info := range infos {
		if info.Stale && info.Policy == policyDisposable && !info.Main && !info.Current && !info.Missing {
			targets = append(targets, info)
		}
	}
	if len(targets) == 0 {
		return infos, reviewedOperation, nil, nil
	}
	styles := cliStylesForWriter(stderrWriter)
	reports, err := updateStaleWorkspaces(repoRoot, targets)
	if err != nil {
		return nil, "", nil, err
	}
	for _, target := range targets {
		if cause := reports[target.Ref.Handle].err; cause != nil {
			fmt.Fprintln(stderrWriter, styles.Warn.Render(fmt.Sprintf("Could not update stale Workspace %s: %s", target.Ref.Handle, summarizeStaleUpdateError(cause))))
		}
	}
	refreshed, reviewedOperation, err := loadTidyReview(repoRoot, cfg, project)
	if err != nil {
		return nil, "", nil, err
	}
	outcome := staleUpdateOutcome(repoRoot, reviewedOperation, targets, reports, refreshed)
	for _, detail := range outcome.details {
		fmt.Fprintln(stderrWriter, styles.Muted.Render("  "+detail))
	}
	// Workspaces that are still stale are reported by the normal skip
	// warning; recovered ones are named here.
	if len(outcome.updated) > 0 {
		fmt.Fprintln(stderrWriter, styles.Info.Render("Updated stale Workspaces (now reviewed under normal rules): "+strings.Join(outcome.updated, ", ")))
	}
	for _, handle := range outcome.reviewOrder {
		fmt.Fprintln(stderrWriter, styles.Warn.Render(fmt.Sprintf("Updated stale Workspace %s is never selected automatically: %s", handle, outcome.review[handle])))
	}
	return refreshed, reviewedOperation, outcome.review, nil
}

const (
	staleUpdateKeptEditsNote = "on-disk edits were kept in a divergent commit (jj log -r 'divergent()') — review before tidying"
	staleUpdateDivergentNote = "its working-copy change is divergent after update-stale (jj log -r 'divergent()') — review before tidying"
)

var staleUpdateCountsRE = regexp.MustCompile(`Added (\d+) files, modified (\d+) files, removed (\d+) files`)

// staleUpdateKeptEdits reports whether jj says update-stale first snapshotted
// on-disk edits of the stale copy. jj keeps those as a divergent sibling of
// the Workspace's change and then checks out the current commit, so nothing
// is discarded, but the edits are no longer in the Workspace's working copy
// and its Tidy evidence does not cover them.
func staleUpdateKeptEdits(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "concurrent modification") || strings.Contains(lower, "(divergent)")
}

// staleUpdateDetails keeps the meaningful lines of jj's update report: a
// concurrent modification, a divergent working copy, and file-change counts.
func staleUpdateDetails(output string) []string {
	details := []string{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(sanitizeTidyPreview(line))
		lower := strings.ToLower(line)
		if strings.Contains(lower, "concurrent modification") || strings.Contains(lower, "divergent") || staleUpdateCountsRE.MatchString(line) {
			details = append(details, line)
		}
	}
	return details
}

// staleUpdateCounts is the compact file-change summary of jj's report, or "".
func staleUpdateCounts(output string) string {
	match := staleUpdateCountsRE.FindStringSubmatch(output)
	if match == nil {
		return ""
	}
	return fmt.Sprintf("added %s, modified %s, removed %s files", match[1], match[2], match[3])
}

type staleUpdateResult struct {
	// recovered are Handles no longer stale that may be preselected like any
	// fresh row; updated labels them for display.
	recovered, updated []string
	// review holds Handles no longer stale that must never be selected or
	// closed automatically, with the reason; reviewOrder keeps target order.
	review      map[string]string
	reviewOrder []string
	// stillStale names the Workspaces that stay stale, with the reason.
	stillStale []string
	// details are per-Workspace lines of what jj reported.
	details []string
}

// staleUpdateOutcome judges each update by the rebuilt review, which is
// authoritative for staleness. A Workspace still stale is named with the
// reason. One no longer stale is held for manual review when jj reported an
// error, said it kept on-disk edits in a divergent commit, or its
// working-copy change is divergent at the rebuilt review's operation;
// otherwise it is recovered.
func staleUpdateOutcome(repoRoot, operation string, targets []workspaceInfo, reports map[string]staleUpdateReport, refreshed []workspaceInfo) staleUpdateResult {
	after := mapInfosByHandle(refreshed)
	result := staleUpdateResult{review: map[string]string{}}
	for _, target := range targets {
		handle := target.Ref.Handle
		report := reports[handle]
		if details := staleUpdateDetails(report.output); len(details) > 0 {
			result.details = append(result.details, handle+": "+strings.Join(details, " · "))
		}
		info, ok := after[handle]
		if !ok {
			continue
		}
		switch {
		case info.Stale && report.err != nil:
			result.stillStale = append(result.stillStale, handle+" ("+summarizeStaleUpdateError(report.err)+")")
		case info.Stale:
			result.stillStale = append(result.stillStale, handle+" (still stale after update-stale)")
		default:
			if note := staleUpdateReviewNote(repoRoot, operation, handle, report); note != "" {
				result.review[handle] = note
				result.reviewOrder = append(result.reviewOrder, handle)
				continue
			}
			result.recovered = append(result.recovered, handle)
			label := handle
			if counts := staleUpdateCounts(report.output); counts != "" {
				label += " (" + counts + ")"
			}
			result.updated = append(result.updated, label)
		}
	}
	return result
}

// staleUpdateReviewNote says why a Workspace that is no longer stale must
// still not be selected automatically, or "" when it may be. The divergence
// query is independent of jj's wording and fails closed.
func staleUpdateReviewNote(repoRoot, operation, handle string, report staleUpdateReport) string {
	switch {
	case report.err != nil:
		return "update-stale reported: " + summarizeStaleUpdateError(report.err) + " — review before tidying"
	case staleUpdateKeptEdits(report.output):
		return staleUpdateKeptEditsNote
	}
	out, err := integrationQuery(repoRoot, operation, "log", "--no-graph", "-r", handle+"@ & divergent()", "-T", `commit_id ++ "\n"`)
	if err != nil {
		return "could not check it for divergence (" + summarizeStaleUpdateError(err) + ") — review before tidying"
	}
	if strings.TrimSpace(out) != "" {
		return staleUpdateDivergentNote
	}
	return ""
}

// reviewNotice names the held Workspaces, grouped by reason.
func (r staleUpdateResult) reviewNotice() string {
	groups, order := map[string][]string{}, []string{}
	for _, handle := range r.reviewOrder {
		note := r.review[handle]
		if _, ok := groups[note]; !ok {
			order = append(order, note)
		}
		groups[note] = append(groups[note], handle)
	}
	parts := []string{}
	for _, note := range order {
		parts = append(parts, strings.Join(groups[note], ", ")+": "+note)
	}
	return strings.Join(parts, ". ")
}

// summarizeStaleUpdateError keeps the first meaningful line of jj's error,
// without the echoed command line, sanitized and bounded for a one-line notice.
func summarizeStaleUpdateError(err error) string {
	text := err.Error()
	if _, after, ok := strings.Cut(text, "update-stale failed: "); ok {
		text = after
	}
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			text = line
			break
		}
	}
	text = strings.TrimSpace(sanitizeTidyPreview(text))
	if runes := []rune(text); len(runes) > 100 {
		text = string(runes[:99]) + "…"
	}
	return emptyDefault(text, "update-stale failed")
}

func joinNotices(parts ...string) string {
	kept := []string{}
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, ". ")
}
