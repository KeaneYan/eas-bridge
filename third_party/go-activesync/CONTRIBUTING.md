# Contributing to go-activesync

Thanks for considering a contribution. The bar is "ship working code
people want to use," not "follow a rigid process" — small PRs welcome.

## Before you open an issue

- **Bugs**: please use the [bug report template](.github/ISSUE_TEMPLATE/bug_report.yml).
  Server type / version / EAS protocol version negotiated are the
  three fields we'll always need.
- **Security issues**: do **not** file publicly. See
  [SECURITY.md](SECURITY.md) for private disclosure.
- **Questions / design discussion**: open a
  [Discussion](https://github.com/hstern/go-activesync/discussions)
  rather than an issue.

## Development setup

You'll need:

- Go ≥ 1.26 (we track the latest 1.26.x patch in CI)
- `make` (everything's wrapped in the top-level Makefile)
- `dot` from graphviz (only if you change a `.dot` file): `brew install graphviz`
- Docker + `docker-compose` (only if you want to run the integration
  tests locally)

```sh
git clone https://github.com/hstern/go-activesync
cd go-activesync
make            # prints all available targets
make test       # unit tests under -race with coverage
make ci         # everything CI runs (lint + tests + vulncheck)
```

## Coding standards

- **`gofmt`** is mandatory; `make fmt` will fix any drift. CI fails on
  drift via `make gofmt-check`.
- **`go vet` clean.** `make vet`.
- **Tests for every change.** New commands need fixture-driven unit
  tests at minimum; ideally also an integration test in
  `eas/integration_test.go`.
- **No external deps without a strong reason.** The library deliberately
  ships with stdlib + `smallstep/pkcs7`. Application-level concerns
  (NTLM, Kerberos, keyrings, persistent state) belong in the consumer,
  not in this library. If you need a new dep in `eas/` or `wbxml/`,
  mention why in the PR description.
- **Library-grade error handling.** Don't `panic` from library code;
  return errors. Wrap with `fmt.Errorf("eas: …: %w", err)` so callers
  can `errors.Is` / `errors.As` against the underlying type.
- **Doc comments on every exported symbol.** Match the prose style of
  what's already there (declarative, not aspirational).
- **No bare TODOs in main.** TODOs are acceptable in tests and
  experiments; in shipped library code, file an issue and link it.

## Adding an EAS command

The general shape (using `MoveItems` as the reference):

1. Confirm the command's WBXML codepage is registered in
   `wbxml/codepages.go`. If not, add it (one map literal).
2. Add a file in `eas/` named after the command (`moveitems.go`).
   Build the request with `wbxml.E(...)` and `c.post(ctx, "Cmd",
   doc)`. Parse the response. Define typed args + result structs.
3. Write unit tests (`*_test.go`) with hand-crafted WBXML fixtures
   using the `newTestClient` helper in `client_test.go`.
4. If the command is user-relevant, add an integration test in
   `eas/integration_test.go` and (optionally) an `Example*` function in
   `example_test.go` so it shows up on pkg.go.dev.
5. Update `eas/README.md`'s command catalog table.

## Submitting a PR

1. Fork + branch (any name).
2. Make your change. Run `make ci` before pushing — it catches the
   same things CI will.
3. Open the PR against `main` with a description that says **what** and
   **why**. The "what" is usually obvious from the diff; the "why" is
   what reviewers actually need.
4. CI will run lint + unit tests + integration tests. PRs that affect
   production code without test coverage will get nudged for tests.
5. One approving review + green CI = merge. We squash on merge to keep
   `main` tidy.

## Releases (maintainer notes)

Tags follow [SemVer](https://semver.org/). Push a tag like `v0.2.0` to
trigger the [release workflow](.github/workflows/release.yml), which
runs the lint+test gate, generates release notes from commits since the
previous tag, and warms up `proxy.golang.org` so the new version
appears on pkg.go.dev promptly.

## Code of conduct

By participating in this project you agree to abide by the
[Code of Conduct](CODE_OF_CONDUCT.md).
