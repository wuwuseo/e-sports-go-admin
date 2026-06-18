# Repository Guidelines

## Project Structure & Module Organization

`main.go` starts the Fiber application. Most application code lives under `internal/`: handlers parse HTTP requests, services contain business logic, middleware enforces cross-cutting rules, and `cron/`, `plugin/`, and `tasks/` implement background or extension features. Ent schemas are in `internal/ent/schema/`; the other files under `internal/ent/` are generated. Configuration is stored in `config/config.yml`, API documentation in `docs/`, runtime files in `data/`, and auxiliary commands in `cmd/`. Go tests are colocated with source files as `*_test.go`.

## Build, Test, and Development Commands

- `go run main.go` — run the API locally using the configured services.
- `go build -o go-admin.exe main.go` — build the Windows executable.
- `go test ./...` — run the complete test suite.
- `go test -cover ./...` — report package coverage.
- `golangci-lint run` — run configured formatting and static-analysis checks.
- `go generate ./internal/ent` — regenerate Ent code after schema changes.

Use Go 1.25.1 or the version declared in `go.mod`. Local database, cache, and storage dependencies must match `config/config.yml`.

## Coding Style & Naming Conventions

Run `gofmt` on every changed Go file. Follow standard Go naming: short lowercase package names, exported identifiers in `PascalCase`, and unexported identifiers in `camelCase`. Keep handlers thin and place domain logic in focused services. Prefer explicit dependency injection over new globals. Do not manually edit generated Ent files; modify schemas and regenerate them.

## Testing Guidelines

Use the standard `testing` package with `TestXxx` names. Prefer focused or table-driven tests near the package under test. Existing database tests commonly use Ent’s in-memory SQLite helpers; filesystem tests should use `t.TempDir()`. Add regression tests for bug fixes and authorization, upload, cron, or plugin edge cases. No fixed coverage threshold is enforced, but changed behavior should be covered.

## Commit & Pull Request Guidelines

History follows Conventional Commits, including scoped messages such as `feat(permission): add policy safeguards` and `fix: filter system permissions`. Keep commits focused. Pull requests should explain the behavior change, identify configuration or migration impact, link relevant issues, and list verification commands. Include screenshots or API examples when responses or user-visible behavior changes.

## Security & Configuration Tips

Never commit secrets, credentials, generated binaries, or runtime contents from `data/`. Review Casbin policies and system-permission seeds when adding protected routes. Preserve upload path checks, cron SSRF restrictions, sensitive-header filtering, and plugin trust boundaries.
