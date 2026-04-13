# XActions vs x-cli — detail comparison

A load-bearing reference for maintainers. Focus: how the two projects handle
auth, transport, throttling, storage, and operational risk. Every claim is
pinned to a file path so it can be re-verified when either side drifts.

XActions paths in this doc are relative to
`reference/XActions/` in this repo (vendored via `git clone`).
x-cli paths are project-relative.

---

## 1. TL;DR

| Axis                  | XActions                                                        | x-cli                                                              |
|-----------------------|-----------------------------------------------------------------|--------------------------------------------------------------------|
| Runtime               | Node.js + Python dual-runtime, ~340K LOC                        | Single Go binary, ~2K LOC                                          |
| Scope                 | 5 products in one repo (CLI, MCP, API, extension, Python pkg)   | CLI only, no MCP, no server, no DB                                 |
| Auth modes            | Guest token, cookie import, credential login (multi-subtask)   | Cookie import only. No guest, no credential login.                 |
| Cookie storage        | `~/.xactions/config.json` plaintext; optional AES-256-GCM file  | OS keychain primary (`go-keyring`) + AES-GCM file fallback         |
| TLS fingerprint       | Default Node `fetch` (distinctive JA3/JA4)                      | Default Go `net/http` for now; `utls` Chrome impersonation planned |
| Header rotation       | UA rotates across 5 strings per request                         | UA + client-hints pinned per session                               |
| Endpoints             | Hardcoded in `src/scrapers/twitter/http/endpoints.js`           | Data in `endpoints.yaml`, override at `~/.config/x-cli/`           |
| Throttling (reads)    | Exponential backoff on 429 only                                 | Per-endpoint token bucket + server-hint autopause                  |
| Throttling (writes)   | None in HTTP client. Browser-script guidance only.              | Min-gap + jitter + daily cap + autopause on error clusters         |
| Mutations default     | Execute immediately                                             | Dry-run by default; `--apply` required                             |
| Egress IP check       | None                                                            | `x doctor` flags cloud ASN; `grow` refuses from cloud              |
| ToS warning           | README footnote                                                 | On-screen warning at `x auth import`                               |
| Persistence           | Prisma + Postgres + Redis + Bull                                | None. CLI is stateless except for the session blob.                |

---

## 2. Authentication — deep dive

This is where the two projects differ the most and it's the section where
future drift matters most. Read carefully.

### 2.1 Required cookies

Both sides require the same two cookies:

- `auth_token` — the session token. In the real browser it's HttpOnly;
  copying it out requires DevTools → Application → Cookies.
- `ct0` — CSRF token. Sent as a cookie and also mirrored into
  `x-csrf-token`.

`twid` (user id) is optional for both.

**XActions:**
`reference/XActions/src/scrapers/twitter/http/auth.js:260` checks for
`auth_token`, `:263` checks for `ct0`, both throwing `AuthError` if absent.

**x-cli:**
`api/auth.go` — `ParseCookieString` returns a map; `RequireAuthCookies`
enforces both keys and returns `*AuthError`.

### 2.2 Bearer token

Both sides use the same public bearer token embedded in X's web client JS
bundle. Identical value.

**XActions:**
`reference/XActions/src/scrapers/twitter/http/endpoints.js:31`
`reference/XActions/src/scrapers/twitter/http/auth.js:25`
(duplicated in two places — will drift).

**x-cli:**
`endpoints.yaml` under `bearer:`. Single source of truth, loaded by
`api/endpoints.go` → `LoadEndpoints`. Patchable without recompiling.

### 2.3 Cookie import flow

**XActions:**
`reference/XActions/src/scrapers/twitter/http/auth.js:257`
`loginWithCookies(cookieString)`:
1. Parse via `parseCookieString`.
2. Call `validateSession()` → `/i/api/1.1/account/verify_credentials.json`.
3. On failure, wipe `#cookies` and throw.
4. On success, cache the user object.

**x-cli:**
`cmd/auth.go` → `runAuthImport`:
1. Print on-screen risk warning (two lines, can't skip).
2. `cmdutil.ReadSecret` — reads without terminal echo if TTY.
3. Parse with `api.ParseCookieString`, enforce required keys.
4. Construct an `api.Client` with just this cookie set.
5. Call `client.VerifyCredentials(ctx)` — same endpoint.
6. On success, persist via `store.Save` (keychain-first).
7. Print `✓ logged in as @handle (name)`.

**Key difference:** x-cli shows the ToS / ban-risk warning before the
cookie is even typed. XActions does not.

### 2.4 Guest token

**XActions:**
`reference/XActions/src/scrapers/twitter/http/auth.js:199`
`getGuestToken()` hits `/1.1/guest/activate.json`, caches result for
~2.5h, includes a thundering-herd guard via `#guestActivationPromise`.
Used for anonymous reads of public endpoints.

**x-cli:** deliberately not implemented for v0.1. Rationale:

- Every useful scraping command needs more than the guest-token surface
  permits (follower lists, paginated timelines, search all require an
  authenticated session).
- Adds complexity for a narrow win.
- If we find a read we *really* want anonymized, we'll add it as a
  mode flag on the client, not a separate code path.

If we ever need it, the shape of `Client.VerifyCredentials` in
`api/auth.go` is a template: add `Client.AcquireGuestToken` that mutates
`session.guestToken` and `applyHeaders` picks between `x-guest-token` and
cookie+csrf based on what's set.

### 2.5 Credential login (username + password)

**XActions** implements the full multi-step login flow at
`reference/XActions/src/scrapers/twitter/http/auth.js:302`:

```
LoginJsInstrumentationSubtask
  → LoginEnterUserIdentifierSSO
  → LoginEnterPassword
  → AccountDuplicationCheck
  → LoginAcid (email verification)
  → LoginTwoFactorAuthChallenge  ← throws, cannot auto-solve
```

It hits `/i/api/1.1/onboarding/task.json` with a hardcoded `subtask_versions`
blob (`auth.js:391`) and injects an **empty** `js_instrumentation.response`
at `auth.js:311`. Extracts `Set-Cookie` manually from the final response
(`auth.js:542`).

**x-cli** does not have a credential login path and will not. Rationale:

1. Hitting the onboarding endpoint from a non-browser is one of the
   cleanest "automation" signals X has. LoginAcid challenges are nearly
   guaranteed on new IPs; CAPTCHAs aren't far behind.
2. Empty `js_instrumentation` fails closed when X hardens the gate,
   bricking the entire path.
3. 2FA can't be automated; users have to fall back to cookie import
   anyway. Shipping two auth modes where one dead-ends on 2FA is worse
   than shipping one.
4. A locked-out account is a much worse outcome than "had to open
   DevTools once."

If you're thinking about reintroducing this: don't. Read
`skills/x-cli/references/auth.md` section "What x-cli does not support,
and will not."

### 2.6 Session validation

Both call `/i/api/1.1/account/verify_credentials.json` with the full
authenticated header set and parse `id_str` + `screen_name` + `name`.

- XActions: `auth.js:567` `validateSession()`. Returns
  `{ valid, user, reason, status }`.
- x-cli: `api/auth.go` `(*Client).VerifyCredentials(ctx)`. Returns
  `(*User, error)` where error is `*AuthError` on 401/403 and `*APIError`
  on anything else ≥ 400.

x-cli surfaces verify-credentials in two places: during `x auth import`
(gate before saving) and in `x doctor` (diagnostic).

### 2.7 Session persistence

**XActions** writes cookies to a JSON file in the
[`twitter-scraper`](https://github.com/the-convocation/twitter-scraper)
on-disk format (`auth.js:641` `saveCookies(filePath)`):

- If an `encryptionKey` was passed at construction, sensitive cookies
  (`auth_token`, `ct0`, `kdt`) are wrapped with `aes-256-gcm` using
  `scryptSync(key, 'xactions-salt', 32)` as KDF.
- Otherwise, values are written plaintext.

Default deployment: **plaintext** in `~/.xactions/config.json`. The
encryption is opt-in and nothing in the CLI prompts for a key.

**x-cli** in `internal/store/session.go`:

- Primary: `zalando/go-keyring`. macOS Keychain, libsecret on Linux,
  Credential Manager on Windows.
- Fallback (keychain unavailable, e.g. Linux without a secret daemon):
  AES-256-GCM file at `~/.config/x-cli/session.enc`. Key derived from
  `machineID()` via SHA-256. Not a defense against an attacker with root;
  just so the blob isn't plaintext on disk.
- `Save` tries keychain first; on error falls back to file.
- `Load` tries keychain first; on error tries file; returns `(nil, nil)`
  if neither has anything (new install).
- `Delete` wipes both.

**Key differences:**

1. x-cli is keychain-first, not plaintext-first.
2. x-cli does not require the user to pass an encryption key — the
   machine-id fallback is automatic.
3. x-cli persists only one session. XActions persists a `cookieArray`
   that mirrors the twitter-scraper format; useful for cross-tool sharing
   but fatter on disk.

### 2.8 Refresh

**XActions** (`auth.js:619` `refreshSession()`): if credentials were
stored in-process, re-runs `loginWithCredentials`. Otherwise throws.

**x-cli:** no refresh. On `AuthError`, tells the user to re-import. This
is a direct consequence of not supporting credential login — there's
nothing to refresh with.

### 2.9 Request headers (authenticated)

Both send the same conceptual set. The differences are in the details that
matter for fingerprinting.

**XActions** (`reference/XActions/src/scrapers/twitter/http/client.js:141`
`_buildHeaders(authenticated=true)`):

```
authorization:           Bearer <public_bearer>
user-agent:              random pick from 5 UA strings per request
accept:                  */*
accept-language:         en-US,en;q=0.9
content-type:            application/json
x-twitter-active-user:   yes
x-twitter-client-language: en
x-csrf-token:            <ct0>                      (when authenticated)
x-twitter-auth-type:     OAuth2Session              (when authenticated)
cookie:                  rebuilt from #cookies map  (when authenticated)
```

No client-hint headers. UA is chosen at random *per request* from
`reference/XActions/src/scrapers/twitter/http/auth.js:37`.

**x-cli** (`api/client.go` `applyHeaders`):

```
Authorization:           Bearer <public_bearer>
User-Agent:              pinned Chrome 120 macOS string, per-session
Accept:                  */*
Accept-Language:         en-US,en;q=0.9
x-twitter-active-user:   yes
x-twitter-client-language: en

sec-ch-ua:               "Not_A Brand";v="8", "Chromium";v="120", "Google Chrome";v="120"
sec-ch-ua-mobile:        ?0
sec-ch-ua-platform:      "macOS"
sec-fetch-dest:          empty
sec-fetch-mode:          cors
sec-fetch-site:          same-origin

x-csrf-token:            <ct0>                     (authenticated)
x-twitter-auth-type:     OAuth2Session             (authenticated)
Cookie:                  rebuilt from Session.Cookies map
Content-Type:            application/json          (only when body present)
```

**Key differences:**

1. x-cli sends `sec-ch-ua*` + `sec-fetch-*` client-hint headers that
   match a real Chrome. XActions sends none. Client-hints are emitted by
   every modern Chrome; their absence is a fingerprint on its own.
2. x-cli pins the UA per session, not per request. Rotating UA without
   rotating TLS, headers, and client-hints *together* is worse than a
   stable UA — it looks like fake diversity. See
   `skills/x-cli/references/auth.md` section "Account risk" for the
   reasoning.
3. x-cli only sets `Content-Type` on requests with a body. XActions
   always sets it, which for GETs is a minor tell.

### 2.10 TLS fingerprint

Neither project currently impersonates Chrome's TLS ClientHello.

- XActions uses Node `fetch` → Node's TLS, which has a distinct JA3/JA4
  that does not match any browser.
- x-cli uses Go's `net/http` → `crypto/tls`, same problem, different
  fingerprint.

**x-cli plan:** wire `refraction-networking/utls` with a Chrome 120
`ClientHelloID` as an opt-out (default on) round-tripper. Will land in
`internal/tlsprint/` before `grow` mutations go real. `go mod tidy`
dropped the dependency for v0.0 because there's no import yet.

**XActions plan:** none that I can see in the code.

### 2.11 Cookie exfiltration on rotation

Both need to handle `ct0` refresh. Real X rotates `ct0` mid-session by
sending `Set-Cookie: ct0=...` on some responses.

- **XActions** does not read `Set-Cookie` on subsequent responses after
  cookie import. It only does so during credential login
  (`auth.js:542` `#extractLoginCookies`). This means a long-running
  XActions session will silently desync from the real `ct0` and start
  failing on mutations.
- **x-cli** has the same gap today. Fix planned: after every response,
  scan `resp.Cookies()` and merge any updated values into
  `Session.Cookies`, then persist. Simple and important.

**TODO (x-cli):** wire `Client.mergeSetCookies(resp)` into `request()`
post-response. File a tracking note in the code at `api/client.go`
around the response-handling branch.

---

## 3. Transport layer

### 3.1 Core request shape

**XActions:**
`reference/XActions/src/scrapers/twitter/http/client.js:177` `request()`.
Retry loop with `2^attempt * 1000 + jitter` backoff (`:237`), bailing on
`AuthError`, `NotFoundError`, and explicit `TwitterApiError` except
`RateLimitError`. 429 dispatches to a pluggable strategy
(`WaitingRateLimitStrategy` or `ErrorRateLimitStrategy`).

**x-cli:** `api/client.go` `(*Client).request(ctx, method, url, body, opts)`:
- Buffers the body once so retries don't drain a reader twice.
- Retries on network error, 5xx, and 429 (up to `maxRetries`, default 3).
- Backs off with `2^attempt * 1s + rand [0,500ms)` on network + 5xx.
- On 429, reads `x-rate-limit-reset`, sleeps until the server-provided
  reset time, clamped to [1s, 10m].
- 401/403 and 404 short-circuit the retry loop and return the response to
  the caller for typed-error conversion.

**Difference that matters:** x-cli buffers the body before the retry loop
so retries see identical bytes. XActions receives a plain body argument
and trusts the caller to pass a string. For JSON mutations that's fine,
but for future form-encoded retries it's a latent bug in XActions.

### 3.2 GraphQL builder

Both resolve `<queryId>/<OperationName>` against a base, and URL-encode
`variables` + `features` into query parameters for GETs, POST body for
mutations.

- XActions (`client.js:258` `graphql`, `client.js:291` `graphqlPaginate`):
  supports GET queries and POST mutations in one method. Paginate is an
  async generator.
- x-cli (`api/client.go` `GraphQL`): GET only for v0.0. Mutations will
  land in a separate `Mutate` method so they go through the mutation
  throttle path unambiguously. Pagination helper is a TODO — will live
  on the client as `GraphQLPaginate(ctx, name, vars, onPage func([]byte) bool)`
  once `followers` / `following` land.

### 3.3 Cursor extraction

Both walk the GraphQL response for a timeline entry with
`entryId` starting with `cursor-bottom`.

- XActions (`client.js:323` `_extractCursor`): two-level walk. Finds an
  `instructions` array, loops over `entries` / `moduleItems`, checks
  `content.value` or `content.itemContent.value` or the
  `content.cursorType === 'Bottom'` form.
- x-cli (`api/client.go` `ExtractBottomCursor` + `walkForCursor`):
  fully recursive, walks any `map[string]any` or `[]any`, picks the first
  `instructions` key and then the first entry whose `entryId` starts with
  the given prefix. Handles the two value-location shapes the same way.

Behavior is equivalent for known response shapes. x-cli's walker is
slightly more defensive against unfamiliar nesting, at the cost of not
short-circuiting the walk. Fine for v0.1 scale.

### 3.4 REST (v1.1)

Both use the same REST surface for follow/unfollow/block/mute.

- XActions: `client.js:376` `rest(path, options)` — always POST with
  form-urlencoded body by default, no GET path.
- x-cli: `api/client.go` `(*Client).REST(ctx, name, form, result)`:
  looks the endpoint up by name in `endpoints.yaml`, dispatches based on
  declared `method`. Routes through `Throttle.AwaitMutation` when the
  entry is `kind: mutation`, through `Throttle.AwaitRead` otherwise.

---

## 4. Throttling & safety posture

This is where x-cli puts real work that XActions doesn't have.

### 4.1 Read throttling

**XActions:** exponential backoff on 429. No proactive rate limiting.
`WaitingRateLimitStrategy` at `client.js:43` waits for
`x-rate-limit-reset` once the server says to; there's no per-endpoint
token bucket and no steady-state pacing.

**x-cli** (`api/throttle.go`):

- Token bucket per endpoint name, configured via `endpoints.yaml`:
  `rps` (refill rate), `burst` (max tokens). Defaults 0.5 rps / 2 burst.
- `Throttle.AwaitRead(ctx, name, rps, burst)` blocks until a token is
  available. Respects ctx cancellation.
- Buckets created lazily, shared across all calls to the same endpoint
  from one process.

### 4.2 Mutation pacing

**XActions:** none in code. CLAUDE.md guidance at
`reference/XActions/CLAUDE.md` says "all automation must include 1-3s
delays between actions" but there is no enforcement layer on the HTTP
client. Whether a consumer inserts a delay depends on the browser-script
author, not the library.

**x-cli** (`api/throttle.go` `AwaitMutation`):

- `min_gap` + `max_gap` per mutation endpoint in `endpoints.yaml`
  (defaults 8s / 22s for `friendshipsCreate`). Actual gap is uniform
  random in `[min, max]`.
- `daily_cap` per endpoint, enforced against an in-memory counter that
  resets on `time.Now().YearDay()` change. Returns `BudgetExhaustedError`
  when hit.
- Not persisted across restarts — a deliberate v0.0 choice. Will move to
  `~/.config/x-cli/state.json` before `grow` ships with real mutations.

### 4.3 Autopause on error clusters

**XActions:** none.

**x-cli** (`api/throttle.go` `Observe` + `checkAutopause`):

- On any 429 with a reset header, sets `autopauseUntil = reset`.
- On any 429 without a reset, or any 5xx, increments `errorStreak`.
- When `errorStreak >= autopause_on_error_cluster` (default 3), sets
  `autopauseUntil = now + autopause_duration` (default 10 min).
- On 2xx, zeroes `errorStreak`.
- Every `AwaitRead` and `AwaitMutation` checks autopause first and blocks
  until the deadline passes.

### 4.4 Egress / ASN check

**XActions:** none. Will happily run from any IP.

**x-cli** (`cmd/doctor.go` `egressInfo` + `isCloudASN`):

- Looks up egress IP + ASN via `ipinfo.io/json`.
- Matches ASN+org against a list of known cloud/hosting providers.
- On a cloud match, prints a warning and sets doctor's exit code.
- Planned (before `grow` ships): `grow` subcommand refuses to start on a
  cloud ASN unless the config explicitly opts in.

### 4.5 Dry-run default for mutations

**XActions:** mutations execute immediately. No built-in dry-run.

**x-cli:** all `grow` subcommands will dry-run by default and require
`--apply` to mutate. This is enforced at the cobra command layer (not yet
wired since the subcommands are stubs; will land with the first real
growth command). The config knob `safety.require_apply_flag` locks this
on for users who want belt + suspenders.

---

## 5. Endpoint map management

**XActions:**
`reference/XActions/src/scrapers/twitter/http/endpoints.js` hardcodes
every query id, operation name, and features blob. Rotating a query id
requires editing the file, rebuilding/republishing the Node package, and
a user `npm update`.

**x-cli:**
`endpoints.yaml` is data, loaded at runtime by
`api/endpoints.go` → `LoadEndpoints`. Resolved in this order:

1. `--endpoints` flag
2. `~/.config/x-cli/endpoints.yaml`
3. `./endpoints.yaml` next to the binary

Which means: when X rotates query ids or feature flags, a user can fix
their install by dropping an updated YAML into `~/.config/x-cli/`, no
binary rebuild needed. A signed `x-cli update endpoints` command that
pulls a signed YAML from a repo is the obvious extension — planned, not
yet shipped.

---

## 6. Scope comparison

Everything XActions has, organized by whether x-cli ports it, adapts it,
or drops it. "Adapt" means we take the idea, not the code.

| XActions subsystem                                  | x-cli status | Notes                                                                                            |
|------------------------------------------------------|--------------|--------------------------------------------------------------------------------------------------|
| HTTP scraper (GraphQL) — 7K LOC                     | Adapt        | Clean reimplementation in Go, endpoints as data. `api/client.go` + `api/profile.go` etc.         |
| Stealth browser (puppeteer-extra)                    | Drop         | HTTP-only project. Users needing browser automation use a userscript.                            |
| Playwright Python package                            | Drop         | Separate rewrite, not portable.                                                                  |
| Monolithic `src/cli/index.js` (3,207 LOC, 140 cmds)  | Drop         | Structure is the bug. We re-scope to ~16 commands, one file per resource.                        |
| MCP server (4K LOC, 140 tools)                       | Drop         | CLI IS the skill (`skills/x-cli/SKILL.md`). No separate MCP process.                             |
| Express backend + Prisma + Postgres                  | Drop         | Not a CLI concern.                                                                               |
| Bull + Redis job queue                               | Drop         | Replaced by in-process throttle + (future) state file.                                           |
| Stripe + x402 payments                               | Drop         | Not a CLI concern. Compliance risk for no user value at this stage.                              |
| Chrome Manifest V3 extension                         | Drop         | Separate runtime, separate project if anyone wants it.                                           |
| Remotion video rendering / xspace voice             | Drop         | Tangential. Not a Go ecosystem fit either.                                                       |
| Plugin system (dynamic `import()`)                   | Drop         | Dynamic plugin loading is an attack surface for a CLI binary.                                    |
| Thought-leader agent + A2A protocol                  | Drop         | Experimental in the source; nothing to port.                                                     |
| 60+ browser-paste scripts in `src/*.js`              | Reference    | Useful as a feature wishlist. Reimplement anything worth keeping as real CLI commands.           |
| Guest token flow                                     | Defer        | Not needed for v0.1 scope. Easy to add later.                                                    |
| Credential login flow                                | Drop         | Intentional. See §2.5.                                                                            |
| Cookie AES-GCM file persistence                      | Adapt        | Our version: machine-id-derived key, keychain primary, no user-managed key.                      |
| Rate limit strategies (Wait / Error)                 | Adapt        | Our version: token bucket + mutation budget + autopause, configured via YAML.                    |

---

## 7. Storage layout comparison

**XActions:** sprawl.

```
~/.xactions/config.json        session cookies, plaintext by default
prisma://...                   User, Subscription, Operation, JobQueue, snapshots
redis://...                    Bull job queue
~/.xactions/cookies.json       twitter-scraper format
```

**x-cli:** minimal.

```
$KEYCHAIN/x-cli/session         primary session blob
~/.config/x-cli/session.enc     AES-GCM fallback when keychain unavailable
~/.config/x-cli/config.yaml     optional user config
~/.config/x-cli/endpoints.yaml  optional user endpoint override
~/.config/x-cli/state.json      (planned) mutation counter persistence
```

Single session. Single binary. One optional config file. No background
processes.

---

## 8. Operational posture

| Check                                              | XActions | x-cli                          |
|----------------------------------------------------|----------|--------------------------------|
| First-run ToS / account-risk warning               | No       | Yes (at `x auth import`)       |
| Cookie import gated on verify_credentials success  | Yes      | Yes                            |
| Cookie rotation from `Set-Cookie` midsession       | No       | Not yet — tracked in §2.11     |
| Diagnostic command                                 | No       | `x doctor`                     |
| Egress ASN warning                                 | No       | `x doctor`                     |
| Dry-run default for mutations                      | No       | Yes (`grow` subcommands)       |
| Daily mutation cap enforced in code                | No       | Yes (`api/throttle.go`)        |
| Pause-on-error cluster                             | No       | Yes (`api/throttle.go`)        |
| Structured typed errors for auth / rate / api      | Yes      | Yes (`api/errors.go`)          |
| Plaintext secrets on disk                          | Yes      | No                             |
| Credential login code path                         | Yes      | No, by design                  |

---

## 9. What x-cli leaves open

Not everything is solved. Things a future version must address, in rough
order of urgency:

1. **`Set-Cookie` midsession cookie rotation** (§2.11). Real failure
   mode for any long-lived scrape.
2. **`utls` TLS impersonation**. Biggest single fingerprint reduction
   available to us. Required before `grow` ships.
3. **Mutation counter persistence across restarts**. Required before
   `grow` ships; otherwise a user can reset their daily cap by killing
   the process.
4. **Paginated GraphQL helper** on the client. Needed for `followers`,
   `following`, `tweets list`, `search posts`.
5. **Signed `x-cli update endpoints`**. When X rotates query ids,
   every install needs a fast path to recover. Until we have this, users
   patch `~/.config/x-cli/endpoints.yaml` by hand.
6. **Per-process bucket sharing / cross-process coordination.** If a
   user runs two `x` processes in parallel, throttle state is local to
   each. Not urgent, but a real constraint worth documenting.
7. **Secrets hygiene test.** A linter / test that fails if a cookie value
   could leak into logs. Not blocking v0.1 but cheap to add.

---

## 10. Re-verification checklist

When X changes something and this doc drifts, re-check these specific
points before trusting the comparison:

- [ ] Bearer token in `endpoints.yaml` still works against
      `verify_credentials`.
- [ ] GraphQL query ids for `UserByScreenName`, `UserTweets`, `Followers`,
      `Following`, `SearchTimeline` are accepted (not 404).
- [ ] `features` blob does not return a missing-key 400 from any shipped
      endpoint.
- [ ] `x-csrf-token` + `Cookie` header combination still authenticates.
      If X rotates to a different csrf scheme, both sides break.
- [ ] `sec-ch-ua` client-hint values still match a live Chrome. Bump the
      pinned version (`"120"` → current major) as needed; keep the UA
      string in sync.

If any of the above fail, the right fix is almost always YAML, not code.
