package guard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// The deletion gate is the one part of the guard that decides without the model, and deliberately so: deleting
// is the act the rules cannot talk their way out of. It runs on the command line alone and answers four cases:
//
//	rm -r / rm -rf   never allowed, anywhere, no second chance
//	rm outside       never allowed, the target has to lie inside the project root
//	rm inside        allowed after one refusal and on a repeated attempt that carries an approval word
//	unresolvable     never allowed: a variable, a glob, a tilde, a placeholder or a symlink is not a target
//
// The approval is a word in a comment of the command because the guard sees no conversation: a visible act with
// no other purpose is the only authorisation it can verify. The refusal counter is per session, because the
// first attempt has to be refused even when the word is already there — it is what forces the second thought.
//
// Two boundaries are chosen on purpose:
//   - The project root comes from the request's project directory, never from the shell's own `cd` or from the
//     call's workdir. A command that changes directory moves its targets, not the boundary it may delete in.
//   - The gate reads the Bash command line and the shell strings of a REPL call. Anything deeper (a script file
//     a shell is told to run, an indirection through a variable that holds the command name) is not a command
//     line the guard can see; those cases stay with the destructive patterns and rule 25. That is a known
//     limit, not a claim of completeness.
//
// An approved deletion only speaks for itself: when the command line holds other commands as well, the model
// still judges the rest (a `gh pr merge` next to an approved delete is not approved by it).

// maxGateCommandChars bounds what the gate parses. A deletion's command line is far below this; the cap exists
// because a pathological line costs quadratic time in the token walk, and a caller that times out answers PASS.
const maxGateCommandChars = 8192

// deletionCommandPattern is what makes the length cap worth applying: a word that is exactly rm, not the
// substring a long prose line happens to contain (`confirmation`).
var deletionCommandPattern = regexp.MustCompile(`(?i)\brm\b`)

// checkDeletion judges a shell call that deletes files. judged is false when the command carries no deletion,
// and also when it carries an approved deletion plus other commands: then the model judges the rest of the line.
func checkDeletion(req Request) (Decision, bool) {
	root := projectRoot(cwdOf(req))
	var sites []deletionSite
	var messages []string
	approved, onlyDeletions := true, true
	for _, review := range deletionReviews(req) {
		if review.tooLong {
			return Decision{
				Decision:    "BLOCK",
				Violations:  []string{"Rule 51 (enforced in code): this command line is too long for the deletion gate to read (" + strconv.Itoa(maxGateCommandChars) + " characters at most), so what it deletes cannot be checked."},
				Enforceable: true,
			}, true
		}
		sites = append(sites, review.sites...)
		onlyDeletions = onlyDeletions && review.onlyDeletions
		if len(review.sites) > 0 {
			messages = append(messages, review.text)
			// One approval word speaks for the line it stands in, not for another cell or another command of
			// the same call.
			approved = approved && review.approved
		}
	}
	if len(sites) == 0 {
		return Decision{}, false
	}
	return decideDeletion(req, sites, approved, onlyDeletions, root, strings.Join(messages, "\n"))
}

// cwdOf is the project directory the call belongs to: the boundary every target is measured against.
func cwdOf(req Request) string {
	return effectiveWorkdir(req.CWD, "")
}

// deletionReviews is one review per command line. A Bash call carries one; a REPL cell carries the shell strings
// it hands to sh(...), and each is parsed on its own, so a `cd` in one cell cannot move the targets of the next.
func deletionReviews(req Request) []deletionReview {
	switch canonicalTool(req.Tool) {
	case "Bash":
		command, workdir := bashCommand(req)
		if command == "" {
			return nil
		}
		return []deletionReview{reviewDeletions(command, effectiveWorkdir(req.CWD, workdir))}
	case "REPL":
		literals := replShellLiterals(replCode(req))
		reviews := make([]deletionReview, 0, len(literals))
		for _, literal := range literals {
			reviews = append(reviews, reviewDeletions(literal, effectiveWorkdir(req.CWD, "")))
		}
		return reviews
	}
	return nil
}

// deletionReview is what one command line says: the text it was read from, where its deletions are, whether a
// comment in it carries the approval word, and whether it holds anything besides deletions.
type deletionReview struct {
	text          string
	sites         []deletionSite
	approved      bool
	onlyDeletions bool
	tooLong       bool
}

// reviewDeletions parses one command line and collects its deletions with the directory in effect at that point.
func reviewDeletions(command, cwd string) deletionReview {
	if len(command) > maxGateCommandChars && deletionCommandPattern.MatchString(command) {
		return deletionReview{text: command, tooLong: true}
	}
	line := splitShellSegments(command)
	return deletionReview{
		text:          command,
		sites:         deletionSites(line.segments, cwd, 0),
		approved:      hasApprovalWord(line),
		onlyDeletions: deletionsOnly(line.segments, 0),
	}
}

// deletionSite is one rm invocation together with the directory it runs in.
type deletionSite struct {
	command deletionCommand
	cwd     string
}

// decideDeletion turns the collected sites into the one decision. judged is false in exactly one case: the
// deletion is approved and the command line holds other commands too, so the model still has the last word on
// those.
func decideDeletion(req Request, sites []deletionSite, approved, onlyDeletions bool, root, command string) (Decision, bool) {
	var reasons []string
	for _, site := range sites {
		reasons = append(reasons, judgeDeletion(site.command, root, site.cwd)...)
	}
	if len(reasons) > 0 {
		return Decision{Decision: "BLOCK", Violations: []string{strings.Join(reasons, " ")}, Enforceable: true}, true
	}

	if req.SessionID == "" {
		// The counter is the whole point of the second stage, and it cannot be kept without a session.
		return Decision{
			Decision:    "BLOCK",
			Violations:  []string{"Rule 51 (enforced in code): the call carries no session id, so the guard cannot remember the first refusal this stage needs; the deletion stays refused."},
			Enforceable: true,
		}, true
	}
	if !approved {
		if err := recordDeletionRefusal(req.DataDir, req.SessionID); err != nil {
			return deletionStateDecision(err), true
		}
		return Decision{Decision: "BLOCK", Violations: []string{deletionApprovalInstruction(command)}, Enforceable: true}, true
	}
	refused, err := recordDeletionApproval(req.DataDir, req.SessionID)
	if err != nil {
		return deletionStateDecision(err), true
	}
	if !refused {
		// The word alone is not enough: without a refusal in this session the attempt is still the first one.
		return Decision{Decision: "BLOCK", Violations: []string{deletionFirstAttemptInstruction(command)}, Enforceable: true}, true
	}
	if !onlyDeletions {
		// Approved, but the line does more than delete. The model sees the whole call and judges the rest; its
		// rule 25 block on the deletion itself is the price of not letting one approval cover a merge.
		return Decision{}, false
	}
	return Decision{Decision: "PASS", Detail: "rule 51: deletion approved by the approval word in the command"}, true
}

// deletionStateDecision is the refusal an unusable counter produces: the message names the fault instead of
// asking for an approval word that could never work.
func deletionStateDecision(err error) Decision {
	return Decision{
		Decision:    "BLOCK",
		Violations:  []string{"Rule 51 (enforced in code): the guard's state file is unusable (" + err.Error() + "), so the first refusal of this session cannot be recorded and the deletion stays refused."},
		Enforceable: true,
	}
}

// bashCommand is the command line and the working directory the call carries. opencode sends the working
// directory beside the command; a harness that sends only the command keeps the request's project directory.
func bashCommand(req Request) (string, string) {
	var bash struct {
		Command string `json:"command"`
		Workdir string `json:"workdir"`
	}
	if json.Unmarshal(req.ToolInput, &bash) != nil {
		return "", ""
	}
	return bash.Command, bash.Workdir
}

// replCode is the code of a REPL call.
func replCode(req Request) string {
	var repl struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(req.ToolInput, &repl) != nil {
		return ""
	}
	return repl.Code
}

// replShellLiterals are the string literals a REPL cell hands to sh(...). They are command lines, and a REPL
// call is how this project runs git and shell work, so leaving them unread would leave the deletion rule
// unenforced on the harness's own second path.
//
// The scan is quoting-aware on purpose: `sh(` inside a string or a comment is text about a shell call, not one
// (a test fixture, a doc comment), and blocking that would refuse the documentation of this very rule.
func replShellLiterals(code string) []string {
	var literals []string
	runes := []rune(code)
	for i := 0; i < len(runes); i++ {
		switch runes[i] {
		case '\'', '"', '`':
			// A string or a template literal: skip it whole, it carries no call.
			i = skipJSString(runes, i)
		case '/':
			if i+1 < len(runes) && runes[i+1] == '/' {
				for i < len(runes) && runes[i] != '\n' {
					i++
				}
			} else if i+1 < len(runes) && runes[i+1] == '*' {
				if end := strings.Index(string(runes[i+2:]), "*/"); end >= 0 {
					i += end + 3
				} else {
					i = len(runes)
				}
			}
		case 's':
			if i+1 >= len(runes) || runes[i+1] != 'h' {
				continue
			}
			// The word before must not be part of a longer identifier (`bash(`, `push(`).
			if i > 0 && isIdentRune(runes[i-1]) {
				continue
			}
			cursor := i + 2
			for cursor < len(runes) && (runes[cursor] == ' ' || runes[cursor] == '\t') {
				cursor++
			}
			if cursor >= len(runes) || runes[cursor] != '(' {
				continue
			}
			start := cursor + 1
			for start < len(runes) && (runes[start] == ' ' || runes[start] == '\t' || runes[start] == '\n') {
				start++
			}
			if start >= len(runes) || (runes[start] != '\'' && runes[start] != '"' && runes[start] != '`') {
				continue
			}
			end := skipJSString(runes, start)
			if end < len(runes) {
				literals = append(literals, unquoteShellLiteral(string(runes[start+1:end])))
			}
			i = end
		}
	}
	return literals
}

// skipJSString is the index of the closing quote of the string that starts at index, or len(runes) when the
// string never closes. An escape skips the next character.
func skipJSString(runes []rune, index int) int {
	quote := runes[index]
	for cursor := index + 1; cursor < len(runes); cursor++ {
		if runes[cursor] == '\\' {
			cursor++
			continue
		}
		if runes[cursor] == quote {
			return cursor
		}
	}
	return len(runes)
}

// unquoteShellLiteral removes the escapes a JS literal adds to a shell command. Only the two that change the
// command's meaning are needed: an escaped quote and an escaped backslash.
func unquoteShellLiteral(literal string) string {
	literal = strings.ReplaceAll(literal, `\"`, `"`)
	literal = strings.ReplaceAll(literal, `\'`, `'`)
	return strings.ReplaceAll(literal, `\\`, `\`)
}

// isIdentRune reports whether a rune can be part of a JS identifier.
func isIdentRune(r rune) bool {
	return r == '_' || r == '$' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

// effectiveWorkdir resolves the call's working directory against the project directory and makes it absolute.
// An empty result means the directory is unknown, which the target check treats as a refusal.
func effectiveWorkdir(cwd, workdir string) string {
	dir := cwd
	switch {
	case workdir == "":
	case filepath.IsAbs(workdir) || dir == "":
		dir = workdir
	default:
		dir = filepath.Join(dir, workdir)
	}
	if dir == "" {
		return ""
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	return filepath.Clean(abs)
}

// deletionCommand is one rm invocation: whether it recurses and what it was told to delete.
type deletionCommand struct {
	recursive bool
	targets   []shellToken
}

// shellToken is one word of a command line. quoted marks text that came from a quoted region. wild marks a word
// the shell expands before rm sees it, which makes the target unresolvable.
type shellToken struct {
	text   string
	quoted bool
	wild   bool
}

// shellLine is a tokenised command line plus the text of its comments: a comment is no part of any command, and
// it is where the approval word is written.
type shellLine struct {
	segments [][]shellToken
	comments []string
}

// maxShellNesting bounds the recursion into `sh -c` and `eval` arguments. Two levels cover a wrapper around a
// wrapper; anything deeper is not a workflow this gate has to see into.
const maxShellNesting = 2

// splitShellSegments tokenises a command line and splits it at the separators that start a new command. The
// quoting is tracked because `echo "rm -rf /"` is one argument and `grep rm` is a pattern: neither is a
// deletion, while an unquoted rm at a command position is.
func splitShellSegments(command string) shellLine {
	var line shellLine
	var current []shellToken
	var word strings.Builder
	started := false
	quoted, wild, braces := false, false, false

	flushWord := func() {
		if !started {
			return
		}
		text := word.String()
		// Brace expansion needs a comma or a range; `{}` is find's placeholder and `{file}` is literal.
		current = append(current, shellToken{text: text, quoted: quoted, wild: wild || (braces && strings.Contains(text, ","))})
		word.Reset()
		started, quoted, wild, braces = false, false, false, false
	}
	flushSegment := func() {
		flushWord()
		if len(current) > 0 {
			line.segments = append(line.segments, current)
			current = nil
		}
	}

	runes := []rune(command)
	inSingle, inDouble := false, false
	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		switch {
		case inSingle:
			if ch == '\'' {
				inSingle = false
				quoted = true
				continue
			}
			started = true
			word.WriteRune(ch)
		case inDouble:
			switch ch {
			case '"':
				inDouble = false
				quoted = true
			case '\\':
				quoted = true
				started = true
				if i+1 < len(runes) {
					i++
					word.WriteRune(runes[i])
				}
			case '$', '`':
				wild = true
				started = true
				word.WriteRune(ch)
			default:
				started = true
				word.WriteRune(ch)
			}
		default:
			switch ch {
			case '\\':
				quoted = true
				started = true
				if i+1 < len(runes) {
					i++
					word.WriteRune(runes[i])
				}
			case '\'':
				inSingle = true
				quoted = true
				started = true
			case '"':
				inDouble = true
				quoted = true
				started = true
			case ' ', '\t', '\r':
				flushWord()
			case '\n', ';', '&', '|', '(', ')':
				flushSegment()
			case '<', '>':
				// A redirection: its target is no operand of this command, and a word of digits in front is
				// the file descriptor (`2> /dev/null`).
				if started && !quoted && isAllDigits(word.String()) {
					word.Reset()
					started = false
				}
				flushSegment()
			case '`':
				// Command substitution: its content runs as a command of its own.
				flushSegment()
			case '$':
				if i+1 < len(runes) && runes[i+1] == '(' {
					i++
					flushSegment()
					continue
				}
				wild = true
				started = true
				word.WriteRune(ch)
			case '#':
				if started {
					// A hash inside a word is literal, as in `issue#12.txt`.
					word.WriteRune(ch)
					continue
				}
				flushWord()
				var comment strings.Builder
				for i+1 < len(runes) && runes[i+1] != '\n' {
					i++
					comment.WriteRune(runes[i])
				}
				if text := strings.TrimSpace(comment.String()); text != "" {
					line.comments = append(line.comments, text)
				}
				flushSegment()
			case '*', '?', '[', ']', '~':
				wild = true
				started = true
				word.WriteRune(ch)
			case '{', '}':
				braces = true
				started = true
				word.WriteRune(ch)
			default:
				started = true
				word.WriteRune(ch)
			}
		}
	}
	flushSegment()
	return line
}

// commandWrappers run the command that follows them, so the word behind one is still a command position.
var commandWrappers = map[string]bool{
	"sudo": true, "doas": true, "env": true, "command": true, "exec": true,
	"nice": true, "nohup": true, "time": true, "timeout": true, "xargs": true,
	"stdbuf": true, "builtin": true, "do": true, "then": true, "else": true,
}

// findExecs introduce a command of their own inside a find expression.
var findExecs = map[string]bool{"-exec": true, "-execdir": true, "-ok": true}

// shellInterpreters take a command line as their argument, which this gate reads as a command line again.
var shellInterpreters = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true,
}

// deletionsOnly reports whether a command line does nothing but delete. A line that also merges a pull request
// or sends a request hands that act to the model even when the deletion itself was approved — and a shell or an
// eval is only as innocent as the payload it runs, so it is read again rather than waved through.
func deletionsOnly(segments [][]shellToken, depth int) bool {
	for _, segment := range segments {
		word := commandWord(segment)
		if word < 0 {
			return false
		}
		name := filepath.Base(segment[word].text)
		switch {
		case name == "rm", name == "cd", name == "pushd":
		case name == "eval":
			code, ok := joinTokens(segment[word+1:])
			if !ok || depth >= maxShellNesting || !deletionsOnly(splitShellSegments(code).segments, depth+1) {
				return false
			}
		case shellInterpreters[name]:
			code, ok := shellDashC(segment[word+1:])
			if !ok || depth >= maxShellNesting || !deletionsOnly(splitShellSegments(code).segments, depth+1) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// deletionSites walks the segments in order, so a `cd` in front of a deletion moves the directory its targets
// resolve in. The root it is measured against does not move: that one comes from the call's project.
func deletionSites(segments [][]shellToken, cwd string, depth int) []deletionSite {
	var sites []deletionSite
	for _, segment := range segments {
		if word := commandWord(segment); word >= 0 {
			switch segment[word].text {
			case "cd", "pushd":
				cwd = changeDirectory(cwd, segment[word+1:])
			}
			if depth < maxShellNesting && shellInterpreters[filepath.Base(segment[word].text)] {
				if code, ok := shellDashC(segment[word+1:]); ok {
					sites = append(sites, deletionSites(splitShellSegments(code).segments, cwd, depth+1)...)
				}
			}
			if depth < maxShellNesting && segment[word].text == "eval" {
				if code, ok := joinTokens(segment[word+1:]); ok {
					sites = append(sites, deletionSites(splitShellSegments(code).segments, cwd, depth+1)...)
				}
			}
		}
		for _, index := range rmPositions(segment) {
			command := parseRm(segment[index+1:])
			sites = append(sites, deletionSite{command: command, cwd: cwd})
		}
	}
	return sites
}

// changeDirectory is the directory a `cd` leaves behind, or "" when it cannot be resolved: an unknown
// directory makes every later target unknown, which the target check refuses.
func changeDirectory(cwd string, args []shellToken) string {
	index := 0
	for index < len(args) && strings.HasPrefix(args[index].text, "-") {
		index++
	}
	if index >= len(args) {
		// `cd` without an argument goes home, `cd -` goes back: neither is a directory the guard knows.
		return ""
	}
	target := args[index]
	if target.wild || target.text == "-" || strings.HasPrefix(target.text, "~") {
		return ""
	}
	path := target.text
	if !filepath.IsAbs(path) {
		if cwd == "" {
			return ""
		}
		path = filepath.Join(cwd, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	return filepath.Clean(abs)
}

// rmPositions is where rm stands as a command in a segment: at the first word, behind a wrapper or its flags
// (`sudo -u root rm`, `timeout 5 rm`), behind a find -exec, or under an absolute path (`/bin/rm`). `echo rm` and
// `grep -rn rm` are arguments, not commands, and are left alone.
func rmPositions(segment []shellToken) []int {
	var positions []int
	for index, token := range segment {
		if filepath.Base(token.text) != "rm" {
			continue
		}
		switch {
		case index == 0, findExecs[segment[index-1].text]:
			positions = append(positions, index)
		case commandPrefix(segment[:index]):
			positions = append(positions, index)
		}
	}
	return positions
}

// commandPrefix reports whether everything before a word could be the run-up to a command: wrappers, their
// flags, flag arguments, environment assignments, timeouts and program paths.
func commandPrefix(tokens []shellToken) bool {
	for index, token := range tokens {
		switch {
		case commandWrappers[token.text]:
		case isExecutablePath(token.text):
		case strings.HasPrefix(token.text, "-"):
		case isAssignment(token.text):
		case isAllDigits(token.text):
		case index > 0 && strings.HasPrefix(tokens[index-1].text, "-"):
			// the argument of a wrapper's own flag: sudo -u root
		default:
			return false
		}
	}
	return true
}

// commandWord is the first word of a segment that is not part of a wrapper's run-up. A path counts: `/bin/bash`
// is the command, and the caller looks at its base name.
func commandWord(segment []shellToken) int {
	for index, token := range segment {
		if commandWrappers[token.text] || strings.HasPrefix(token.text, "-") ||
			isAssignment(token.text) || isAllDigits(token.text) {
			continue
		}
		return index
	}
	return -1
}

// shellDashC is the command line of a `sh -c` invocation. The flag may be clustered with others (`bash -lc`),
// which is the shape a login shell is usually started with.
func shellDashC(args []shellToken) (string, bool) {
	for index, arg := range args {
		if strings.HasPrefix(arg.text, "--") || !strings.HasPrefix(arg.text, "-") {
			continue
		}
		if strings.Contains(arg.text, "c") && index+1 < len(args) {
			return args[index+1].text, true
		}
	}
	return "", false
}

// joinTokens is the argument text of `eval`, which is a command line again.
func joinTokens(tokens []shellToken) (string, bool) {
	parts := make([]string, 0, len(tokens))
	for _, token := range tokens {
		parts = append(parts, token.text)
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, " "), true
}

// parseRm reads one rm invocation's flags and targets.
func parseRm(args []shellToken) deletionCommand {
	var deletion deletionCommand
	onlyTargets := false
	for _, arg := range args {
		if !onlyTargets && arg.text == "--" {
			onlyTargets = true
			continue
		}
		if !onlyTargets && strings.HasPrefix(arg.text, "--") {
			if arg.text == "--recursive" {
				deletion.recursive = true
			}
			continue
		}
		if !onlyTargets && strings.HasPrefix(arg.text, "-") && arg.text != "-" {
			for _, flag := range strings.TrimLeft(arg.text, "-") {
				if flag == 'r' || flag == 'R' {
					deletion.recursive = true
				}
			}
			continue
		}
		if arg.text == "" || arg.text == "-" {
			// "-" is stdin, not a file.
			continue
		}
		deletion.targets = append(deletion.targets, arg)
	}
	return deletion
}

// judgeDeletion answers one rm invocation: every reason it names makes the deletion final. An empty result
// means the deletion may go ahead once the session has been refused and the repeated attempt carries the word.
func judgeDeletion(deletion deletionCommand, root, cwd string) []string {
	if deletion.recursive {
		return []string{"Rule 51 (enforced in code): rm -r/rm -rf is never allowed, not even inside a scratch or temp directory."}
	}
	if len(deletion.targets) == 0 {
		// `xargs rm -f` and a heredoc both hand rm a target on stdin: no literal operand, no static answer.
		return []string{"Rule 51 (enforced in code): this rm has no literal target, so what it deletes cannot be checked."}
	}
	if root == "" {
		return []string{"Rule 51 (enforced in code): the project root of the call is unknown, so the target cannot be checked."}
	}
	if cwd == "" {
		return []string{"Rule 51 (enforced in code): the working directory of the call is unknown, so the target cannot be checked."}
	}
	for _, target := range deletion.targets {
		if target.wild || strings.HasPrefix(target.text, "~") || strings.Contains(target.text, "{}") {
			return []string{"Rule 51 (enforced in code): the target " + strconv.Quote(target.text) + " is not statically resolvable (variable, glob, placeholder or tilde)."}
		}
		path := target.text
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		path = filepath.Clean(path)
		if path == root {
			return []string{"Rule 51 (enforced in code): the target is the project root itself."}
		}
		if !withinRoot(root, path) {
			return []string{"Rule 51 (enforced in code): the target " + strconv.Quote(target.text) + " lies outside the project root; deletions may not leave it."}
		}
		if hasSymlinkComponent(root, path) {
			return []string{"Rule 51 (enforced in code): the target " + strconv.Quote(target.text) + " resolves through a symlink."}
		}
	}
	return nil
}

// projectRoot is the git root of a directory, or the directory itself when no checkout encloses it.
func projectRoot(dir string) string {
	if dir == "" {
		return ""
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	abs = filepath.Clean(abs)
	for current := abs; ; {
		if info, err := os.Stat(filepath.Join(current, ".git")); err == nil && (info.IsDir() || info.Mode().IsRegular()) {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return abs
		}
		current = parent
	}
}

// withinRoot reports whether a target lies inside the root. The root itself is handled before this call.
func withinRoot(root, path string) bool {
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

// hasSymlinkComponent reports whether a path below the root is, or runs through, a symlink. A path the guard
// cannot see is not guessed at: the lexical containment still holds, and rm -rf is refused regardless.
func hasSymlinkComponent(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return false
	}
	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return false
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return true
		}
	}
	return false
}

// approvalWords are the words that turn a repeated attempt into an authorised one. A fixed list: a free-form
// "sounds authorised" judgement is exactly what the guard cannot make.
var approvalWords = regexp.MustCompile(`(?i)\b(freigabe|freigegeben|genehmigt|approved|approval)\b`)

// hasApprovalWord looks for the word in a comment of the command. Only a comment counts: it has no other
// purpose, which makes it the one act in a command line that cannot happen by accident. A filename
// (`freigabe.txt`), a path (`docs/approval.md`) or an argument (`echo approved`) is not an authorisation.
func hasApprovalWord(line shellLine) bool {
	for _, comment := range line.comments {
		if approvalWords.MatchString(comment) {
			return true
		}
	}
	return false
}

// deletionApprovalInstruction asks for the authorisation the first stage is missing.
func deletionApprovalInstruction(command string) string {
	return "Rule 51 (enforced in code): this deletion inside the project needs explicit approval. If it is " +
		"authorised, repeat the command with one approval word in a comment (freigabe, freigegeben, genehmigt, " +
		"approved, approval), typically at the end: " + strconv.Quote(command+"  # Freigabe: <who>") + "."
}

// deletionFirstAttemptInstruction explains the refusal the counter forces on the first attempt.
func deletionFirstAttemptInstruction(command string) string {
	return "Rule 51 (enforced in code): this is the first deletion attempt of this session, and it is refused so " +
		"the deletion is made deliberately. Repeat the same command with an approval word in a comment to " +
		"proceed: " + strconv.Quote(command) + "."
}

func isAllDigits(text string) bool {
	if text == "" {
		return false
	}
	for _, char := range text {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

// isExecutablePath reports whether a word is a path to a program rather than a flag or an argument.
func isExecutablePath(text string) bool {
	return strings.HasPrefix(text, "/") || strings.HasPrefix(text, "./") || strings.HasPrefix(text, "../")
}

// isAssignment reports whether a word is a leading environment assignment (`A=1 cmd`).
func isAssignment(text string) bool {
	name, _, found := strings.Cut(text, "=")
	if !found || name == "" {
		return false
	}
	return !strings.ContainsAny(name, "/.")
}
