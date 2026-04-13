# Endpoints: query IDs and features

## Why this is data, not code

X's web client talks to an internal GraphQL gateway. Each operation has a
23-character `queryId` that changes every time X ships a new web bundle.
The operation name (`UserByScreenName`, `Followers`, etc.) is stable; the
id is not.

x-cli stores all query ids, operation names, and the `features` blob in
`endpoints.yaml`. This lets you patch a broken command without rebuilding
the binary: drop an updated file at `~/.config/x-cli/endpoints.yaml` and
rerun.

## When to update

Symptoms:

- `api graphql <Op>: http 404` — the query id was rotated out.
- `api graphql <Op>: http 400` with a body mentioning `feature` — the
  features blob is missing a new flag.
- `api graphql <Op>: http 400` with a body mentioning `variables` — a
  required variable was added; add it to the call site.

## Where to find fresh ids

Two well-maintained references:

- [`d60/twikit`](https://github.com/d60/twikit) — `twikit/client/gql.py`
  (`Endpoint` enum). Python but the ids are language-agnostic.
- [`the-convocation/twitter-scraper`](https://github.com/the-convocation/twitter-scraper)
  — `src/api-data.ts`.

When both agree, trust the more recently updated repo. If only one has an
id you need, use it.

You can also capture them yourself:

1. Open x.com in Chrome, log in, open DevTools → Network → `graphql`.
2. Trigger the action you care about (open a profile, scroll the timeline,
   run a search).
3. Find the request — the URL is `https://x.com/i/api/graphql/<queryId>/<OpName>`.
4. Copy both values into `endpoints.yaml`.

## Updating the features blob

`features` is an object of booleans that X's gateway uses to gate schema
changes. Every query call must send the set the server currently expects,
or you get a 400.

When a 400 response mentions an unknown feature key, add it to
`endpoints.yaml` with the default value the server expects (usually `true`
for new features, `false` if the key name has `_enabled` and you see 400s
on both values). Copy the current Chrome request body if in doubt.

## Per-endpoint rate budgets

Every GraphQL op has `rps` and `burst` fields that feed the read token
bucket. Mutation REST entries have `min_gap`, `max_gap`, and `daily_cap`.
See `throttle.md` for the model.

Rule of thumb: if you observe frequent 429s on an endpoint, halve its `rps`
before increasing gaps elsewhere. Burstiness gets you flagged faster than
steady-state volume.

## Bearer token

The bearer token in `endpoints.yaml` is the public one embedded in X's web
client JavaScript bundle. It's the same token `twikit` and
`twitter-scraper` use. If X rotates it (rare), update the `bearer` field.
It is not scoped to your account.
