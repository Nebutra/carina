package daemon

import (
	"errors"
	"strings"
	"testing"
)

func TestDecodeClaudeCLIOutputPreservesStructuredErrorOnNonZeroExit(t *testing.T) {
	out := []byte(`{"type":"result","subtype":"success","is_error":true,"api_error_status":null,"result":"Not logged in \u00b7 Please run /login"}`)
	_, err := decodeClaudeCLIOutput(out, nil, errors.New("exit status 1"), "")
	if err == nil || !strings.Contains(err.Error(), "Not logged in") {
		t.Fatalf("error = %v, want actionable Claude CLI message", err)
	}

	info := classifyProviderError(err)
	if info.Code != "provider_authentication_failed" || info.Category != "authentication" || info.Provider != "anthropic" || info.Retryable || info.UserAction == "" {
		t.Fatalf("classification = %+v", info)
	}
}

func TestDecodeClaudeCLIOutputClassifiesAPIStatus(t *testing.T) {
	out := []byte(`{"type":"result","subtype":"error","is_error":true,"api_error_status":429,"result":"request failed"}`)
	_, err := decodeClaudeCLIOutput(out, nil, errors.New("exit status 1"), "")
	info := classifyProviderError(err)
	if info.Code != "provider_rate_limited" || info.Category != "rate_limit" || !info.Retryable || info.HTTPStatus != 429 {
		t.Fatalf("classification = %+v", info)
	}
}

func TestDecodeClaudeCLIOutputUsesBoundedStderrFallback(t *testing.T) {
	stderr := []byte("  Not logged in\nPlease run /login  ")
	_, err := decodeClaudeCLIOutput(nil, stderr, errors.New("exit status 1"), "")
	if err == nil || err.Error() != "claude reasoner: Not logged in Please run /login" {
		t.Fatalf("error = %v", err)
	}
	if info := classifyProviderError(err); info.Category != "authentication" {
		t.Fatalf("classification = %+v", info)
	}
}

func TestDecodeClaudeCLIOutputReturnsUsageOnSuccess(t *testing.T) {
	out := []byte(`{"type":"result","subtype":"success","is_error":false,"result":"{\"tool\":\"done\"}","usage":{"input_tokens":12,"output_tokens":3,"cache_creation_input_tokens":4,"cache_read_input_tokens":5}}`)
	result, err := decodeClaudeCLIOutput(out, nil, nil, "sonnet")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Text != `{"tool":"done"}` || result.Usage.Provider != "anthropic" || result.Usage.Model != "sonnet" || result.Usage.InputTokens != 12 || result.Usage.OutputTokens != 3 || result.Usage.CacheWriteTokens != 4 || result.Usage.CacheReadTokens != 5 {
		t.Fatalf("result = %+v", result)
	}
}
