# Hard-rename legacy workspace vocabulary

`jjw` will replace legacy `app`, `worktrees_root`, `name_list`, `stateful`, `select`, and `cd` naming with the canonical Project/Workspace language, even though this breaks existing flags, configs, commands, and scripts. The cleaner vocabulary is preferred over compatibility because the current names preserve the UX ambiguity this redesign is meant to remove; old names should be treated as unknown rather than accepted with migration shims, and config parsing should reject unknown YAML keys rather than ignoring them.
