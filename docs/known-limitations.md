# Known limitations (the watch list)

Deliberate, documented tradeoffs — not bugs. lodestar favors **precision over recall**
(a false cross-edge costs more trust than a missed one), so several filters accept a
small, rare false-*negative* to kill a large amount of noise. Each entry names the
tradeoff, why it's fine for now, and the tighter fix to apply *if the real corpus shows
it biting*. Revisit an entry when it actually costs a real edge — not preemptively.

## isMockName false-drops a real service named `Mock*`/`Stub*`/… (2026-07-01)

**Where:** `internal/parse/golang_grpc.go` — `isMockName` / `mockPrefixes`.

**What:** gRPC test doubles (`NewMockFooClient`, `RegisterStubFooServer`, …) are ~60% of
raw `New*Client`/`Register*Server` hits and never a live service edge, so they're filtered
by a leading-token match (`Mock`/`Fake`/`Stub`/`Spy`/`Mocked`). It's a plain `HasPrefix`,
so a **real** service whose name starts with one of those tokens — `Mockingbird`, `Stubbs`,
`Spyglass`, `Faked…` — is also dropped.

**Why it's fine for now (Michael's call):** such service names are rare, and the noise
removed is large and real (mocks 27→0 on a 13-repo slice). "For now it'll be fine — it
works." Merged in PR #3 with this watch attached.

**The fix if it bites:** tighten to a camelCase boundary — the prefix must be followed by
an uppercase letter or end of string. `MockFoo` → `Mock`+`Foo` (drop, it's a mock *of* Foo);
`Mockingbird` → `Mock`+`ingbird` (keep, real name). One-line change to `isMockName`.

**Signal to act:** a legitimate service disappearing from the map on the real corpus. Until
then, leave it.
