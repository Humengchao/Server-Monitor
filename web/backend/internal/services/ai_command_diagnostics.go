package services

import (
	"strconv"
	"strings"
	"unicode"

	"server-monitor/internal/models"
)

// This is deliberately a small language, not a general shell parser. We parse
// literal argv and a few connectors, validate EVERY invocation, then rebuild
// commands from the validated values. We never execute the original text on
// this path. Expansion, redirection, comments, escapes, scripts and background
// jobs remain exact-approval operations. A pipe may only feed a known stdin
// filter; filters cannot open files, start programs or write output files.
type aiDiagnosticPart struct {
	words []string
	after string
}

func aiParseDiagnostic(command, platform string) ([]aiDiagnosticPart, bool) {
	var parts []aiDiagnosticPart
	var words []string
	var word strings.Builder
	quote := byte(0)
	started, quoted := false, false
	flush := func() bool {
		if !started {
			return true
		}
		// Quoted command names have different invocation semantics in PS.
		if len(words) == 0 && quoted {
			return false
		}
		words = append(words, word.String())
		word.Reset()
		started, quoted = false, false
		return len(words) <= 64
	}
	for i := 0; i < len(command); i++ {
		c := command[i]
		// Reject even inert quoted expansion syntax: this restricted language
		// never needs to guess whether a second interpreter will evaluate it.
		if strings.ContainsRune("`$\\(){}<>\x00", rune(c)) {
			return nil, false
		}
		if quote != 0 {
			if c == quote {
				// PowerShell doubles single quotes within literal strings.
				if platform == "windows" && quote == '\'' && i+1 < len(command) && command[i+1] == '\'' {
					word.WriteByte(c)
					i++
				} else {
					quote = 0
				}
			} else {
				if c < 32 || c == 127 {
					return nil, false
				}
				word.WriteByte(c)
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote, started, quoted = c, true, true
			continue
		}
		if c == ' ' || c == '\t' {
			if !flush() {
				return nil, false
			}
			continue
		}
		if c == ';' || c == '\n' || c == '\r' || c == '|' || c == '&' {
			if !flush() {
				return nil, false
			}
			op := string(c)
			if c == '\r' || c == '\n' {
				op = ";"
			}
			if (c == '|' || c == '&') && i+1 < len(command) && command[i+1] == c {
				op += string(c)
				i++
			}
			if op == "&" || (platform == "windows" && (op == "&&" || op == "||")) {
				return nil, false
			}
			if len(words) == 0 {
				// Blank lines and a newline after ';' are harmless. Repeated
				// semicolons, empty pipelines and incomplete conditions aren't.
				if (c == '\n' || c == '\r') && (len(parts) == 0 || parts[len(parts)-1].after == ";") {
					continue
				}
				return nil, false
			}
			parts = append(parts, aiDiagnosticPart{words: words, after: op})
			words = nil
			if len(parts) > 32 {
				return nil, false
			}
			continue
		}
		if c < 32 || c >= 127 || strings.ContainsRune("#[]!*?~", rune(c)) {
			return nil, false
		}
		started = true
		word.WriteByte(c)
	}
	if quote != 0 || !flush() {
		return nil, false
	}
	if len(words) > 0 {
		parts = append(parts, aiDiagnosticPart{words: words})
	} else if len(parts) > 0 {
		if parts[len(parts)-1].after != ";" {
			return nil, false
		}
		parts[len(parts)-1].after = ""
	}
	return parts, len(parts) > 0 && len(parts) <= 32
}

func aiAutomaticCommand(server *models.Server, command string) (AIAction, bool) {
	parts, ok := aiParseDiagnostic(command, server.ServerType)
	if !ok {
		return AIAction{}, false
	}
	var rendered []string
	for i, part := range parts {
		fromPipe := i > 0 && parts[i-1].after == "|"
		var action AIAction
		if fromPipe {
			if server.ServerType == "windows" {
				ok = aiWindowsDiagnosticFilter(part.words)
			} else {
				ok = aiLinuxDiagnosticFilter(part.words)
			}
			if ok {
				action.Command = aiRenderDiagnostic(part.words, server.ServerType)
			}
		} else {
			action, ok = aiDiagnosticInvocation(server, part.words)
		}
		if !ok {
			return AIAction{}, false
		}
		// Existing typed package/service operations are still supported on
		// their own; composing mutations would need a separate execution plan.
		if len(parts) == 1 {
			return action, true
		}
		if action.Mutating || action.RequiresApproval {
			return AIAction{}, false
		}
		rendered = append(rendered, action.Command)
		if part.after != "" {
			rendered = append(rendered, part.after)
		}
	}
	return AIAction{Command: strings.Join(rendered, " "), Summary: "Inspect server: " + command}, true
}

func aiDiagnosticInvocation(server *models.Server, words []string) (AIAction, bool) {
	// Preserve the existing narrowly typed installs/service actions. Joining
	// must never reinterpret a quoted argument containing a space as argv.
	plainWords := true
	for _, word := range words {
		if word == "" || strings.ContainsFunc(word, unicode.IsSpace) {
			plainWords = false
		}
	}
	if plainWords {
		if action, ok := aiAutomaticSimpleCommand(server, strings.Join(words, " ")); ok {
			return action, true
		}
	}
	var rewritten []string
	var ok bool
	if server.ServerType == "windows" {
		rewritten, ok = aiWindowsDiagnostic(words)
	} else {
		rewritten, ok = aiLinuxDiagnostic(words)
	}
	if !ok {
		return AIAction{}, false
	}
	return AIAction{Command: aiRenderDiagnostic(rewritten, server.ServerType), Summary: "Inspect server: " + strings.Join(words, " ")}, true
}

func aiRenderDiagnostic(words []string, platform string) string {
	result := make([]string, len(words))
	for i, word := range words {
		if word != "" && strings.IndexFunc(word, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_.:/=,+@", r))
		}) == -1 {
			result[i] = word
		} else if platform == "windows" {
			result[i] = aiPSLiteral(word)
		} else {
			result[i] = QuoteFileArgument(word)
		}
	}
	return strings.Join(result, " ")
}

func aiIn(value, allowed string) bool {
	for _, option := range strings.Fields(allowed) {
		if value == option {
			return true
		}
	}
	return false
}

func aiAllOptions(words []string, allowed string) bool {
	for _, word := range words {
		if !aiIn(word, allowed) {
			return false
		}
	}
	return true
}

func aiCount(value string) bool {
	if value == "" || strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) != -1 {
		return false
	}
	n, err := strconv.Atoi(value)
	return err == nil && n > 0 && n <= 10000
}

func aiColumns(value, allowed string) bool {
	for _, property := range strings.Split(value, ",") {
		if !aiIn(strings.ToLower(property), strings.ToLower(allowed)) {
			return false
		}
	}
	return true
}

const aiSystemdProperties = "Id LoadState ActiveState SubState UnitFileState Result NRestarts MainPID ExecMainStatus MemoryCurrent CPUUsageNSec TasksCurrent ActiveEnterTimestamp InactiveEnterTimestamp"

func aiSystemdDiagnostic(words []string) ([]string, bool) {
	verb := ""
	var units []string
	for i := 1; i < len(words); i++ {
		word := words[i]
		if verb == "" && aiIn(word, "is-system-running is-active is-enabled is-failed status show list-units list-unit-files") {
			verb = word
		} else if aiIn(word, "--no-pager --plain --no-legend --all -a --full -l --failed --quiet -q") {
			continue
		} else if word == "--state" || word == "--type" || word == "--property" || word == "-p" || strings.HasPrefix(word, "--state=") || strings.HasPrefix(word, "--type=") || strings.HasPrefix(word, "--property=") {
			key, value, hasValue := strings.Cut(word, "=")
			if !hasValue {
				i++
				if i >= len(words) {
					return nil, false
				}
				value = words[i]
			}
			allowed := "failed active inactive activating deactivating running exited dead loaded not-found masked enabled disabled static"
			if key == "--type" {
				allowed = "service socket target timer mount automount path slice scope swap device"
			} else if key == "--property" || key == "-p" {
				allowed = aiSystemdProperties
			}
			if !aiColumns(value, allowed) {
				return nil, false
			}
		} else if verb != "" && ValidServiceName(word) {
			units = append(units, word)
		} else {
			return nil, false
		}
	}
	if verb == "" {
		if !aiAllOptions(words[1:], "--failed --no-pager --plain --no-legend --all -a --full -l") || len(words) == 1 {
			return nil, false
		}
		verb = "list-units"
	}
	if (verb == "status" || verb == "show") && len(units) == 0 {
		return nil, false
	}
	if verb == "status" {
		// status includes logs and full process argv. Report state fields
		// instead; arbitrary logs still need explicit content approval.
		return append([]string{"systemctl", "--no-pager", "show", "--property=Id,LoadState,ActiveState,SubState,UnitFileState,Result,NRestarts,ExecMainStatus", "--"}, units...), true
	}
	if verb == "show" {
		found := false
		for _, word := range words {
			found = found || word == "--property" || word == "-p" || strings.HasPrefix(word, "--property=")
		}
		if !found {
			return nil, false
		}
	}
	return append(append([]string{}, words...), "--no-pager"), true
}

func aiLinuxDiagnostic(words []string) ([]string, bool) {
	args := words[1:]
	ok := false
	switch words[0] {
	case "true", "false", "whoami", "id", "uptime", "nproc":
		ok = len(args) == 0
	case "uname":
		ok = aiAllOptions(args, "-a -s -r -m -n -v -p -i -o -srm --all --kernel-name --kernel-release --machine")
	case "hostname":
		ok = aiAllOptions(args, "-f -s -I -i --fqdn --short --all-ip-addresses --ip-address")
	case "date":
		ok = aiAllOptions(args, "-u --utc -R --rfc-email --iso-8601 --iso-8601=seconds")
	case "free":
		ok = aiAllOptions(args, "-h -m -g -k -b -w -t --human --mega --giga --wide --total")
	case "df":
		ok = true
		for i := 0; i < len(args); i++ {
			if aiIn(args[i], "-h -H -T -hT -Th -i -a -l -P --human-readable --print-type --inodes --local /") {
				continue
			}
			if (args[i] == "-x" || args[i] == "-t") && i+1 < len(args) && aiIn(args[i+1], "tmpfs devtmpfs overlay squashfs ext4 xfs btrfs nfs nfs4") {
				i++
				continue
			}
			ok = false
		}
	case "ss":
		ok = len(args) > 0 && aiAllOptions(args, "-l -n -t -u -p -a -s -lnt -lntu -lntp -lntup -tuln -tulpn -tulpen -tunlp -ant -antp --listening --numeric --tcp --udp --processes --summary")
	case "ip":
		// ip accepts abbreviated actions (e.g. 'a' = add). Only permit
		// flags, one known object and an optional explicit show/list verb.
		for len(args) > 0 && aiIn(args[0], "-brief -br -4 -6 -s -o") {
			args = args[1:]
		}
		ok = len(args) >= 1 && aiIn(args[0], "address addr a link route r") && (len(args) == 1 || len(args) == 2 && aiIn(args[1], "show list"))
	case "ps":
		// Process arguments and environment may contain unknown credentials.
		ok = len(args) == 2 && aiIn(args[0], "-eo -o") && aiColumns(args[1], "pid ppid user stat pcpu pmem comm etime time rss vsz nlwp")
	case "lsblk":
		ok = aiAllOptions(args, "-a -f -l -n -p -b --all --fs --list --noheadings --paths --bytes")
	case "cat":
		ok = len(args) > 0
		for _, filename := range args {
			ok = ok && aiPublicDiagnosticFile(filename, "linux")
		}
	case "systemctl":
		return aiSystemdDiagnostic(words)
	case "docker":
		// No client-selected --format, context/host, compose files or inspect:
		// those can expose credentials or retarget the selected server.
		if len(args) >= 1 && args[0] == "ps" && aiAllOptions(args[1:], "-a --all --no-trunc -q --quiet") {
			return append(append([]string{}, words...), "--format", "table {{.ID}}\t{{.Names}}\t{{.Image}}\t{{.Status}}\t{{.Ports}}"), true
		}
		ok = len(args) == 1 && aiIn(args[0], "images version") || len(args) == 2 && aiIn(args[0], "image volume network") && args[1] == "ls" || len(args) == 2 && args[0] == "stats" && args[1] == "--no-stream"
	}
	return words, ok
}

func aiLinuxDiagnosticFilter(words []string) bool {
	args := words[1:]
	switch words[0] {
	case "head", "tail":
		return len(args) == 0 || len(args) == 2 && aiIn(args[0], "-n -c --lines --bytes") && aiCount(args[1])
	case "wc":
		return aiAllOptions(args, "-l -w -c -m --lines --words --bytes --chars")
	case "sort":
		return aiAllOptions(args, "-n -r -u -h --numeric-sort --reverse --unique --human-numeric-sort")
	case "uniq":
		return aiAllOptions(args, "-c -d -u --count --repeated --unique")
	case "grep":
		pattern := false
		for i := 0; i < len(args); i++ {
			arg := args[i]
			if !pattern && aiIn(arg, "-i -v -n -c -E -F -w -x --ignore-case --invert-match --line-number --count --extended-regexp --fixed-strings --word-regexp --line-regexp") {
				continue
			}
			if !pattern && (arg == "-m" || arg == "--max-count") && i+1 < len(args) && aiCount(args[i+1]) {
				i++
				continue
			}
			if arg == "--" && !pattern && i+2 == len(args) {
				return true
			}
			if pattern || strings.HasPrefix(arg, "-") {
				return false
			}
			pattern = true
		}
		return pattern
	}
	return false
}

const aiWindowsProperties = "Name DisplayName Status StartType ServiceName Id ProcessName CPU WorkingSet WorkingSet64 PM WS Handles NPM Path Caption Version OSArchitecture LastBootUpTime FreePhysicalMemory TotalVisibleMemorySize TotalPhysicalMemory NumberOfProcesses NumberOfLogicalProcessors NumberOfCores LoadPercentage DeviceID DriveType FileSystem Size FreeSpace Used Free Root Provider Count Average Minimum Maximum Sum"

func aiWindowsDiagnostic(words []string) ([]string, bool) {
	args := words[1:]
	switch strings.ToLower(words[0]) {
	case "get-service":
		if len(args) == 0 || len(args) == 2 && strings.EqualFold(args[0], "-Name") && ValidServiceName(args[1]) {
			return words, true
		}
	case "get-process":
		if len(args) == 0 || len(args) == 2 && strings.EqualFold(args[0], "-Name") && ValidServiceName(args[1]) {
			return words, true
		}
	case "get-date", "get-computerinfo", "whoami", "hostname":
		return words, len(args) == 0
	case "get-psdrive":
		return words, len(args) == 2 && strings.EqualFold(args[0], "-PSProvider") && strings.EqualFold(args[1], "FileSystem")
	case "get-ciminstance":
		if len(args) == 2 && strings.EqualFold(args[0], "-ClassName") {
			args = args[1:]
		}
		if len(args) == 1 && aiIn(strings.ToLower(args[0]), "win32_operatingsystem win32_processor win32_computersystem win32_logicaldisk") {
			return words, true
		}
	case "docker":
		return aiLinuxDiagnostic(words)
	}
	return nil, false
}

func aiWindowsDiagnosticFilter(words []string) bool {
	args := words[1:]
	if len(args) == 0 {
		return false
	}
	switch strings.ToLower(words[0]) {
	case "select-object", "format-table":
		for i := 0; i < len(args); i++ {
			arg := strings.ToLower(args[i])
			if aiIn(arg, "-first -last -skip") && strings.EqualFold(words[0], "Select-Object") && i+1 < len(args) && aiCount(args[i+1]) {
				i++
				continue
			}
			if arg == "-property" && i+1 < len(args) && aiColumns(args[i+1], aiWindowsProperties) {
				i++
				continue
			}
			if (arg == "-autosize" || arg == "-hidetableheaders") && strings.EqualFold(words[0], "Format-Table") {
				continue
			}
			if !aiColumns(args[i], aiWindowsProperties) {
				return false
			}
		}
		return true
	case "sort-object":
		for _, arg := range args {
			if !aiIn(strings.ToLower(arg), "-descending -unique -property") && !aiColumns(arg, aiWindowsProperties) {
				return false
			}
		}
		return true
	case "measure-object":
		for _, arg := range args {
			if !aiIn(strings.ToLower(arg), "-average -minimum -maximum -sum -property") && !aiColumns(arg, aiWindowsProperties) {
				return false
			}
		}
		return true
	}
	return false
}
