package daemon

import (
	"regexp"
	"strings"
	"unicode"
)

// defaultInteractiveAgent is the operator-facing default when the client does
// not name an agent. Build remains available via an explicit agent field
// (TUI /agent, gateway carina/build, plan approval).
const defaultInteractiveAgent = "converse"

var repoPathLike = regexp.MustCompile(`(?i)(?:^|[\s'"(\[{])(?:[\w.-]+/)+[\w.-]+|[\w.-]+\.(?:go|rs|zig|ts|tsx|js|jsx|py|md|toml|json|yml|yaml|c|h|cc|cpp|java|kt|swift)(?:$|[\s'")\]},:])`)

var repoWorkKeywords = []string{
	"implement", "implementation", "refactor", "patch", "debug", "compile",
	"codebase", "workspace", "source code", "unit test", "fix bug", "fix the",
	"edit the", "edit file", "search the", "grep", "read file", "open file",
	"list files", "repository", "this repo", "the repo", "crate",
	"cargo test", "go test", "make test", "bug in", "failing test",
	"search", "fix", "edit",
	"实现", "修复", "重构", "改代码", "看看代码", "搜一下",
	"创建", "新建", "写一个", "写一份",
}

var conversationalChatter = map[string]bool{
	"hi": true, "hii": true, "hello": true, "hey": true, "yo": true, "sup": true,
	"thanks": true, "thank you": true, "thx": true, "ok": true, "okay": true,
	"ping": true, "help": true, "你好": true, "嗨": true, "在吗": true, "谢谢": true,
	"早上好": true, "晚上好": true, "你好啊": true, "who are you": true,
	"what can you do": true, "what do you do": true,
}

func shouldLoadProjectInstructions(agent, userPrompt string) bool {
	switch strings.TrimSpace(agent) {
	case "explore":
		return false
	case "build", "plan":
		return true
	default:
		return looksLikeRepoWork(userPrompt)
	}
}

func looksLikeRepoWork(prompt string) bool {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" || isConversationalChatter(trimmed) {
		return false
	}
	if repoPathLike.MatchString(trimmed) {
		return true
	}
	lower := strings.ToLower(trimmed)
	for _, kw := range repoWorkKeywords {
		if containsWord(lower, kw) {
			return true
		}
	}
	return false
}

func isConversationalChatter(prompt string) bool {
	p := strings.ToLower(strings.TrimSpace(prompt))
	p = strings.TrimRight(p, ".!?。！？…")
	p = strings.TrimSpace(p)
	return conversationalChatter[p]
}

func containsWord(lower, word string) bool {
	if word == "" {
		return false
	}
	for _, r := range word {
		if r > unicode.MaxASCII {
			return strings.Contains(lower, word)
		}
	}
	start := 0
	for {
		i := strings.Index(lower[start:], word)
		if i < 0 {
			return false
		}
		i += start
		leftOK := i == 0 || !isASCIIWordByte(lower[i-1])
		right := i + len(word)
		rightOK := right == len(lower) || !isASCIIWordByte(lower[right])
		if leftOK && rightOK {
			return true
		}
		start = i + 1
	}
}

func isASCIIWordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '_'
}
