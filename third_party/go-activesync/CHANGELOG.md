# Changelog

All notable changes to this project are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) loosely
and the project follows [Semantic Versioning](https://semver.org/).

## [v1.1.0] — 2026-05-10

### Added

- **Autodiscover well-known fallback.** After the four schema-aware
  Autodiscover steps fail, the library now probes the canonical EAS
  path at `<domain>`, `autodiscover.<domain>`, and `mail.<domain>`
  via HTTP OPTIONS and accepts any 2xx response carrying an
  `MS-Server-ActiveSync` or `MS-ASProtocolVersions` header. Handles
  deployments whose autodiscover responder doesn't speak the EAS
  `mobilesync` request schema — notably SOGo, which historically
  implements only the Outlook schema and rejects mobilesync with
  HTTP 400 `<ErrorCode>601</ErrorCode>`. Default on; opt out via the
  new `AutodiscoverOptions.SkipWellKnownFallback` field.
- **`cmd/eas-autoprobe`** — one-shot, read-only EAS protocol probe.
  Runs Autodiscover, Provision, FolderSync, plus per-class Sync /
  GetItemEstimate / FetchEmail / SearchEmail / GAL / Settings / OOF
  against one account. Reports per-step OK/FAIL with elapsed
  milliseconds; exits non-zero if any step failed. Human-readable
  table (default) or `-json` output. Credentials via `EAS_PROBE_USER`
  / `EAS_PROBE_SERVER` / `EAS_PROBE_PASSWORD` env vars (password
  never via flag). Optional `-ping` step for the long-poll path.

### Docs

- `eas/README.md` gains an Autodiscover section documenting the
  5-step flow and a Server-specific notes table covering known
  interop gaps with SOGo autodiscover and Z-Push BackendIMAP
  (`ResolveRecipients` status 5, empty `Accounts` from
  `GetUserInformation`, the Ping `<Folder>` parser shape already
  shipped in v0.2).
- Repo-root README gets a Packages-table entry for `cmd/eas-autoprobe`
  and a note about the well-known fallback in the Autodiscover
  capability list.

### testenv

- New `make probe` target runs `cmd/eas-autoprobe` against the local
  Z-Push stack using the existing testenv credentials, with those
  credentials hoisted to individual make variables so both `test`
  and `probe` reference one source of truth.

## [v1.0.0] — 2026-05-10

**Breaking change.** The library API is now interface-first. Every
public surface of `eas` is reachable through interfaces, the concrete
client struct is unexported, and a new `eas/easmock` subpackage
provides hand-written test doubles so consumers can unit-test their
EAS-touching code without standing up an `httptest.Server`.

### Breaking

- `eas.Client` is now an interface, not a struct. It composes ten
  feature-area sub-interfaces — `EmailClient`, `CalendarClient`,
  `ContactsClient`, `TasksClient`, `NotesClient`, `FolderClient`,
  `SettingsClient`, `SearchClient`, `ProvisionClient`, `PingClient` —
  plus a `LastPolicy()` method.
- `eas.NewClient(cfg)` returns the `Client` interface; the underlying
  concrete type is unexported.
- Method signatures are unchanged. The existing 52 exported methods
  are now interface methods on `Client`.

### Migration

For most callers the change is a one-liner:

```go
// before:
var c *eas.Client
c, err = eas.NewClient(cfg)

// after:
var c eas.Client
c, err = eas.NewClient(cfg)
```

Type inference (`c, err := eas.NewClient(cfg)`) keeps working without
changes. Callers that embedded `*eas.Client` in their own types or
reached for unexported fields will need to switch to the interface;
the test-double path covers the most common reason callers reached for
the concrete (mocking).

### Added

- `eas/easmock` subpackage with one struct per `eas` interface and
  func-typed fields callers override. Methods whose `Func` is nil
  return a sentinel error, so misbehaving code paths surface loudly
  rather than silently calling a no-op. `easmock.Client` is the
  umbrella mock for the full `eas.Client` interface.
- `eas/interfaces.go` collects the type-level surface in one file for
  reading.

### Internal

- The concrete client lives at `httpClient` (unexported).
- `AGENTS.md` adds a hard rule: new commands extend the appropriate
  sub-interface plus the matching `easmock` mock; the compile-time
  conformance assertion in each easmock file catches drift.
- Architecture diagram (`diagrams/architecture.svg`) updated to show
  `eas.Client` as an interface and `easmock` as a satisfying box.

## [v0.2.1] — 2026-05-10

No library changes since v0.2.0 — the `eas/` and `wbxml/` public API
and behavior are unchanged. This is a test- and infrastructure-hardening
release.

### Testing

- Unit-test coverage 87.9% → 99.3% across `eas/` and `wbxml/`. Every
  documented error path (state-store failures, malformed server
  responses, protocol-status branches, parser edge cases) now has a
  regression test. Remaining uncovered code is genuinely defensive
  stdlib failure paths (`xml.Marshal`, `gzip.Buffer` write/close,
  `http.NewRequestWithContext` with hard-coded inputs, `pkcs7`
  internal errors, `net.DefaultResolver` fallback).
- `wbxml` exercises every header-truncation, `mb_u_int32` overflow,
  and unexpected-token path in encode/decode.
- New shared test helpers (`errStateStore`, `errSentinel`,
  `countingState`) for state-store failure injection across packages.

### testenv

- Z-Push BackendCombined IMAP pins to address the known
  SmartReply/SmartForward Status=120, FolderDelete HTTP 500, and
  Ping cold-boot Status=7 edges (#3): `IMAP_DEFAULT_CHARSET=UTF-8`,
  `IMAP_INLINE_FORWARD=true`, `IMAP_FOLDER_PREFIX=''`.
- `testenv/README.md` gains a "Known limitations" section that
  documents these edges and the workarounds the integration tests
  already encode.

### CI

- `gotestsum` wraps both unit and integration test runs and emits
  JUnit XML alongside the existing coverage profile; uploads feed
  Codecov Test Analytics (with caveats — see
  [codecov/feedback#637](https://github.com/codecov/feedback/discussions/637)).
- README embeds the Codecov coverage sunburst chart under a new
  "Coverage map" subsection.

## [v0.2.0] — 2026-05-10

### Fixed

- `parseEASTime` now handles iCalendar basic form (`YYYYMMDDTHHMMSSZ`),
  the wire format Z-Push BackendCalDAV / BackendCardDAV emit. Without
  this, every Z-Push-fronted CalDAV event arrived with a zero
  `StartTime`.
- Ping response parser handles the spec-correct
  `<Folder>id</Folder>` text form (MS-ASCMD §2.2.2.11.2). Previously
  `Status` came back as `2` but `ChangedFolders` was always empty
  against Z-Push.
- Per-item Sync helpers (`Create*`/`Update*`/`Delete*` for Event,
  Contact, Task, Note) now do the documented Status=3
  reset-and-replay, matching `SyncCalendar` and `ApplyEmailChanges`.
- `Settings.GetOof` returns a hard error on unparseable timestamps
  instead of silently returning a zero `time.Time`.

### Changed

- Default `Config.DeviceType` flipped from `"MCP"` to `"GoActiveSync"`.
  Only affects callers leaving it blank; explicit settings unaffected.

### Testing

- `testenv` now bundles Radicale alongside Dovecot + Postfix;
  integration suite covers Email, Calendar, Contacts, Tasks, and Ping
  notification.
- Unit-test coverage 73% → 88%.

### Docs

- `AGENTS.md` describes the project for AI coding agents.
- Library docs no longer reference unannounced consumers.

## [v0.1.0] — 2026-05-09

Initial release. Microsoft Exchange ActiveSync (EAS) client for Go.
Implements every command in MS-ASCMD across protocol versions
12.0–16.1, all major auth schemes (Basic, Bearer, NTLM, SPNEGO,
mTLS), and the WBXML codec the spec is built on.

Tested against Z-Push 2.7.6 + Dovecot via the bundled testenv stack.

[v1.1.0]: https://github.com/hstern/go-activesync/releases/tag/v1.1.0
[v1.0.0]: https://github.com/hstern/go-activesync/releases/tag/v1.0.0
[v0.2.1]: https://github.com/hstern/go-activesync/releases/tag/v0.2.1
[v0.2.0]: https://github.com/hstern/go-activesync/releases/tag/v0.2.0
[v0.1.0]: https://github.com/hstern/go-activesync/releases/tag/v0.1.0
