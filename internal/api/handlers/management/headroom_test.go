package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestHeadroomStartReturnsSuccessForHealthyExternalService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	handler := NewHandlerWithoutConfigFilePath(&config.Config{
		SDKConfig: config.SDKConfig{
			TokenSaver: config.TokenSaverConfig{
				Headroom: config.TokenSaverHeadroomConfig{URL: server.URL},
			},
		},
	}, nil)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/headroom/start", nil)

	handler.HeadroomStart(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Success bool `json:"success"`
		Managed bool `json:"managed"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.Success {
		t.Fatal("expected successful start response")
	}
	if body.Managed {
		t.Fatal("expected external service to be unmanaged")
	}
}

func TestWaitForHeadroomHealthyRetriesUntilHealthy(t *testing.T) {
	attempts := 0
	healthy := waitForHeadroomHealthy("http://127.0.0.1:8787", 200*time.Millisecond, time.Millisecond, func(string) bool {
		attempts++
		return attempts == 3
	})

	if !healthy {
		t.Fatal("expected health wait to succeed")
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
}

func TestWaitForHeadroomHealthyTimesOut(t *testing.T) {
	attempts := 0
	healthy := waitForHeadroomHealthy("http://127.0.0.1:8787", 5*time.Millisecond, time.Millisecond, func(string) bool {
		attempts++
		return false
	})

	if healthy {
		t.Fatal("expected health wait to time out")
	}
	if attempts == 0 {
		t.Fatal("expected at least one health attempt")
	}
}
