package config

import "testing"

func TestTokenSaverDefaultsDisabled(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("{}"))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if cfg.TokenSaver.Enabled {
		t.Fatal("token saver enabled by default")
	}
	if cfg.TokenSaver.Headroom.TimeoutMS != 3000 {
		t.Fatalf("headroom timeout = %d, want 3000", cfg.TokenSaver.Headroom.TimeoutMS)
	}
	if cfg.TokenSaver.PXPipe.MinChars != 25000 {
		t.Fatalf("pxpipe min chars = %d, want 25000", cfg.TokenSaver.PXPipe.MinChars)
	}
	if cfg.TokenSaver.PXPipe.TimeoutMS != 15000 {
		t.Fatalf("pxpipe timeout = %d, want 15000", cfg.TokenSaver.PXPipe.TimeoutMS)
	}
}

func TestTokenSaverParsesConfig(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
token-saver:
  enabled: true
  rtk: true
  caveman:
    enabled: true
    level: terse
  ponytail:
    enabled: true
    level: standard
  headroom:
    enabled: true
    url: "http://127.0.0.1:20129"
    timeout-ms: 99
    compress-user-messages: true
    proxy-token-env: HEADROOM_PROXY_TOKEN
  pxpipe:
    enabled: true
    min-chars: 123
    timeout-ms: 456
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if !cfg.TokenSaver.Enabled || !cfg.TokenSaver.RTK {
		t.Fatalf("token saver booleans not parsed: %+v", cfg.TokenSaver)
	}
	if cfg.TokenSaver.Caveman.Level != "terse" || cfg.TokenSaver.Ponytail.Level != "standard" {
		t.Fatalf("prompt levels not parsed: %+v", cfg.TokenSaver)
	}
	if cfg.TokenSaver.Headroom.URL != "http://127.0.0.1:20129" || !cfg.TokenSaver.Headroom.CompressUserMessages || cfg.TokenSaver.Headroom.ProxyTokenEnv != "HEADROOM_PROXY_TOKEN" {
		t.Fatalf("headroom not parsed: %+v", cfg.TokenSaver.Headroom)
	}
	if cfg.TokenSaver.PXPipe.MinChars != 123 || cfg.TokenSaver.PXPipe.TimeoutMS != 456 {
		t.Fatalf("pxpipe not parsed: %+v", cfg.TokenSaver.PXPipe)
	}
}
