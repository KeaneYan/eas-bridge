# AGENTS.md

Instructions for AI coding agents (Claude Code, Cursor, Aider, Codex,
etc.) working in this repository. If you're a human contributor, read
[CONTRIBUTING.md](CONTRIBUTING.md) instead — it covers the same ground
with more handholding.

## What this repo is

A Go implementation of Microsoft Exchange ActiveSync (EAS) — the
mobile-mail sync protocol. Two packages:

- `eas/` — the EAS client. Every MS-ASCMD command across protocol
  versions 12.0 / 12.1 / 14.0 / 14.1 / 16.0 / 16.1.
- `wbxml/` — WAP Binary XML codec. All 25 EAS code pages.

`testenv/` is a Docker stack (Z-Push 2.7 + Dovecot + Postfix + Radicale)
used by integration tests. `diagrams/` is graphviz `.dot` source +
rendered `.svg` for the READMEs.

The project is a public OSS library (MIT, hstern/go-activesync).

## Daily commands

Everything runs through the top-level Makefile. `make` with no target
prints all available targets.

```
make ci          # everything CI runs: lint + race tests + coverage
make test        # race detector + coverage
make fmt         # gofmt -w (fix drift)
make svg         # re-render diagrams/*.dot → *.svg (needs graphviz)
```

Integration tests are gated on the `integration` build tag and need the
docker stack running:

```
cd testenv
make up          # build image + start container, wait for healthy
make test        # run go test -tags integration -v ./eas
make down        # tear it all down
```

`testenv/make test` sets the env vars for you. To run tests against a
different EAS server (your own Z-Push, SOGo, Exchange Online, …) set
`EAS_INTEGRATION_URL`, `_USER`, `_PASS`, `_DEVICE` yourself and run
`go test -tags integration ./eas` directly.

## Hard rules

These are non-negotiable. CI enforces most of them; your future self
will resent any drift.

1. **No plaintext secrets, anywhere.** Not in configs, not in
   examples, not in comments. Library examples should reference an
   external `pw` variable (resolved by the caller from a keyring or
   secret manager) rather than embedding a string.
2. **Tests for every change.** New commands need fixture-driven unit
   tests at minimum, ideally also an integration test in
   `eas/integration_test.go`. Don't ask first; write them alongside.
3. **`gofmt` clean** (`make gofmt-check`). `make fmt` fixes drift.
4. **`go vet` clean** (`make vet`).
5. **`go mod tidy` clean** (`make tidy-check`). Don't add a dep without
   a strong reason — see #6.
6. **Stdlib + `smallstep/pkcs7`** is the entire library dep budget.
   Application-level concerns (keyrings, persistent state stores,
   credential managers) belong in calling code, not in `eas/` or
   `wbxml/`. NTLM and Kerberos are wired in `eas/` via thin transport
   wrappers around `Azure/go-ntlmssp` and `jcmturner/gokrb5/v8`.
7. **`govulncheck` clean** (`make vulncheck`). If a CVE lands in a Go
   stdlib package, bump the `go-version` clause in
   `.github/workflows/ci.yml` and `go.mod` rather than vendoring around
   it.
8. **Library packages stay library-shaped.** `eas/` and `wbxml/` only
   import each other, the stdlib, and the listed third-party deps. No
   application-shaped types (CLI flags, JSON-RPC payloads, framework
   handles) in the public API surface — the library's job is to speak
   EAS, not to know who's calling it.
9. **Don't break the public API on patch releases.** SemVer applies.
   Renaming an exported symbol or changing a function signature is a
   minor (pre-1.0) or major (post-1.0) bump.
10. **The library API is interface-first.** `eas.Client` is the
    umbrella interface; sub-interfaces (`EmailClient`, `CalendarClient`,
    …) live in `eas/interfaces.go`. New commands add a method to the
    appropriate sub-interface, implement it on the unexported
    `*httpClient` concrete in `eas/`, and add a corresponding `Func`
    field + stub method to the matching mock in `eas/easmock/`. The
    compile-time `var _ eas.X = (*X)(nil)` line in each easmock file
    catches drift if you forget the mock side.

## Coding style

Go conventions, plus a few project-specific habits:

- **Doc comments on every exported symbol.** Match the prose style of
  what's already there: declarative, present tense, names the thing
  before describing it. ("`SyncEmail` issues a Sync command…", not
  "Issues a Sync command for the given folder.")
- **Default to no comments.** Only comment when the *why* is
  non-obvious — a hidden constraint, an MS-spec quirk, a workaround
  for a specific server bug. Don't restate what the code does.
- **Errors wrap with package prefix:** `fmt.Errorf("eas: foo: %w", err)`
  so callers can `errors.Is`/`errors.As` against the underlying type.
- **Library code never `panic`s.** Return errors. Save panics for
  programmer mistakes (nil receiver, etc.) where they're already
  unavoidable.
- **No `TODO` in shipped library code.** TODOs are fine in tests and
  experiments; in `eas/` or `wbxml/`, file an issue and link it.
- **Copyright header on every new source file** (Go, sh, conf, php):
  `Copyright (C) 2026 Henry Stern` + SPDX-License-Identifier line.

## Adding a new EAS command

Reference: `eas/moveitems.go` is a good clean example. The flow:

1. Confirm the command's WBXML codepage is registered in
   `wbxml/codepages.go`. If not, add it (one map literal).
2. Create `eas/<command>.go`. Build the request with `wbxml.E(...)`,
   send it via `c.post(ctx, "<Cmd>", doc)`, parse the response. Define
   typed args + result structs.
3. Write fixture-based unit tests in `eas/<command>_test.go` using
   `newTestClient` from `client_test.go`.
4. If the command is user-relevant, add an integration test in
   `eas/integration_test.go`. If it's API-shaped enough to be useful as
   documentation, add an `Example*` function in `eas/example_test.go`
   so it shows up on pkg.go.dev.
5. Update `eas/README.md`'s command catalog table.

## EAS sync semantics — read this before testing client-side mutations

The EAS Sync command does **not** echo a client's own adds back to it.
Once `CreateEvent` (or any other Sync/Add) returns, the server-side
state knows the item was created by *this* client+device, and won't
send it back as `Added` on the next Sync. To verify a CRUD round-trip,
do the verifying Sync from a *fresh client with a different DeviceID*
— see `TestIntegration_Calendar_CRUD` for the pattern. Z-Push tracks
sync state per `(user, device)`; reusing the original DeviceID would
also rotate keys out from under the original client.

## The integration testenv (`testenv/`)

Single-container Docker stack at `localhost:8580` exposing
`/Microsoft-Server-ActiveSync`. Z-Push uses BackendCombined to route:

- mail folders → BackendIMAP → Dovecot
- calendar folders → BackendCalDAV → Radicale
- contact folders → BackendCardDAV → Radicale

Test user `integration / integration`. Pre-seeded calendar at
`/integration/calendar` and addressbook at `/integration/addressbook`
in Radicale.

Edits to anything under `testenv/` require an image rebuild:

```
cd testenv
docker compose down -v
docker rmi testenv-zpush:latest
docker compose build
docker compose up -d
```

Z-Push logs are inside the container at `/var/log/z-push/`; useful
when a Sync returns 500.

## What to NOT do

- Don't add a feature flag, fallback, or backwards-compat shim
  speculatively. The public API is the public API; everything else is
  fair game to change.
- Don't create planning, decision, or analysis docs unless the user
  asks. Work from the conversation; PR descriptions hold the *why*.
- Don't commit unless the user asks. The convention is "I'll commit
  when you say so."
- Don't push to `main` without a green local `make ci`.
- Don't bypass the integration tests when touching `eas/calendar.go`,
  `eas/sync.go`, `eas/itemoperations.go`, or anything in `testenv/` —
  these have load-bearing behavior that the unit tests don't cover.
- Don't render `.svg` by hand. `make svg` runs graphviz; it's
  reproducible. Source of truth is the `.dot`.

## Cutting a release

Before pushing a `v*` tag, the changelog must already reflect what the
tag will say. The release workflow does NOT generate or update
`CHANGELOG.md` — that's a manual step done in the same commit (or
just before) the tag is created. Skipping it leaves the published
release notes pointing at a `CHANGELOG.md` that lies.

Order of operations:

1. **Update `CHANGELOG.md`.** Add a new `## [vX.Y.Z] — YYYY-MM-DD`
   section above the previous one, fill it in, and update the
   compare-link footnotes at the bottom of the file. Mirror the same
   prose into the tag annotation message in step 3 — they should
   read identically.
2. **Commit.** `git commit -m "changelog: vX.Y.Z"`. Push to `main`
   and confirm CI is green before tagging.
3. **Create the annotated tag.** `git tag -a vX.Y.Z -m "<body>"`.
   Use the same content as the `CHANGELOG.md` entry for the version.
4. **Push the tag.** `git push origin vX.Y.Z`. The release workflow
   creates the GitHub release, warms `proxy.golang.org`, and emits
   notes. Edit the auto-generated body to match the tag message if
   it diverged.
5. **Verify.** Watch `pkg.go.dev/github.com/hstern/go-activesync@vX.Y.Z`
   for indexing (usually within a few minutes).

If you find yourself about to tag without a matching `CHANGELOG.md`
entry, stop and add it first. Skip-and-fix-later is a near-100%
guarantee that the release page and the file disagree.

## Repo layout cheat sheet

```
eas/                       EAS client (public API)
  *.go                     one file per command + shared types
  client.go                HTTP transport, auth, retry policy
  integration_test.go      gated on `integration` build tag
  README.md                package landing page (with diagrams)
  diagrams/                .dot source + rendered .svg
wbxml/                     WBXML codec (public API)
  codepages.go             EAS code page registry
  reader.go / writer.go    token-level I/O
  README.md                package landing page
testenv/                   integration test target (Docker)
  Dockerfile.zpush         Z-Push + Dovecot + Postfix + Radicale
  zpush-patch.sh           sed-patches stock Z-Push configs
  README.md                stack details and troubleshooting
diagrams/                  top-level architecture diagram
.github/workflows/         CI (lint + test + integration + codeql + release)
Makefile                   `make ci`, `make test`, `make svg`, …
```

## Pointers

- **PR + branch protection**: `main` requires `lint` + `test` status
  checks. The `integration` job runs but is informational on PRs.
- **Releases**: tag `v*` → `.github/workflows/release.yml` runs the
  lint/test gate, generates notes, warms `proxy.golang.org`.
- **Security**: see `SECURITY.md`. Private disclosure to henry@stern.ca.
- **Issue templates**: `.github/ISSUE_TEMPLATE/`. Bug reports must
  include server type/version + EAS protocol version.
