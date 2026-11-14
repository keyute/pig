# Kopi guidelines

## Workflow

- You must always use Taskfile to build, test, lint and format: `task build`, `task test`, `task lint`, `task fmt`. Tests run with `-race`.
- Never commit, tag or push.

## Layout

- Root `main.go` only calls `cmd.Execute()`.
- `cmd/` holds one file per cobra subcommand, each as `func xCmd() *cobra.Command`. No `init()`.
- Everything else lives under `internal/<noun>`.
- Behaviour lives in `internal/app`, the frontend-neutral controller. Frontends such as `internal/line` only pass input in and render events.

## PiG boundary

- Only `internal/pigruntime` (and tests) import `github.com/MichaelKinsy/PiG`; depguard enforces it.
- Never import PiG `internal/` packages, never use `coding/cli`, never add a `replace` for PiG, and never copy upstream code.
- Create every PiG session with every input explicit: agent dir, in-memory settings, `coding.NoResources`, `SkipBuiltinTools`, and explicit tool names for both `AllowedTools` and `InitialActiveToolNames`. A nil there turns on ambient discovery or stock tools.
- Never call PiG's `Session.ListSessions` or `Session.Events`.

## Authority

- Every model-facing tool goes through `internal/policy`: `Gate.Before` in the pre-dispatch hook and `Gate.Admit` inside execution.
- Only human control input changes mode. Sessions start in plan and resume in plan.
- Never add a shell or file-modifying tool without a designed OS execution boundary.

## State

- Kopi reads and writes only under its home (`$KOPI_HOME`, default `~/.kopi`). Never read or write `~/.pi`, `~/.pig`, `~/.claude` or any other tool's store, in code or in tests.
- Tests use `t.TempDir()`, synthetic credentials and no network, and synchronise on channels, never sleeps.

## Style

- Keep implementations simple. Prefer an established, maintained library over hand-rolled code when one fits; do not add one for something trivial.
- Wrap errors with `%w` and word them `failed to …`. Put a package's sentinel errors in its `errors.go`.
- Log with `log/slog` only.
- Test with testify `assert`/`require` and hand-written fakes. Each test file pairs with its source file. Test helpers shared across packages live in a `<pkg>test` sub-package.
- Comments carry only what the code cannot say.
