package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPAddr         string
	DataDir          string
	NodeID           string
	NodeName         string
	APISecret        string
	KeepRecentBlocks int
	GenesisPath      string
	ShutdownTimeout  time.Duration
}

func Load() Config {
	keepBlocks, _ := strconv.Atoi(getEnv("KEEP_RECENT_BLOCKS", "3000"))

	httpAddr := getEnv("HTTP_ADDR", "")
	if httpAddr == "" {
		if port := os.Getenv("PORT"); port != "" {
			httpAddr = ":" + port
		} else {
			httpAddr = ":8080"
		}
	}

	return Config{
		HTTPAddr:         httpAddr,
		DataDir:          getEnv("DATA_DIR", "./data"),
		NodeID:           getEnv("NODE_ID", "validator-1"),
		NodeName:         getEnv("NODE_NAME", "east-validator"),
		APISecret:        getEnv("API_SECRET", ""),
		KeepRecentBlocks: keepBlocks,
		GenesisPath:      getEnv("GENESIS_PATH", "./genesis.json"),
		ShutdownTimeout:  10 * time.Second,
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
