# Repository Guidelines Design

## Objective

Create a root-level `AGENTS.md` titled **Repository Guidelines**. The document will be a concise, repository-specific contributor guide of approximately 250–350 words.

## Content

The guide will contain:

- **Project Structure & Module Organization:** identify `main.go`, `internal/` packages, generated Ent code and schemas, `config/`, `docs/`, runtime `data/`, and colocated Go tests.
- **Build, Test, and Development Commands:** document `go run main.go`, `go build`, `go test ./...`, coverage, `golangci-lint run`, and Ent regeneration.
- **Coding Style & Naming Conventions:** require `gofmt`, standard Go naming, focused packages, thin handlers, service-layer business logic, and avoidance of manual edits to generated Ent files.
- **Testing Guidelines:** describe table-driven or focused `TestXxx` tests, SQLite/Ent test helpers, temporary directories, and package-level or full-suite test commands.
- **Commit & Pull Request Guidelines:** reflect the repository’s Conventional Commit history, including scoped forms such as `feat(permission): ...`; require a clear PR description, verification notes, linked issues, and API/UI evidence where relevant.
- **Security & Configuration Tips:** warn against committing secrets or runtime data and call out permission seeds, Casbin rules, upload handling, cron SSRF controls, and plugin trust boundaries.

## Constraints

The document will use direct Markdown headings, short paragraphs, and compact examples. It will avoid duplicating the detailed runtime-agent documentation supplied previously and will focus on actionable contributor behavior.

## Verification

After writing, verify the title, word count, required sections, repository-specific paths and commands, and inspect the final Git diff to ensure no unrelated files are modified.
