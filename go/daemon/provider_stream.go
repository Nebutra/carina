package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Nebutra/carina/go/auth"
	modelrouter "github.com/Nebutra/carina/go/model-router"
)

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
		return fmt.Errorf("%s: read event stream: %w", provider, err)
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
	err = consumeProviderSSE(o.errorName(), response.Body, func(event providerSSEEvent) error {
		if bytes.Equal(bytes.TrimSpace(event.data), []byte("[DONE]")) {
			return nil
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content json.RawMessage `json:"content"`
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
	if text.Len() == 0 {
		return nil, fmt.Errorf("%s: empty response", o.id)
	}
	cachedTokens = clampCachedTokens(cachedTokens, promptTokens)
	return &modelrouter.Response{
		Provider: o.Name(), Model: responseModel, Text: text.String(),
		InputTokens: promptTokens - cachedTokens, OutputTokens: outputTokens,
		CacheReadTokens: cachedTokens, EffectiveReasoningEffort: effectiveEffort,
	}, nil
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
	err = consumeProviderSSE(o.errorName(), response.Body, func(event providerSSEEvent) error {
		if bytes.Equal(bytes.TrimSpace(event.data), []byte("[DONE]")) {
			return nil
		}
		var chunk struct {
			Type     string `json:"type"`
			Delta    string `json:"delta"`
			Error    any    `json:"error"`
			Response struct {
				OutputText string `json:"output_text"`
				Usage      struct {
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
		case "response.completed":
			inputTokens = chunk.Response.Usage.InputTokens
			outputTokens = chunk.Response.Usage.OutputTokens
			cachedTokens = chunk.Response.Usage.InputDetails.CachedTokens
			if text.Len() == 0 && chunk.Response.OutputText != "" {
				text.WriteString(chunk.Response.OutputText)
				emitProviderDelta(callback, chunk.Response.OutputText)
			}
		case "response.failed", "error":
			return providerResponseError{provider: o.errorName(), status: response.StatusCode, contentType: "text/event-stream", kind: "responses stream failed"}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if text.Len() == 0 {
		return nil, fmt.Errorf("%s: empty response", o.id)
	}
	cachedTokens = clampCachedTokens(cachedTokens, inputTokens)
	return &modelrouter.Response{
		Provider: o.Name(), Model: responseModel, Text: text.String(),
		InputTokens: inputTokens - cachedTokens, OutputTokens: outputTokens,
		CacheReadTokens: cachedTokens, EffectiveReasoningEffort: effectiveEffort,
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
	response, err := o.httpClient().Do(request)
	if err != nil {
		return nil, fmt.Errorf("%s: request: %w", o.errorName(), err)
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
	response, err := a.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%s: request: %w", a.errorName(), err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, statusError(a.errorName(), response)
	}

	var text strings.Builder
	var inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens int
	err = consumeProviderSSE(a.errorName(), response.Body, func(event providerSSEEvent) error {
		var chunk struct {
			Type    string `json:"type"`
			Message struct {
				Usage struct {
					InputTokens         int `json:"input_tokens"`
					OutputTokens        int `json:"output_tokens"`
					CacheCreationTokens int `json:"cache_creation_input_tokens"`
					CacheReadTokens     int `json:"cache_read_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
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
		case "content_block_delta":
			if chunk.Delta.Type == "text_delta" {
				text.WriteString(chunk.Delta.Text)
				emitProviderDelta(callback, chunk.Delta.Text)
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
	if text.Len() == 0 {
		return nil, fmt.Errorf("%s: empty response", a.id)
	}
	return &modelrouter.Response{
		Provider: a.Name(), Model: responseModel, Text: text.String(),
		InputTokens: inputTokens, OutputTokens: outputTokens,
		CacheReadTokens: cacheReadTokens, CacheWriteTokens: cacheWriteTokens,
		EffectiveReasoningEffort: effectiveEffort,
	}, nil
}
