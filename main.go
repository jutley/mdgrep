package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
)

// ANSI codes — only used when color is enabled.
const (
	ansiReset  = "\033[0m"
	ansiBold   = "\033[1m"
	ansiDim    = "\033[2m"
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiCyan   = "\033[36m"
)

type options struct {
	re            *regexp.Regexp
	filesOnly     bool
	listUnmatched bool // -L
	count         bool // -c
	invertMatch   bool // -v
	maxCount      int  // -m (0 = unlimited)
	before        int
	after         int
	fmFields      []string
	color         bool
	quiet         bool        // -q
	celProg       cel.Program // --filter CEL expression, nil if not set
}

type lineInfo struct {
	text     string
	headings []string // breadcrumb snapshot at this line
	lineNum  int
}

type headingEntry struct {
	level int
	title string
}

// expandCombinedFlags rewrites POSIX-style combined short flags so Go's flag
// package can parse them. "-Rci" becomes ["-R", "-c", "-i"]. If a value flag
// like -m appears in the combo, the remaining characters are its value:
// "-Rcm5" becomes ["-R", "-c", "-m", "5"].
func expandCombinedFlags(args []string) []string {
	boolFlags := map[byte]bool{
		'r': true, 'R': true, 'i': true, 'w': true,
		'l': true, 'L': true, 'c': true, 'v': true, 'q': true,
	}
	valueFlags := map[byte]bool{
		'm': true, 'A': true, 'B': true, 'C': true,
	}

	out := make([]string, 0, len(args))
	for _, arg := range args {
		// Only touch args of the form -XY... (single dash, 2+ chars, not --)
		if len(arg) < 3 || arg[0] != '-' || arg[1] == '-' {
			out = append(out, arg)
			continue
		}
		chars := arg[1:]
		// Validate: every char must be a known flag up to (and including) the
		// first value flag. After a value flag, the remaining chars are its value.
		valid := true
		for i := 0; i < len(chars); i++ {
			ch := chars[i]
			if valueFlags[ch] {
				break // rest is the value, stop checking
			}
			if !boolFlags[ch] {
				valid = false
				break
			}
		}
		if !valid {
			out = append(out, arg)
			continue
		}
		for i := 0; i < len(chars); i++ {
			ch := chars[i]
			out = append(out, "-"+string(ch))
			if valueFlags[ch] && i+1 < len(chars) {
				out = append(out, chars[i+1:]) // rest is the value
				break
			}
		}
	}
	return out
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "index" {
		cmdIndex(os.Args[2:])
		return
	}
	os.Args = append(os.Args[:1], expandCombinedFlags(os.Args[1:])...)
	recursive := flag.Bool("r", false, "recursively search directories for .md files")
	flag.BoolVar(recursive, "R", false, "recursively search directories for .md files")
	caseInsensitive := flag.Bool("i", false, "case-insensitive matching")
	wordMatch := flag.Bool("w", false, "match whole words only")
	filesOnly := flag.Bool("l", false, "print names of files with matches")
	listUnmatched := flag.Bool("L", false, "print names of files with no matches")
	count := flag.Bool("c", false, "print count of matching lines per file")
	invertMatch := flag.Bool("v", false, "invert match — select non-matching lines")
	maxCount := flag.Int("m", 0, "stop after N matches per file (0 = unlimited)")
	afterCtx := flag.Int("A", 0, "lines of context after each match")
	beforeCtx := flag.Int("B", 0, "lines of context before each match")
	ctxLines := flag.Int("C", 0, "lines of context before and after each match")
	fmFieldsStr := flag.String("frontmatter", "title,description,tags", "frontmatter fields to show (comma-separated, empty string to suppress)")
	filterExpr := flag.String("filter", "", "CEL expression to filter by frontmatter (e.g. '\"engineering\" in fm.tags')")
	noColor := flag.Bool("no-color", false, "disable color output")
	forceColor := flag.Bool("color", false, "force color output even when not a TTY")
	quiet := flag.Bool("q", false, "quiet — no output, exit code only")
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: mdgrep [flags] PATTERN [FILE...]")
		os.Exit(2)
	}

	patStr := args[0]
	files := args[1:]

	if *ctxLines > 0 {
		if *afterCtx == 0 {
			*afterCtx = *ctxLines
		}
		if *beforeCtx == 0 {
			*beforeCtx = *ctxLines
		}
	}

	if *caseInsensitive {
		patStr = "(?i)" + patStr
	}
	if *wordMatch {
		patStr = `\b(?:` + patStr + `)\b`
	}
	re, err := regexp.Compile(patStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mdgrep: invalid pattern: %v\n", err)
		os.Exit(2)
	}

	// Compile optional CEL filter expression.
	var celProg cel.Program
	if *filterExpr != "" {
		var err error
		celProg, err = compileCEL(*filterExpr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mdgrep: %v\n", err)
			os.Exit(2)
		}
	}

	useColor := isTTY()
	if *forceColor {
		useColor = true
	}
	if *noColor {
		useColor = false
	}

	var fmFields []string
	for _, f := range strings.Split(*fmFieldsStr, ",") {
		if t := strings.TrimSpace(f); t != "" {
			fmFields = append(fmFields, t)
		}
	}

	opts := options{
		re:            re,
		filesOnly:     *filesOnly,
		listUnmatched: *listUnmatched,
		count:         *count,
		invertMatch:   *invertMatch,
		maxCount:      *maxCount,
		before:        *beforeCtx,
		after:         *afterCtx,
		fmFields:      fmFields,
		color:         useColor,
		quiet:         *quiet,
		celProg:       celProg,
	}

	// With -r and no explicit paths, default to the current directory.
	if *recursive && len(files) == 0 {
		files = []string{"."}
	}
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "mdgrep: no files specified")
		os.Exit(2)
	}

	// Expand glob patterns and directories into a flat list of files.
	var expanded []string
	for _, arg := range files {
		info, err := os.Stat(arg)
		if err == nil && info.IsDir() {
			if !*recursive {
				fmt.Fprintf(os.Stderr, "mdgrep: %s: is a directory (use -r to search recursively)\n", arg)
				continue
			}
			filepath.WalkDir(arg, func(path string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				if strings.HasSuffix(strings.ToLower(path), ".md") {
					expanded = append(expanded, path)
				}
				return nil
			})
			continue
		}
		// Fall back to glob expansion for non-directory paths.
		matches, globErr := filepath.Glob(arg)
		if globErr != nil || len(matches) == 0 {
			expanded = append(expanded, arg)
		} else {
			expanded = append(expanded, matches...)
		}
	}

	anyMatch := false
	for _, f := range expanded {
		result := processFile(f, opts)
		switch result {
		case fileError:
			os.Exit(2)
		case fileNoMatch:
			if opts.listUnmatched {
				printFilename(f, opts.color)
				anyMatch = true
			}
		case fileMatched:
			if !opts.listUnmatched {
				anyMatch = true
			}
		}
	}
	if !anyMatch {
		os.Exit(1)
	}
}

func isTTY() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func printFilename(path string, color bool) {
	if color {
		fmt.Printf("%s%s%s\n", ansiBold+ansiCyan, path, ansiReset)
	} else {
		fmt.Println(path)
	}
}

// fileResult distinguishes outcomes so the caller can handle -L and exit codes correctly.
type fileResult int

const (
	fileFiltered fileResult = iota // CEL filter rejected this file — exclude from -L
	fileNoMatch                    // passed filter but no pattern matches — counts for -L
	fileMatched                    // passed filter and has matches
	fileError                      // could not open or read the file
)

func processFile(path string, opts options) fileResult {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mdgrep: %v\n", err)
		return fileError
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20) // 1 MB max line — handles wide tables

	var (
		fm           map[string]interface{}
		inFM         bool
		fmLines      []string
		headingStack []headingEntry
		infos        []lineInfo
		lineNum      int
	)

	for sc.Scan() {
		line := sc.Text()
		lineNum++

		// Frontmatter: YAML block delimited by --- at the very top of the file.
		if lineNum == 1 && line == "---" {
			inFM = true
			continue
		}
		if inFM {
			if line == "---" || line == "..." {
				inFM = false
				fm = parseFrontmatter(fmLines)
			} else {
				fmLines = append(fmLines, line)
			}
			continue
		}

		// Update the heading stack when we encounter a heading line.
		if level, title := parseHeading(line); level > 0 {
			// Pop any headings at the same or deeper level.
			for len(headingStack) > 0 && headingStack[len(headingStack)-1].level >= level {
				headingStack = headingStack[:len(headingStack)-1]
			}
			headingStack = append(headingStack, headingEntry{level, title})
		}

		// Snapshot the current breadcrumb for this line.
		crumb := make([]string, len(headingStack))
		for i, h := range headingStack {
			crumb[i] = h.title
		}
		infos = append(infos, lineInfo{text: line, headings: crumb, lineNum: lineNum})
	}

	// Apply CEL frontmatter filter before scanning for pattern matches.
	if opts.celProg != nil && !evalCELFilter(opts.celProg, fm) {
		return fileFiltered
	}

	// Collect matching line indices, respecting -v and -m.
	var matchIdxs []int
	for i, info := range infos {
		hit := opts.re.MatchString(info.text)
		if opts.invertMatch {
			hit = !hit
		}
		if hit {
			matchIdxs = append(matchIdxs, i)
			if opts.maxCount > 0 && len(matchIdxs) >= opts.maxCount {
				break
			}
		}
	}

	if len(matchIdxs) == 0 {
		return fileNoMatch
	}

	// -L and -q need no further output — caller handles -L printing.
	if opts.listUnmatched || opts.quiet {
		return fileMatched
	}

	if opts.filesOnly {
		printFilename(path, opts.color)
		return fileMatched
	}

	if opts.count {
		renderCount(path, fm, len(matchIdxs), opts)
		return fileMatched
	}

	if opts.color {
		renderTTY(path, fm, infos, matchIdxs, opts)
	} else {
		renderJSON(path, fm, infos, matchIdxs, opts)
	}
	return fileMatched
}

func parseHeading(line string) (level int, title string) {
	if !strings.HasPrefix(line, "#") {
		return 0, ""
	}
	i := 0
	for i < len(line) && line[i] == '#' {
		i++
	}
	if i >= len(line) || line[i] != ' ' {
		return 0, ""
	}
	return i, strings.TrimSpace(line[i+1:])
}

// parseFrontmatter handles the common YAML subset found in markdown frontmatter:
// simple key: value strings, inline arrays [a, b, c], and block lists (- item).
func parseFrontmatter(lines []string) map[string]interface{} {
	fm := map[string]interface{}{}
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		colonIdx := strings.IndexByte(line, ':')
		if colonIdx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:colonIdx])
		rest := strings.TrimSpace(line[colonIdx+1:])

		// Inline array: [val1, val2, ...]
		if strings.HasPrefix(rest, "[") && strings.HasSuffix(rest, "]") {
			inner := rest[1 : len(rest)-1]
			var items []string
			for _, part := range strings.Split(inner, ",") {
				if t := strings.Trim(strings.TrimSpace(part), `"'`); t != "" {
					items = append(items, t)
				}
			}
			fm[key] = items
			continue
		}

		// Block list: subsequent lines indented with "- "
		if rest == "" && i+1 < len(lines) {
			next := strings.TrimSpace(lines[i+1])
			if strings.HasPrefix(next, "- ") {
				var items []string
				for i+1 < len(lines) {
					trimmed := strings.TrimSpace(lines[i+1])
					if !strings.HasPrefix(trimmed, "- ") {
						break
					}
					items = append(items, strings.TrimPrefix(trimmed, "- "))
					i++
				}
				fm[key] = items
				continue
			}
		}

		// Simple scalar value. Quoted strings stay strings; unquoted values
		// are coerced to int64, float64, or bool so CEL sees proper types.
		if strings.HasPrefix(rest, `"`) || strings.HasPrefix(rest, `'`) {
			fm[key] = strings.Trim(rest, `"'`)
		} else if rest == "true" {
			fm[key] = true
		} else if rest == "false" {
			fm[key] = false
		} else if i, err := strconv.ParseInt(rest, 10, 64); err == nil {
			fm[key] = i
		} else if f, err := strconv.ParseFloat(rest, 64); err == nil {
			fm[key] = f
		} else {
			fm[key] = rest
		}
	}
	return fm
}

func fmDisplay(fm map[string]interface{}, key string) (string, bool) {
	v, ok := fm[key]
	if !ok {
		return "", false
	}
	switch val := v.(type) {
	case string:
		return val, val != ""
	case []string:
		if len(val) == 0 {
			return "", false
		}
		return strings.Join(val, ", "), true
	default:
		s := fmt.Sprintf("%v", v)
		return s, s != ""
	}
}

// includedSet computes which line indices fall within the context window of any
// match, and which indices are direct matches.
func includedSet(matchIdxs []int, total, before, after int) (included, isMatch map[int]bool) {
	included = make(map[int]bool)
	isMatch = make(map[int]bool)
	for _, mi := range matchIdxs {
		isMatch[mi] = true
		lo := mi - before
		if lo < 0 {
			lo = 0
		}
		hi := mi + after
		if hi >= total {
			hi = total - 1
		}
		for j := lo; j <= hi; j++ {
			included[j] = true
		}
	}
	return
}

func renderCount(path string, fm map[string]interface{}, n int, opts options) {
	if opts.color {
		bar := strings.Repeat("─", max(0, 72-len(path)))
		fmt.Printf("%s%s── %s %s%s\n", ansiBold, ansiCyan, path, bar, ansiReset)
		if fm != nil && len(opts.fmFields) > 0 {
			for _, field := range opts.fmFields {
				if val, ok := fmDisplay(fm, field); ok {
					fmt.Printf("  %s%s:%s %s\n", ansiDim, field, ansiReset, val)
				}
			}
		}
		fmt.Printf("  %s%d match", ansiGreen, n)
		if n != 1 {
			fmt.Print("es")
		}
		fmt.Printf("%s\n\n", ansiReset)
	} else {
		data, _ := json.Marshal(map[string]interface{}{
			"file":  path,
			"count": n,
		})
		fmt.Println(string(data))
	}
}

func renderTTY(path string, fm map[string]interface{}, infos []lineInfo, matchIdxs []int, opts options) {
	// File header.
	bar := strings.Repeat("─", max(0, 72-len(path)))
	fmt.Printf("%s%s── %s %s%s\n", ansiBold, ansiCyan, path, bar, ansiReset)

	// Selected frontmatter fields.
	if fm != nil && len(opts.fmFields) > 0 {
		anyPrinted := false
		for _, field := range opts.fmFields {
			if val, ok := fmDisplay(fm, field); ok {
				fmt.Printf("  %s%s:%s %s\n", ansiDim, field, ansiReset, val)
				anyPrinted = true
			}
		}
		if anyPrinted {
			fmt.Println()
		}
	}

	included, isMatch := includedSet(matchIdxs, len(infos), opts.before, opts.after)

	var lastHeadings []string
	prevIdx := -1

	for i, info := range infos {
		if !included[i] {
			continue
		}

		// Separator between non-contiguous match groups.
		if prevIdx >= 0 && i > prevIdx+1 {
			fmt.Printf("  %s--%s\n", ansiDim, ansiReset)
		}
		prevIdx = i

		// Heading breadcrumb — only when it changes.
		if !sliceEqual(info.headings, lastHeadings) {
			if len(info.headings) > 0 {
				fmt.Printf("\n  %s%s%s\n", ansiBold+ansiYellow, strings.Join(info.headings, " > "), ansiReset)
			}
			lastHeadings = info.headings
		}

		lineStr := fmt.Sprintf("%d", info.lineNum)
		if isMatch[i] {
			// Highlight the matched portion(s) within the line.
			highlighted := opts.re.ReplaceAllStringFunc(info.text, func(m string) string {
				return ansiBold + ansiRed + m + ansiReset
			})
			fmt.Printf("  %s%s%s: %s\n", ansiGreen, lineStr, ansiReset, highlighted)
		} else {
			// Context line, dimmed.
			fmt.Printf("  %s%s: %s%s\n", ansiDim, lineStr, info.text, ansiReset)
		}
	}
	fmt.Println()
}

func renderJSON(path string, fm map[string]interface{}, infos []lineInfo, matchIdxs []int, opts options) {
	included, _ := includedSet(matchIdxs, len(infos), opts.before, opts.after)

	// Build the subset of frontmatter the caller asked for.
	selectedFM := map[string]interface{}{}
	if fm != nil {
		for _, field := range opts.fmFields {
			if v, ok := fm[field]; ok {
				selectedFM[field] = v
			}
		}
	}

	for _, mi := range matchIdxs {
		var before []string
		for j := mi - opts.before; j < mi; j++ {
			if j >= 0 && included[j] {
				before = append(before, infos[j].text)
			}
		}
		var after []string
		for j := mi + 1; j <= mi+opts.after && j < len(infos); j++ {
			if included[j] {
				after = append(after, infos[j].text)
			}
		}

		rec := map[string]interface{}{
			"file":     path,
			"headings": infos[mi].headings,
			"line":     infos[mi].lineNum,
			"text":     infos[mi].text,
		}
		if len(selectedFM) > 0 {
			rec["frontmatter"] = selectedFM
		}
		if len(before) > 0 {
			rec["before"] = before
		}
		if len(after) > 0 {
			rec["after"] = after
		}

		data, _ := json.Marshal(rec)
		fmt.Println(string(data))
	}
}

// evalCELFilter evaluates the compiled CEL program against a file's frontmatter.
// Returns false if the expression evaluates to false, errors, or frontmatter is nil.
func evalCELFilter(prg cel.Program, fm map[string]interface{}) bool {
	if fm == nil {
		fm = map[string]interface{}{}
	}
	out, _, err := prg.Eval(map[string]interface{}{"fm": fmForCEL(fm)})
	if err != nil {
		return false
	}
	b, ok := out.(types.Bool)
	return ok && bool(b)
}

// fmForCEL converts the frontmatter map into a form CEL can work with.
// []string slices become []ref.Val lists so CEL's 'in' operator works correctly.
func fmForCEL(fm map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(fm))
	for k, v := range fm {
		switch val := v.(type) {
		case []string:
			items := make([]ref.Val, len(val))
			for i, s := range val {
				items[i] = types.String(s)
			}
			out[k] = types.DefaultTypeAdapter.NativeToValue(items)
		default:
			out[k] = v
		}
	}
	return out
}

func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// compileCEL compiles a CEL filter expression into an evaluable program.
func compileCEL(expr string) (cel.Program, error) {
	celEnv, err := cel.NewEnv(
		cel.Variable("fm", cel.MapType(cel.StringType, cel.DynType)),
	)
	if err != nil {
		return nil, fmt.Errorf("CEL env error: %w", err)
	}
	ast, issues := celEnv.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("--filter: %w", issues.Err())
	}
	if !ast.OutputType().IsEquivalentType(cel.BoolType) {
		return nil, fmt.Errorf("--filter expression must return bool, got %v", ast.OutputType())
	}
	prog, err := celEnv.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("CEL program error: %w", err)
	}
	return prog, nil
}

// indexEntry holds the resolved metadata for one file in an index.
type indexEntry struct {
	path  string
	title string
	desc  string
}

// scanForIndex reads a file, applies the optional CEL filter, and returns a
// resolved indexEntry. skip=true means the file was filtered out.
func scanForIndex(path string, celProg cel.Program) (entry indexEntry, skip bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return entry, false, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)

	var (
		fm      map[string]interface{}
		inFM    bool
		fmLines []string
		lineNum int
		h1s     []string
	)

	for sc.Scan() {
		line := sc.Text()
		lineNum++

		if lineNum == 1 && line == "---" {
			inFM = true
			continue
		}
		if inFM {
			if line == "---" || line == "..." {
				inFM = false
				fm = parseFrontmatter(fmLines)
			} else {
				fmLines = append(fmLines, line)
			}
			continue
		}

		if level, title := parseHeading(line); level == 1 {
			h1s = append(h1s, title)
		}
	}

	if celProg != nil && !evalCELFilter(celProg, fm) {
		return entry, true, nil
	}

	// Resolve title: frontmatter title > sole H1 > filename stem.
	title := ""
	if fm != nil {
		title, _ = fmDisplay(fm, "title")
	}
	if title == "" && len(h1s) == 1 {
		title = h1s[0]
	}
	if title == "" {
		base := filepath.Base(path)
		title = strings.TrimSuffix(base, filepath.Ext(base))
	}

	desc := ""
	if fm != nil {
		desc, _ = fmDisplay(fm, "description")
	}

	return indexEntry{path: path, title: title, desc: desc}, false, nil
}

func cmdIndex(args []string) {
	fset := flag.NewFlagSet("index", flag.ExitOnError)
	filterExpr := fset.String("filter", "", "CEL expression to filter files by frontmatter (e.g. '\"engineering\" in fm.tags')")
	noColor := fset.Bool("no-color", false, "disable color output")
	forceColor := fset.Bool("color", false, "force color output even when not a TTY")
	fset.Parse(args)

	dirs := fset.Args()
	if len(dirs) == 0 {
		dirs = []string{"."}
	}

	var celProg cel.Program
	if *filterExpr != "" {
		var err error
		celProg, err = compileCEL(*filterExpr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mdgrep index: %v\n", err)
			os.Exit(2)
		}
	}

	useColor := isTTY()
	if *forceColor {
		useColor = true
	}
	if *noColor {
		useColor = false
	}

	var files []string
	for _, dir := range dirs {
		info, err := os.Stat(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mdgrep index: %v\n", err)
			os.Exit(2)
		}
		if info.IsDir() {
			filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				if strings.HasSuffix(strings.ToLower(path), ".md") {
					files = append(files, path)
				}
				return nil
			})
		} else if strings.HasSuffix(strings.ToLower(dir), ".md") {
			files = append(files, dir)
		}
	}

	var entries []indexEntry
	for _, path := range files {
		e, skip, err := scanForIndex(path, celProg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mdgrep index: %v\n", err)
			os.Exit(2)
		}
		if !skip {
			entries = append(entries, e)
		}
	}

	if len(entries) == 0 {
		os.Exit(1)
	}

	if useColor {
		renderIndexTTY(entries)
	} else {
		renderIndexMarkdown(entries)
	}
}

func renderIndexTTY(entries []indexEntry) {
	for _, e := range entries {
		fmt.Printf("%s%s%s  %s%s%s\n", ansiBold+ansiCyan, e.title, ansiReset, ansiDim, e.path, ansiReset)
		if e.desc != "" {
			fmt.Printf("  %s\n", e.desc)
		}
	}
}

func renderIndexMarkdown(entries []indexEntry) {
	for _, e := range entries {
		line := fmt.Sprintf("- [%s](%s)", e.title, e.path)
		if e.desc != "" {
			line += " — " + e.desc
		}
		fmt.Println(line)
	}
}
