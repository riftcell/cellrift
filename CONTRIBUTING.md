# Contributing to Cellrift

Thanks for your interest in contributing.

This project has a [Code of Conduct](CODE_OF_CONDUCT.md); by participating you
agree to abide by it. Note its project-specific clause: discussing protocol
weaknesses and malformed traffic is on-topic here, but using this project to
coordinate attacks against systems you do not own is not. See
[`TERMS.md`](TERMS.md).

## Prerequisites

- Go 1.26.6 or later (`go version`) — this is a security pin, not an
  arbitrary floor; see `go.mod`'s own comment for the advisories it clears

`make lint` runs [`staticcheck`](https://staticcheck.io/) via `go tool` — no separate install needed; the version is pinned in `go.mod`.

## Discuss before you build

Open a GitHub issue **before** starting work on:

- A new protocol adapter
- A change to a public interface (`ProtocolAdapter`, `StepResult`, scenario YAML schema)
- A feature that touches more than one package

For small bug fixes, documentation changes, and new scenario files, go straight to a PR.

## Workflow

1. **Fork** the repository on GitHub
2. **Create a branch** from `main` — use a short prefix: `feat/`, `fix/`, `docs/`, `chore/`
3. **Make your changes** — keep commits small and focused
4. **Run checks** before pushing: `make build && make test && make lint`
5. **Open a pull request** against `main` with a clear description of what and why, and a note on how you tested the change

## Commit messages

Use the imperative present tense and keep the subject line under 72 characters:

```text
Add Diameter CCR/CCA credit-control step
Fix SIP dialog CSeq not incrementing on BYE
Refactor buildAVPsFromFields to table-driven lookup
```

Reference a GitHub issue number when relevant: `Fix #42 — receive timeout not propagated`.

## AI tooling

AI tools (GitHub Copilot, Claude, ChatGPT, and others) may be used freely at any stage of development — for writing code, tests, documentation, or reviewing your own changes before submission.

The committer is fully responsible for everything that lands under their name. Before opening a pull request, verify that AI-generated or AI-assisted code meets the same bar as hand-written code:

- Read and understand every line you submit — do not merge output you cannot explain
- Run the full test suite (`make test`) and fix any failures
- Check that complexity, naming, and structure follow the conventions in [CLAUDE.md](CLAUDE.md)
- Review the diff as a whole before pushing, not just the individual edits

AI assistance does not transfer or reduce the committer's responsibility for correctness, security, or code quality.

## Guidelines

- New protocol adapters must implement `ProtocolAdapter` in `internal/protocols/common/`
- New scenario YAML files must pass `cellrift validate` before committing
- All new packages must include at least one unit test
- Keep the binary self-contained — no new runtime dependencies without discussion first

### Protocol contribution checklist

A new protocol adapter is not complete until all of the following are in the same PR:

- [ ] Adapter package under `internal/protocols/<name>/` implementing `ProtocolAdapter`
- [ ] Unit tests covering the happy path and at least one error path
- [ ] One or more scenario YAML files under `scenarios/`
- [ ] Entry added to `docs/roadmap.md` under the appropriate phase
- [ ] `CHANGELOG.md` updated under `Unreleased`
- [ ] If output format changes: `make rebuildexamples` run and golden files committed

### Synthetic test data

Never commit real subscriber data. All test scenarios and fixture files must use:

- Phone numbers from the Ofcom test range (`447700900xxx`) or equivalent ITU reserved ranges
- `.lab.local` domains for SIP identities and hostnames
- Fictional IMSIs, IMEIs, and IP addresses

## Changelog

Update `CHANGELOG.md` for every user-facing change: new feature, bug fix, changed behaviour, or new CLI flag. Internal refactors and test-only changes do not require an entry.

## Licence grant

By opening a pull request you certify that:

1. You have the right to submit your contribution under the terms of the [MIT Licence](LICENSE)
2. Your contribution does not include code from third-party sources with licences incompatible with MIT
3. You understand that your contribution will be distributed under the MIT Licence without additional restrictions

This applies equally to code you wrote, code adapted from other sources, and AI-generated code you have reviewed and are submitting as your own work.

## Reporting Issues

Open an issue on GitHub with a minimal reproduction case. For security issues, email the maintainers directly rather than filing a public issue.
