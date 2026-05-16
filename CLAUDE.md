# mdgrep — Claude Notes

## Build and run

```bash
go build -o mdgrep .
./mdgrep -r "pattern" testdata/
```

No code generation. No Makefile. Just `go build`.

## Project layout

```
main.go          Everything — single file, ~550 lines
testdata/        Mock Markdown corpus (Aethon X7 fictional vehicle docs)
  README.md
  CHANGELOG.md
  aethon-x7-manual.md
  engineering/
    gravitic-systems.md
    battery-systems.md
    software/
      firmware-guide.md
      ota-updates.md
  operations/
    manufacturing.md
    quality-control.md
  service/
    technician-guide.md
  regulatory/
    safety-standards.md
```

## Architecture

Single-pass file scanner. For each file:

1. Parse YAML frontmatter from the leading `---` block
2. Walk lines, maintaining a heading stack that snapshots into each `lineInfo`
3. Apply optional CEL filter against frontmatter — return early if rejected
4. Collect matching line indices (respecting `-v` and `-m`)
5. Render matches with their heading breadcrumb snapshots

The heading breadcrumb is snapshotted on every line during the scan. When a match is found, its heading context is already recorded — there is no second pass.

## Key types

```go
type lineInfo struct {
    text     string
    headings []string  // breadcrumb at this line, e.g. ["H1", "H2", "H3"]
    lineNum  int
}

type fileResult int  // fileFiltered | fileNoMatch | fileMatched | fileError
```

`fileResult` lets the main loop distinguish CEL-filtered files (excluded from `-L`) from files with no pattern match (counted for `-L`) from errors (immediate exit 2).

## Frontmatter parsing

Hand-rolled parser in `parseFrontmatter`, no external YAML library. Handles:
- `key: value` scalars (unquoted values are coerced to int64, float64, or bool)
- `key: "quoted string"` (always stays a string)
- `key: [a, b, c]` inline arrays
- Block arrays (lines starting with `- ` after an empty value)

Quoted values are never coerced so `version: "3.2.1"` stays a string even though it contains dots.

## CEL filter (`--filter`)

Uses `github.com/google/cel-go`. The frontmatter is exposed as `fm`, a `map(string, dyn)`. Field access: `fm.tags`, `fm.owner`, etc.

`[]string` values in frontmatter are converted to `[]ref.Val` lists before being passed to the CEL activation so the `in` operator works correctly.

The expression must return `bool` — this is checked at compile time with `ast.OutputType().IsEquivalentType(cel.BoolType)`. A non-bool expression exits 2 immediately.

CEL runtime errors (e.g. accessing a missing field without a presence check) return `false`, so the file is skipped rather than crashing.

## Flag parsing

Standard `go flag` package. Two preprocessing steps applied to `os.Args` before `flag.Parse`:

1. **`expandCombinedFlags`** — expands POSIX-style combined short flags (`-Rwi` → `-R -w -i`). Only expands args where every character is a known single-char flag. Value flags (`-m`, `-A`, `-B`, `-C`) consume the remaining characters as their value (`-m5` → `-m 5`).

2. Go's `flag.Parse` stops at the first non-flag argument (standard behavior). Flags must come before `PATTERN`. This matches POSIX grep behavior.

## Output

- **TTY**: ANSI color, heading breadcrumbs reprinted only when they change, `--` gap separators between non-contiguous match groups
- **Non-TTY**: newline-delimited JSON, one object per match
- Detected via `os.Stdout.Stat()` checking `os.ModeCharDevice`; overridden by `--color` / `--no-color`

## Exit codes

- `0` — match found
- `1` — no match
- `2` — error (bad pattern, file not found, invalid CEL expression)

File errors exit immediately (first bad path stops processing).

## Dependencies

- `github.com/google/cel-go` — CEL expression evaluation for `--filter`

Everything else is stdlib.

## Things to know

- The `testdata/` corpus is a coherent fictional universe (Aethon Mobility Group, makers of the X7 gravitic vehicle). Terms like "resonator", "Casimir plate", "coolant", and "plasma exhaust" appear across multiple files at different nesting levels — good for testing cross-file and recursive searches.
- `parseFrontmatter` is intentionally minimal. It handles the subset of YAML that appears in common Markdown frontmatter. It is not a general YAML parser.
- There is no indexing. Every search is a full scan. This is intentional for v1.
