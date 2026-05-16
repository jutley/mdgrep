# mdgrep

`mdgrep` is grep for Markdown. It searches files for a pattern and returns matches with the heading breadcrumb that leads to each one — so you know not just that a line matched, but where in the document it lives.

```
mdgrep [flags] PATTERN [FILE...]
```

## Why

Plain `grep` on Markdown gives you the matched line and the file path. It doesn't tell you whether that line is under "Installation > macOS" or "Troubleshooting > Known Issues". `mdgrep` reconstructs the heading hierarchy at each match so the result is immediately navigable.

## Install

```bash
go install github.com/jutley/mdgrep@latest
```

Or build from source:

```bash
git clone https://github.com/jutley/mdgrep
cd mdgrep
go build -o mdgrep .
```

## Usage

```
mdgrep [flags] PATTERN [FILE...]
```

Flags must come before `PATTERN`. `PATTERN` is a Go regular expression. `FILE` arguments accept file paths and glob patterns; when combined with `-r`, directory paths are walked recursively for `.md` files.

## Output

**TTY** (when stdout is a terminal): colorized output with a file header, selected frontmatter fields, heading breadcrumbs, highlighted matches, and `--` separators between non-contiguous match groups.

**Non-TTY** (pipe, redirect): one JSON object per match, printed as newline-delimited JSON. Each object contains `file`, `headings`, `line`, `text`, and optionally `frontmatter`, `before`, and `after`.

Force either mode with `--color` or `--no-color`.

## Flags

### Matching

| Flag | Description |
|------|-------------|
| `-i` | Case-insensitive matching |
| `-w` | Whole-word matching (wraps pattern in `\b...\b`) |
| `-v` | Invert match — select non-matching lines |
| `-m N` | Stop after N matches per file |

### Output mode

| Flag | Description |
|------|-------------|
| `-l` | Print names of files with matches |
| `-L` | Print names of files with **no** matches |
| `-c` | Print count of matching lines per file |
| `-q` | Quiet — no output, exit code only |

### Context

| Flag | Description |
|------|-------------|
| `-A N` | N lines of context after each match |
| `-B N` | N lines of context before each match |
| `-C N` | N lines of context before and after each match |

### File selection

| Flag | Description |
|------|-------------|
| `-r`, `-R` | Recursively search directories for `.md` files |
| `--filter EXPR` | CEL expression to pre-filter files by frontmatter (see below) |

### Display

| Flag | Description |
|------|-------------|
| `--frontmatter FIELDS` | Comma-separated frontmatter fields to show (default: `title,description,tags`; set to empty string to suppress) |
| `--color` | Force color output even when not a TTY |
| `--no-color` | Disable color output |

Short boolean flags can be combined: `-Rwi`, `-Rci`, `-lv`.

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | Match found |
| 1 | No match |
| 2 | Error (file not found, invalid pattern, etc.) |

## Examples

Search for a pattern across all Markdown files in a directory:

```bash
mdgrep -r "resonator" docs/
```

Case-insensitive whole-word match with context:

```bash
mdgrep -riw -C 2 "battery" docs/
```

List files containing a pattern, without showing matches:

```bash
mdgrep -rl "coolant" docs/
```

Audit — which files don't mention a topic at all:

```bash
mdgrep -rL "error handling" docs/
```

Count matches per file, sorted by frequency:

```bash
mdgrep -rc "deprecated" docs/ | jq -s 'sort_by(-.count)[]'
```

Show only the first 3 matches per file:

```bash
mdgrep -rm3 "TODO" docs/
```

## Frontmatter filter (`--filter`)

`--filter` accepts a [CEL](https://cel.dev) expression evaluated against each file's YAML frontmatter before the pattern search runs. Files that don't satisfy the expression are skipped entirely.

The frontmatter is exposed as a map named `fm`. Access fields with `fm.fieldname`.

```bash
# Only search engineering-tagged files
mdgrep -r --filter '"engineering" in fm.tags' "coolant" docs/

# Scalar field substring match
mdgrep -r --filter 'fm.owner.contains("eng")' "plasma" docs/

# Compound condition
mdgrep -r --filter '"engineering" in fm.tags && fm.classification == "internal"' "coolant" docs/

# Numeric comparison
mdgrep -r --filter 'fm.model_year >= 2047' "governor" docs/

# Negation
mdgrep -r --filter '!(fm.status == "deprecated")' "install" docs/

# Field presence check
mdgrep -r --filter '"classification" in fm && fm.classification != "public"' "api" docs/

# Combine with -L for gap audits: ops docs missing error coverage
mdgrep -rL --filter '"operations" in fm.tags' "error" docs/
```

CEL reference for common patterns:

| Intent | Expression |
|--------|-----------|
| Tag exact membership | `"engineering" in fm.tags` |
| Tag substring match | `fm.tags.exists(t, t.contains("eng"))` |
| Scalar contains | `fm.owner.contains("eng")` |
| Scalar equals | `fm.classification == "internal"` |
| Numeric comparison | `fm.model_year >= 2047` |
| Field presence | `"field" in fm` |
| Boolean AND | `expr1 && expr2` |
| Boolean OR | `expr1 \|\| expr2` |
| Negation | `!expr` |

## Frontmatter

`mdgrep` parses YAML frontmatter from the `---` block at the top of each file. Supported value types:

- Scalars: `title: My Document`
- Quoted strings: `version: "1.0.0"` (stays a string)
- Unquoted numbers: `model_year: 2047` (parsed as integer)
- Booleans: `draft: true`
- Inline arrays: `tags: [a, b, c]`
- Block arrays:
  ```yaml
  tags:
    - a
    - b
  ```

## JSON output

Each match in non-TTY mode is one JSON line:

```json
{
  "file": "docs/engineering/battery.md",
  "frontmatter": { "title": "Battery Systems", "tags": ["engineering"] },
  "headings": ["Battery Systems", "Thermal Management", "Cooling Circuit"],
  "line": 42,
  "text": "Do not substitute standard automotive coolant.",
  "before": ["previous line"],
  "after": ["next line"]
}
```

`before` and `after` only appear when `-A`, `-B`, or `-C` context flags are used. `frontmatter` only appears when `--frontmatter` is non-empty.

This format composes well with `jq`:

```bash
# Extract just the heading paths of all matches
mdgrep -r "coolant" docs/ | jq -r '.headings | join(" > ")'

# Find which sections across all files match, grouped by file
mdgrep -r "deprecated" docs/ | jq -r '"\(.file): \(.headings[-1])"'
```
