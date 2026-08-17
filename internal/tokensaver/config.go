package tokensaver

import (
	"context"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

const OptOutHeader = "X-CLIProxy-Token-Saver"

type Options struct {
	Context context.Context
	Body    []byte
	Model   string
	Format  string
	Headers http.Header
	Session string
	Config  config.TokenSaverConfig
}

type Stats struct {
	Applied          bool
	RTKHits          int
	BytesBefore      int
	BytesAfter       int
	Caveman          bool
	Ponytail         bool
	Headroom         bool
	HeadroomSkip     string
	HeadroomEndpoint string
	HeadroomStats    HeadroomStats
	HeadroomBefore   SizeSnapshot
	HeadroomAfter    SizeSnapshot
	HeadroomPhantom  bool
}

type HeadroomStats struct {
	TokensBefore       int            `json:"tokens_before"`
	TokensAfter        int            `json:"tokens_after"`
	TokensSaved        int            `json:"tokens_saved"`
	Ratio              float64        `json:"ratio"`
	Transforms         []string       `json:"transforms"`
	TransformSummary   map[string]int `json:"transforms_summary"`
	CCRHashes          []string       `json:"ccr_hashes"`
	CompressionSkipped bool           `json:"compression_skipped"`
	SkipReason         string         `json:"skip_reason"`
}

type SizeSnapshot struct {
	BodyBytes        int
	MessageBytes     int
	ToolSchemaBytes  int
	ToolHistoryBytes int
}
