# Network checks and log inspection

Network checks use a separate one-minute backend scheduler and retain seven days
of samples. The source is either the panel host or one of the current user's
assigned servers. The target must also be an assigned server owned by that user;
the API never accepts an arbitrary destination address. SSH and Agent sources use
the same connection cache. Pausing monitoring on either endpoint pauses probes.
Agent enrollment does not infer a target IP from the HTTP connection: that peer
may be a reverse proxy or NAT gateway. Before using an Agent host as an ICMP
target, set its reachable IP or hostname in the server form. Otherwise the check
reports `target_address_required` with unknown packet counts. This address is
not required for the Agent to act as a probe source or for its management tunnel.

`GET/POST /api/network/checks`, `PUT/DELETE /api/network/checks/:check_id`,
`POST /api/network/checks/:check_id/run`,
`GET /api/network/checks/:check_id/trend?since=...&until=...`, and
`GET /api/network/checks/:check_id/history?since=...&until=...&limit=...&cursor=...` require
authentication. A check contains `name`, nullable `source_server_id` (null means
the panel), `target_server_id`, `packets` (1–10), `enabled`, `loss_threshold`
(0–100%), and nullable `latency_threshold_ms`. Accounts can configure 100 checks;
at most four probes execute concurrently. Manual probes share those bounds.

Samples expose `sent`, `received`, `loss_percent`, `min_ms`, `avg_ms`, `max_ms`,
and `mdev_ms`, all nullable where no measurement exists. `status` is `ok`, `loss`,
`down`, `error`, or `unknown`. `down` means sent ICMP packets received no replies;
it is not SSH or Agent availability. Execution/permission failures have unknown
packet counts and never manufacture 100% loss. BusyBox ping can provide min/mean/
max without deviation; in that case only `mdev_ms` remains null. The Windows
source computes statistics from individual .NET Ping replies.

Both history endpoints accept RFC3339 `since` and `until` timestamps spanning at
most seven days. Their `summary` covers the entire requested range. Loss is
`100 * (sum(sent)-sum(received))/sum(sent)`, and mean RTT is weighted by received
packets. Failed/unknown samples are counted separately and never enter the loss
denominator.

The trend response is `{buckets,summary,since,until,bucket_seconds}`. It covers the
complete requested interval in at most 500 buckets, including empty intervals.
Buckets are at least one minute wide, matching the scheduled probe cadence; an
hour uses 60 buckets and a seven-day range uses at most 500.
Each bucket has `recorded_at`, `until`, and `status`, plus the same statistics as
the summary: `sample_count`, `known_samples`, `unknown_samples`, `loss_samples`,
`down_samples`, `threshold_exceeded_samples`, packet totals, loss percentage, and
minimum/average/maximum RTT. A bucket's status reports the worst observed result
(`down`, then `loss`, then `error`, then `ok`); an empty bucket has `status: "none"`,
zero sample counts and null RTT/loss values. Counts preserve mixed outcomes even
when a bucket also contains successful probes.

The chart draws reply-weighted average RTT as a solid line and peak RTT as a
dashed line. Small RTT dots keep isolated measurements visible; empty intervals
remain gaps. Separate markers identify no replies (red), packet loss or threshold
breaches (amber), and unknown attempts (gray). Hover details include peak RTT and
all exception counts, so an average cannot hide a brief outage or latency spike.

Raw history returns `{samples,summary,truncated,next_cursor}`, newest first. The
API accepts `limit` from 1 to 2,000 (default 300); the panel requests 20 samples per
page. `truncated` means an older page exists. Pass the opaque `next_cursor` back
with the same check, `since`, and `until`; null means the last page. The cursor
uses timestamp plus sample identity, so samples sharing a timestamp are neither
skipped nor duplicated. It is navigation state, not an authorization credential;
ownership is checked on every request.

The browser refreshes the check list every 15 seconds but rereads trend history
only when the selected checks, range, or selected checks' latest observations
change. A separate visible-page timer checks once a minute whether the rolling
window needs to advance when probes stop; background tabs pause these timers.
Manual probes refresh their new result immediately. Switching targets or ranges
cancels obsolete requests. Older raw pages keep their original time window even
while the chart refreshes; returning to the first page resumes the latest window.
The panel displays the raw page's actual start and end times.

Changing a check's source/target clears its incomparable history and rejects late
samples from the old pair. Threshold transitions are passed to the shared
notification queue; unknown samples do not announce recovery.

The panel runtime needs `ping` and permission to send ICMP. A missing utility or
missing ICMP capability produces an explicit probe error. Linux commands follow
the [iputils ping manual](https://man7.org/linux/man-pages/man8/ping.8.html).

## Log endpoints

`GET /api/servers/:id/logs` queries Linux journald; Windows system journals return
`partial: true, reason: "unsupported"`. `GET
/api/servers/:id/docker/containers/:containerId/logs/page` reads Docker logs.
Both require ownership and use the shared SSH/Agent connection cache.

Parameters:

- `since` / optional `until`: RFC3339 timestamps, at most seven days apart.
- `unit`: an explicit journal service unit; alert links never infer one.
- `priority`: journal priority 0–7, including all more severe priorities.
- `keyword`: case-sensitive literal substring, not a regular expression.
- `limit`: 1–500 scanned records (default 200).
- `cursor`: opaque continuation tied to the original filters; `since` remains
  required in HTTP continuation requests so the query identity stays stable.

The response is `{entries,next_cursor,truncated,partial,reason,scanned}`.
Each entry includes `id`, `time`, `message`, optional `unit`/`priority`, and
`stream` (`journal`, `stdout`, or `stderr`). Reads have a 12-second deadline,
512 KiB per-stream capture limit and four-reader concurrency cap. JSON journal
records preserve multiline and binary messages. Keyword filtering applies to
the bounded scanned page: a page can have no matches yet provide a next cursor.

Journal continuation uses `--after-cursor` instead of command-line `--since`;
the two are never passed together. The official
[journalctl documentation](https://www.freedesktop.org/software/systemd/man/258/journalctl.html)
defines JSON output, forward line limits and cursor semantics. Access warnings,
missing journald, expired cursors, malformed output and timeouts are explicit
partial states rather than an apparently successful empty log.

Docker preserves stdout and stderr separately and uses timestamps plus per-stream
boundary counts to avoid repeating entries during short polling. Its CLI provides
a bounded tail rather than arbitrary forward journal-style pagination; when
older records were omitted, the API returns `truncated: true, partial: true,
reason: "tail_limit"`. Narrow the time interval to inspect older output. See
the [Docker logs reference](https://docs.docker.com/reference/cli/docker/container/logs/).

The browser follows by issuing cancellable cursor reads every three seconds and
retains at most 2,000 displayed entries. Navigation/filter changes cancel pending
reads. Alert log links contain the owning host and a ±10-minute incident window;
service and container entry points carry their known unit/container explicitly.
