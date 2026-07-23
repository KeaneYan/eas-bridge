# easmock — hand-written test doubles for the `eas` interfaces

`easmock` provides drop-in fakes for every interface
[`eas`](../) declares. Each interface in `eas` (the umbrella
`Client` plus ten feature-area sub-interfaces) has a sibling struct
here with one func-typed field per method.

## Why

The `eas` package's `NewClient` returns the `eas.Client` interface so
callers can swap in a fake during unit tests. `easmock` is the fake
they swap in — already written, zero codegen, zero deps.

Compared to standing up an `httptest.Server` plus a real
`StateStore`, an easmock-based test:

- has no goroutines and no network sockets;
- runs in microseconds;
- exposes call arguments directly to test assertions;
- fails loudly with a sentinel error when the code under test calls
  a method the test didn't configure (instead of silently 200-OK).

## How

Each method's behavior is controlled by a `*Func` field on the mock
struct. Set the Func to return what your scenario needs; leave it
nil and the method returns
`errors.New("easmock: <Method> not implemented")`.

```go
import (
    "context"
    "testing"

    "github.com/hstern/go-activesync/eas"
    "github.com/hstern/go-activesync/eas/easmock"
)

func TestInboxSummary(t *testing.T) {
    var c eas.Client = &easmock.Client{
        EmailClient: easmock.EmailClient{
            SyncEmailFunc: func(_ context.Context, fid string, _ eas.EmailSyncOptions) (*eas.EmailSyncResult, error) {
                if fid != "inbox" {
                    t.Errorf("unexpected folder %q", fid)
                }
                return &eas.EmailSyncResult{
                    SyncKey: "S2",
                    Added:   []eas.EmailItem{{Subject: "hello"}},
                }, nil
            },
        },
    }

    // Pass c to your code under test.
    summary := codeUnderTest(c)
    if summary != "1 new: hello" {
        t.Errorf("got %q", summary)
    }
}
```

Sub-interfaces compose into the umbrella the same way the real
`eas.Client` does:

```go
mock := &easmock.Client{
    EmailClient:  easmock.EmailClient{ /* … */ },
    FolderClient: easmock.FolderClient{ /* … */ },
}
var _ eas.Client = mock // compile-time conformance check
```

If your code only depends on a slice of the protocol (e.g. an
inbox-summarising function that needs `EmailClient` + `FolderClient`),
declare the narrower interface in your code and pass the matching
sub-mock directly — you don't have to wrap it in `easmock.Client`.

## What's covered

| `eas` interface       | `easmock` mock          | Methods |
|---|---|---|
| `eas.Client`          | `easmock.Client`        | umbrella, embeds the sub-mocks below |
| `eas.EmailClient`     | `easmock.EmailClient`   | 9 |
| `eas.CalendarClient`  | `easmock.CalendarClient` | 5 |
| `eas.ContactsClient`  | `easmock.ContactsClient` | 4 |
| `eas.TasksClient`     | `easmock.TasksClient`    | 5 |
| `eas.NotesClient`     | `easmock.NotesClient`    | 4 |
| `eas.FolderClient`    | `easmock.FolderClient`   | 10 |
| `eas.SettingsClient`  | `easmock.SettingsClient` | 6 |
| `eas.SearchClient`    | `easmock.SearchClient`   | 3 |
| `eas.ProvisionClient` | `easmock.ProvisionClient` | 4 |
| `eas.PingClient`      | `easmock.PingClient`     | 1 |

Each file ends with
`var _ eas.X = (*X)(nil)` so a missing method on the mock fails the
package build immediately — drift between `eas` and `easmock` can't
sneak in unnoticed.

## What's not covered

- `eas.Autodiscover` is a package function, not a method, so it has no
  mock. Wrap it in your own indirection if you need to fake
  autodiscovery.
- `eas.StateStore` already lives in the `eas` package as an interface;
  `eas.NewMemoryState` is the in-memory implementation suitable for
  tests. There's no easmock equivalent because `MemoryState` already
  is the test double.

## See also

- [`eas`](../) — the main package and its interfaces
- [godoc](https://pkg.go.dev/github.com/hstern/go-activesync/eas/easmock) — full API reference
