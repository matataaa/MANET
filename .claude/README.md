# MANET subagent setup

Six project subagents for the very-srs/MANET mesh stack (batman-adv over 802.11ax/s/ah, Alfred gossip, ARM64 SBCs: CM4, Raspberry Pi 4 — Pi 5 planned). The project is still in the testing phase — the nodes are test hardware, recoverable via reflash from the repo.

## Installation

Place all six `.md` files in `.claude/agents/` at the root of your repo and commit them to git, so the whole team (and future sessions) gets them too.

```
.claude/agents/
├── manet-architect.md
├── manet-implementer.md
├── manet-reviewer.md
├── manet-tester.md
├── manet-pr.md
└── manet-node-debug.md
```

## Integration with your existing `.cursor/rules` and `.cursor/skills`

This matters and costs you no extra work in the agent files themselves:

- **CLAUDE.md loads automatically into every subagent.** Every subagent above (except the built-in Explore/Plan agents, which you don't use) gets the full CLAUDE.md hierarchy of your project in its context on startup. If your "Project Context" file (with the reference to `.cursor/rules/manet-project.mdc`) is literally named `CLAUDE.md` at the repo root, you don't need to duplicate the architecture (batman-adv, Alfred, AP selection, interface naming) in every agent prompt — that already happens automatically. The six agents above reference it explicitly instead of repeating everything.
- **Note**: `manet-pr` deliberately diverges from the "never commit on behalf of the user" rule in `manet-project.mdc` — this agent does commit and push, but only after your explicit approval at each step. Consider updating that rule in `manet-project.mdc` so it isn't inconsistent with what `manet-pr` actually does.
- **`.cursor/skills/mesh-debug/SKILL.md` is not automatically discovered by Claude Code** — that's Cursor's skill location. Copy or symlink it to `.claude/skills/mesh-debug/SKILL.md` so Claude Code can find and invoke it via the Skill tool, for example when you ask an agent to debug a live node.

## The chain

1. **manet-architect** (read-only, opus) — validates the design before any code gets written.
2. **manet-implementer** (Read/Edit/Write/Bash, inherit) — builds the implementation. Creates its own feature branch (existing project rule) and mirrors live-node fixes back into the repo.
3. **manet-reviewer** (read-only, opus) — independent second look, can't change anything. Explicitly checks for the interface-naming/AP-selection pitfall that your own mesh-debug skill documents as a known issue.
4. **manet-tester** (Read/Bash, sonnet) — runs tests/linters, reports only failing checks.
5. **manet-pr** (Read/Grep/Glob/Bash, sonnet, permissionMode `default`) — itself runs `git commit`, `push`, and `gh pr create` once reviewer and tester have signed off, but asks for your explicit approval at every step (commit, push, PR), explained in plain language — suited to not wanting to be deep in git yourself.
6. **manet-node-debug** (Read/Bash, sonnet) — SSHes into a live test node and runs the full diagnostic sweep from your `mesh-debug` skill (interfaces, batman-adv, bridge, Alfred, services, provisioning, performance). Because this is test hardware (recoverable via reflash from the repo, not production), routine actions — restart, config fix, reflash — run freely without asking for approval each time. It only checks in first for something genuinely destructive, or before rolling out an unverified fix to the whole fleet at once. Also flags when a live fix is really a repo bug that should go through manet-implementer.

Typical use, in a single prompt to Claude Code:

> Use manet-architect to validate this design, then have manet-implementer build it, manet-reviewer review it, manet-tester run the tests, and finally manet-pr handle the commit and PR.

Claude then calls them in sequence. At manet-pr, you get a plain-language explanation and an approval prompt before each git step (commit, push, creating the PR) actually happens.

## To adjust for your situation

- **`model:` fields** are a starting point (architect/reviewer on `opus` for the heaviest reasoning work, implementer on `inherit`, tester/pr on `sonnet`). Adjust to what your plan allows — feel free to lower everything to `sonnet` if `opus` isn't available.
- **Linter names** in `manet-implementer.md`, `manet-reviewer.md`, and `manet-tester.md` now cover Go (`golangci-lint`, `go vet`, `go test -race`), shell (`shellcheck`), C (`cppcheck`/`clang-tidy`), and Python (`flake8`/`mypy`) — adjust if the repo uses a different toolchain (e.g. a specific `.golangci.yml` config, or a language not yet covered).
- **Hooks**: for hard enforcement (not just an instruction in the prompt) you can add a `PostToolUse` hook to `manet-implementer.md` that automatically runs the linter after every `Edit` and blocks on failures. A `PreToolUse` hook on `manet-pr` that flatly blocks `git commit`/`git push`/`gh pr create` is an extra safety net on top of the prompt instruction, if you want that.
- **`isolation: worktree`** is worth it for `manet-implementer` if you want to keep experimental changes separate from your main checkout until the reviewer signs off.