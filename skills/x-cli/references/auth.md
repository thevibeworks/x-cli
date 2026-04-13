# Authentication and account safety

## How x-cli thinks about auth

There is no OAuth, no application key, no X developer account involved.
x-cli imports **your** session cookies from a real browser and sends them
with the same internal headers X's web app sends. From X's backend point
of view, every request x-cli makes is you, from wherever you ran it.

Two cookies matter:

- `auth_token` — the session token (HttpOnly in the browser; you still need
  to copy it out manually from DevTools).
- `ct0` — the CSRF token. Sent both as a cookie and as `x-csrf-token`.

Everything else is optional. `twid` identifies the user id and is convenient
for sanity checks but not required for the request to succeed.

## Import flow

1. Log into x.com in a real browser on a machine you normally use.
2. DevTools → Application → Cookies → `https://x.com`
3. Copy the values for `auth_token` and `ct0`.
4. `x auth import`  → paste `auth_token=...; ct0=...; twid=u%3D...`
5. `x auth status` should print `session ok — @yourhandle`.

`x auth import` hits `/1.1/account/verify_credentials.json` before saving.
If the cookies are wrong or expired, nothing gets stored.

## Where the cookie lives

- **Primary**: OS keychain via `go-keyring` (Keychain on macOS, libsecret on
  Linux, Credential Manager on Windows).
- **Fallback**: `~/.config/x-cli/session.enc`, AES-256-GCM encrypted with a
  key derived from the machine id. This is not a defense against an attacker
  with root; it's just so the file isn't plaintext.

x-cli never writes the cookie to `~/.config/x-cli/config.yaml` or anywhere
else. `x auth logout` removes both keychain entry and file fallback.

## Cookie lifetime

- Cookies expire. When they do, `x auth status` and any scraping command
  will return an `AuthError`. Re-import from the browser.
- Re-importing from a different device can trigger X's "new login" email
  warning. That's a real email to a real user — not a false positive.
- If your account gets locked or rate-limited, x-cli cannot recover it for
  you. Go to x.com in the browser, handle the interstitial, then export
  fresh cookies.

## Account risk

Reading public data (profiles, tweets, search, followers, media) on your
own session is comparable to scrolling on x.com. You might trip a read rate
limit; you will not normally get suspended.

Mutations (`x grow`, future `x follow`, `x unfollow`, `x like`) are a
different animal. Every mutation ties your account id directly to a write
action with a non-browser TLS fingerprint. Signals X uses that x-cli cannot
fully hide:

- **TLS fingerprint.** Go's default `net/http` has a very different
  JA3/JA4 from Chrome. x-cli uses `refraction-networking/utls` to impersonate
  a Chrome ClientHelloID, which closes most of this gap, but not all of it.
- **Header set.** x-cli sends a pinned client-hint header profile
  (`sec-ch-ua`, `sec-fetch-*`) that matches the UA. It does not rotate
  UAs per request — rotating UAs without rotating everything else is worse
  than pinning.
- **JS instrumentation.** X's real web client computes anti-bot tokens in
  JavaScript. x-cli sends an empty instrumentation payload for reads; for
  writes it does the same. If X starts gating writes on a valid token,
  this whole path breaks.
- **Behavioral pattern.** x-cli has a per-mutation min gap + jitter + a hard
  daily cap, and autopauses after an error cluster. Beyond that it does not
  model "human" behavior — no breaks between sessions, no weekends, no sleep.
  If you run mutations 24/7 you will get flagged regardless.
- **Egress IP / ASN.** If your session normally logs in from residential
  ISP X and suddenly makes mutations from AWS, that's a cheap signal for X.
  `x doctor` prints your ASN and refuses mutations from cloud ranges by
  default.

## Rules of thumb

- Reads: low risk. Run freely, within the built-in throttle.
- Writes: treat every 100 follows or unfollows as a real action on your
  account. Space them over hours. Use `--dry-run` (the default) first and
  inspect the list before `--apply`.
- Never run `x grow` from a cloud VM.
- Never run `x grow` under cron. Long-lived automated writes are the single
  most reliable way to get action-blocked.
- If `x doctor` prints a warning, fix the warning before you do anything
  that mutates.

## What x-cli does not support, and will not

- **Credential login** (username/password to `/i/api/1.1/onboarding/task.json`).
  It's high-signal, triggers LoginAcid / 2FA / captcha, and gets accounts
  locked. Import from a real browser.
- **Headless browser fallback.** x-cli is HTTP-only. If an operation needs
  Puppeteer-style automation, it is out of scope. Use a browser extension
  or a DevTools-console userscript for that work.
- **Multiple accounts at once.** The session store holds one session. Use
  separate `--config` files and point each run at a different one if you
  really need this.
