package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Nebutra/carina/go/auth"
	modelrouter "github.com/Nebutra/carina/go/model-router"
)

// errProviderStreamIdle is returned when no SSE body bytes arrive for
// providerStreamIdleTimeout. It is not automatically retryable.
var errProviderStreamIdle = errors.New("provider stream idle timeout")

// providerStreamError is a classified streaming failure. Body/idle budget
// exhaustion is intentionally non-retryable so agent runs do not re-pay
// side effects; operators still have explicit execution.retry.
type providerStreamError struct {
	provider string
	phase    string // request | idle | body
	err      error
}

func (e providerStreamError) Error() string {
	switch e.phase {
	case "idle":
		return fmt.Sprintf(
			"%s: model stream stalled (no events for %s). Not auto-retried. Check the proxy/network, then retry explicitly if needed",
			e.provider, providerStreamIdleTimeout,
		)
	case "body":
		return fmt.Sprintf(
			"%s: model stream stopped while reading events. Not auto-retried. Check the proxy/network, then retry explicitly if needed",
			e.provider,
		)
	default:
		return fmt.Sprintf("%s: model stream request failed: %v", e.provider, e.err)
	}
}

func (e providerStreamError) Unwrap() error { return e.err }

func (e providerStreamError) ProviderError() providerErrorInfo {
	info := providerErrorInfo{
		Code:       "provider_stream_failed",
		Category:   "unavailable",
		Provider:   e.provider,
		Retryable:  false,
		UserAction: "check the model proxy and network, then retry explicitly",
	}
	switch e.phase {
	case "idle", "body":
		info.Code = "provider_stream_budget_exceeded"
		info.Category = "timeout"
		info.Retryable = false
	case "request":
		// First-byte / dial failures may be transient; still prefer not to
		// auto-retry multi-turn agent runs with prior tool effects. Header
		// timeouts stay non-retryable at reasoner level unless transport
		// classification promotes them (see classifyProviderError).
		if isTransientStreamRequestError(e.err) {
			info.Code = "provider_stream_unavailable"
			info.Category = "unavailable"
			info.Retryable = true
			info.UserAction = "wait briefly or choose another provider"
		} else {
			info.Code = "provider_stream_request_failed"
			info.Category = "unavailable"
			info.Retryable = false
		}
	}
	return info
}

func isTransientStreamRequestError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		// ResponseHeaderTimeout is a timeout but usually means upstream
		// hung before first byte — one bounded auto-retry is reasonable.
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"connection reset", "connection refused", "broken pipe", "unexpected eof",
		"i/o timeout", "temporarily unavailable", "bad gateway", "service unavailable",
		"gateway timeout",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	// Client.Timeout while reading body must never look transient.
	if strings.Contains(msg, "while reading body") || strings.Contains(msg, "client.timeout") {
		return false
	}
	return false
}

func wrapProviderStreamRequestError(provider string, err error) error {
	if err == nil {
		return nil
	}
	return providerStreamError{provider: provider, phase: "request", err: err}
}

func wrapProviderStreamBodyError(provider string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, errProviderStreamIdle) {
		return providerStreamError{provider: provider, phase: "idle", err: err}
	}
	// Legacy Client.Timeout wrapping / net body deadline.
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "while reading body") ||
		strings.Contains(msg, "client.timeout") ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, errProviderStreamIdle) {
		return providerStreamError{provider: provider, phase: "body", err: err}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return providerStreamError{provider: provider, phase: "body", err: err}
	}
	// Malformed SSE / response errors pass through for their own classifiers.
	var classified providerErrorClassifier
	if errors.As(err, &classified) {
		return err
	}
	return fmt.Errorf("%s: read event stream: %w", provider, err)
}

// idleTimeoutReader aborts Read when no bytes arrive within idle.
type idleTimeoutReader struct {
	r    io.Reader
	idle time.Duration
}

func (r idleTimeoutReader) Read(p []byte) (int, error) {
	if r.idle <= 0 {
		return r.r.Read(p)
	}
	type outcome struct {
		n   int
		err error
	}
	ch := make(chan outcome, 1)
	go func() {
		n, err := r.r.Read(p)
		ch <- outcome{n: n, err: err}
	}()
	timer := time.NewTimer(r.idle)
	defer timer.Stop()
	select {
	case out := <-ch:
		return out.n, out.err
	case <-timer.C:
		return 0, errProviderStreamIdle
	}
}

func streamBodyReader(body io.Reader) io.Reader {
	return idleTimeoutReader{r: body, idle: providerStreamIdleTimeout}
}

type providerSSEEvent struct {
	name string
	data []byte
}

func consumeProviderSSE(provider string, reader io.Reader, consume func(providerSSEEvent) error) error {
	limited := &io.LimitedReader{R: reader, N: maxProviderResponseBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 64<<10), maxProviderResponseBytes)
	var name string
	var data []string
	dispatch := func() error {
		if len(data) == 0 {
			name = ""
			return nil
		}
		event := providerSSEEvent{name: name, data: []byte(strings.Join(data, "\n"))}
		name, data = "", nil
		return consume(event)
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			name = value
		case "data":
			data = append(data, value)
		}
	}
	if err := scanner.Err(); err != nil {
		return wrapProviderStreamBodyError(provider, err)
	}
	if limited.N == 0 {
		return providerResponseError{provider: provider, status: http.StatusOK, contentType: "text/event-stream", kind: "response too large"}
	}
	return dispatch()
}

func emitProviderDelta(callback modelrouter.StreamCallback, delta string) {
	if callback != nil && delta != "" {
		callback(modelrouter.StreamEvent{Delta: delta})
	}
}

func (o *openAIProvider) Stream(ctx context.Context, req modelrouter.Request, callback modelrouter.StreamCallback) (*modelrouter.Response, error) {
	if !o.responses {
		return o.completeChatStream(ctx, req, callback)
	}
	response, err := o.completeResponsesStream(ctx, req, callback)
	if err == nil || !responsesEndpointUnsupported(err) {
		return response, err
	}
	if callback != nil {
		callback(modelrouter.StreamEvent{Reset: true})
	}
	response, chatErr := o.completeChatStream(ctx, req, callback)
	if chatErr != nil {
		return nil, fmt.Errorf("openai-compatible chat fallback: %w", chatErr)
	}
	return response, nil
}

func (o *openAIProvider) completeChatStream(ctx context.Context, req modelrouter.Request, callback modelrouter.StreamCallback) (*modelrouter.Response, error) {
	cred, hasCred, err := o.credential()
	if err != nil {
		return nil, err
	}
	model, responseModel, override := o.resolveModel(req)
	content := any(req.Prompt)
	if len(req.Media) > 0 {
		parts := []map[string]any{{"type": "text", "text": req.Prompt}}
		for _, media := range req.Media {
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": mediaDataURI(media)}})
		}
		content = parts
	}
	bodyMap := map[string]any{
		"model": model, "max_tokens": agentMaxOutputTokens,
		"messages": []map[string]any{{"role": "user", "content": content}},
	}
	mergeRawBody(bodyMap, o.body)
	mergeRawBody(bodyMap, override.Body)
	attachOpenAITools(bodyMap, req.Tools)
	bodyMap["stream"] = true
	if _, exists := bodyMap["stream_options"]; !exists {
		bodyMap["stream_options"] = map[string]any{"include_usage": true}
	}
	effectiveEffort, err := applyNativeReasoningEffort(o.id, model, req.ReasoningEffort, bodyMap)
	if err != nil {
		return nil, err
	}
	response, err := o.openAIStreamRequest(ctx, "chat/completions", bodyMap, override, cred.Value, hasCred)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, statusError(o.errorName(), response)
	}

	var text strings.Builder
	var promptTokens, outputTokens, cachedTokens int
	acc := map[int]*openAIToolCallAcc{}
	err = consumeProviderSSE(o.errorName(), streamBodyReader(response.Body), func(event providerSSEEvent) error {
		if bytes.Equal(bytes.TrimSpace(event.data), []byte("[DONE]")) {
			return nil
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   json.RawMessage `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				PromptDetails    struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(event.data, &chunk); err != nil {
			return providerResponseError{provider: o.errorName(), status: response.StatusCode, contentType: "text/event-stream", kind: "malformed chat event"}
		}
		for _, choice := range chunk.Choices {
			delta := textFromRaw(choice.Delta.Content)
			text.WriteString(delta)
			emitProviderDelta(callback, delta)
			for _, call := range choice.Delta.ToolCalls {
				entry := acc[call.Index]
				if entry == nil {
					entry = &openAIToolCallAcc{}
					acc[call.Index] = entry
				}
				if call.ID != "" {
					entry.id = call.ID
				}
				if call.Function.Name != "" {
					entry.name = call.Function.Name
				}
				entry.args.WriteString(call.Function.Arguments)
			}
		}
		if chunk.Usage.PromptTokens != 0 || chunk.Usage.CompletionTokens != 0 {
			promptTokens = chunk.Usage.PromptTokens
			outputTokens = chunk.Usage.CompletionTokens
			cachedTokens = chunk.Usage.PromptDetails.CachedTokens
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	calls := finishOpenAIToolCallAcc(acc)
	if text.Len() == 0 && len(calls) == 0 {
		return nil, fmt.Errorf("%s: empty response", o.id)
	}
	cachedTokens = clampCachedTokens(cachedTokens, promptTokens)
	return &modelrouter.Response{
		Provider: o.Name(), Model: responseModel, Text: text.String(),
		InputTokens: promptTokens - cachedTokens, OutputTokens: outputTokens,
		CacheReadTokens: cachedTokens, EffectiveReasoningEffort: effectiveEffort,
		ToolCalls: calls,
	}, nil
}

type openAIToolCallAcc struct {
	id, name string
	args     strings.Builder
}

func finishOpenAIToolCallAcc(acc map[int]*openAIToolCallAcc) []modelrouter.ToolCall {
	if len(acc) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(acc))
	for index := range acc {
		indexes = append(indexes, index)
	}
	slices.Sort(indexes)
	calls := make([]modelrouter.ToolCall, 0, len(indexes))
	for _, index := range indexes {
		entry := acc[index]
		if entry.name == "" {
			continue
		}
		calls = append(calls, modelrouter.ToolCall{
			ID: entry.id, Name: entry.name, Arguments: encodeJSONArguments(entry.args.String()),
		})
	}
	return calls
}

func (o *openAIProvider) completeResponsesStream(ctx context.Context, req modelrouter.Request, callback modelrouter.StreamCallback) (*modelrouter.Response, error) {
	cred, hasCred, err := o.credential()
	if err != nil {
		return nil, err
	}
	model, responseModel, override := o.resolveModel(req)
	input := any(req.Prompt)
	if len(req.Media) > 0 {
		parts := []map[string]any{{"type": "input_text", "text": req.Prompt}}
		for _, media := range req.Media {
			parts = append(parts, map[string]any{"type": "input_image", "image_url": mediaDataURI(media)})
		}
		input = []map[string]any{{"role": "user", "content": parts}}
	}
	bodyMap := map[string]any{"model": model, "input": input, "max_output_tokens": agentMaxOutputTokens}
	mergeRawBody(bodyMap, o.body)
	attachResponsesTools(bodyMap, req.Tools)
	mergeRawBody(bodyMap, override.Body)
	bodyMap["stream"] = true
	effectiveEffort, err := applyNativeReasoningEffort(o.id, model, req.ReasoningEffort, bodyMap)
	if err != nil {
		return nil, err
	}
	response, err := o.openAIStreamRequest(ctx, "responses", bodyMap, override, cred.Value, hasCred)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return nil, statusError(o.errorName(), response)
	}

	var text strings.Builder
	var inputTokens, outputTokens, cachedTokens int
	acc := map[int]*openAIToolCallAcc{}
	var completedCalls []modelrouter.ToolCall
	err = consumeProviderSSE(o.errorName(), streamBodyReader(response.Body), func(event providerSSEEvent) error {
		if bytes.Equal(bytes.TrimSpace(event.data), []byte("[DONE]")) {
			return nil
		}
		var chunk struct {
			Type        string `json:"type"`
			Delta       string `json:"delta"`
			OutputIndex int    `json:"output_index"`
			Error       any    `json:"error"`
			Arguments   any    `json:"arguments"`
			Item        struct {
				Type      string `json:"type"`
				ID        string `json:"id"`
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments any    `json:"arguments"`
			} `json:"item"`
			Response struct {
				OutputText string `json:"output_text"`
				Output     []struct {
					Type      string `json:"type"`
					Name      string `json:"name"`
					CallID    string `json:"call_id"`
					Arguments any    `json:"arguments"`
				} `json:"output"`
				Usage struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
					InputDetails struct {
						CachedTokens int `json:"cached_tokens"`
					} `json:"input_tokens_details"`
				} `json:"usage"`
			} `json:"response"`
		}
		if err := json.Unmarshal(event.data, &chunk); err != nil {
			return providerResponseError{provider: o.errorName(), status: response.StatusCode, contentType: "text/event-stream", kind: "malformed responses event"}
		}
		kind := chunk.Type
		if kind == "" {
			kind = event.name
		}
		switch kind {
		case "response.output_text.delta":
			text.WriteString(chunk.Delta)
			emitProviderDelta(callback, chunk.Delta)
		case "response.output_item.added":
			if chunk.Item.Type == "function_call" && chunk.Item.Name != "" {
				entry := &openAIToolCallAcc{id: nonempty(chunk.Item.CallID, chunk.Item.ID), name: chunk.Item.Name}
				if args := strings.TrimSpace(string(encodeJSONArguments(chunk.Item.Arguments))); args != "" && args != "{}" && args != "null" {
					entry.args.WriteString(args)
				}
				acc[chunk.OutputIndex] = entry
			}
		case "response.function_call_arguments.delta":
			entry := acc[chunk.OutputIndex]
			if entry == nil {
				entry = &openAIToolCallAcc{}
				acc[chunk.OutputIndex] = entry
			}
			entry.args.WriteString(chunk.Delta)
		case "response.function_call_arguments.done":
			entry := acc[chunk.OutputIndex]
			if entry == nil {
				entry = &openAIToolCallAcc{}
				acc[chunk.OutputIndex] = entry
			}
			if args := strings.TrimSpace(string(encodeJSONArguments(chunk.Arguments))); args != "" && args != "{}" && args != "null" {
				entry.args.Reset()
				entry.args.WriteString(args)
			}
		case "response.completed":
			inputTokens = chunk.Response.Usage.InputTokens
			outputTokens = chunk.Response.Usage.OutputTokens
			cachedTokens = chunk.Response.Usage.InputDetails.CachedTokens
			if text.Len() == 0 && chunk.Response.OutputText != "" {
				text.WriteString(chunk.Response.OutputText)
				emitProviderDelta(callback, chunk.Response.OutputText)
			}
			var fromCompleted []modelrouter.ToolCall
			for _, item := range chunk.Response.Output {
				if item.Type == "function_call" && item.Name != "" {
					fromCompleted = append(fromCompleted, modelrouter.ToolCall{
						ID: item.CallID, Name: item.Name, Arguments: encodeJSONArguments(item.Arguments),
					})
				}
			}
			if len(fromCompleted) > 0 {
				completedCalls = fromCompleted
			}
		case "response.failed", "error":
			return providerResponseError{provider: o.errorName(), status: response.StatusCode, contentType: "text/event-stream", kind: "responses stream failed"}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	calls := completedCalls
	if len(calls) == 0 {
		calls = finishOpenAIToolCallAcc(acc)
	}
	if text.Len() == 0 && len(calls) == 0 {
		return nil, fmt.Errorf("%s: empty response", o.id)
	}
	cachedTokens = clampCachedTokens(cachedTokens, inputTokens)
	return &modelrouter.Response{
		Provider: o.Name(), Model: responseModel, Text: text.String(),
		InputTokens: inputTokens - cachedTokens, OutputTokens: outputTokens,
		CacheReadTokens: cachedTokens, EffectiveReasoningEffort: effectiveEffort,
		ToolCalls: calls,
	}, nil
}

func (o *openAIProvider) openAIStreamRequest(ctx context.Context, path string, bodyMap map[string]any, override requestOverride, credential string, hasCredential bool) (*http.Response, error) {
	body, _ := json.Marshal(bodyMap)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint(path), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("content-type", "application/json")
	request.Header.Set("accept", "text/event-stream")
	if hasCredential {
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	o.applyExtraHeaders(request.Header)
	applyHeaders(request.Header, override.Headers)
	response, err := o.streamHTTPClient().Do(request)
	if err != nil {
		return nil, wrapProviderStreamRequestError(o.errorName(), err)
	}
	return response, nil
}

func (a *anthropicProvider) Stream(ctx context.Context, req modelrouter.Request, callback modelrouter.StreamCallback) (*modelrouter.Response, error) {
	cred, ok := a.auth.Resolve()
	if !ok {
		return nil, fmt.Errorf("%s: credential not set", a.errorName())
	}
	if cred.Kind != auth.APIKey && cred.Kind != auth.Bearer && cred.Kind != auth.OAuth {
		return nil, fmt.Errorf("%s: supported credential not set", a.errorName())
	}
	model, responseModel, override := a.resolveModel(req)
	messages := any([]map[string]string{{"role": "user", "content": req.Prompt}})
	if req.StablePrefix != "" || len(req.Media) > 0 {
		blocks := make([]map[string]any, 0, 2+len(req.Media))
		if req.StablePrefix != "" {
			blocks = append(blocks,
				map[string]any{"type": "text", "text": req.StablePrefix, "cache_control": map[string]string{"type": "ephemeral"}},
				map[string]any{"type": "text", "text": req.VolatileSuffix})
		} else {
			blocks = append(blocks, map[string]any{"type": "text", "text": req.Prompt})
		}
		blocks = append(blocks, anthropicImageBlocks(req.Media)...)
		messages = []map[string]any{{"role": "user", "content": blocks}}
	}
	bodyMap := map[string]any{"model": model, "max_tokens": agentMaxOutputTokens, "messages": messages, "stream": true}
	mergeRawBody(bodyMap, a.body)
	mergeRawBody(bodyMap, override.Body)
	attachAnthropicTools(bodyMap, req.Tools)
	bodyMap["stream"] = true
	effectiveEffort, err := validateReasoningEffort(nativeReasoningEffortSpec(a.id, model), req.ReasoningEffort)
	if err != nil {
		return nil, fmt.Errorf("%s/%s: %w", a.errorName(), model, err)
	}
	if effectiveEffort != "" {
		bodyMap["thinking"] = map[string]any{"type": "adaptive"}
		bodyMap["output_config"] = map[string]any{"effort": effectiveEffort}
	}
	body, _ := json.Marshal(bodyMap)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicEndpoint(a.baseURL, "messages"), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("content-type", "application/json")
	request.Header.Set("accept", "text/event-stream")
	cred.Apply(request.Header)
	request.Header.Set("anthropic-version", "2023-06-01")
	applyHeaders(request.Header, a.headers)
	applyHeaders(request.Header, override.Headers)
	response, err := streamHTTPClientOr(a.client).Do(request)
	if err != nil {
		return nil, wrapProviderStreamRequestError(a.errorName(), err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, statusError(a.errorName(), response)
	}

	var text strings.Builder
	var inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens int
	var anthropicAcc []openAIToolCallAcc
	currentCall := -1
	err = consumeProviderSSE(a.errorName(), streamBodyReader(response.Body), func(event providerSSEEvent) error {
		var chunk struct {
			Type    string `json:"type"`
			Index   int    `json:"index"`
			Message struct {
				Usage struct {
					InputTokens         int `json:"input_tokens"`
					OutputTokens        int `json:"output_tokens"`
					CacheCreationTokens int `json:"cache_creation_input_tokens"`
					CacheReadTokens     int `json:"cache_read_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
			ContentBlock struct {
				Type  string         `json:"type"`
				ID    string         `json:"id"`
				Name  string         `json:"name"`
				Input map[string]any `json:"input"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
			Usage struct {
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(event.data, &chunk); err != nil {
			return providerResponseError{provider: a.errorName(), status: response.StatusCode, contentType: "text/event-stream", kind: "malformed messages event"}
		}
		kind := chunk.Type
		if kind == "" {
			kind = event.name
		}
		switch kind {
		case "message_start":
			inputTokens = chunk.Message.Usage.InputTokens
			outputTokens = chunk.Message.Usage.OutputTokens
			cacheReadTokens = chunk.Message.Usage.CacheReadTokens
			cacheWriteTokens = chunk.Message.Usage.CacheCreationTokens
		case "content_block_start":
			currentCall = -1
			if chunk.ContentBlock.Type == "tool_use" && chunk.ContentBlock.Name != "" {
				entry := openAIToolCallAcc{id: chunk.ContentBlock.ID, name: chunk.ContentBlock.Name}
				if len(chunk.ContentBlock.Input) > 0 {
					entry.args.WriteString(string(encodeJSONArguments(chunk.ContentBlock.Input)))
				}
				anthropicAcc = append(anthropicAcc, entry)
				currentCall = len(anthropicAcc) - 1
			}
		case "content_block_delta":
			if chunk.Delta.Type == "text_delta" {
				text.WriteString(chunk.Delta.Text)
				emitProviderDelta(callback, chunk.Delta.Text)
			}
			if chunk.Delta.Type == "input_json_delta" && currentCall >= 0 && currentCall < len(anthropicAcc) {
				anthropicAcc[currentCall].args.WriteString(chunk.Delta.PartialJSON)
			}
		case "message_delta":
			if chunk.Usage.OutputTokens != 0 {
				outputTokens = chunk.Usage.OutputTokens
			}
		case "error":
			return providerResponseError{provider: a.errorName(), status: response.StatusCode, contentType: "text/event-stream", kind: "messages stream failed"}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	anthropicCalls := make([]modelrouter.ToolCall, 0, len(anthropicAcc))
	for _, entry := range anthropicAcc {
		if entry.name == "" {
			continue
		}
		anthropicCalls = append(anthropicCalls, modelrouter.ToolCall{
			ID: entry.id, Name: entry.name, Arguments: encodeJSONArguments(entry.args.String()),
		})
	}
	if text.Len() == 0 && len(anthropicCalls) == 0 {
		return nil, fmt.Errorf("%s: empty response", a.errorName())
	}
	return &modelrouter.Response{
		Provider: a.Name(), Model: responseModel, Text: text.String(),
		InputTokens: inputTokens, OutputTokens: outputTokens,
		CacheReadTokens: cacheReadTokens, CacheWriteTokens: cacheWriteTokens,
		EffectiveReasoningEffort: effectiveEffort,
		ToolCalls:                anthropicCalls,
	}, nil
}
