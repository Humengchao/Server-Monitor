package services

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"server-monitor/internal/models"
)

func validateAIJSON(raw []byte) error {
	if len(raw) == 0 || len(raw) > AIArgumentsLimit || !utf8.Valid(raw) {
		return errors.New("tool arguments must be valid UTF-8 JSON of at most 32 KiB")
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("tool arguments must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var value func(int) error
	value = func(depth int) error {
		if depth > 12 {
			return errors.New("tool arguments are nested too deeply")
		}
		token, err := decoder.Token()
		if err != nil {
			return errors.New("invalid tool arguments JSON")
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return errors.New("duplicate or invalid tool argument")
				}
				seen[name] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid tool arguments")
		}
		_, err = decoder.Token()
		return err
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("tool arguments contain trailing JSON")
	}
	return nil
}

func decodeAIArguments(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid tool arguments: %w", err)
	}
	return nil
}

func aiInspectCommand(platform string) string {
	if platform == "windows" {
		return `$os=Get-CimInstance Win32_OperatingSystem; $os | Select-Object Caption,Version,OSArchitecture,LastBootUpTime; [Security.Principal.WindowsIdentity]::GetCurrent().Name; Get-Command winget,choco -ErrorAction SilentlyContinue | Select-Object Name,Source; Get-Service -Name EventLog,WinRM -ErrorAction SilentlyContinue | Select-Object Name,Status,StartType`
	}
	return `uname -srm; id; if [ -r /etc/os-release ]; then cat /etc/os-release; fi; for m in apt-get dnf yum apk zypper systemctl rc-service; do command -v "$m" || true; done; if command -v systemctl >/dev/null 2>&1; then systemctl is-system-running || true; fi`
}

var aiPackageID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9+._:-]{0,127}$`)

func aiPackageCommand(platform string, args aiPackages) (string, error) {
	if len(args.Packages) > 32 || (len(args.Packages) == 0 && !args.Refresh) {
		return "", errors.New("provide 1–32 package IDs, or refresh=true with an empty list")
	}
	for _, name := range args.Packages {
		lower := strings.ToLower(name)
		if !aiPackageID.MatchString(name) || strings.Contains(name, "..") || strings.HasSuffix(name, "-") || lower == "all" {
			return "", errors.New("package must be an exact repository ID without options, paths, or URLs")
		}
		for _, suffix := range []string{".deb", ".rpm", ".msi", ".msix", ".exe", ".ps1", ".sh"} {
			if strings.HasSuffix(lower, suffix) {
				return "", errors.New("local installer files require an explicitly approved run_command")
			}
		}
	}
	if platform == "windows" {
		if args.Manager != "winget" && args.Manager != "choco" {
			return "", errors.New("this package manager does not match the server platform")
		}
		if args.Refresh || len(args.Packages) == 0 {
			return "", errors.New("Windows package tools require package IDs and do not support refresh")
		}
		var commands []string
		for _, name := range args.Packages {
			if args.Manager == "winget" {
				commands = append(commands, "& winget install --id "+aiPSLiteral(name)+" --exact --silent --accept-package-agreements --accept-source-agreements --disable-interactivity; if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }")
			} else {
				commands = append(commands, "& choco install "+aiPSLiteral(name)+" --yes --no-progress; if ($LASTEXITCODE -eq 3010) { Write-Output 'Installation succeeded; a reboot is required and was not started.'; $global:LASTEXITCODE=0 } elseif ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }")
			}
		}
		return strings.Join(commands, "; "), nil
	}
	prefix, refresh := "", ""
	switch args.Manager {
	case "apt":
		// apt-get otherwise falls back to substring regular expressions when a
		// name containing '.' or '+' is missing. Only explicit anchored patterns
		// are allowed by this fixed option, and our package grammar rejects them.
		prefix = "env DEBIAN_FRONTEND=noninteractive apt-get install --yes --no-remove -o APT::Cmd::Pattern-Only=true --"
		refresh = "apt-get update"
	case "dnf":
		prefix = "dnf install --assumeyes --"
		refresh = "dnf makecache"
	case "yum":
		prefix = "yum install --assumeyes --"
		refresh = "yum makecache"
	case "apk":
		prefix = "apk add --"
		refresh = "apk update"
	case "zypper":
		prefix = "zypper --non-interactive install --"
		refresh = "zypper --non-interactive refresh"
	default:
		return "", errors.New("unsupported Linux package manager")
	}
	if len(args.Packages) == 0 {
		return refresh, nil
	}
	for _, name := range args.Packages {
		prefix += " " + QuoteFileArgument(name)
	}
	if args.Refresh {
		return refresh + " && " + prefix, nil
	}
	return prefix, nil
}

func aiServiceCommand(platform string, args aiService) (string, error) {
	if !ValidServiceName(args.Name) {
		return "", errors.New("invalid exact service name")
	}
	switch args.Action {
	case "status", "start", "stop", "restart", "reload", "enable", "disable":
	default:
		return "", errors.New("unsupported service action")
	}
	if platform == "windows" {
		name := aiPSLiteral(args.Name)
		switch args.Action {
		case "status":
			return "Get-Service -Name " + name + " -ErrorAction Stop | Select-Object Name,Status,StartType", nil
		case "start":
			return "Start-Service -Name " + name + " -ErrorAction Stop", nil
		case "stop":
			return "Stop-Service -Name " + name + " -ErrorAction Stop", nil
		case "restart":
			return "Restart-Service -Name " + name + " -ErrorAction Stop", nil
		case "enable":
			return "Set-Service -Name " + name + " -StartupType Automatic -ErrorAction Stop", nil
		case "disable":
			return "Set-Service -Name " + name + " -StartupType Disabled -ErrorAction Stop", nil
		default:
			return "", errors.New("Windows services do not support reload; choose a distinct action")
		}
	}
	if args.Action == "status" {
		// status prints recent journal lines and full process arguments, which
		// can contain credentials. Read only the state properties instead.
		return "systemctl --no-pager show --property=Id,LoadState,ActiveState,SubState,UnitFileState -- " + QuoteFileArgument(args.Name), nil
	}
	return "systemctl --no-pager " + args.Action + " -- " + QuoteFileArgument(args.Name), nil
}

func aiPSLiteral(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }

func aiWindowsCommand(script string) string {
	wrapper := "$ErrorActionPreference='Stop'; $ProgressPreference='SilentlyContinue'; [Console]::OutputEncoding=[Text.UTF8Encoding]::new($false); $global:LASTEXITCODE=0; try {\n" + script + "\nif ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }\n} catch { [Console]::Error.WriteLine($_.Exception.Message); exit 1 }"
	return "powershell -NoProfile -NonInteractive -EncodedCommand " + encodePowerShell(wrapper)
}

func aiSensitiveService(name string) bool {
	name = strings.ToLower(strings.TrimSuffix(name, ".service"))
	for _, part := range []string{"ssh", "sshd", "winrm", "firewall", "ufw", "iptables", "nftables", "network", "systemd", "dbus", "polkit", "audit", "defender", "windefend", "mpssvc", "eventlog", "server-monitor", "servermonitor", "cron", "tasksch", "schedule", "rpcss", "lsass", "sssd", "fail2ban", "reboot", "halt", "poweroff", "shutdown", "kexec", "rescue", "emergency"} {
		if strings.Contains(name, part) {
			return true
		}
	}
	// Non-service units can arm timers/path triggers, alter mounts or swap,
	// and manage process groups. Treat them as system control, not an ordinary
	// application restart. State inspection remains available in every mode.
	for _, suffix := range []string{".target", ".socket", ".mount", ".timer", ".path", ".automount", ".swap", ".scope", ".slice", ".device"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// This is an allowlist grammar, not an attempt to prove arbitrary shell text
// safe with a regex. Accepted inputs contain only separated inert tokens and
// must match an entire known operation with known options.
func aiAutomaticCommand(server *models.Server, command string) (AIAction, bool) {
	if strings.ContainsAny(command, "\r\n\t;|&><`$(){}[]\\\"'!*?~%\x00") {
		return AIAction{}, false
	}
	for _, r := range command {
		if r > 127 || unicode.IsControl(r) {
			return AIAction{}, false
		}
	}
	words := strings.Fields(command)
	if len(words) == 0 {
		return AIAction{}, false
	}
	plain := strings.Join(words, " ")
	readonly := map[string]bool{"uname": true, "uname -a": true, "uname -srm": true, "id": true, "whoami": true, "hostname": true, "uptime": true, "df -h": true, "df -hT": true, "free -m": true, "free -h": true, "nproc": true, "date -u": true, "ss -lnt": true, "ss -lntu": true, "ss -lntup": true, "ps -eo pid,ppid,user,stat,pcpu,pmem,comm": true, "systemctl is-system-running": true, "systemctl --failed --no-pager": true, "docker ps": true, "docker ps -a": true, "docker version": true, "docker stats --no-stream": true, "docker images": true, "docker image ls": true, "docker volume ls": true, "docker network ls": true, "docker compose ps": true, "nginx -t": true, "nginx -v": true, "cat /etc/os-release": true}
	if server.ServerType == "windows" {
		readonly = map[string]bool{"whoami": true, "hostname": true, "Get-Date": true, "Get-Service": true, "Get-Process": true, "Get-ComputerInfo": true, "Get-PSDrive -PSProvider FileSystem": true, "docker ps": true, "docker ps -a": true, "docker version": true, "docker stats --no-stream": true, "docker images": true}
	}
	if readonly[plain] {
		command := plain
		switch plain {
		case "docker ps", "docker ps -a":
			// Docker's default table includes process arguments, which commonly
			// contain application credentials unknown to the panel's redactor.
			command += ` --format 'table {{.ID}}\t{{.Names}}\t{{.Image}}\t{{.Status}}\t{{.Ports}}'`
		case "docker compose ps":
			command += ` --format 'table {{.Name}}\t{{.Service}}\t{{.State}}\t{{.Ports}}'`
		}
		// nginx -t opens configured log files and can create missing ones. It
		// remains a routine operation, but must not run in a read-only task.
		return AIAction{Command: command, Summary: "Inspect server: " + plain, Mutating: plain == "nginx -t"}, true
	}
	prepare := func(name string, args any) (AIAction, bool) {
		raw, _ := json.Marshal(args)
		action, err := PrepareAIAction(server, AIToolCall{Function: AIFunctionCall{Name: name, Arguments: string(raw)}})
		return action, err == nil
	}
	if server.ServerType == "linux" {
		manager := words[0]
		if manager == "apt-get" {
			manager = "apt"
		}
		if len(words) == 2 && ((manager == "apt" || manager == "apk") && words[1] == "update" || (manager == "dnf" || manager == "yum") && words[1] == "makecache" || manager == "zypper" && words[1] == "refresh") {
			return prepare("install_packages", aiPackages{Manager: manager, Packages: []string{}, Refresh: true})
		}
		if len(words) >= 3 && words[1] == "install" && (manager == "apt" || manager == "dnf" || manager == "yum" || manager == "zypper") {
			packages := words[2:]
			if len(packages) > 0 && (packages[0] == "-y" || packages[0] == "--yes") {
				packages = packages[1:]
			}
			return prepare("install_packages", aiPackages{Manager: manager, Packages: packages})
		}
		if len(words) >= 3 && manager == "apk" && words[1] == "add" {
			return prepare("install_packages", aiPackages{Manager: manager, Packages: words[2:]})
		}
		if len(words) == 3 && (words[0] == "systemctl" || words[0] == "service") {
			name, action := words[2], words[1]
			if words[0] == "service" {
				name, action = words[1], words[2]
			}
			return prepare("manage_service", aiService{Name: name, Action: action})
		}
	} else if len(words) == 3 && words[1] == "-Name" {
		action := map[string]string{"Get-Service": "status", "Start-Service": "start", "Restart-Service": "restart", "Stop-Service": "stop"}[words[0]]
		if action != "" {
			return prepare("manage_service", aiService{Name: words[2], Action: action})
		}
	}
	return AIAction{}, false
}

func aiCommandRiskReason(command string) string {
	lower := strings.ToLower(command)
	if strings.ContainsAny(command, "\r\n;|&><`$()") {
		return "Shell composition, redirection, or expansion can execute additional actions; review the exact command"
	}
	for _, word := range []string{"rm ", "remove-item", "del ", "rmdir", "format", "mkfs", "wipefs", "diskpart", "dd ", "reinstall", "reset", "purge", "uninstall", "autoremove", "reboot", "shutdown", "restart-computer", "stop-computer", "docker prune", "docker system", "--privileged", "--mount", "-v "} {
		if strings.Contains(lower, word) {
			return "This command can delete data, reinstall software, reboot, or grant privileged host access"
		}
	}
	if strings.Contains(lower, "encodedcommand") || strings.Contains(lower, "-enc ") || strings.Contains(lower, "base64") {
		return "Encoded or indirect scripts conceal the actions that will execute"
	}
	return "This program or argument combination is outside the automatically verified command set"
}

func aiFilePath(value, platform string) (string, error) {
	if value == "" || !utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl) {
		return "", ErrFilePath
	}
	if platform == "windows" {
		value = strings.ReplaceAll(value, "\\", "/")
		if strings.HasPrefix(value, "//") || strings.Contains(value, "/../") || strings.HasSuffix(value, "/..") {
			return "", errors.New("UNC, device, and traversal paths are not supported")
		}
		candidate := strings.TrimPrefix(value, "/")
		if len(candidate) < 4 || candidate[1:3] != ":/" || !((candidate[0] >= 'A' && candidate[0] <= 'Z') || (candidate[0] >= 'a' && candidate[0] <= 'z')) {
			return "", errors.New("Windows files require an absolute drive path")
		}
		if strings.Contains(candidate[2:], ":") {
			return "", errors.New("alternate data streams are not supported")
		}
		for _, part := range strings.Split(candidate[3:], "/") {
			if strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") || strings.ContainsAny(part, "<>|?*\"") {
				return "", ErrFilePath
			}
			stem := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
			if regexp.MustCompile(`^(CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])$`).MatchString(stem) {
				return "", errors.New("Windows device paths are not supported")
			}
		}
		value = candidate
	}
	clean, err := CleanRemotePath(value, false)
	if err != nil {
		return "", err
	}
	if platform == "linux" && !strings.HasPrefix(clean, "/") {
		return "", ErrFilePath
	}
	if clean == "/" || strings.HasSuffix(clean, ":/") {
		return "", errors.New("file path names a root directory")
	}
	return clean, nil
}

func aiSecretPath(filename string) bool {
	lower := strings.ToLower(strings.ReplaceAll(filename, "\\", "/"))
	base := path.Base(lower)
	for _, part := range strings.Split(lower, "/") {
		if part == ".ssh" || part == ".aws" || part == ".azure" || part == ".kube" || part == ".gnupg" || part == ".config" || part == ".credentials" || part == ".docker" || part == ".password-store" || part == "secrets" || part == "private" {
			return true
		}
	}
	// Credential-store backups are as sensitive as the original. In
	// particular shadow- is created by the normal account-management tools.
	for _, sensitive := range []string{"shadow", "gshadow", "opasswd", "sam", "security", "ntds.dit", "htpasswd", ".htpasswd", "htdigest", ".htdigest", ".netrc", ".pgpass", ".my.cnf", ".git-credentials", ".npmrc", ".dockercfg", "credentials", "credentials.json", "kubeconfig", "id_rsa", "id_ed25519", "id_ecdsa", "id_dsa"} {
		if base == sensitive || strings.HasPrefix(base, sensitive+".") || strings.HasPrefix(base, sensitive+"-") || strings.HasPrefix(base, sensitive+"~") {
			return true
		}
	}
	if strings.Contains(lower, "/redis/") || base == "redis.conf" || strings.HasSuffix(base, ".acl") {
		return true
	}
	if base == ".env" || strings.HasPrefix(base, ".env.") || strings.HasSuffix(base, ".env") || strings.HasSuffix(base, "history") || strings.HasSuffix(base, "history.txt") || lower == "/etc/environment" {
		return true
	}
	for _, suffix := range []string{".key", ".pem", ".p12", ".pfx", ".jks", ".kdbx", ".keytab"} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	for _, part := range []string{"server-monitor-agent/config.json", "servermonitoragent/config.json", "/etc/ssl/private/", "/windows/system32/config/", "/microsoft/protect/", "/docker/config.json", "/kubernetes/"} {
		if strings.Contains(lower, part) {
			return true
		}
	}
	return false
}

func aiSpecialPath(filename string) bool {
	lower := strings.ToLower(filename)
	for _, prefix := range []string{"/proc/", "/sys/", "/dev/", "/run/", "/var/run/"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func aiSecurityOrStartupPath(filename string) bool {
	lower := strings.ToLower(strings.ReplaceAll(filename, "\\", "/"))
	base := path.Base(lower)
	if aiSpecialPath(lower) {
		return true
	}
	for _, part := range []string{"/etc/ssh/", "/etc/pam.", "/etc/sudoers", "/etc/passwd", "/etc/group", "/etc/security/", "/etc/systemd/", "/lib/systemd/", "/etc/init.d/", "/etc/cron", "/etc/rc", "/etc/profile", "/etc/bash", "/etc/zsh", "/etc/ld.so", "/etc/apt/", "/etc/yum", "/etc/dnf/", "/etc/apk/", "/etc/zypp/", "/etc/network", "/etc/netplan/", "/etc/firewall", "/etc/ufw/", "/etc/nft", "/bin/", "/sbin/", "/boot/", "/windows/", "/startup/", "/start menu/", "/powershell/", "/openssh/"} {
		if strings.Contains(lower, part) {
			return true
		}
	}
	for _, suffix := range []string{".sh", ".bash", ".zsh", ".fish", ".ps1", ".psm1", ".psd1", ".bat", ".cmd", ".exe", ".dll", ".com", ".msi", ".reg", ".service", ".socket", ".timer", ".target", ".desktop", ".py", ".js", ".ts", ".php", ".pl", ".rb", ".lua", ".jar", ".so"} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return strings.HasPrefix(base, ".bash") || strings.HasPrefix(base, ".zsh") || base == ".profile" || base == "authorized_keys" || strings.Contains(base, "compose") || base == "dockerfile" || base == "makefile"
}

var aiSecretContent = regexp.MustCompile(`(?im)(-----BEGIN [A-Z ]*PRIVATE KEY-----|(?:password|passwd|api[_-]?key|access[_-]?token|secret(?:[_-]?access)?[_-]?key|client[_-]?secret|bot[_-]?token|authorization|secret|token)["']?\s*[:=]\s*["']?[^\s"';},]{1,}|["']auth["']\s*:\s*["'][A-Za-z0-9+/=_-]{4,}["']|\bAKIA[A-Z0-9]{16}\b|\bgh[pousr]_[A-Za-z0-9_]{20,}|https?://[^/\s:@]+:[^/\s@]+@|\b(?:Bearer|Basic)\s+[A-Za-z0-9+/=_-]{8,})`)

// Password files can be named arbitrarily. Recognize standard crypt/bcrypt
// and Apache password hashes even without a password= label, so renaming a
// credential store cannot make its contents eligible for model context.
var aiPasswordHashContent = regexp.MustCompile(`(?:^|[\s:"'=])(?:\$(?:2[aby]\$[0-9]{2}\$[./A-Za-z0-9]{53}|(?:apr1|1|5|6)\$(?:rounds=[0-9]+\$)?[./A-Za-z0-9]{1,16}\$[./A-Za-z0-9]{22,86}|y\$[./A-Za-z0-9]+\$[./A-Za-z0-9]+\$[./A-Za-z0-9]+)|\{SHA\}[A-Za-z0-9+/]{27}=)`)

// Redis directives and ACL passwords use whitespace and prefix tokens.
var aiServiceSecretContent = regexp.MustCompile(`(?im)^\s*(?:(?:requirepass|masterauth|auth-pass|password|passwd|secret|token|api[_-]?key)\s+[^#\s]+|user\s+\S+[^\r\n]*(?:[>][^\s]+|#[a-f0-9]{64}))`)

func aiContainsSecrets(content string) bool {
	return aiSecretContent.MatchString(content) || aiPasswordHashContent.MatchString(content) || aiServiceSecretContent.MatchString(content)
}

// Only public OS identity files may enter model context without file-specific
// approval. Arbitrary application files can contain unlabelled credentials.
func aiPublicDiagnosticFile(filename, platform string) bool {
	if platform != "linux" {
		return false
	}
	switch filename {
	case "/etc/os-release", "/usr/lib/os-release", "/etc/debian_version", "/etc/alpine-release", "/etc/redhat-release":
		return true
	}
	return false
}
