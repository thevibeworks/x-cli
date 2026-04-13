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
│   ├── list <screen-name>                # -n --replies   (UserTweets / UserTweetsAndReplies)
│   └── get <tweet-id>                    # TweetResultByRestId
├── search
│   ├── posts <query>                     # -n --product --since --until --from --to --lang --filter --exclude --min-likes --min-retweets
│   └── users <query>                     # -n
├── followers <screen-name>               # -n
├── following <screen-name>               # -n
├── thread
│   └── unroll <tweet-id>                 # --all-authors  (TweetDetail conversation walk)
├── media
│   └── download <tweet-id|url>           # -o --quality
├── monitor
│   └── account <screen-name>             # -i --once  (poll loop, streams new tweets + follower delta)
└── grow                                  # mutations; dry-run unless --apply
    ├── follow-engagers <tweet-id|url>    # -n --min-followers --apply --i-know-its-a-cloud-ip
    └── follow-by-keyword <query>         # -n --min-followers --apply --i-know-its-a-cloud-ip
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

x tweets list jack -n 50
x tweets list jack -n 50 --replies
x tweets get 1234567890123456789

x search posts "golang" --product Latest -n 100
x search posts "rust" --from rob --since 2026-01-01 --min-likes 100
x search users "kubernetes"

x followers jack -n 200
x following jack -n 200
```

Pagination is automatic; `--limit` caps the number of items, not pages. Reads
are paced by the built-in token bucket from `endpoints.yaml`; you cannot flood
an endpoint even with concurrent commands sharing the same throttle.

## Threads and media

```bash
x thread unroll 1234567890123456789                    # self-thread only
x thread unroll 1234567890123456789 --all-authors      # include replies from anyone
x media download 1234567890123456789 -o ./dl
x media download https://x.com/jack/status/12345
x media download 1234... --quality orig                # original-resolution images
```

Media download picks the highest-bitrate `video/mp4` variant for videos and
applies a `?name=large` size hint to images. Files are written as
`<tweetID>_<index>.<ext>` to the output directory.

## Monitoring

```bash
x monitor account elonmusk -i 30s              # streams new tweets + follower delta
x monitor account elonmusk --once              # one snapshot, exit
```

Monitor is a polling loop, not a websocket — X does not expose a stream
interface on the web endpoints. The interval is clamped to a minimum of 15s
in code; anything below is both pointless and suspicious.

## Growth (mutations — careful)

```bash
# Follow likers + retweeters of a tweet (dedup'd, sorted by follower count desc)
x grow follow-engagers 1234567890123456789 -n 25
x grow follow-engagers 1234567890123456789 -n 25 --min-followers 100
x grow follow-engagers 1234567890123456789 -n 25 --apply

# Follow distinct authors of recent tweets matching a query
x grow follow-by-keyword "golang devops" -n 20
x grow follow-by-keyword "kubernetes" -n 20 --min-followers 500 --apply
```

Both subcommands:

  - dry-run by default; `--apply` is required to mutate
  - filter by `--min-followers`
  - cap at `-n` follows per run (default 25)
  - respect the global mutation throttle: min 8s / max 22s random gap,
    daily cap of 200, autopause for 10 minutes after 3 consecutive errors
  - on 429, read `x-rate-limit-reset` from the response and wait until
    then — not a fixed sleep
  - refuse to mutate from a cloud ASN unless `--i-know-its-a-cloud-ip`
    is also passed
  - treat X's "you have already followed this user" envelope as success
    (idempotent), so a re-run after a partial batch does not log false
    failures

Override the throttle defaults in `~/.config/x-cli/config.yaml` if you
know what you're doing.

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
