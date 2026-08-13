package conversationimport

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxSourceFiles    = 5000
	maxDiscoveryBytes = 256 << 20
	maxFileBytes      = 64 << 20
	maxLineBytes      = 4 << 20
	maxMessageBytes   = 256 << 10
	maxMessages       = 4000
)

func Discover(options Options) (Result, error) {
	sources := options.Sources
	if len(sources) == 0 {
		sources = []Source{SourceClaude, SourceCodex}
	}
	var result Result
	remainingBytes := int64(maxDiscoveryBytes)
	for _, source := range sources {
		root, err := sourceRoot(source, options.SourceRoot)
		if err != nil {
			result.Warnings = append(result.Warnings, err.Error())
			continue
		}
		conversations, warnings, consumedBytes, exhausted, err := discoverSource(source, root, options, remainingBytes)
		remainingBytes -= consumedBytes
		result.Warnings = append(result.Warnings, warnings...)
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s: %v", source, err))
			continue
		}
		result.Conversations = append(result.Conversations, conversations...)
		if exhausted {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s: discovery byte limit reached", source))
			break
		}
	}
	sort.SliceStable(result.Conversations, func(i, j int) bool {
		if !result.Conversations[i].UpdatedAt.Equal(result.Conversations[j].UpdatedAt) {
			return result.Conversations[i].UpdatedAt.After(result.Conversations[j].UpdatedAt)
		}
		return result.Conversations[i].ID < result.Conversations[j].ID
	})
	return result, nil
}

func Load(source Source, root, path string) (Conversation, error) {
	resolvedRoot, err := sourceRoot(source, root)
	if err != nil {
		return Conversation{}, err
	}
	path, err = containedRegularFile(resolvedRoot, path)
	if err != nil {
		return Conversation{}, err
	}
	return parseFile(source, path)
}

func sourceRoot(source Source, override string) (string, error) {
	if override != "" {
		info, err := os.Stat(override)
		if err != nil || !info.IsDir() {
			return "", fmt.Errorf("%s source root is unavailable", source)
		}
		return filepath.Abs(override)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("%s: resolve home directory", source)
	}
	var root string
	switch source {
	case SourceClaude:
		root = filepath.Join(home, ".claude", "projects")
	case SourceCodex:
		root = filepath.Join(home, ".codex")
	default:
		return "", fmt.Errorf("unsupported conversation source %q", source)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%s history was not found", source)
	}
	return root, nil
}

func discoverSource(source Source, root string, options Options, byteBudget int64) ([]Conversation, []string, int64, bool, error) {
	paths, consumedBytes, exhausted, err := collectSourcePaths(root, byteBudget)
	if err != nil {
		return nil, nil, consumedBytes, exhausted, err
	}
	var conversations []Conversation
	var warnings []string
	for _, path := range paths {
		conversation, err := parseFile(source, path)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", filepath.Base(path), err))
			continue
		}
		if len(conversation.Messages) == 0 {
			continue
		}
		if !options.AllWorkspaces && options.WorkspaceRoot != "" && !samePath(conversation.WorkspaceRoot, options.WorkspaceRoot) {
			continue
		}
		conversations = append(conversations, conversation)
	}
	return conversations, warnings, consumedBytes, exhausted, nil
}

func collectSourcePaths(root string, byteBudget int64) ([]string, int64, bool, error) {
	var paths []string
	var consumedBytes int64
	exhausted := false
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if len(paths) >= maxSourceFiles {
			return filepath.SkipAll
		}
		if !entry.Type().IsRegular() || !strings.EqualFold(filepath.Ext(path), ".jsonl") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		if info.Size() > byteBudget-consumedBytes {
			exhausted = true
			return filepath.SkipAll
		}
		consumedBytes += info.Size()
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return nil, consumedBytes, exhausted, err
	}
	return paths, consumedBytes, exhausted, nil
}

func parseFile(source Source, path string) (Conversation, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return Conversation{}, fmt.Errorf("source conversation is unavailable")
	}
	if info.Size() > maxFileBytes {
		return Conversation{}, fmt.Errorf("source conversation exceeds %d MiB", maxFileBytes>>20)
	}
	file, err := os.Open(path)
	if err != nil {
		return Conversation{}, fmt.Errorf("open source conversation: %w", err)
	}
	defer file.Close()
	conversation := Conversation{Source: source, Path: path}
	scanner := bufio.NewScanner(io.LimitReader(file, maxFileBytes+1))
	scanner.Buffer(make([]byte, 64<<10), maxLineBytes)
	seen := map[string]bool{}
	line := 0
	for scanner.Scan() {
		line++
		if len(conversation.Messages) >= maxMessages {
			conversation.Warnings = append(conversation.Warnings, "message limit reached")
			break
		}
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		if !utf8.Valid(raw) {
			conversation.Warnings = append(conversation.Warnings, fmt.Sprintf("line %d is not valid UTF-8", line))
			continue
		}
		var record map[string]any
		if json.Unmarshal(raw, &record) != nil {
			conversation.Warnings = append(conversation.Warnings, fmt.Sprintf("line %d is malformed", line))
			continue
		}
		var messages []Message
		switch source {
		case SourceClaude:
			messages = consumeClaude(&conversation, record)
		case SourceCodex:
			messages = consumeCodex(&conversation, record)
		}
		for _, message := range messages {
			message.Content = sanitizeVisible(message.Content)
			if message.Role == "" || message.Content == "" || len(message.Content) > maxMessageBytes {
				if len(message.Content) > maxMessageBytes {
					conversation.Warnings = append(conversation.Warnings, fmt.Sprintf("line %d message is too large", line))
				}
				continue
			}
			message.Fingerprint = fingerprint(source, conversation.ID, message)
			duplicateKey := message.Role + "\x00" + message.Timestamp.UTC().Format(time.RFC3339Nano) + "\x00" + message.Content
			if seen[duplicateKey] {
				continue
			}
			seen[duplicateKey] = true
			conversation.Messages = append(conversation.Messages, message)
			if message.Timestamp.After(conversation.UpdatedAt) {
				conversation.UpdatedAt = message.Timestamp
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return Conversation{}, fmt.Errorf("read source conversation: %w", err)
	}
	if conversation.ID == "" {
		conversation.ID = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	// Recompute now that a late metadata record may have supplied the session id.
	for index := range conversation.Messages {
		conversation.Messages[index].Fingerprint = fingerprint(source, conversation.ID, conversation.Messages[index])
	}
	if source == SourceCodex {
		conversation.Messages = preferCodexVisibleEvents(conversation.Messages)
	}
	conversation.MessageCount = len(conversation.Messages)
	conversation.Title = conversationTitle(conversation.Messages)
	return conversation, nil
}

func consumeClaude(conversation *Conversation, record map[string]any) []Message {
	typeName := stringValue(record["type"])
	if id := stringValue(record["sessionId"]); id != "" {
		conversation.ID = id
	}
	if cwd := stringValue(record["cwd"]); cwd != "" {
		conversation.WorkspaceRoot = cwd
	}
	if typeName != "user" && typeName != "assistant" {
		return nil
	}
	message, _ := record["message"].(map[string]any)
	role := stringValue(message["role"])
	if role == "" {
		role = typeName
	}
	if role != "user" && role != "assistant" {
		return nil
	}
	return []Message{{ID: stringValue(record["uuid"]), Role: role, Content: visibleContent(message["content"]), Timestamp: timeValue(record["timestamp"])}}
}

func consumeCodex(conversation *Conversation, record map[string]any) []Message {
	typeName := stringValue(record["type"])
	payload, _ := record["payload"].(map[string]any)
	if typeName == "session_meta" {
		conversation.ID = firstString(payload, "id", "session_id")
		conversation.WorkspaceRoot = stringValue(payload["cwd"])
		if timestamp := timeValue(payload["timestamp"]); timestamp.After(conversation.UpdatedAt) {
			conversation.UpdatedAt = timestamp
		}
		return nil
	}
	if typeName == "event_msg" {
		switch stringValue(payload["type"]) {
		case "user_message":
			return []Message{{ID: "event:user", Role: "user", Content: stringValue(payload["message"]), Timestamp: timeValue(record["timestamp"])}}
		case "agent_message":
			return []Message{{ID: "event:assistant", Role: "assistant", Content: stringValue(payload["message"]), Timestamp: timeValue(record["timestamp"])}}
		}
	}
	if typeName != "response_item" || stringValue(payload["type"]) != "message" {
		return nil
	}
	role := stringValue(payload["role"])
	if role != "user" && role != "assistant" {
		return nil
	}
	return []Message{{ID: "response:" + firstString(payload, "id", "message_id"), Role: role, Content: visibleContent(payload["content"]), Timestamp: timeValue(record["timestamp"])}}
}

func preferCodexVisibleEvents(messages []Message) []Message {
	eventCounts := map[string]int{}
	for _, message := range messages {
		if strings.HasPrefix(message.ID, "event:") {
			eventCounts[message.Role+"\x00"+message.Content]++
		}
	}
	if len(eventCounts) == 0 {
		return messages
	}
	usedEvents := map[string]int{}
	filtered := make([]Message, 0, len(messages))
	for _, message := range messages {
		key := message.Role + "\x00" + message.Content
		if strings.HasPrefix(message.ID, "response:") && eventCounts[key] > usedEvents[key] {
			usedEvents[key]++
			continue
		}
		filtered = append(filtered, message)
	}
	return filtered
}

func visibleContent(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	blocks, ok := value.([]any)
	if !ok {
		return ""
	}
	var text []string
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		typeName := stringValue(block["type"])
		if typeName == "text" || typeName == "input_text" || typeName == "output_text" {
			if value := stringValue(block["text"]); value != "" {
				text = append(text, value)
			}
		}
	}
	return strings.Join(text, "\n")
}

func sanitizeVisible(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\x00", ""))
	for _, tag := range []string{"system-reminder", "local-command-caveat", "local-command-stdout"} {
		for {
			start := strings.Index(value, "<"+tag+">")
			if start < 0 {
				break
			}
			endTag := "</" + tag + ">"
			end := strings.Index(value[start:], endTag)
			if end < 0 {
				value = strings.TrimSpace(value[:start])
				break
			}
			value = strings.TrimSpace(value[:start] + value[start+end+len(endTag):])
		}
	}
	return value
}

func conversationTitle(messages []Message) string {
	for _, message := range messages {
		if message.Role != "user" {
			continue
		}
		value := strings.Join(strings.Fields(message.Content), " ")
		if len([]rune(value)) > 72 {
			value = string([]rune(value)[:69]) + "..."
		}
		return value
	}
	return "Imported conversation"
}

func fingerprint(source Source, conversationID string, message Message) string {
	input := strings.Join([]string{string(source), conversationID, message.ID, message.Role, message.Timestamp.UTC().Format(time.RFC3339Nano), message.Content}, "\x00")
	sum := sha256.Sum256([]byte(input))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func containedRegularFile(root, path string) (string, error) {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve source root: %w", err)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve source conversation: %w", err)
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("source conversation is outside the selected history root")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("source conversation is not a regular file")
	}
	return path, nil
}

func samePath(left, right string) bool {
	l, errL := filepath.Abs(left)
	r, errR := filepath.Abs(right)
	return errL == nil && errR == nil && filepath.Clean(l) == filepath.Clean(r)
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func firstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := stringValue(values[key]); value != "" {
			return value
		}
	}
	return ""
}

func timeValue(value any) time.Time {
	text, _ := value.(string)
	timestamp, _ := time.Parse(time.RFC3339Nano, text)
	return timestamp
}
