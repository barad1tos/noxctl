# docs/screenshots/

Bear-rendered screenshots referenced from the main README's
"What noxctl does to your vault" section.

## Asset slots

| File         | Source state                                                                                                                | Status                                   |
|--------------|-----------------------------------------------------------------------------------------------------------------------------|------------------------------------------|
| `before.png` | Bear filtered to `#nox-demo/books`, five demo atoms visible, no master                                                       | pending — capture per instructions below |
| `after.png`  | Same filter after `noxctl apply --config examples/demo-vault/noxctl.toml`, six notes visible (5 atoms + 1 `✱ Books` master) | pending — capture per instructions below |

## Blueprint gallery slots

Five screenshots referenced from the main README's "Choosing a blueprint" section — one per blueprint, showing the master (and a Tier-2 hub where relevant). Each maps to a copy-pasteable config in `examples/<blueprint>.toml`.

| File                                   | Blueprint                | What to show                                                        | Status  |
|----------------------------------------|--------------------------|---------------------------------------------------------------------|---------|
| `blueprint-flat-list.png`              | `flat-list`              | the master with its flat bullet list of atoms                       | pending |
| `blueprint-grouped-vertical.png`       | `grouped-vertical`       | the master with one `## Bucket (N)` H2 section per bucket           | pending |
| `blueprint-hub-routed.png`             | `hub-routed`             | the master listing hubs, plus one Tier-2 hub note listing its atoms | pending |
| `blueprint-hub-routed-with-subtag.png` | `hub-routed-with-subtag` | same Tier-2 shape, buckets sourced from `#tag/bucket` sub-tags       | pending |
| `blueprint-umbrella.png`               | `umbrella`               | the umbrella master aggregating its child domains                   | pending |

Capture each by applying the matching `examples/<blueprint>.toml` (or screenshotting the equivalent master already in your vault), then crop to the rendered master/hub. Optimize to under ~200 KB and commit; the embeds in the main README pick them up by filename automatically.

## How to (re-)capture

1. Run `examples/demo-vault/setup.sh` to populate Bear with the
   five sanitized demo atoms tagged `#nox-demo/books`. The script
   is idempotent — re-running it skips notes that already exist.
2. In Bear: select the `#nox-demo/books` tag in the sidebar to
   filter the note list. Take a screenshot of the editor pane
   (not the full Bear window — crop to the note list + active
   note). Save as `before.png`.
3. Run `noxctl apply --config examples/demo-vault/noxctl.toml`.
4. Reload the `#nox-demo/books` filter in Bear. The note list now
   contains a sixth note titled `✱ Books`. Take a screenshot
   showing the new master plus one atom with its canonical
   tag-line visible. Save as `after.png`.
5. Optimize the PNGs (e.g. `pngcrush` or `oxipng`) so each is
   under ~200 KB.
6. Commit both files and update the README to wire the image tags
   (the placeholder note in "What noxctl does to your vault" gets
   replaced).

## Why the assets live in-repo

GitHub user-attachments URLs are tied to issue/PR upload context
and can break when issues are deleted or transferred. Checking the
PNGs into git keeps the README rendering correctly forever, at the
one-time cost of ~400 KB to the repo size.
