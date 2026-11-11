package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	DatabaseURL      string
	GammaBaseURL     string
	DataBaseURL      string
	HTTPTimeout      time.Duration
	GammaRPS         float64
	DataRPS          float64
	RetryMax         int
	Timezone         string
	PageSize         int
	MaxOffsetSafety  int
	MinWindow        time.Duration
}

func Default() Config {
	return Config{
		DatabaseURL:     getenv("DATABASE_URL", "postgres://rn1:rn1@localhost:5432/rn1?sslmode=disable"),
		GammaBaseURL:    getenv("GAMMA_BASE_URL", "https://gamma-api.polymarket.com"),
		DataBaseURL:     getenv("DATA_BASE_URL", "https://data-api.polymarket.com"),
		HTTPTimeout:     getenvDuration("HTTP_TIMEOUT", 30*time.Second),
		GammaRPS:        getenvFloat("GAMMA_RPS", 5.0),
		DataRPS:         getenvFloat("DATA_RPS", 3.0),
		RetryMax:        getenvInt("RETRY_MAX", 5),
		Timezone:        getenv("TIMEZONE", "Asia/Seoul"),
		PageSize:        getenvInt("PAGE_SIZE", 500),
		// Docs say /activity offset cap is 10,000, but the live Data API
		// returns HTTP 400 "max historical activity offset of 3000 exceeded"
		// at offset >= 3000. Keep the default below that with some headroom.
		MaxOffsetSafety: getenvInt("MAX_OFFSET_SAFETY", 2500),
		MinWindow:       getenvDuration("MIN_WINDOW", time.Hour),
	}
}

func (c Config) Validate() error {
	if c.DatabaseURL == "" {
		return errors.New("database url required")
	}
	if c.PageSize <= 0 || c.PageSize > 500 {
		return fmt.Errorf("page size out of range: %d", c.PageSize)
	}
	if c.MaxOffsetSafety <= 0 || c.MaxOffsetSafety > 3000 {
		return fmt.Errorf("max offset safety out of range: %d (live API rejects offset >= 3000)", c.MaxOffsetSafety)
	}
	if c.MinWindow <= 0 {
		return fmt.Errorf("min window must be positive")
	}
	return nil
}

func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getenvFloat(key string, def float64) float64 {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func getenvDuration(key string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
