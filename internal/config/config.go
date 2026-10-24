// Package config holds runtime configuration shared by the subcommands.
package config

import (
	"flag"
	"os"
)

// Config is the runtime configuration of a gopress process.
type Config struct {
	Addr    string // listen address of the HTTP server
	DataDir string // directory holding gopress.db, uploads/, plugins/
}

// RegisterFlags binds the config fields to fs. Defaults come from the
// GOPRESS_ADDR / GOPRESS_DATA environment variables when set.
func (c *Config) RegisterFlags(fs *flag.FlagSet) {
	fs.StringVar(&c.Addr, "addr", env("GOPRESS_ADDR", "127.0.0.1:8080"), "HTTP listen address (env GOPRESS_ADDR)")
	fs.StringVar(&c.DataDir, "data", env("GOPRESS_DATA", "./local/data"), "data directory (env GOPRESS_DATA)")
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}
