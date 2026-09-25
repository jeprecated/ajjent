package main

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var tidyReadCommandFn = runTidyReadCommand

// No pager, external diff formatter, working-copy snapshot, or unbounded capture.
// The caller supplies one deadline for the entire evidence/preview request.
func runTidyReadCommand(ctx context.Context, repo, operation string, limit int, args ...string) (string, bool, error) {
	argv := append([]string{"-R", repo, "--ignore-working-copy", "--at-op=" + operation, "--color=never", "--no-pager"}, args...)
	cmd := exec.CommandContext(ctx, "jj", argv...)
	setCommandWorkingDir(cmd, "jj", argv...)
	out := boundedCommandOutput{limit: limit}
	stderr := boundedCommandOutput{limit: 4096}
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		err = ctx.Err()
	} else if err != nil {
		err = fmt.Errorf("%w: %s", err, sanitizeTidyPreview(stderr.buffer.String()))
		if stderr.exceeded {
			err = fmt.Errorf("%w [stderr truncated]", err)
		}
	}
	return out.buffer.String(), out.exceeded, err
}

func sanitizeTidyPreview(text string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if r == '\t' {
			return ' '
		}
		if !unicode.IsPrint(r) {
			return '�'
		}
		return r
	}, text)
}

func boundedPreviewSection(text string, truncated bool, limit int) string {
	lines := strings.Split(strings.TrimSuffix(sanitizeTidyPreview(text), "\n"), "\n")
	if len(lines) > limit {
		lines = lines[:limit]
		truncated = true
	}
	text = strings.Join(lines, "\n")
	if text == "" && !truncated {
		text = "(none in this pinned view)"
	}
	if truncated {
		text += "\n[truncated: bounded preview, not complete history/diff]"
	}
	return text
}

func (r *tidyGraphEvidence) uniqueIDs(ctx context.Context, handle string, selected []selectorItem) ([]string, error) {
	closing := map[string]bool{handle: true}
	for _, item := range selected {
		closing[item.Handle] = true
	}
	unique := []string{}
	for id := range r.work[handle] {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		covered := false
		for other, ids := range r.work {
			if !closing[other] && ids[id] {
				covered = true
				break
			}
		}
		if !covered {
			unique = append(unique, id)
		}
	}
	sort.Strings(unique)
	return unique, nil
}

func (r *tidyGraphEvidence) preview(ctx context.Context, handle string, selected []selectorItem) (string, error) {
	info, ok := mapInfosByHandle(r.infos)[handle]
	if !ok {
		return "", fmt.Errorf("highlighted Workspace is not in reviewed evidence")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	sections := []string{"Pinned operation: " + r.operation}
	if info.Missing {
		sections = append(sections, "Missing Workspace: registration-only; preserve leftover files. Registered history below is not current disk state.")
	}
	if info.Conflict {
		sections = append(sections, "Conflicts at review: graph safety still requires force.")
	}
	read := func(title string, lines int, args ...string) error {
		out, truncated, err := tidyReadCommandFn(ctx, r.repo, r.operation, 64*1024, args...)
		sections = append(sections, title)
		if err != nil {
			return fmt.Errorf("%s: %w", title, err)
		}
		sections = append(sections, boundedPreviewSection(out, truncated, lines))
		return nil
	}
	const template = `change_id.short(12) ++ " " ++ commit_id.short(12) ++ " " ++ if(conflict, "[conflict] ", "") ++ description.first_line() ++ "\n"`
	relevant := "mutable() & ::" + handle + "@ & " + workspaceRelevantRevset()
	if err := read("Relevant mutable ancestor log (change ID / commit ID / description; empty cursors omitted)", 30, "log", "--no-graph", "-n", "31", "-r", relevant, "-T", template); err != nil {
		return strings.Join(sections, "\n"), err
	}
	unique, err := r.uniqueIDs(ctx, handle, selected)
	if err != nil {
		return strings.Join(sections, "\n"), err
	}
	sections = append(sections, fmt.Sprintf("Unique to this Workspace vs actual surviving set: %d mutable revision(s); selected rows cannot protect it", len(unique)))
	if len(unique) > 0 {
		ids := unique[:min(31, len(unique))]
		if err := read("Unique revision detail (bounded ID sample, not a new safety verdict)", 30, "log", "--no-graph", "-n", "31", "-r", strings.Join(ids, " | "), "-T", template); err != nil {
			return strings.Join(sections, "\n"), err
		}
	}
	if info.Missing {
		return strings.Join(sections, "\n"), nil
	}
	main := ""
	for _, candidate := range r.infos {
		if candidate.Main {
			main = candidate.Ref.Handle
			break
		}
	}
	if main == "" {
		return strings.Join(sections, "\n"), fmt.Errorf("Main comparison unavailable: no reviewed Main Workspace")
	}
	label := "Main comparison: " + main + "@ -> " + handle + "@ (NOT unique-work or ancestry proof)"
	if err := read(label+" / changed files", 80, "diff", "--from", main+"@", "--to", handle+"@", "--summary"); err != nil {
		return strings.Join(sections, "\n"), err
	}
	if err := read(label+" / patch", 120, "diff", "--from", main+"@", "--to", handle+"@", "--git", "--context", "3"); err != nil {
		return strings.Join(sections, "\n"), err
	}
	return strings.Join(sections, "\n"), nil
}

type tidyPreviewMsg struct {
	id   uint64
	text string
	err  error
}

func (m *selectorModel) stopPreview() {
	if m.previewCancel != nil {
		m.previewCancel()
		m.previewCancel = nil
	}
	m.previewID++
	m.previewKey = ""
}

func (m *selectorModel) requestTidyPreview() tea.Cmd {
	if !m.previewOpen {
		if m.previewCancel != nil {
			m.stopPreview()
		}
		return nil
	}
	visible := m.visibleItems()
	handle := ""
	if m.cursor >= 0 && m.cursor < len(visible) {
		handle = m.opts.Items[visible[m.cursor]].Handle
	}
	selected := m.selectedItems()
	handles := []string{}
	for _, item := range selected {
		handles = append(handles, item.Handle)
	}
	sort.Strings(handles)
	key := handle + "\x00" + strings.Join(handles, "\x00") + fmt.Sprint(m.opts.ForceEnabled)
	if key == m.previewKey {
		return nil
	}
	m.stopPreview()
	m.previewKey = key
	m.previewOffset = 0
	if handle == "" {
		m.previewText = "No Workspace highlighted"
		return nil
	}
	if m.opts.PreviewTidy == nil {
		m.previewText = "Preview unavailable: no reviewed evidence loader"
		return nil
	}
	m.previewText = "Loading bounded read-only preview…"
	ctx, cancel := context.WithCancel(context.Background())
	m.previewCancel = cancel
	id, load := m.previewID, m.opts.PreviewTidy
	return func() tea.Msg {
		text, err := load(ctx, handle, selected)
		return tidyPreviewMsg{id: id, text: text, err: err}
	}
}

func clipTidyPreviewLine(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(text) > width {
		if width == 1 {
			return "…"
		}
		return clipSelectorLine(text, width-1) + "…"
	}
	return text
}

func (m selectorModel) tidyPreviewView() string {
	if m.height <= 0 {
		return ""
	}
	handle, policy, status := "none", "-", "-"
	mark := "[ ]"
	visible := m.visibleItems()
	if m.cursor >= 0 && m.cursor < len(visible) {
		idx := visible[m.cursor]
		item := m.opts.Items[idx]
		handle, policy, status = item.Handle, item.Policy, item.Status
		if item.Disabled {
			mark = "[-]"
		} else if m.selected[idx] {
			mark = "[x]"
		}
	}
	title := fmt.Sprintf("Preview force=%v: %s", m.opts.ForceEnabled, handle)
	if strings.HasPrefix(m.previewText, "Preview error:") {
		title = "ERROR: " + title
	}
	rows := []string{title, "v back | PgUp/PgDn scroll | ↑/↓ Workspace", mark + " Policy: " + policy + " | Main-relative: " + status, "Graph: " + m.evidence[handle]}
	content := strings.Split(m.previewText, "\n")
	count := max(0, m.height-5)
	offset := min(m.previewOffset, max(0, len(content)-count))
	rows = append(rows, content[offset:min(offset+count, len(content))]...)
	footer := "Space/p/f/Enter/q unchanged | Main diff != safety"
	if offset+count < len(content) {
		footer = "More below: PgDn | v back | diff != safety"
	}
	if m.notice != "" {
		footer = m.notice + " | v back"
	}
	blocked := m.problem != ""
	if blocked {
		footer = tidyBlockedPrefix + strings.TrimPrefix(m.problem, tidyBlockedPrefix)
	}
	if len(rows)+1 > m.height && blocked {
		// Keep a blocked Enter visible even in a tiny terminal.
		rows = rows[:max(0, m.height-1)]
	}
	rows = append(rows, footer)
	rows = rows[:min(len(rows), m.height)]
	for i := range rows {
		rows[i] = clipTidyPreviewLine(sanitizeTidyPreview(rows[i]), m.width)
	}
	if blocked {
		rows[len(rows)-1] = selectorStyles().Warn.Render(rows[len(rows)-1])
	}
	return strings.Join(rows, "\n")
}
