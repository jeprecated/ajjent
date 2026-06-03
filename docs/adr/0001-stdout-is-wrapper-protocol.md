# Keep stdout as the shell-wrapper protocol

`jjw` commands reserve stdout for machine-readable paths or data that shell wrappers can act on, while human-facing progress and prompts go to stderr. This preserves reliable navigation flows even when commands are used interactively, at the cost of making some direct CLI output less conversational.
