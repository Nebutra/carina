package markdown

import (
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"

	"github.com/Nebutra/carina/go/tui/theme"
)

// Highlighting guardrails (per the rich-text plan, milestone P2): a pathological
// block falls back to plain RoleCodeBlock instead of burning render time.
const (
	highlightMaxBytes = 512 << 10
	highlightMaxLines = 10000
)

// chromaRole is the one place where chroma token categories map to semantic
// theme roles. Everything unmapped renders as plain RoleCodeBlock, so a lexer
// can never introduce a color outside the design-token system.
func chromaRole(t chroma.TokenType) (theme.Role, bool) {
	switch {
	case t == chroma.KeywordType:
		return theme.RoleSyntaxType, true
	case t.InCategory(chroma.Keyword):
		return theme.RoleSyntaxKeyword, true
	case t.InCategory(chroma.Comment):
		return theme.RoleSyntaxComment, true
	case t.InSubCategory(chroma.LiteralString):
		return theme.RoleSyntaxString, true
	case t.InSubCategory(chroma.LiteralNumber):
		return theme.RoleSyntaxNumber, true
	case t.InSubCategory(chroma.NameFunction):
		return theme.RoleSyntaxFunction, true
	case t == chroma.NameClass, t.InSubCategory(chroma.NameBuiltin), t == chroma.NameNamespace:
		return theme.RoleSyntaxType, true
	default:
		return 0, false
	}
}

// highlightEligible applies the size guardrails; the language check is
// separate so tests can pin the budget decision in isolation.
func highlightEligible(code string) bool {
	return len(code) <= highlightMaxBytes && strings.Count(code, "\n") < highlightMaxLines
}

// highlightLines tokenizes a fenced code block into per-source-line token
// runs. Any miss — empty or unknown language, guardrail overflow, lexer
// error — returns ok=false and the caller renders plain RoleCodeBlock.
// Deterministic: chroma lexers are pure functions of the source text.
func highlightLines(code, lang string) ([][]chroma.Token, bool) {
	if lang == "" || !highlightEligible(code) {
		return nil, false
	}
	lexer := lexers.Get(lang)
	if lexer == nil {
		return nil, false
	}
	it, err := chroma.Coalesce(lexer).Tokenise(nil, code)
	if err != nil {
		return nil, false
	}
	return chroma.SplitTokensIntoLines(it.Tokens()), true
}
