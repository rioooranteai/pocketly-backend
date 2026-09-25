package config

import (
	"os"
	"strings"

	"github.com/joho/godotenv"
)

/*
maxImageSize is the largest receipt image, in bytes, accepted by
/transactions/scan. It is a fixed application limit rather than an
environment variable, so every deployment enforces the same cap.
*/
const maxImageSize int64 = 5 * 1024 * 1024

/*
Config holds all environment-derived settings the application needs
at startup: server port, database location, JWT signing secret, and
AI provider credentials.
*/
type Config struct {
	AppPort   string
	DBPath    string
	JWTSecret string

	/*
		AIAPIKey is the API key for the TypeSafe categorizer. It is read
		so the key is ready in the environment, but TypeSafe is not
		wired in Bootstrap; OpenAICategorizer is in use and takes its
		key from VisionConfig.APIKey.
	*/
	AIAPIKey string

	/*
		TrustedProxies lists the proxy IPs/CIDRs allowed to set
		X-Forwarded-For. Empty means none are trusted and the client IP
		is taken from the TCP connection, which keeps per-IP rate
		limiting from being bypassed with a spoofed header.
	*/
	TrustedProxies []string

	VisionConfig VisionConfig
}

/*
VisionConfig holds the settings for receipt scanning: the vision
provider's API key and the largest image, in bytes, the app accepts.
APIKey is the OpenAI key, so OpenAICategorizer uses it as well.
*/
type VisionConfig struct {
	APIKey      string
	MaxFileSize int64
}

/*
Load reads environment variables into a Config. It attempts to load a
local .env file first, but does not fail if one is absent — in
production, environment variables are typically injected directly by
the deployment platform (Docker, Kubernetes, etc.) instead.
*/
func Load() *Config {
	_ = godotenv.Load()

	var trustedProxies []string
	for _, proxy := range strings.Split(os.Getenv("TRUSTED_PROXIES"), ",") {
		if proxy = strings.TrimSpace(proxy); proxy != "" {
			trustedProxies = append(trustedProxies, proxy)
		}
	}

	return &Config{
		AppPort:        os.Getenv("APP_PORT"),
		DBPath:         os.Getenv("DB_PATH"),
		JWTSecret:      os.Getenv("JWT_SECRET"),
		AIAPIKey:       os.Getenv("AI_API_KEY"),
		TrustedProxies: trustedProxies,

		VisionConfig: VisionConfig{
			APIKey:      os.Getenv("VISION_API_KEY"),
			MaxFileSize: maxImageSize,
		},
	}
}
