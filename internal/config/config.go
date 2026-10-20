// Package config loads ProjectPulse settings from flags and environment.
package config

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config contains all runtime settings.
type Config struct {
	RunAddress, DatabaseURI, InspectorAddress, AuthSecret string
	CheckInterval                                         time.Duration
	WorkerCount                                           int
}

// Load parses flags and then applies environment-variable overrides.
func Load(args []string) (Config, error) {
	fs := flag.NewFlagSet("projectpulse", flag.ContinueOnError)
	c := Config{}
	fs.StringVar(&c.RunAddress, "a", ":8080", "listen address")
	fs.StringVar(&c.DatabaseURI, "d", "postgres://postgres:postgres@localhost:5432/projectpulse?sslmode=disable", "PostgreSQL URI")
	fs.StringVar(&c.InspectorAddress, "i", "http://localhost:8081", "inspector URL")
	fs.StringVar(&c.AuthSecret, "s", "", "token signing secret")
	fs.DurationVar(&c.CheckInterval, "c", 24*time.Hour, "automatic check interval")
	fs.IntVar(&c.WorkerCount, "w", 2, "worker count")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if v, ok := os.LookupEnv("RUN_ADDRESS"); ok {
		c.RunAddress = v
	}
	if v, ok := os.LookupEnv("DATABASE_URI"); ok {
		c.DatabaseURI = v
	}
	if v, ok := os.LookupEnv("INSPECTOR_ADDRESS"); ok {
		c.InspectorAddress = v
	}
	if v, ok := os.LookupEnv("AUTH_SECRET"); ok {
		c.AuthSecret = v
	}
	if v, ok := os.LookupEnv("CHECK_INTERVAL"); ok {
		d, e := time.ParseDuration(v)
		if e != nil {
			return c, fmt.Errorf("CHECK_INTERVAL: %w", e)
		}
		c.CheckInterval = d
	}
	if v, ok := os.LookupEnv("WORKER_COUNT"); ok {
		n, e := strconv.Atoi(v)
		if e != nil {
			return c, fmt.Errorf("WORKER_COUNT: %w", e)
		}
		c.WorkerCount = n
	}
	if len(c.AuthSecret) < 32 {
		return c, fmt.Errorf("AUTH_SECRET must contain at least 32 characters")
	}
	if c.RunAddress == "" || c.DatabaseURI == "" || c.InspectorAddress == "" {
		return c, fmt.Errorf("RUN_ADDRESS, DATABASE_URI and INSPECTOR_ADDRESS must not be empty")
	}
	if c.WorkerCount < 1 || c.CheckInterval < time.Second {
		return c, fmt.Errorf("worker count must be positive and check interval at least one second")
	}
	return c, nil
}
