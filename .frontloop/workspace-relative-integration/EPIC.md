# Epic: Workspace-relative integration and nested cleanup

Add a machine-safe, crash-recoverable Ajj integration operation that always lands child Workspaces into the Current Workspace, supports later integration of that Workspace into Main, and makes each adopted Workspace layer safely tidyable.

## Sequence

1. Freeze the JSON protocol and Jujutsu transaction boundary.
2. Implement the durable journal and strict machine CLI.
3. Implement single/provider-default and target-anchored ordered-line integration.
4. Make close/tidy safe across nested integration layers.
5. Document and validate the complete recursive lifecycle, including Fable 5 judgment.
