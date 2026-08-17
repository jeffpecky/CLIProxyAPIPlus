package tokensaver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

func Apply(opts Options) (out []byte, stats Stats) {
	defer func() {
		if recovered := recover(); recovered != nil {
			out = opts.Body
			stats = Stats{HeadroomSkip: fmt.Sprintf("token saver panic: %v", recovered)}
		}
	}()

	if !opts.Config.Enabled || len(opts.Body) == 0 || isOptedOut(headerValue(opts.Headers, OptOutHeader)) {
		return opts.Body, Stats{}
	}

	var root any
	dec := json.NewDecoder(bytes.NewReader(opts.Body))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		return opts.Body, Stats{}
	}

	stats.BytesBefore = len(opts.Body)
	if opts.Config.RTK {
		stats.RTKHits = applyRTK(root)
	}
	if opts.Config.Headroom.Enabled {
		var result headroomResult
		root, result = applyHeadroom(root, opts)
		stats.Headroom = result.applied
		stats.HeadroomSkip = result.reason
		stats.HeadroomEndpoint = result.endpoint
		stats.HeadroomStats = result.stats
		stats.HeadroomBefore = result.before
		stats.HeadroomAfter = result.after
		stats.HeadroomPhantom = isPhantomHeadroomSavings(result.stats, result.before, result.after)
	}
	if opts.Config.Caveman.Enabled {
		stats.Caveman = injectCaveman(root, opts.Format, opts.Config.Caveman.Level)
	}
	if opts.Config.Ponytail.Enabled {
		stats.Ponytail = injectPonytail(root, opts.Format, opts.Config.Ponytail.Level)
	}

	encoded, err := json.Marshal(root)
	if err != nil {
		return opts.Body, Stats{}
	}
	stats.BytesAfter = len(encoded)
	stats.Applied = stats.RTKHits > 0 || stats.Caveman || stats.Ponytail || stats.Headroom
	if !stats.Applied {
		return opts.Body, stats
	}
	return encoded, stats
}

func isOptedOut(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "off")
}

func headerValue(headers map[string][]string, key string) string {
	for name, values := range headers {
		if strings.EqualFold(name, key) {
			for _, value := range values {
				if isOptedOut(value) {
					return "off"
				}
			}
		}
	}
	return ""
}
