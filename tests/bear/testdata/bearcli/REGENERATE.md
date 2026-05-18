# Refreshing bearcli Fixtures

These JSON files capture `bearcli cat --format json` output for representative
note shapes. The version directory `v<N>` tracks the bearcli JSON schema — if
a future Bear release changes that shape (renames a field, drops one), capture
a fresh `v<N+1>/` directory and migrate tests over.

## When to refresh

- New bearcli major version (`bearcli --version` jumps).
- A failing fixture test that turns out to reflect a real Bear schema change.
- Adding a new shape category for a recently-found edge case (spec component 2
  recognition table is the source of truth for which shapes matter).

## How to refresh

Create the four test notes in Bear manually:

- **fresh_untitled** — x-callback URL `bear://x-callback-url/create?tags=library/quotes&open_note=yes`, click it, do NOT type anything.
- **legacy_stale_h1** — manually paste a body with a `+`-encoded H1 and a legacy `title=` URL. Documents the pre-spec shape; not auto-recovered.
- **user_authored_h1** — x-callback create as above, then type `# My custom title` immediately as first body line.
- **preamble_body** — hand-author H1 + epigraph + canonical tag-line + `---` + body.

For each note, capture:

```bash
ID=<note-id>
/Applications/Bear.app/Contents/MacOS/bearcli cat "$ID" --format json \
  --fields id,title,content,hash,tags,created \
  | jq '.' > tests/bear/testdata/bearcli/v1/<category>.json
```

Run the fixture suite to confirm shapes still parse:

```bash
go test ./tests/bear/ -run TestBearcliFixtures -v
```

If the suite passes, commit. If not, update the test or the parsing code — NOT
the fixture (the fixture is the source of truth for Bear's behavior).
