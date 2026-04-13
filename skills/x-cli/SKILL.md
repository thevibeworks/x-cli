---
name: x-cli
description: >
  Command-line client for scraping and lightly automating X (Twitter). Use
  when the user wants to read profiles, tweets, followers, search results,
  threads, or media from X; monitor an account; or run throttled growth
  actions (follow engagers / keyword follow). Talks to X's internal web
  endpoints with an imported browser cookie — not the official API. All
  mutations are dry-run by default.
---

# x-cli

```
x
├── auth
│   ├── import                            # paste cookie header, stores in OS keychain
│   ├── status                            # verify_credentials against X
│   └── logout                            # remove stored session
├── doctor                                # endpoints, session, egress IP, ASN check
├── config
│   ├── get [key]
│   └── path
├── profile
│   └── get <screen-name>                 # UserByScreenName
├── tweets
│   ├── list <screen-name>                # --limit --cursor
│   ├── get <tweet-id>                    # TweetDetail
│   └── replies <screen-name>
├── search
│   ├── posts <query>                     # --limit --latest
│   └── users <query>
├── followers <screen-name>               # --limit
├── following <screen-name>               # --limit
├── thread
│   └── unroll <tweet-id>                 # reconstruct a thread from a root tweet
├── media
│   └── download <id|url> [--out dir]     # image + video
├── monitor
│   └── account <screen-name>             # --interval poll loop, streams diffs
└── grow                                  # mutations; dry-run unless --apply
    ├── follow-engagers <tweet-id>        # --max --min-followers
    └── follow-by-keyword <query>         # --max --min-followers --lang
```

Global flags: `--config`, `--endpoints`, `--json`, `-v/--verbose`.

## Before anything else

x-cli uses your real logged-in session. Automating reads is low risk; mutating
at scale is not. Run `x doctor` once after `x auth import`. If `doctor`
reports a cloud ASN for your egress IP, do not run `x grow` — your session
normally logs in from residential and X will flag that asymmetry.

```bash
x auth import           # paste: auth_token=...; ct0=...; twid=u%3D...
x doctor                # expect: endpoints ok, session ok, egress not-cloud
```

## Read-only scraping

```bash
x profile get jack
x profile get jack --json

x tweets list jack --limit 50
x tweets get 1234567890123456789

x search posts "golang" --latest --limit 100
x search users "kubernetes"

x followers jack --limit 200
x following jack --limit 200
```

Pagination is automatic; `--limit` caps the number of items, not pages. Reads
are paced by the built-in token bucket from `endpoints.yaml`; you cannot flood
an endpoint even with concurrent commands sharing the same throttle.

## Threads and media

```bash
x thread unroll 1234567890123456789          # prints the thread in order
x media download 1234567890123456789 --out ./dl
x media download https://x.com/jack/status/12345
```

Media download grabs the best-quality variants (m3u8 → mp4, all image sizes).

## Monitoring

```bash
x monitor account elonmusk --interval 30s    # streams new tweets + follower delta
```

Monitor is a polling loop, not a websocket — X does not expose a stream
interface on the web endpoints. Respect the interval; anything under 15s is
both pointless and suspicious.

## Growth (mutations — careful)

```bash
x grow follow-engagers 1234567890123456789 --max 50
# ^ dry-run: prints who would be followed, does not mutate.

x grow follow-engagers 1234567890123456789 --max 50 --apply
# ^ actually follows, respecting:
#     - min 8s / max 22s random gap between follows
#     - daily cap (default 200)
#     - automatic 10-min pause after 3 consecutive errors
#     - refuses to run if doctor reported a cloud ASN
```

Defaults enforce the min-gap/jitter/daily-cap; override in
`~/.config/x-cli/config.yaml` if you know what you're doing. If a mutation
returns 429, x-cli reads `x-rate-limit-reset` and waits until then — not a
fixed sleep.

## Throttling model

Per-endpoint token bucket for reads (rps + burst in `endpoints.yaml`), plus
a global mutation state: minimum gap, jitter, daily cap, autopause after an
error cluster. One `x` process shares one bucket; multiple concurrent `x`
calls do not share state. Prefer one long-running command over many short
ones when you're doing bulk work.

## Pitfalls

```bash
# Query-ID rotation — x-cli treats endpoints.yaml as data.
# If a command starts failing with "api graphql ... 404" or "feature error",
# update endpoints.yaml (local override at ~/.config/x-cli/endpoints.yaml)
# from the latest twikit/twitter-scraper endpoint map and retry.

# Cookie expires or gets invalidated → 'x auth status' will fail.
# Fix: re-import from your browser.  x-cli does NOT refresh cookies.

# No credential/password login, on purpose.
# Logging in from a non-browser triggers LoginAcid / 2FA / captcha and
# raises your account's risk score. Always import from an already-logged-in
# real browser.
```

## References

- `references/auth.md` — cookie import flow, keychain, risks
- `references/throttle.md` — pacing rules, budgets, how to tune
- `references/endpoints.md` — how to patch query IDs when X rotates them
