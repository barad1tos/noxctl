#!/usr/bin/env bash
# examples/demo-vault/setup.sh — prepare a sanitized demo Bear vault
# for the screenshot assets referenced in README's "What noxctl does
# to your vault" section.
#
# Creates five fake atom notes under the #nox-demo/books tag (English-
# only content, no personal data) so the maintainer can:
#
#   1. Open Bear and take a "before" screenshot showing the five
#      atoms in Bear's tag-filtered note list — no master, no
#      canonical tag-line, just the human-authored bodies.
#   2. Run `noxctl apply` against examples/demo-vault/noxctl.toml.
#   3. Take an "after" screenshot showing the same atoms now
#      carrying canonical tag-lines PLUS the new auto-generated
#      "✱ Books" master.
#
# Idempotent: running the script twice produces five notes, not ten.
# Existing notes with matching titles are detected via `bearcli list`
# and skipped — bearcli has no "create-if-missing" verb, so the
# script approximates it.
#
# Safety:
# - Only writes notes tagged #nox-demo/books — never touches any other
#   tag or note in the operator's vault.
# - The corresponding noxctl.toml in this directory has ONLY the
#   #nox-demo/books domain, so a follow-up `noxctl apply --config
#   examples/demo-vault/noxctl.toml` cannot accidentally regen the
#   operator's real catalog.
# - To undo: run `noxctl destroy nox-demo/books --config examples/
#   demo-vault/noxctl.toml`, which trashes the auto-generated master
#   and strips the canonical line from each atom. Then trash the
#   five atoms from Bear's UI.

set -euo pipefail

BEARCLI="${BEARCLI:-/Applications/Bear.app/Contents/MacOS/bearcli}"

if [[ ! -x "$BEARCLI" ]]; then
  echo "error: bearcli not found at $BEARCLI" >&2
  echo "set BEARCLI=/path/to/bearcli if Bear is installed elsewhere" >&2
  exit 1
fi

# Five sanitized English book entries — public-domain titles +
# author names. Tagged #nox-demo/books exclusively.
declare -a BOOKS=(
  "Foundation|Isaac Asimov|Foundation by Isaac Asimov — first novel of the original trilogy. A mathematician predicts the fall of the Galactic Empire and devises a plan to shorten the dark age that follows."
  "Sapiens|Yuval Noah Harari|Sapiens — a brief history of humankind. Tracks Homo sapiens from cognitive revolution through the agricultural and scientific revolutions."
  "The Pragmatic Programmer|Andy Hunt and Dave Thomas|The Pragmatic Programmer — software craftsmanship guidance. Topics include DRY, orthogonality, prototyping, refactoring, and testing."
  "Project Hail Mary|Andy Weir|Project Hail Mary by Andy Weir — a lone astronaut wakes with no memory aboard a spacecraft. Hard-SF problem-solving in the vein of The Martian."
  "Dune|Frank Herbert|Dune by Frank Herbert — desert planet Arrakis, the spice melange, House Atreides, and the rise of Paul Muldib. The opening volume of the Dune Chronicles."
)

created=0
skipped=0

for entry in "${BOOKS[@]}"; do
  title="${entry%%|*}"
  rest="${entry#*|}"
  author="${rest%%|*}"
  body="${rest#*|}"

  # Bear titles must be unique enough for the demo. Use the title
  # directly; `bearcli list --tag nox-demo/books --fields title` will
  # tell us if it already exists.
  existing=$("$BEARCLI" list --location notes --tag nox-demo/books \
    --format json --fields title 2>/dev/null | \
    grep -c "\"title\":\"$title\"" || true)

  if [[ "$existing" != "0" ]]; then
    echo "skip: \"$title\" already exists" >&2
    skipped=$((skipped + 1))
    continue
  fi

  # Construct the atom body. The H1 IS the title; Bear infers note
  # title from the first heading. Body below is two paragraphs.
  content=$(printf '# %s\n\n%s\n\n— %s\n\n#nox-demo/books\n' \
    "$title" "$body" "$author")

  "$BEARCLI" create --title "$title" --text "$content" >/dev/null
  echo "created: \"$title\""
  created=$((created + 1))
done

echo
echo "demo vault setup complete: $created created, $skipped skipped"
echo
echo "Next steps:"
echo "  1. Open Bear, filter by #nox-demo/books, take the BEFORE screenshot"
echo "     and save it as docs/screenshots/before.png"
echo "  2. Run: noxctl apply --config examples/demo-vault/noxctl.toml"
echo "  3. Reload the #nox-demo/books filter, take the AFTER screenshot"
echo "     and save it as docs/screenshots/after.png"
echo "  4. Commit the screenshots and README image-tag wire."
echo
echo "To undo: noxctl destroy nox-demo/books --config examples/demo-vault/noxctl.toml"
echo "         (trashes the master; atoms remain. Trash them manually if needed.)"
