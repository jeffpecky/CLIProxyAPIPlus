package config

func normalizeTokenSaverConfig(cfg *Config) {
	if cfg == nil {
		return
	}
	if cfg.TokenSaver.Headroom.TimeoutMS <= 0 {
		cfg.TokenSaver.Headroom.TimeoutMS = 3000
	}
	if cfg.TokenSaver.PXPipe.MinChars <= 0 {
		cfg.TokenSaver.PXPipe.MinChars = 25000
	}
	if cfg.TokenSaver.PXPipe.TimeoutMS <= 0 {
		cfg.TokenSaver.PXPipe.TimeoutMS = 15000
	}
}
