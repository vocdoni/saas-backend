# dbaccess

Time-boxed direct access to a DigitalOcean managed MongoDB cluster. Grants
your current public IP to the cluster's *trusted sources* allow-list, opens
`mongosh`, and revokes the rule on exit — no bastion, no VPN, no standing
firewall rule.

## Prerequisites

- `doctl` authenticated with a token that has database **write** scope. The
  tool works by editing the cluster's trusted-sources allow-list (exposed
  in the DO API as `/databases/{id}/firewall`). Without write scope the
  append call 403s.

  Token scopes required, on the `database` resource:
  - `database:read` — for `databases list` and `firewalls list`
  - `database:update` — for `firewalls append` / `firewalls remove`

  A legacy Personal Access Token with `Write` checked works too. Create at
  <https://cloud.digitalocean.com/account/api/tokens>, then `doctl auth init`.
- `curl`, `awk` on `PATH`.
- A client: `mongosh` on `PATH` (default), or MongoDB Compass installed
  for `DB_CLIENT=compass`.

## Usage

Everything unset is prompted for interactively:

```sh
./dbaccess.sh
# cluster name: my-mongo-cluster
# mongo user:   readonly-user
# mongo database: analytics
# Enter password: *****
```

Or set env vars to skip the prompts:

```sh
DB_CLUSTER=my-mongo-cluster DB_USER=readonly-user ./dbaccess.sh analytics
```

To browse the data in **MongoDB Compass** instead of `mongosh`:

```sh
DB_CLIENT=compass DB_CLUSTER=my-mongo-cluster DB_USER=readonly-user \
  ./dbaccess.sh analytics
```

Compass registers `mongodb://` and `mongodb+srv://` as URL schemes, so the
tool hands it the connection URI directly and Compass opens with it
pre-filled. The grant stays open until you press enter at:

```
press enter when done to revoke access:
```

#### The password goes into the URI

Unlike `mongosh`, **Compass has no password prompt** — it takes the URI as
given and fails auth if the credential is missing. So on the compass path the
tool asks for the password (or reads `DB_PASS`), percent-encodes it, and
embeds it in the URI. What the terminal normally shows is the redacted form
(`lucas:***@…`) — the exception is the no-handler path below, where the full
URI is printed because you have to paste it somewhere.

That is a real exposure, so know where it lands:

- in the argument to `open`, briefly visible in `ps` while the call runs
- in macOS LaunchServices, which records opened URLs
- in Compass's saved-connections store, until you delete the connection there
- in your terminal scrollback, when no handler is registered for the scheme
  and the tool prints the URI for you to paste

On Linux the hand-off only happens when a handler is actually registered
(`xdg-mime query default x-scheme-handler/mongodb+srv` is non-empty). Without
one, `xdg-open`'s fallback for an unknown scheme is the **default browser** —
which would put the password in the URL bar, browser history, and possibly a
search engine. The tool prints the URI to paste instead.

The `mongosh` path is unchanged — no password is prompted for or embedded,
and the URI it builds is byte-identical to previous versions.

If you supply your own `MONGO_URI`, a `{PASS}` placeholder is substituted
when a password is set; if the template has no `{PASS}`, the tool splices one
in after `{USER}` — but only when the template literally contains `{USER}@`.
A template with a hardcoded username (`mongodb://lucas@{HOST}/{DB}`) or no
userinfo at all has nowhere to splice, so rather than dropping the credential
silently the tool refuses to start and tells you to add `{PASS}` yourself.
With no password, the whole `:{PASS}` segment is dropped.

Configuration:

| Var          | Meaning                                            |
| ------------ | -------------------------------------------------- |
| `DB_CLUSTER` | cluster name (as shown by `doctl databases list`)  |
| `DB_USER`    | mongo user                                         |
| `DB_NAME`    | mongo database (also accepted as positional arg)   |
| `DB_CLIENT`  | `mongosh` (default), or `compass`/`gui`            |
| `DB_PASS`    | mongo password — compass only; prompted if unset   |
| `MONGO_URI`  | full connection URI template (see below)           |

### Pre-existing rule for your IP

If the cluster already has an `ip_addr` rule for the public IP you're
about to grant — a standing office allow, a monitoring host, a teammate
on the same NAT'd IP — the tool detects it, prints the matching UUID(s),
and asks:

```
an ip_addr rule for X.X.X.X already exists on <cluster>:
  <uuid>
adopt it and revoke on exit? [y/N]:
```

- **No** (default) — the script exits without touching anything.
- **Yes** — the trap adopts the existing rule and revokes it on exit.
  Use this to reap a stale rule left behind by a hard-killed prior run,
  or when you know the rule is yours to clean up.

### Cleanup failures are surfaced

If the exit-time `list` or `remove` fails (token expired mid-session,
transient API error), the tool prints a `WARNING:` with the exact
`doctl` command to run yourself. Silent leaks would invert the tool's
whole guarantee.

### Exiting cleanly (so the rule gets revoked)

The trap only fires once `mongosh` returns. At the `mongo>` prompt, use
any of:

- `.exit` (or `exit`)
- `quit()`
- **Ctrl-D** (EOF)

**Not Ctrl-C** — inside `mongosh` that just cancels the current query
and drops you back at the prompt; the shell stays open and no rule is
revoked.

With `DB_CLIENT=compass` the session is the `press enter when done to
revoke access:` prompt, not the app — `open` returns immediately and
quitting Compass would take unrelated connections with it. Here Ctrl-C
**does** revoke (the opposite of the `mongosh` case above): it kills the
script at the read, which fires the trap. Compass keeps the connection in
its saved list afterwards; reconnecting without a live rule just times
out, so re-run the tool.

Closing the terminal window or sending `SIGTERM` / `SIGINT` to the
script still fires the trap. Only a hard kill (`kill -9`, power loss,
OOM) skips it — in that case run the script again from the same IP and
exit cleanly to reap the stale rule, or delete it via the DO console.

### Custom connection URI

By default the tool builds:

```
mongodb+srv://{USER}@{HOST}/{DB}?tls=true&authSource=admin
```

Override via `MONGO_URI`. Placeholders `{USER}`, `{HOST}`, `{DB}` are
substituted (all optional — put a literal URI in if you don't need them).

```sh
# Non-SRV endpoint with an explicit port and replica set
MONGO_URI='mongodb://{USER}@{HOST}:27017/{DB}?tls=true&authSource=admin&replicaSet=rs0' \
  ./dbaccess.sh
```

`mongosh` will still prompt for the password if it isn't in the URI.

## Self-check

```sh
DB_CLUSTER=my-mongo-cluster ./selfcheck.sh
```

Non-destructive. Verifies cluster resolution, the `ip_addr` type filter
(so cleanup can never delete an `app` rule by mistake), and the IPv4 regex
boundaries. Exits nonzero on any failure.

## Acceptance test (one-time, writes to production)

Not in `selfcheck.sh`. With sign-off, run the tool once and confirm:

1. Your IP appears in `doctl databases firewalls list <uuid>` while
   `mongosh` is open.
2. It's gone after you exit.
3. Any pre-existing `app` rules are unchanged.
