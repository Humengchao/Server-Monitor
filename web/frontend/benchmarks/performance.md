Production browser measurements, 2026-10-18 (Windows, local Chromium).

Run `npm run build`, start `npm run preview -- --host 127.0.0.1 --port 4174`, then
`node scripts/performance.mjs http://127.0.0.1:4174`. All APIs are mocked; no host
commands run. The script uses Chrome's `Performance.TaskDuration` counter.

| Scenario | Before | After |
| --- | ---: | ---: |
| 100 hosts, main-thread work over 5.5 seconds | 15 ms | 1 ms |
| 500 hosts, main-thread work over 5.5 seconds | 62 ms | 9 ms |
| 1,000 hosts, main-thread work over 5.5 seconds | 73 ms | 21 ms |
| 2,096,023-byte text, 20 inserted characters, elapsed | 343 ms | 221 ms |
| Same editor input, main-thread work | 312 ms | 191 ms |
| Large text passed through UTF-8 encoding while typing | 41,920,670 bytes | 0 bytes |

Dashboard measurements hold subsequent inventory responses to isolate clock and
render work from changed samples. The new clock schedules freshness/expiry
boundaries instead of revisiting the entire fleet on an interval; unchanged
responses also share row references. This does not measure initial rendering or
server/network capacity, and does not establish a need for virtualization.

The editor keeps CodeMirror's persistent text tree, updates UTF-8 byte counts
from changed spans, and serializes only when saving/exporting. Save limits include
CRLF expansion. Existing draft protection, CRLF preservation, and session-expiry
export tests remain applicable; additional tests cover Unicode boundary edits,
multiple selections, and undo.

These are paired single-run development measurements, not universal latency
guarantees. Hardware load and browser scheduling affect short-duration timings;
rerun the script on target hardware before setting performance budgets.
