package executor

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
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
