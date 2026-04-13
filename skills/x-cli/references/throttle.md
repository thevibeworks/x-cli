# Throttle and pacing

## The model

x-cli has two throttling layers:

1. **Per-endpoint token bucket (reads).** Every GraphQL op in
   `endpoints.yaml` carries an `rps` and `burst`. A bucket refills at `rps`
   tokens/second up to `burst`. A call waits until one token is available.
   This applies uniformly across parallel goroutines in one process.
2. **Global mutation state (writes).** Mutations ignore the per-endpoint
   bucket and instead honor:
   - `min_gap` — minimum wall-clock time since the last mutation
   - `max_gap` — upper bound of the random jitter applied on top of `min_gap`
   - `daily_cap` — hard ceiling on mutations per local calendar day

Both layers **react to server hints**: when a response returns 429 with
`x-rate-limit-reset`, x-cli sets an autopause deadline to that timestamp
and blocks every subsequent call (read or write) until it passes. Three
consecutive 5xx/429 clusters trigger a 10-minute autopause as well.

## Defaults

```yaml
# endpoints.yaml excerpt
graphql:
  UserTweets:
    rps: 0.3        # ~ 18 req/min
    burst: 2
  Followers:
    rps: 0.2        # ~ 12 req/min
    burst: 1
rest:
  friendshipsCreate:
    kind: mutation
    min_gap: 8s
    max_gap: 22s
    daily_cap: 200
```

Rationale:

- Read rates of `0.2–0.5 rps` stay well below X's normal web-client read
  rate while still being fast enough for meaningful scraping.
- `8–22s` mutation spacing models a real user who sometimes clicks quickly
  and sometimes pauses; a fixed 10s gap is easier to fingerprint.
- `200 mutations/day` is below published thresholds where X routinely
  action-blocks, but is not a guarantee. Shrink it if you're cautious.

## Overriding

Override per-endpoint values in `~/.config/x-cli/endpoints.yaml` — x-cli
loads that file in preference to the one shipped with the binary. Do not
edit the shipped file; your changes get clobbered on upgrade.

Global defaults live in `~/.config/x-cli/config.yaml`:

```yaml
throttle:
  read_rps: 0.3
  read_burst: 2
  mutation_min_gap: 12s
  mutation_max_gap: 35s
  mutation_daily_cap: 120
  autopause_on_error_cluster: 3
```

## When it autopauses

- Any request returns 429 → wait until `x-rate-limit-reset`.
- Three consecutive 4xx-not-429 or 5xx responses → 10-minute pause.
- `x grow` from a cloud ASN → refuses to start unless you pass
  `--i-know-its-a-cloud-ip`, and even then only if the config allows it.

## What it will not do

- **Cross-process coordination.** Each `x` run has its own throttle state.
  If you run `x tweets list` and `x followers` in parallel shells, they do
  not share a bucket. Prefer one command, or use `xargs -P1`.
- **Persist mutation counters across restarts.** The daily cap resets when
  you restart the process. This is deliberate for v0.1; if it becomes a
  problem we'll persist the counter to `~/.config/x-cli/state.json`.
- **Simulate human behavior beyond jitter.** No weekly schedules, no
  reading delay before liking, no "scroll time". x-cli paces individual
  requests; shaping sessions is the user's job.

## Tuning tips

- Start with defaults. Run `x doctor` and a representative command.
- If `x-rate-limit-remaining` drops fast, lower the `rps` on that endpoint,
  not the global default.
- Never raise `rps` above `1.0` without strong reason. You will get 429s
  before it helps.
- For long-running scrapes, prefer one `--limit 10000` invocation over
  ten `--limit 1000` invocations — the throttle inside one process spaces
  requests better.
