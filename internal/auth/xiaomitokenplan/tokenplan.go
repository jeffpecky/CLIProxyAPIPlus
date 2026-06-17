package xiaomitokenplan

const (
	// DefaultRegion is the default region for Xiaomi TokenPlan API.
	DefaultRegion = "cn"

	// DefaultBaseURL is the base URL for Xiaomi TokenPlan API.
	DefaultBaseURL = "https://api.xiaomitokenplan.com"
)

// RegionBaseURL returns the base URL for the given region.
func RegionBaseURL(region string) string {
	switch region {
	case "cn", "CN", "china":
		return "https://api.xiaomitokenplan.com"
	case "global", "us", "eu":
		return "https://api-global.xiaomitokenplan.com"
	default:
		return DefaultBaseURL
	}
}
