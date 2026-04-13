# x-cli

A small, sharp command-line tool for scraping and lightly automating X
(formerly Twitter) from your own logged-in session. One static Go binary,
built-in throttling, keychain-stored cookies, no server, no database, no MCP.

> **Heads-up.** x-cli talks to X's internal web endpoints, not a supported
> public API. Your real account cookie is the identity. This is not an official
> client, there is no SLA, and mutations (follow / unfollow / like) can get
> your account rate-limited, action-blocked, or suspended. Reading public data
> is low risk. Mutating at scale is not. Read `skills/x-cli/references/auth.md`
> before you run anything with `grow`.

## What it does

- `x profile get <user>` — scrape profile
- `x followers <user>` / `x following <user>` — paginated scraping
- `x tweets list <user>` / `x tweets get <id>` — user timeline and single tweet
- `x search posts <query>` / `x search users <query>` — scrape search results
- `x thread unroll <id>` — reassemble a thread from a root tweet
- `x media download <id|url>` — download images and videos from a tweet
- `x monitor account <user>` — poll a profile/timeline and stream deltas
- `x grow follow-engagers <tweet-id>` — follow likers/retweeters of a tweet (mutation, dry-run by default)
- `x grow follow-by-keyword <query>` — follow authors matching a query (mutation, dry-run by default)

## What it is not

- Not a wrapper over X's official v2 API. No API keys, no OAuth.
- No MCP server. The CLI *is* the skill — see `skills/x-cli/SKILL.md`.
- No Chrome extension, no dashboard, no database, no payments.
- No credential/password login. Cookie import only.

## Install

```
make build
./bin/x auth import
./bin/x doctor
./bin/x profile get jack
```

## Auth

Log into x.com in your real browser, DevTools → Application → Cookies, copy
`auth_token` and `ct0`, then:

```
x auth import
# paste: auth_token=...; ct0=...; twid=u%3D...
```

**Where the cookie lives.** x-cli tries the OS keychain first (`go-keyring`:
Keychain on macOS, libsecret on Linux, Credential Manager on Windows). If the
keychain is unavailable — headless boxes, containers, CI, Linux without a
Secret Service daemon — x-cli falls back to an AES-256-GCM file at
`$XDG_CONFIG_HOME/x-cli/session.enc` with mode `0600`.

**Be honest about the fallback.** The file's encryption key is derived from a
machine-stable seed (`/etc/machine-id` or the hostname), not from a passphrase
you control. Its job is to keep the cookie from being casually visible in
plaintext and to fail-closed on a file copied between machines. It is **not**
a defense against an attacker with read access to your home directory — they
can reproduce the key and decrypt it. Treat the keychain path as the real
at-rest protection; treat the file fallback as obfuscation.

`x auth logout` removes both the keychain entry and the file fallback.

## Throttle

Built in. Per-endpoint token buckets; mutation commands have a hard daily
budget, minimum action gap, and jitter. Configured in `endpoints.yaml`
alongside the query IDs. Mutations require `--apply`; default is dry-run.

## Layout

```
cmd/         cobra commands
api/         HTTP transport, endpoints, throttle, auth
internal/    cmdutil, keychain store, TLS fingerprint, version
skills/      agentic skill (CLI as skill)
endpoints.yaml   query IDs + features + per-endpoint budgets
```

## Credits

Endpoint map cross-referenced from [`twikit`](https://github.com/d60/twikit)
and [`twitter-scraper`](https://github.com/the-convocation/twitter-scraper),
both MIT. Reference layout inspired by
[`atlas-cli`](https://github.com/lroolle/atlas-cli) and
[`gh`](https://github.com/cli/cli). `XActions` is a reference only; no code
was copied (BSL-1.1).
