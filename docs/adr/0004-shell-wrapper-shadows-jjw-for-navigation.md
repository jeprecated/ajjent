# Shell integration shadows jjw for navigation

Interactive shell integration will wrap the `jjw` command name itself so navigation-oriented commands such as create, open, and close can change the caller's directory after reading the binary's stdout protocol. The raw binary remains available through a documented escape hatch for scripts, but the interactive UX favors `jjw create/open/close` doing what users expect.
