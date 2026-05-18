# parity launchd job

Daily 02:00 UTC cron runs `noxctl plan --config-source=both -o json` and
captures the result in `~/.cache/noxctl-parity/<date>.json`. The
companion subcommand `noxctl parity-check` evaluates those files for
the 7-day clean streak required before the  atomic deletion
(D-12).

## Install

1. Replace `CHANGEME` placeholders in the plist with your real `$HOME`
   path (e.g., `/Users/cloud`). Replace `path/to/noxctl.toml` with the
   absolute path to your `noxctl.toml`. **Both substitutions are
   mandatory** — launchd's CWD is `/`, so relative paths fail
   silently.

2. Copy the plist into your LaunchAgents directory:

       cp examples/launchd/io.barad1tos.noxctl-parity.plist \
          ~/Library/LaunchAgents/

3. Bootstrap and enable:

       launchctl bootstrap gui/$(id -u) \
           ~/Library/LaunchAgents/io.barad1tos.noxctl-parity.plist
       launchctl enable gui/$(id -u)/io.barad1tos.noxctl-parity

4. (Optional) Run once manually to seed the cache without waiting for
   02:00:

       launchctl kickstart gui/$(id -u)/io.barad1tos.noxctl-parity

5. Wait 7 days, then verify the deletion gate:

       noxctl parity-check
 # exit 0 = PASS (7 consecutive clean days)
 # exit 1 = FAIL (drift in window)
 # exit 2 = ERROR (cache directory unreadable)

## Uninstall

       launchctl bootout gui/$(id -u)/io.barad1tos.noxctl-parity
       rm ~/Library/LaunchAgents/io.barad1tos.noxctl-parity.plist

## Retention

The cron retains 14 days of `<date>.json` files (7 for the gate + 7
for debugging context). Older files are deleted automatically by the
plist's inline `find -mtime +14 -delete` step.

## Troubleshooting

- **No JSON files appear after 02:00** — check
  `/Users/<you>/.cache/noxctl-parity-launchd.err` for stderr from the
  shell wrapper. Most common cause: `path/to/noxctl.toml` placeholder
  was not replaced.
- **Files appear but contain only stderr** — the `--config` path is
  wrong. The shell wrapper redirects stdout (the JSON) to `<date>.json`
  and stderr (errors from `noxctl plan`) to `<date>.err`. Inspect the
  `.err` companion file to see what went wrong.
- **Privacy** — daily JSON files contain Bear note titles. They live
  under your `$HOME` and are never networked, but be aware of the
  scope before sharing the cache directory off-machine.

## Why this is opt-in

The plist is shipped under `examples/` rather than installed
automatically because every operator's `$HOME` and `noxctl.toml` path
differ — there is no safe default to template at build time. The
README + plist together are sufficient to reproduce Roman's setup; no
hidden step lives outside this directory.
