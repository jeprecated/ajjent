# Force close abandons unique mutable work

`ajj close --force` means more than bypassing a safety prompt: it closes the Workspace and abandons the unique mutable changes that are not reachable from Main or any other Workspace. The clearer `--discard` spelling was considered but rejected, so documentation and UI copy must make the destructive meaning of force explicit before it is applied.
