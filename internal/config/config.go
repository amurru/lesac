package config

import (
	"errors"
	"fmt"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	defaultBindAddress     = ":8080"
	defaultReadTimeout     = 15 * time.Second
	defaultWriteTimeout    = 15 * time.Second
	defaultIdleTimeout     = 60 * time.Second
	defaultShutdownTimeout = 10 * time.Second
	defaultUploadLimit     = 64 * 1024 * 1024
	defaultSweepInterval   = 1 * time.Minute
	defaultSweepBatchSize  = 100
	defaultStorageDriver   = "filesystem"
	defaultStorageRoot     = "./data"
	defaultSQLitePath      = "./data/lesac.db"
)

// Config is the root application configuration loaded from TOML.
type Config struct {
	Server   ServerConfig   `toml:"server"`
	Uploads  UploadsConfig  `toml:"uploads"`
	Storage  StorageConfig  `toml:"storage"`
	Metadata MetadataConfig `toml:"metadata"`
	Cleanup  CleanupConfig  `toml:"cleanup"`
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	BindAddress     string        `toml:"bind_address"`
	ReadTimeout     time.Duration `toml:"read_timeout"`
	WriteTimeout    time.Duration `toml:"write_timeout"`
	IdleTimeout     time.Duration `toml:"idle_timeout"`
	ShutdownTimeout time.Duration `toml:"shutdown_timeout"`
}

// UploadsConfig defines upload-specific limits.
type UploadsConfig struct {
	MaxUploadSize int64 `toml:"max_upload_size"`
}

// StorageConfig configures the blob storage backend.
type StorageConfig struct {
	Driver  string `toml:"driver"`
	RootDir string `toml:"root_dir"`
}

// MetadataConfig configures the metadata backend.
type MetadataConfig struct {
	SQLitePath string `toml:"sqlite_path"`
}

// CleanupConfig controls periodic expiration cleanup behavior.
type CleanupConfig struct {
	Enabled   bool          `toml:"enabled"`
	Interval  time.Duration `toml:"interval"`
	BatchSize int           `toml:"batch_size"`
}

// Load reads TOML configuration from path, applies defaults, and validates it.
func Load(path string) (Config, error) {
	cfg := Config{}
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode toml config %q: %w", path, err)
	}

	applyDefaults(&cfg)
	if err := validate(cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.Server.BindAddress == "" {
		cfg.Server.BindAddress = defaultBindAddress
	}
	if cfg.Server.ReadTimeout <= 0 {
		cfg.Server.ReadTimeout = defaultReadTimeout
	}
	if cfg.Server.WriteTimeout <= 0 {
		cfg.Server.WriteTimeout = defaultWriteTimeout
	}
	if cfg.Server.IdleTimeout <= 0 {
		cfg.Server.IdleTimeout = defaultIdleTimeout
	}
	if cfg.Server.ShutdownTimeout <= 0 {
		cfg.Server.ShutdownTimeout = defaultShutdownTimeout
	}
	if cfg.Uploads.MaxUploadSize <= 0 {
		cfg.Uploads.MaxUploadSize = defaultUploadLimit
	}
	if cfg.Storage.Driver == "" {
		cfg.Storage.Driver = defaultStorageDriver
	}
	if cfg.Storage.RootDir == "" {
		cfg.Storage.RootDir = defaultStorageRoot
	}
	if cfg.Metadata.SQLitePath == "" {
		cfg.Metadata.SQLitePath = defaultSQLitePath
	}
	if cfg.Cleanup.Interval <= 0 {
		cfg.Cleanup.Interval = defaultSweepInterval
	}
	if cfg.Cleanup.BatchSize <= 0 {
		cfg.Cleanup.BatchSize = defaultSweepBatchSize
	}
}

func validate(cfg Config) error {
	if cfg.Storage.Driver != "filesystem" {
		return fmt.Errorf("unsupported storage driver %q", cfg.Storage.Driver)
	}
	if cfg.Server.BindAddress == "" {
		return errors.New("server.bind_address is required")
	}
	if cfg.Metadata.SQLitePath == "" {
		return errors.New("metadata.sqlite_path is required")
	}
	if cfg.Storage.RootDir == "" {
		return errors.New("storage.root_dir is required")
	}
	if cfg.Uploads.MaxUploadSize <= 0 {
		return errors.New("uploads.max_upload_size must be greater than zero")
	}
	if cfg.Cleanup.Enabled && cfg.Cleanup.Interval <= 0 {
		return errors.New("cleanup.interval must be greater than zero when cleanup is enabled")
	}
	if cfg.Cleanup.Enabled && cfg.Cleanup.BatchSize <= 0 {
		return errors.New("cleanup.batch_size must be greater than zero when cleanup is enabled")
	}
	return nil
}
