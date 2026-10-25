package executor

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// resetOpenCodeSessionState isolates the package-level rotation state and
// threshold for a test, restoring both afterwards.
func resetOpenCodeSessionState(t *testing.T) {
	t.Helper()
	prevThreshold := openCodeSessionProactiveRotate
	prevStates := openCodeSessionStates
	t.Cleanup(func() {
		openCodeSessionProactiveRotate = prevThreshold
		openCodeSessionStates = prevStates
	})
	openCodeSessionStates = map[string]*openCodeSessionEntry{}
}

func newOpenCodeTestAuth(serverURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Provider:   "opencode",
		Attributes: map[string]string{"base_url": serverURL},
	}
}

func newOpenCodeTestRequest() cliproxyexecutor.Request {
	return cliproxyexecutor.Request{
		Model:   "test-model",
		Payload: []byte(`{"model":"test-model","messages":[{"role":"user","content":"hi"}]}`),
	}
}

// sessionRecorder collects the x-opencode-session header of every upstream hit.
type sessionRecorder struct {
	mu       sync.Mutex
	sessions []string
	calls    int32
}

func (s *sessionRecorder) record(r *http.Request) {
	atomic.AddInt32(&s.calls, 1)
	s.mu.Lock()
	s.sessions = append(s.sessions, r.Header.Get(openCodeSessionHeader))
	s.mu.Unlock()
}

func (s *sessionRecorder) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sessions...)
}

func (s *sessionRecorder) count() int32 {
	return atomic.LoadInt32(&s.calls)
}

const openCodeFreeUsageLimitBody = `{"type":"error","error":{"type":"FreeUsageLimitError","message":"Free usage limit reached. Subscribe to OpenCode Go to keep going: https://opencode.ai/go"}}`

const openCodeSuccessBody = `{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

func TestOpenCodeExecutorRotationOnFreeUsageLimitExecute(t *testing.T) {
	resetOpenCodeSessionState(t)

	rec := &sessionRecorder{}
	server := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		if rec.count() == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(openCodeFreeUsageLimitBody))
			return
		}
		w.Write([]byte(openCodeSuccessBody))
	})
	defer server.Close()

	executor := NewOpenCodeExecutor(&config.Config{})
	auth := newOpenCodeTestAuth(server.URL)
	resp, err := executor.Execute(context.Background(), auth, newOpenCodeTestRequest(), cliproxyexecutor.Options{Stream: false})
	if err != nil {
		t.Fatalf("Execute after rotation error: %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("expected non-empty response payload after rotation")
	}
	if got := rec.count(); got != 2 {
		t.Fatalf("upstream calls = %d, want 2 (429 then recovered retry)", got)
	}
	sessions := rec.snapshot()
	if len(sessions) != 2 || !openCodeIsValidSessionFormat(sessions[0]) || !openCodeIsValidSessionFormat(sessions[1]) {
		t.Fatalf("unexpected upstream sessions: %v", sessions)
	}
	if sessions[0] == sessions[1] {
		t.Fatalf("session not rotated on free usage limit: %q", sessions[0])
	}
	// Attribute cache and alias headers must now point at the rotated session.
	if got := auth.Attributes["header:"+openCodeSessionHeader]; got != sessions[1] {
		t.Errorf("cached session attr = %q, want %q", got, sessions[1])
	}
	if got := auth.Attributes["header:x-opencode-session-id"]; got != sessions[1] {
		t.Errorf("alias x-opencode-session-id = %q, want %q", got, sessions[1])
	}
}

func TestOpenCodeExecutorRotationOnFreeUsageLimitStream(t *testing.T) {
	resetOpenCodeSessionState(t)

	rec := &sessionRecorder{}
	server := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "text/event-stream")
		if rec.count() == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(openCodeFreeUsageLimitBody))
			return
		}
		w.Write([]byte("data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	})
	defer server.Close()

	executor := NewOpenCodeExecutor(&config.Config{})
	auth := newOpenCodeTestAuth(server.URL)
	res, err := executor.ExecuteStream(context.Background(), auth, newOpenCodeTestRequest(), cliproxyexecutor.Options{Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream after rotation error: %v", err)
	}
	if got := rec.count(); got != 2 {
		t.Fatalf("upstream calls = %d, want 2 (429 then recovered retry)", got)
	}
	sessions := rec.snapshot()
	if len(sessions) != 2 || sessions[0] == sessions[1] {
		t.Fatalf("session not rotated on free usage limit stream: %v", sessions)
	}
	sawContent := false
	deadline := time.After(3 * time.Second)
chunks:
	for {
		select {
		case chunk, ok := <-res.Chunks:
			if !ok {
				break chunks
			}
			if strings.Contains(string(chunk.Payload), "ok") {
				sawContent = true
			}
		case <-deadline:
			break chunks
		}
	}
	if !sawContent {
		t.Fatal("stream produced no content chunk after rotation retry")
	}
}

func TestOpenCodeSessionProactiveRotation(t *testing.T) {
	resetOpenCodeSessionState(t)
	openCodeSessionProactiveRotate = 2

	rec := &sessionRecorder{}
	server := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(openCodeSuccessBody))
	})
	defer server.Close()

	executor := NewOpenCodeExecutor(&config.Config{})
	auth := newOpenCodeTestAuth(server.URL)
	for i := 0; i < 5; i++ {
		if _, err := executor.Execute(context.Background(), auth, newOpenCodeTestRequest(), cliproxyexecutor.Options{Stream: false}); err != nil {
			t.Fatalf("request %d error: %v", i+1, err)
		}
	}
	sessions := rec.snapshot()
	if len(sessions) != 5 {
		t.Fatalf("upstream calls = %d, want 5", len(sessions))
	}
	for _, s := range sessions {
		if !openCodeIsValidSessionFormat(s) {
			t.Fatalf("non-canonical session id: %q", s)
		}
	}
	// Threshold 2: first session serves one request, the triggering request
	// gets the rotated id, and the fresh id stays stable for another run.
	if sessions[0] == sessions[1] {
		t.Fatalf("expected proactive rotation, got stable session %q", sessions[1])
	}
	if sessions[1] != sessions[2] {
		t.Fatalf("rotated session not reused: %q vs %q", sessions[1], sessions[2])
	}
	if sessions[2] == sessions[3] {
		t.Fatalf("expected second proactive rotation after %q", sessions[2])
	}
	if sessions[3] != sessions[4] {
		t.Fatalf("second rotated session not reused: %q vs %q", sessions[3], sessions[4])
	}
}

func TestOpenCodeExecutorOther429NotRotated(t *testing.T) {
	resetOpenCodeSessionState(t)

	rec := &sessionRecorder{}
	server := newTestServer(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"code":"tpm_rate_limit_exceeded","message":"Tokens per minute limit exceeded"}}`))
	})
	defer server.Close()

	executor := NewOpenCodeExecutor(&config.Config{})
	auth := newOpenCodeTestAuth(server.URL)
	_, err := executor.Execute(context.Background(), auth, newOpenCodeTestRequest(), cliproxyexecutor.Options{Stream: false})
	if err == nil {
		t.Fatal("expected non-free-usage 429 to surface as an error")
	}
	if got := rec.count(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1 (no rotation for other 429s)", got)
	}
}
