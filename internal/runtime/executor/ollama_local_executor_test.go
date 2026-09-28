package executor

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func TestConvertOpenAIToOllama_BasicMessageConversion(t *testing.T) {
	input := `{"messages":[{"role":"user","content":"hello"}]}`
	out := convertOpenAIToOllama("llama3", []byte(input), false)

	var body map[string]interface{}
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msgs := body["messages"].([]interface{})
	m := msgs[0].(map[string]interface{})
	if m["role"] != "user" {
		t.Errorf("role = %v, want user", m["role"])
	}
	if m["content"] != "hello" {
		t.Errorf("content = %v, want hello", m["content"])
	}
}

func TestConvertOpenAIToOllama_ContentArrayFlattening(t *testing.T) {
	input := `{"messages":[{"role":"user","content":[
		{"type":"text","text":"line1"},
		{"type":"text","text":"line2"}
	]}]}`
	out := convertOpenAIToOllama("m", []byte(input), false)

	var body map[string]interface{}
	json.Unmarshal(out, &body)
	msgs := body["messages"].([]interface{})
	m := msgs[0].(map[string]interface{})
	got := m["content"].(string)
	if got != "line1\nline2" {
		t.Errorf("content = %q, want %q", got, "line1\nline2")
	}
}

func TestConvertOpenAIToOllama_ImageExtraction(t *testing.T) {
	input := `{"messages":[{"role":"user","content":[
		{"type":"text","text":"describe"},
		{"type":"image_url","image_url":{"url":"data:image/png;base64,AAA"}}
	]}]}`
	out := convertOpenAIToOllama("m", []byte(input), false)

	var body map[string]interface{}
	json.Unmarshal(out, &body)
	msgs := body["messages"].([]interface{})
	m := msgs[0].(map[string]interface{})
	if m["content"] != "describe" {
		t.Errorf("content = %v, want describe", m["content"])
	}
	images := m["images"].([]interface{})
	if len(images) != 1 {
		t.Fatalf("images len = %d, want 1", len(images))
	}
	if images[0].(string) != "AAA" {
		t.Errorf("image = %v, want AAA", images[0])
	}
}

func TestConvertOpenAIToOllama_OptionsMapping(t *testing.T) {
	input := `{"max_tokens":100,"temperature":0.7,"top_p":0.9}`
	out := convertOpenAIToOllama("m", []byte(input), false)

	var body map[string]interface{}
	json.Unmarshal(out, &body)
	opts := body["options"].(map[string]interface{})
	if opts["num_predict"].(float64) != 100 {
		t.Errorf("num_predict = %v, want 100", opts["num_predict"])
	}
	if opts["temperature"].(float64) != 0.7 {
		t.Errorf("temperature = %v, want 0.7", opts["temperature"])
	}
	if opts["top_p"].(float64) != 0.9 {
		t.Errorf("top_p = %v, want 0.9", opts["top_p"])
	}
}

func TestConvertOpenAIToOllama_StopSequences(t *testing.T) {
	input := `{"stop":["END","STOP"]}`
	out := convertOpenAIToOllama("m", []byte(input), false)

	var body map[string]interface{}
	json.Unmarshal(out, &body)
	opts := body["options"].(map[string]interface{})
	stopArr := opts["stop"].([]interface{})
	if len(stopArr) != 2 {
		t.Fatalf("stop len = %d, want 2", len(stopArr))
	}
	if stopArr[0].(string) != "END" {
		t.Errorf("stop[0] = %v, want END", stopArr[0])
	}
}

func TestConvertOpenAIToOllama_ToolsPassthrough(t *testing.T) {
	input := `{"tools":[{"type":"function","function":{"name":"get_weather"}}]}`
	out := convertOpenAIToOllama("m", []byte(input), false)

	var body map[string]interface{}
	json.Unmarshal(out, &body)
	tools := body["tools"].([]interface{})
	if len(tools) != 1 {
		t.Fatalf("tools len = %d, want 1", len(tools))
	}
	tool := tools[0].(map[string]interface{})
	if tool["type"] != "function" {
		t.Errorf("tool type = %v, want function", tool["type"])
	}
}

func TestConvertOpenAIToOllama_StreamFlag(t *testing.T) {
	input := `{}`
	outTrue := convertOpenAIToOllama("m", []byte(input), true)
	outFalse := convertOpenAIToOllama("m", []byte(input), false)

	var bodyTrue, bodyFalse map[string]interface{}
	json.Unmarshal(outTrue, &bodyTrue)
	json.Unmarshal(outFalse, &bodyFalse)

	if bodyTrue["stream"] != true {
		t.Errorf("stream (true case) = %v, want true", bodyTrue["stream"])
	}
	if bodyFalse["stream"] != false {
		t.Errorf("stream (false case) = %v, want false", bodyFalse["stream"])
	}
}

func TestConvertOpenAIToOllama_ModelField(t *testing.T) {
	input := `{}`
	out := convertOpenAIToOllama("mistral", []byte(input), false)

	var body map[string]interface{}
	json.Unmarshal(out, &body)
	if body["model"] != "mistral" {
		t.Errorf("model = %v, want mistral", body["model"])
	}
}

func TestOllamaLocalExecutor_Identifier(t *testing.T) {
	cfg := &config.Config{}
	e := NewOllamaLocalExecutor(cfg)
	if got := e.Identifier(); got != "ollama-local" {
		t.Errorf("Identifier() = %q, want %q", got, "ollama-local")
	}
}

func TestOllamaLocalBaseURL_Default(t *testing.T) {
	got := ollamaLocalBaseURL(nil)
	if got != "http://localhost:11434" {
		t.Errorf("default URL = %q, want %q", got, "http://localhost:11434")
	}
}

func TestOllamaLocalBaseURL_FromAuthAttributes(t *testing.T) {
	a := &auth.Auth{
		Attributes: map[string]string{
			"base_url": "http://custom-host:8080/",
		},
	}
	got := ollamaLocalBaseURL(a)
	if got != "http://custom-host:8080" {
		t.Errorf("URL = %q, want %q", got, "http://custom-host:8080")
	}
}

func TestOllamaLocalBaseURL_EmptyAttribute(t *testing.T) {
	a := &auth.Auth{
		Attributes: map[string]string{
			"base_url": "  ",
		},
	}
	got := ollamaLocalBaseURL(a)
	if got != "http://localhost:11434" {
		t.Errorf("URL = %q, want default %q", got, "http://localhost:11434")
	}
}

func TestFetchOllamaLocalModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"models":[
			{"name":"llama3.2:latest","details":{"family":"llama"}},
			{"name":"qwen2.5:7b","details":{"family":"qwen3"}},
			{"name":"llama3.2:latest","details":{"family":"llama"}},
			{"name":"","details":{}}
		]}`))
	}))
	defer server.Close()

	a := &auth.Auth{Attributes: map[string]string{"base_url": server.URL}}
	models := FetchOllamaLocalModels(context.Background(), a, nil)
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].ID != "llama3.2:latest" || models[1].ID != "qwen2.5:7b" {
		t.Errorf("unexpected model IDs: %q, %q", models[0].ID, models[1].ID)
	}
	for _, m := range models {
		if m.OwnedBy != "ollama-local" || m.Type != "openai" {
			t.Errorf("model %q: owned_by=%q type=%q", m.ID, m.OwnedBy, m.Type)
		}
		if m.Object != "model" || m.DisplayName != m.ID {
			t.Errorf("model %q: object=%q display_name=%q", m.ID, m.Object, m.DisplayName)
		}
	}
}

func TestFetchOllamaLocalModels_ErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer server.Close()

	a := &auth.Auth{Attributes: map[string]string{"base_url": server.URL}}
	if models := FetchOllamaLocalModels(context.Background(), a, nil); models != nil {
		t.Errorf("expected nil models on error status, got %d", len(models))
	}
}

func TestFetchOllamaLocalModels_UnreachableServer(t *testing.T) {
	a := &auth.Auth{Attributes: map[string]string{"base_url": "http://127.0.0.1:1"}}
	if models := FetchOllamaLocalModels(context.Background(), a, nil); models != nil {
		t.Errorf("expected nil models when server unreachable, got %d", len(models))
	}
}

func TestOllamaNDJSONToOpenAIStream_RawJSONFraming(t *testing.T) {
	ndjson := strings.Join([]string{
		`{"model":"m","message":{"role":"assistant","content":"Hi"},"done":false}`,
		`{"model":"m","prompt_eval_count":10,"eval_count":5,"done":true}`,
	}, "\n") + "\n"
	out := make(chan cliproxyexecutor.StreamChunk, 16)
	ollamaNDJSONToOpenAIStream(context.Background(), bufio.NewScanner(strings.NewReader(ndjson)), out)
	close(out)

	var payloads []string
	for chunk := range out {
		payloads = append(payloads, string(chunk.Payload))
	}
	if len(payloads) != 2 {
		t.Fatalf("payloads = %d, want 2 (delta + usage)", len(payloads))
	}
	for i, body := range payloads {
		if strings.HasPrefix(body, "data: ") {
			t.Errorf("payload[%d] has data: prefix; handler adds it: %q", i, body)
		}
		if strings.Contains(body, "[DONE]") {
			t.Errorf("payload[%d] contains [DONE]; handler emits it on channel close: %q", i, body)
		}
		if !strings.HasPrefix(body, "{") {
			t.Errorf("payload[%d] = %q, want raw JSON", i, body)
		}
	}
	usage := gjson.Get(payloads[1], "usage")
	if usage.Get("prompt_tokens").Int() != 10 || usage.Get("completion_tokens").Int() != 5 || usage.Get("total_tokens").Int() != 15 {
		t.Errorf("usage = %s, want 10/5/15", usage.Raw)
	}
}
