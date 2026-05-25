# docs/screenshots/

Bear-rendered screenshots referenced from the main README's
"What noxctl does to your vault" section.

## Asset slots

| File         | Source state                                                                                                                | Status                                   |
|--------------|-----------------------------------------------------------------------------------------------------------------------------|------------------------------------------|
| `before.png` | Bear filtered to `#nox-demo/books`, five demo atoms visible, no master                                                       | pending — capture per instructions below |
| `after.png`  | Same filter after `noxctl apply --config examples/demo-vault/noxctl.toml`, six notes visible (5 atoms + 1 `✱ Books` master) | pending — capture per instructions below |

Per-blueprint visuals are NOT screenshots: the five masters all render as a
list, so master screenshots barely tell them apart. The README's "Choosing a
blueprint" section uses a text schematic of each blueprint's structure instead
(what sits below each link, where buckets come from) — no capture needed.

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
