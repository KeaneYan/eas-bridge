# eas-autoprobe

A one-shot, read-only EAS protocol probe. Useful for verifying a deployment, debugging interop issues with Z-Push / SOGo / Exchange, and as a CI smoke test against [`testenv/`](../../testenv/).

The probe runs the canonical EAS surface against one account, prints per-command results, and exits non-zero if any step failed. It never sends mail, never creates or deletes server-side state beyond the policy key persisted by Provision.

## Install

```sh
go install github.com/hstern/go-activesync/cmd/eas-autoprobe@latest
```

## Usage

```sh
# Autodiscover the endpoint, password from env:
EAS_PROBE_PASSWORD=… eas-autoprobe -email user@example.com

# Pin the endpoint (skips Autodiscover):
EAS_PROBE_PASSWORD=… eas-autoprobe \
    -email user@example.com \
    -server https://mail.example.com/Microsoft-Server-ActiveSync

# Pipe the password from a password manager (no env exposure):
pass show mail/work | eas-autoprobe -email user@example.com -password-stdin

# Machine-readable output:
EAS_PROBE_PASSWORD=… eas-autoprobe -email user@example.com -json

# Verbose: log every HTTP exchange to stderr:
EAS_PROBE_PASSWORD=… eas-autoprobe -email user@example.com -verbose
```

## Flags

| Flag | Default | Notes |
|------|---------|-------|
| `-email` | `$EAS_PROBE_USER` | Account email or username; also used as the Autodiscover input |
| `-server` | `$EAS_PROBE_SERVER` | Pin the EAS endpoint URL and skip Autodiscover |
| `-password-env` | `EAS_PROBE_PASSWORD` | Env var holding the password |
| `-password-stdin` | off | Read the password from stdin (one line, including a trailing newline) |
| `-device-id` | derived | 32-hex device identifier; default is a stable SHA-256 prefix of the email |
| `-as-version` | `14.0` | EAS protocol version sent in `MS-ASProtocolVersion` |
| `-json` | off | Emit one indented JSON record to stdout instead of the human-readable table |
| `-verbose` | off | Log every HTTP exchange at DEBUG (to stderr) |
| `-ping` | `0` | If non-zero, run a `Ping` step with this heartbeat (seconds). Off by default because Ping blocks for the full heartbeat |
| `-timeout` | `2m` | Overall context timeout for the whole run |

Three environment variables back the most-used flags so you can keep everything out of shell history:

| Variable | Backs |
|----------|-------|
| `EAS_PROBE_USER` | `-email` |
| `EAS_PROBE_SERVER` | `-server` |
| `EAS_PROBE_PASSWORD` | the password (no flag — env or `-password-stdin` only) |

The password is *never* accepted as a flag argument; it would land in shell history and process listings.

## Commands run

Each step is independent; a failure on one doesn't abort the rest. The probe always runs these in order:

1. **Autodiscover** — skipped when `-server` is pinned
2. **Provision** — policy handshake (`X-MS-PolicyKey` cached for the rest of the run)
3. **FolderSync** — folder hierarchy
4. **SyncEmail (inbox, prime)** + **SyncEmail (inbox, fetch)** + **GetItemEstimate** + (if inbox is non-empty) **FetchEmail** + **SearchEmail**
5. **SyncCalendar** (prime + fetch)
6. **SyncContacts** (prime + fetch)
7. **SyncTasks** (prime + fetch)
8. **SyncNotes** (prime + fetch) — skipped if the server doesn't expose a Notes folder
9. **GetUserInformation**
10. **GetOof**
11. **ResolveRecipients** — looks up `-email` against the GAL
12. **Ping** — only when `-ping > 0`

## Output formats

**Human-readable** (default): one line per step, name + `OK`/`FAIL` + detail + elapsed milliseconds. Final summary line counts OK / fail / total wall time.

**JSON** (`-json`): one indented JSON record on stdout summarising the entire run. Schema is documented by the `runResult` type in `main.go`. *Not stable yet* — treat it as informational, the same posture as `go test -json`'s output schema before its formalisation.

## Exit codes

| Code | Meaning |
|------|---------|
| `0` | All steps OK |
| `1` | At least one step failed (the run still completed) |
| `2` | Misconfiguration: missing flag, unreadable password, etc. |

## Common failures and what they mean

Most failures fall into one of three categories. See the [`eas/README.md` Server-specific notes table](../../eas/README.md#server-specific-notes) for the full set.

- **`Autodiscover: HTTP 400 <ErrorCode>601</ErrorCode> "Not supported xmlns"`** — your server's autodiscover module doesn't speak the EAS `mobilesync` request schema (typical with stock SOGo). The well-known fallback should kick in and locate the endpoint; if it doesn't, pin `-server` directly.
- **`ResolveRecipients: status 5 (ServerError)`** — your server (typically Z-Push BackendIMAP) doesn't implement the GAL lookup verb. There's no client-side workaround.
- **`Provision: HTTP 449`** — the server requires re-provisioning. The library handles this transparently; if it surfaces as a failure here, the retry logic itself misfired — capture the run with `-verbose` and file an issue.

## See also

- [`testenv/`](../../testenv/) — Docker stack you can probe locally
- [`eas/`](../../eas/) — the library this binary exercises
