package config

import (
	"os"
	"strconv"
	"time"
)

// Config holds the server settings read from the environment.
type Config struct {
	Addr     string
	LogLevel string
	Workers  int

	RateLimitAnon   int           // requests per minute without an API key
	RateLimitKeyed  int           // requests per minute with an API key
	RateLimitWindow time.Duration // length of one limit window
}

// Load reads the configuration from environment variables.
func Load() Config {
	c := Config{Addr: envOr("SHOP_ADDR", ":8080"), LogLevel: envOr("SHOP_LOG_LEVEL", "info"), Workers: 4}
	if n, err := strconv.Atoi(os.Getenv("SHOP_WORKERS")); err == nil && n > 0 {
		c.Workers = n
	}
	c.RateLimitAnon = envInt("RATE_LIMIT_ANON", 60)
	c.RateLimitKeyed = envInt("RATE_LIMIT_KEYED", 600)
	c.RateLimitWindow = parseDuration(envOr("RATE_LIMIT_WINDOW", "1m"), time.Minute)
	return c
}

// envInt reads a positive integer from the environment, or returns def.
func envInt(key string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(key)); err == nil && n > 0 {
		return n
	}
	return def
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// parseDuration parses s, returning def when s is empty, malformed or not
// positive.
func parseDuration(s string, def time.Duration) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return def
	}
	return d
}
