package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"server-monitor/internal/models"
)

const (
	AIArgumentsLimit    = 32 << 10
	AIOutputLimit       = 64 << 10
	AIFileLimit         = 24 << 10
	AIMaxCommandTimeout = 300 * time.Second
)

type AIAction struct {
	ApprovalMode     string          `json:"-"`
	ToolName         string          `json:"tool_name"`
	Arguments        json.RawMessage `json:"arguments"`
	Summary          string          `json:"summary"`
	Command          string          `json:"command,omitempty"`
	RequiresApproval bool            `json:"requires_approval"`
	RiskReason       string          `json:"risk_reason,omitempty"`
	Mutating         bool            `json:"mutating"`
}

type AIActionResult struct {
	Output     string `json:"output"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	Error      string `json:"error,omitempty"`
	Truncated  bool   `json:"truncated"`
	Unknown    bool   `json:"unknown"`
	DurationMS int64  `json:"duration_ms"`
	BackupPath string `json:"backup_path,omitempty"`
}

type aiPackages struct {
	Manager  string   `json:"manager"`
	Packages []string `json:"packages"`
	Refresh  bool     `json:"refresh,omitempty"`
}
type aiService struct {
	Name   string `json:"name"`
	Action string `json:"action"`
}
type aiReadFile struct {
	Path string `json:"path"`
}
type aiWriteFile struct {
	Path             string `json:"path"`
	Content          string `json:"content"`
	ExpectedRevision string `json:"expected_revision"`
}
type aiCommand struct {
	Command        string `json:"command"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

func AITools() []AIToolDefinition {
	property := func(kind, description string) map[string]any {
		return map[string]any{"type": kind, "description": description}
	}
	enum := func(values ...string) map[string]any { return map[string]any{"type": "string", "enum": values} }
	definition := func(name, description string, properties map[string]any, required ...string) AIToolDefinition {
		if required == nil {
			required = []string{}
		}
		return AIToolDefinition{Type: "function", Function: AIFunctionDefinition{Name: name, Description: description, Parameters: map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}}}
	}
	return []AIToolDefinition{
		definition("inspect_server", "Read OS, current user, installed package managers and service-manager availability on the selected server. No arguments or server identifiers.", map[string]any{}),
		definition("install_packages", "Install named packages from the configured package-manager repositories on the selected server. Package IDs only: no options, URLs or local files. refresh updates Linux repository indexes; an empty package list is only valid with refresh. Uses the connection's privileges without automatic sudo.", map[string]any{"manager": enum("apt", "dnf", "yum", "apk", "zypper", "winget", "choco"), "packages": map[string]any{"type": "array", "items": property("string", "Exact repository package ID"), "maxItems": 32}, "refresh": property("boolean", "Refresh Linux indexes before installing")}, "manager", "packages"),
		definition("manage_service", "Inspect or manage one exact service on the selected server. Stop/disable and security or remote-access services require explicit approval. Windows has no reload equivalent.", map[string]any{"name": property("string", "Exact service name"), "action": enum("status", "start", "stop", "restart", "reload", "enable", "disable")}, "name", "action"),
		definition("read_file", "Read one UTF-8 regular text file up to 24 KiB and return content plus SHA-256 revision. Secret files and symlinks are refused. Only public OS identity files may be read automatically; other files require specific approval to send their contents to the model provider. Never use this to retrieve credentials.", map[string]any{"path": property("string", "Absolute file path on this server")}, "path"),
		definition("write_file", "Save one UTF-8 text file up to 24 KiB. Supply the exact SHA-256 revision from read_file, or 'missing' only to create a new file. Existing contents are backed up before atomic replacement. All file writes require exact-action approval in standard mode, including declarative server configuration.", map[string]any{"path": property("string", "Absolute file path"), "content": property("string", "Complete new UTF-8 text"), "expected_revision": property("string", "SHA-256 of current contents, or missing for a new file")}, "path", "content", "expected_revision"),
		definition("run_command", "Run diagnostics or another exact command on the selected server. Verified Linux systemctl state/list/show, uptime, free, df, ss/ip, safe ps fields and Docker state may be combined with semicolons, newlines, && or || and piped through stdin-only grep/head/tail/sort/uniq/wc. Windows verified Get-Service/Get-Process/Get-CimInstance diagnostics support Select-Object/Sort-Object/Measure-Object/Format-Table pipelines and semicolons. All parts are checked; do not use shell expansions, redirection or scripts for these diagnostics. Typed package/service commands remain available individually. Logs, private file contents, unknown programs/options and destructive actions require exact approval in standard mode. A timeout or lost connection can leave an unknown outcome: never automatically retry mutations.", map[string]any{"command": property("string", "Exact command to execute"), "timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 300}}, "command"),
	}
}

// PrepareAIAction owns the policy decision. Model-provided risk declarations,
// extra arguments and target selectors are never accepted.
func PrepareAIAction(server *models.Server, call AIToolCall) (AIAction, error) {
	var action AIAction
	if server == nil || (server.ServerType != "linux" && server.ServerType != "windows") {
		return action, errors.New("unsupported server platform")
	}
	if call.Type != "" && call.Type != "function" {
		return action, errors.New("unsupported tool-call type")
	}
	raw := []byte(call.Function.Arguments)
	if err := validateAIJSON(raw); err != nil {
		return action, err
	}
	action.ToolName = call.Function.Name
	var normalized any
	switch action.ToolName {
	case "inspect_server":
		args := struct{}{}
		if err := decodeAIArguments(raw, &args); err != nil {
			return action, err
		}
		normalized = args
		action.Summary = "Inspect server operating system and available management tools"
		action.Command = aiInspectCommand(server.ServerType)
	case "install_packages":
		var args aiPackages
		if err := decodeAIArguments(raw, &args); err != nil {
			return action, err
		}
		command, err := aiPackageCommand(server.ServerType, args)
		if err != nil {
			return action, err
		}
		normalized = args
		action.Command = command
		action.Mutating = true
		action.Summary = "Install packages with " + args.Manager + ": " + strings.Join(args.Packages, ", ")
		if len(args.Packages) == 0 {
			action.Summary = "Refresh " + args.Manager + " package indexes"
		}
		if args.Refresh && len(args.Packages) > 0 {
			action.Summary = "Refresh indexes and " + strings.ToLower(action.Summary)
		}
	case "manage_service":
		var args aiService
		if err := decodeAIArguments(raw, &args); err != nil {
			return action, err
		}
		command, err := aiServiceCommand(server.ServerType, args)
		if err != nil {
			return action, err
		}
		normalized = args
		action.Command = command
		action.Mutating = args.Action != "status"
		action.Summary = args.Action + " service " + args.Name
		if action.Mutating && (args.Action == "stop" || args.Action == "disable" || aiSensitiveService(args.Name)) {
			action.RequiresApproval = true
			action.RiskReason = "This can stop a service, change security controls, or interrupt remote access"
		}
	case "read_file":
		var args aiReadFile
		if err := decodeAIArguments(raw, &args); err != nil {
			return action, err
		}
		clean, err := aiFilePath(args.Path, server.ServerType)
		if err != nil {
			return action, err
		}
		args.Path = clean
		normalized = args
		if aiSecretPath(clean) {
			return action, errors.New("reading credential or private security files into AI context is not allowed")
		}
		action.Summary = "Read text file " + clean
		if !aiPublicDiagnosticFile(clean, server.ServerType) {
			action.RequiresApproval = true
			action.RiskReason = "Reading this file sends its contents to the model provider; approve this exact file only after checking it contains no private data"
		}
	case "write_file":
		var args aiWriteFile
		if err := decodeAIArguments(raw, &args); err != nil {
			return action, err
		}
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		if value, ok := fields["content"]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return action, errors.New("content is required (use an explicit empty string to empty a file)")
		}
		clean, err := aiFilePath(args.Path, server.ServerType)
		if err != nil {
			return action, err
		}
		args.Path = clean
		if !validAIText(args.Content) || len(args.Content) > AIFileLimit {
			return action, errors.New("file content must be UTF-8 text of at most 24 KiB")
		}
		if args.ExpectedRevision != "missing" && !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(args.ExpectedRevision) {
			return action, errors.New("expected_revision must be a lowercase SHA-256 or missing")
		}
		if aiContainsSecrets(args.Content) {
			return action, errors.New("do not place credentials or private key material in AI file actions")
		}
		normalized = args
		action.Mutating = true
		action.Summary = "Save text file " + clean + " after revision validation and backup"
		action.RequiresApproval = true
		action.RiskReason = "This file's execution or security effect cannot be verified automatically"
		if aiSecretPath(clean) || aiSecurityOrStartupPath(clean) {
			action.RiskReason = "This changes credentials, access controls, executable code, or system startup behavior"
		}
	case "run_command":
		var args aiCommand
		if err := decodeAIArguments(raw, &args); err != nil {
			return action, err
		}
		args.Command = strings.TrimSpace(args.Command)
		if args.Command == "" || !utf8.ValidString(args.Command) || strings.ContainsRune(args.Command, 0) {
			return action, errors.New("command must be nonempty UTF-8 without NUL")
		}
		if args.TimeoutSeconds == 0 {
			args.TimeoutSeconds = 60
		}
		if args.TimeoutSeconds < 1 || args.TimeoutSeconds > 300 {
			return action, errors.New("timeout_seconds must be between 1 and 300")
		}
		normalized = args
		action.Command = args.Command
		action.Summary = "Run command: " + args.Command
		action.RequiresApproval = true
		action.Mutating = true
		action.RiskReason = aiCommandRiskReason(args.Command)
		if prepared, ok := aiAutomaticCommand(server, args.Command); ok {
			action.Command = prepared.Command
			action.RequiresApproval = prepared.RequiresApproval
			action.Mutating = prepared.Mutating
			action.RiskReason = prepared.RiskReason
			action.Summary = prepared.Summary
		}
	default:
		return action, errors.New("unknown AI tool")
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return action, err
	}
	action.Arguments = encoded
	return action, nil
}

// ExecuteAIAction must only receive actions that the task worker has authorized.
// Rebuilding from arguments detects tampering and stale policy; approval itself
// is stored and checked by the worker, never by model-supplied arguments.
func ExecuteAIAction(ctx context.Context, server *models.Server, action AIAction) (result AIActionResult, err error) {
	started := time.Now()
	defer func() {
		result.DurationMS = time.Since(started).Milliseconds()
		if err != nil {
			result.Error = err.Error()
		}
		result.Output, result.Truncated = boundAIOutput(result.Output, result.Truncated)
	}()
	prepared, err := PrepareAIAction(server, AIToolCall{Type: "function", Function: AIFunctionCall{Name: action.ToolName, Arguments: string(action.Arguments)}})
	if err != nil {
		return result, err
	}
	var originalArguments, preparedArguments any
	_ = json.Unmarshal(action.Arguments, &originalArguments)
	_ = json.Unmarshal(prepared.Arguments, &preparedArguments)
	if prepared.Command != action.Command || prepared.Mutating != action.Mutating || prepared.RequiresApproval != action.RequiresApproval || prepared.RiskReason != action.RiskReason || !reflect.DeepEqual(preparedArguments, originalArguments) {
		return result, errors.New("action changed; prepare it again and obtain any required approval")
	}
	if _, err := aiActionApproval(action.ApprovalMode, prepared); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	timeout := 60 * time.Second
	if action.ToolName == "install_packages" {
		timeout = AIMaxCommandTimeout
	}
	if action.ToolName == "run_command" {
		var args aiCommand
		_ = json.Unmarshal(action.Arguments, &args)
		timeout = time.Duration(args.TimeoutSeconds) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client, _, err := DialServer(ctx, server)
	if err != nil {
		return result, err
	}
	defer client.Close()
	stop := context.AfterFunc(ctx, func() { _ = client.Close() })
	defer stop()
	if action.ToolName == "read_file" || action.ToolName == "write_file" {
		return executeAIFile(ctx, client, server, action)
	}
	command := action.Command
	if server.ServerType == "windows" {
		command = aiWindowsCommand(command)
	}
	return executeAICommand(ctx, client, command)
}

func executeAICommand(ctx context.Context, client *ssh.Client, command string) (result AIActionResult, err error) {
	session, err := newSSHSession(ctx, client)
	if err != nil {
		return result, err
	}
	defer session.Close()
	writer := &syncLimitedWriter{limit: AIOutputLimit}
	session.Stdout, session.Stderr = writer, writer
	var attempted atomic.Bool
	done := make(chan error, 1)
	go func() {
		if err := ctx.Err(); err != nil {
			done <- err
			return
		}
		attempted.Store(true)
		done <- session.Run(command)
	}()
	select {
	case err = <-done:
		if ctx.Err() != nil {
			err = ctx.Err()
		}
	case <-ctx.Done():
		err = ctx.Err()
		_ = client.Close()
	}
	result.Output, result.Truncated = writer.result()
	if err == nil {
		code := 0
		result.ExitCode = &code
		return result, nil
	}
	var exit *ssh.ExitError
	if errors.As(err, &exit) {
		code := exit.ExitStatus()
		result.ExitCode = &code
		return result, fmt.Errorf("command exited with status %d", code)
	}
	result.Unknown = attempted.Load()
	return result, err
}

func executeAIFile(ctx context.Context, client *ssh.Client, server *models.Server, action AIAction) (result AIActionResult, err error) {
	remote, err := NewSFTPFiles(ctx, client, server.ServerType)
	if err != nil {
		return result, err
	}
	defer remote.Close()
	store := remote.(*sftpFiles)
	var filename string
	var write aiWriteFile
	if action.ToolName == "write_file" {
		_ = json.Unmarshal(action.Arguments, &write)
		filename = write.Path
	} else {
		var read aiReadFile
		_ = json.Unmarshal(action.Arguments, &read)
		filename = read.Path
	}
	// Reading an executable text file does not execute it. Executable writes
	// still need an approved action unless the user selected fully automatic.
	allowExecutable := action.ToolName == "read_file" || action.RequiresApproval || action.ApprovalMode == "automatic"
	if err := aiVerifyFilePath(store, filename, action.ToolName == "write_file" && write.ExpectedRevision == "missing", allowExecutable); err != nil {
		return result, err
	}
	if action.ToolName == "read_file" {
		content, err := readAIFile(store, filename)
		if err != nil {
			return result, err
		}
		if aiContainsSecrets(string(content)) {
			return result, errors.New("file contains credential-like material; contents were withheld from AI")
		}
		var output bytes.Buffer
		encoder := json.NewEncoder(&output)
		encoder.SetEscapeHTML(false)
		_ = encoder.Encode(map[string]string{"path": filename, "content": string(content), "revision": FileRevision(content)})
		result.Output = output.String()
		return result, nil
	}
	if write.ExpectedRevision == "missing" {
		if _, err := store.client.Lstat(filename); err == nil {
			return result, ErrFileExists
		} else if !os.IsNotExist(err) {
			return result, err
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := store.Write(filename, strings.NewReader(write.Content), int64(len(write.Content)), false); err != nil {
			// The common file store translates a failed rename into Exists when
			// the target is now present. It could be our committed write whose
			// reply failed, so this late Exists cannot prove nothing happened.
			result.Unknown = errors.Is(err, ErrFileExists) || aiUncertainFileError(ctx, err)
			return result, err
		}
	} else {
		current, err := readAIFile(store, filename)
		if err != nil {
			return result, err
		}
		if FileRevision(current) != write.ExpectedRevision {
			return result, ErrFileChanged
		}
		if aiContainsSecrets(string(current)) {
			return result, errors.New("existing file contains credentials; refusing an AI rewrite")
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		result.BackupPath, err = BackupRemoteText(store, filename, current)
		if err != nil {
			result.Unknown = aiUncertainFileError(ctx, err)
			return result, err
		}
		// Recheck after making the backup so an intervening edit is not replaced.
		if err := SaveRemoteText(store, filename, write.Content, write.ExpectedRevision); err != nil {
			result.Unknown = aiUncertainFileError(ctx, err)
			return result, err
		}
	}
	result.Output = "Saved " + filename + "; revision=" + FileRevision([]byte(write.Content))
	return result, nil
}

func readAIFile(store RemoteFiles, filename string) ([]byte, error) {
	var output bytes.Buffer
	if err := store.Read(filename, &output, AIFileLimit); err != nil {
		return nil, err
	}
	if !validAIText(output.String()) {
		return nil, ErrFileNotText
	}
	return output.Bytes(), nil
}

func aiUncertainFileError(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx.Err() != nil {
		return true
	}
	for _, definite := range []error{ErrFileChanged, ErrFileExists, ErrFileNotRegular, ErrFileTooLarge, ErrFileNotText, ErrFilePath, os.ErrNotExist, os.ErrPermission, os.ErrExist} {
		if errors.Is(err, definite) {
			return false
		}
	}
	var status *sftp.StatusError
	if errors.As(err, &status) {
		switch status.FxCode() {
		case sftp.ErrSSHFxNoSuchFile, sftp.ErrSSHFxPermissionDenied, sftp.ErrSSHFxOpUnsupported:
			return false
		}
	}
	// An unrecognized failure can hide a completed rename, including a generic
	// SFTP failure from a backend that committed before its reply failed. Only
	// definite rejections above allow the worker to plan another operation.
	return true
}

func boundAIOutput(output string, truncated bool) (string, bool) {
	output = strings.ToValidUTF8(output, "�")
	if len(output) <= AIOutputLimit {
		return output, truncated
	}
	end := AIOutputLimit
	for end > 0 && !utf8.RuneStart(output[end]) {
		end--
	}
	return output[:end], true
}

func validAIText(text string) bool {
	return utf8.ValidString(text) && !strings.ContainsFunc(text, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' })
}

func aiVerifyFilePath(store *sftpFiles, filename string, allowMissing, allowExecutable bool) error {
	// Canonicalization alone does not protect a harmless pathname pointing at
	// a private key. Refuse symlink components rather than following them.
	parts := strings.Split(strings.TrimPrefix(filename, "/"), "/")
	current := "/"
	if len(parts) > 0 && strings.HasSuffix(parts[0], ":") {
		current = parts[0] + "/"
		parts = parts[1:]
	}
	for index, part := range parts {
		current = path.Join(current, part)
		info, err := store.client.Lstat(current)
		if err != nil {
			if allowMissing && index == len(parts)-1 && os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("AI file operations refuse symlinks; specify and review the real path")
		}
		if index < len(parts)-1 && !info.IsDir() {
			return ErrFileNotRegular
		}
		if index == len(parts)-1 {
			if !info.Mode().IsRegular() {
				return ErrFileNotRegular
			}
			if !allowExecutable && info.Mode().Perm()&0111 != 0 {
				return errors.New("file is executable; prepare an explicitly approved action before changing it")
			}
		}
	}
	return nil
}
