// Package config loads process configuration from the environment.
//
// Values are read once at startup and passed explicitly to the components that
// need them, rather than being reachable from anywhere via a package-level
// variable, which avoids global mutable state. Each phase adds only the
// settings it actually uses.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// StorageNode is one configured storage node. ID is derived deterministically
// from Name at startup, so a restart maps to the same database row.
type StorageNode struct {
	Name string
	URL  string
}

// Config is the fully resolved configuration for one process.
type Config struct {
	APIAddr         string
	LogLevel        slog.Level
	DemoMode        bool
	DatabaseURL     string
	RedisAddr       string
	ShutdownTimeout time.Duration

	JWTSecret string
	JWTTTL    time.Duration

	StorageNodes []StorageNode
	ReplicasN    int
	WriteQuorumW int

	MaxPartSizeBytes int64
	MultipartExpiry  time.Duration

	WALDir          string
	WALSegmentBytes int64

	PresignKeyID     string
	PresignSecret    string
	PresignMaxExpiry time.Duration

	DemoToken string

	MetaCacheTTL     time.Duration
	SmallObjectLimit int64

	EmbeddingModel string
	EmbeddingDim   int
	OpenAIAPIKey   string

	GCInterval    time.Duration
	GCBatch       int
	GCOrphanGrace time.Duration
}

// WorkerConfig configures the background worker process.
type WorkerConfig struct {
	Addr                  string
	LogLevel              slog.Level
	ShutdownTimeout       time.Duration
	DatabaseURL           string
	RedisAddr             string
	StorageNodes          []StorageNode
	HealthInterval        time.Duration
	HealthDownThreshold   int
	HealthUpThreshold     int
	ReplicatorBatch       int
	ReplicatorConcurrency int
	RepairBatch           int
	RepairInterval        time.Duration
	ScrubBatch            int
	ScrubInterval         time.Duration
	EmbeddingModel        string
	EmbeddingDim          int
	OpenAIAPIKey          string
}

// ChaosConfig configures the docker-socket chaos controller.
type ChaosConfig struct {
	Addr       string
	DemoMode   bool
	DemoToken  string
	DockerHost string
	APIBaseURL string
	K6Script   string
	K6BaseURL  string
	LogLevel   slog.Level
}

// NodeConfig is the configuration for one storage node process.
type NodeConfig struct {
	Addr            string
	Name            string
	DataDir         string
	LogLevel        slog.Level
	ShutdownTimeout time.Duration
}

// Load reads API configuration from the environment.
//
// It reports every problem at once instead of failing on the first one, so a
// misconfigured deploy takes one restart to diagnose rather than five.
func Load() (Config, error) {
	var errs []error
	fail := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}

	cfg := Config{
		APIAddr:   envString("API_ADDR", ":8080"),
		RedisAddr: envString("REDIS_ADDR", "localhost:6379"),
	}

	dbURL, err := envRequired("DATABASE_URL")
	fail(err)
	cfg.DatabaseURL = dbURL

	jwtSecret, err := envRequired("JWT_SECRET")
	fail(err)
	cfg.JWTSecret = jwtSecret

	presignSecret, err := envRequired("PRESIGN_SECRET")
	fail(err)
	cfg.PresignSecret = presignSecret
	cfg.PresignKeyID = envString("PRESIGN_KEY_ID", "k1")
	cfg.DemoToken = os.Getenv("DEMO_TOKEN")

	level, err := envLogLevel("LOG_LEVEL", slog.LevelInfo)
	fail(err)
	cfg.LogLevel = level

	demo, err := envBool("DEMO_MODE", false)
	fail(err)
	cfg.DemoMode = demo

	timeout, err := envDuration("SHUTDOWN_TIMEOUT", 15*time.Second)
	fail(err)
	cfg.ShutdownTimeout = timeout

	jwtTTL, err := envDuration("JWT_TTL", 15*time.Minute)
	fail(err)
	cfg.JWTTTL = jwtTTL

	presignMax, err := envDuration("PRESIGN_MAX_EXPIRY", 7*24*time.Hour)
	fail(err)
	cfg.PresignMaxExpiry = presignMax

	multipartExpiry, err := envDuration("MULTIPART_EXPIRY", 24*time.Hour)
	fail(err)
	cfg.MultipartExpiry = multipartExpiry

	nodes, err := parseStorageNodes(envString("STORAGE_NODES",
		"node1=http://localhost:9001,node2=http://localhost:9002,node3=http://localhost:9003"))
	fail(err)
	cfg.StorageNodes = nodes

	replicas, err := envInt("REPLICAS_N", 3)
	fail(err)
	cfg.ReplicasN = replicas

	quorum, err := envInt("WRITE_QUORUM_W", 2)
	fail(err)
	cfg.WriteQuorumW = quorum

	maxPart, err := envInt64("MAX_PART_SIZE_BYTES", 16*1024*1024)
	fail(err)
	cfg.MaxPartSizeBytes = maxPart

	cfg.WALDir = envString("WAL_DIR", "/data/wal")
	walSegment, err := envInt64("WAL_SEGMENT_BYTES", 64*1024*1024)
	fail(err)
	cfg.WALSegmentBytes = walSegment

	cacheTTL, err := envDuration("META_CACHE_TTL", 60*time.Second)
	fail(err)
	cfg.MetaCacheTTL = cacheTTL
	smallLimit, err := envInt64("SMALL_OBJECT_CACHE_LIMIT_BYTES", 1<<20)
	fail(err)
	cfg.SmallObjectLimit = smallLimit

	cfg.EmbeddingModel = envString("EMBEDDING_MODEL", "text-embedding-3-small")
	cfg.EmbeddingDim, err = envInt("EMBEDDING_DIM", 1536)
	fail(err)
	cfg.OpenAIAPIKey = os.Getenv("OPENAI_API_KEY")

	gcInterval, err := envDuration("GC_INTERVAL", time.Hour)
	fail(err)
	cfg.GCInterval = gcInterval
	cfg.GCBatch, err = envInt("GC_BATCH", 100)
	fail(err)
	gcGrace, err := envDuration("GC_ORPHAN_GRACE", 24*time.Hour)
	fail(err)
	cfg.GCOrphanGrace = gcGrace

	if cfg.ReplicasN <= 0 {
		fail(errors.New("REPLICAS_N must be positive"))
	}
	if cfg.WriteQuorumW <= 0 || cfg.WriteQuorumW > cfg.ReplicasN {
		fail(fmt.Errorf("WRITE_QUORUM_W must be between 1 and REPLICAS_N (%d)", cfg.ReplicasN))
	}
	if cfg.MaxPartSizeBytes <= 0 {
		fail(errors.New("MAX_PART_SIZE_BYTES must be positive"))
	}
	if cfg.WALSegmentBytes <= 0 {
		fail(errors.New("WAL_SEGMENT_BYTES must be positive"))
	}
	if cfg.DemoMode && cfg.DemoToken == "" {
		fail(errors.New("DEMO_TOKEN is required when DEMO_MODE is true"))
	}
	if cfg.SmallObjectLimit < 0 {
		fail(errors.New("SMALL_OBJECT_CACHE_LIMIT_BYTES must not be negative"))
	}

	if len(errs) > 0 {
		return Config{}, fmt.Errorf("load config: %w", errors.Join(errs...))
	}
	return cfg, nil
}

// LoadNode reads the configuration for a storage node process.
func LoadNode() (NodeConfig, error) {
	var errs []error
	fail := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}
	cfg := NodeConfig{
		Addr:    envString("NODE_ADDR", ":9000"),
		Name:    envString("NODE_NAME", "node1"),
		DataDir: envString("NODE_DATA_DIR", "/data/chunks"),
	}
	level, err := envLogLevel("LOG_LEVEL", slog.LevelInfo)
	fail(err)
	cfg.LogLevel = level
	timeout, err := envDuration("SHUTDOWN_TIMEOUT", 15*time.Second)
	fail(err)
	cfg.ShutdownTimeout = timeout
	if cfg.Name == "" {
		fail(errors.New("NODE_NAME must not be empty"))
	}
	if cfg.DataDir == "" {
		fail(errors.New("NODE_DATA_DIR must not be empty"))
	}
	if len(errs) > 0 {
		return NodeConfig{}, fmt.Errorf("load node config: %w", errors.Join(errs...))
	}
	return cfg, nil
}

// LoadWorkers reads worker-process configuration.
func LoadWorkers() (WorkerConfig, error) {
	var errs []error
	fail := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}
	cfg := WorkerConfig{
		Addr:           envString("WORKER_ADDR", ":8092"),
		RedisAddr:      envString("REDIS_ADDR", "localhost:6379"),
		EmbeddingModel: envString("EMBEDDING_MODEL", "text-embedding-3-small"),
		OpenAIAPIKey:   os.Getenv("OPENAI_API_KEY"),
	}
	dbURL, err := envRequired("DATABASE_URL")
	fail(err)
	cfg.DatabaseURL = dbURL
	level, err := envLogLevel("LOG_LEVEL", slog.LevelInfo)
	fail(err)
	cfg.LogLevel = level
	timeout, err := envDuration("SHUTDOWN_TIMEOUT", 15*time.Second)
	fail(err)
	cfg.ShutdownTimeout = timeout
	nodes, err := parseStorageNodes(envString("STORAGE_NODES", "node1=http://localhost:9001,node2=http://localhost:9002,node3=http://localhost:9003"))
	fail(err)
	cfg.StorageNodes = nodes
	cfg.HealthInterval, err = envDuration("HEALTH_INTERVAL", 2*time.Second)
	fail(err)
	cfg.HealthDownThreshold, err = envInt("HEALTH_DOWN_THRESHOLD", 3)
	fail(err)
	cfg.HealthUpThreshold, err = envInt("HEALTH_UP_THRESHOLD", 2)
	fail(err)
	cfg.ReplicatorBatch, err = envInt("REPLICATOR_BATCH", 50)
	fail(err)
	cfg.ReplicatorConcurrency, err = envInt("REPLICATOR_CONCURRENCY", 4)
	fail(err)
	cfg.RepairBatch, err = envInt("REPAIR_BATCH", 20)
	fail(err)
	cfg.RepairInterval, err = envDuration("REPAIR_INTERVAL", 15*time.Second)
	fail(err)
	cfg.ScrubBatch, err = envInt("SCRUB_BATCH", 50)
	fail(err)
	cfg.ScrubInterval, err = envDuration("SCRUB_INTERVAL", 30*time.Second)
	fail(err)
	cfg.EmbeddingDim, err = envInt("EMBEDDING_DIM", 1536)
	fail(err)
	if cfg.HealthInterval <= 0 || cfg.ReplicatorBatch <= 0 || cfg.ReplicatorConcurrency <= 0 || cfg.ScrubBatch <= 0 {
		fail(errors.New("worker intervals and batch sizes must be positive"))
	}
	if len(errs) > 0 {
		return WorkerConfig{}, fmt.Errorf("load worker config: %w", errors.Join(errs...))
	}
	return cfg, nil
}

// LoadChaos reads chaos-controller configuration. It refuses to run unless
// DEMO_MODE=true, because it mounts the Docker socket.
func LoadChaos() (ChaosConfig, error) {
	cfg := ChaosConfig{
		Addr:       envString("CHAOS_ADDR", ":8090"),
		DockerHost: envString("DOCKER_HOST", "unix:///var/run/docker.sock"),
		APIBaseURL: envString("API_BASE_URL", "http://api:8080"),
		K6Script:   envString("K6_SCRIPT", "/etc/chaos/loadtest.js"),
		K6BaseURL:  envString("K6_BASE_URL", "http://api:8080"),
	}
	var errs []error
	demo, err := envBool("DEMO_MODE", false)
	if err != nil {
		errs = append(errs, err)
	}
	cfg.DemoMode = demo
	cfg.DemoToken = os.Getenv("DEMO_TOKEN")
	level, err := envLogLevel("LOG_LEVEL", slog.LevelInfo)
	if err != nil {
		errs = append(errs, err)
	}
	cfg.LogLevel = level
	if !demo {
		errs = append(errs, errors.New("DEMO_MODE must be true for the chaos controller"))
	}
	if cfg.DemoToken == "" {
		errs = append(errs, errors.New("DEMO_TOKEN is required"))
	}
	if len(errs) > 0 {
		return ChaosConfig{}, fmt.Errorf("load chaos config: %w", errors.Join(errs...))
	}
	return cfg, nil
}

func parseStorageNodes(raw string) ([]StorageNode, error) {
	var nodes []StorageNode
	seen := make(map[string]bool)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, rawURL, ok := strings.Cut(part, "=")
		if !ok || name == "" || rawURL == "" {
			return nil, fmt.Errorf("STORAGE_NODES entry %q must be name=url", part)
		}
		if seen[name] {
			return nil, fmt.Errorf("STORAGE_NODES has duplicate node name %q", name)
		}
		seen[name] = true
		u, err := url.Parse(rawURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("STORAGE_NODES entry %q has invalid url %q", name, rawURL)
		}
		nodes = append(nodes, StorageNode{Name: name, URL: strings.TrimRight(rawURL, "/")})
	}
	if len(nodes) == 0 {
		return nil, errors.New("STORAGE_NODES must contain at least one node")
	}
	return nodes, nil
}

func envString(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envRequired(key string) (string, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return "", fmt.Errorf("%s is required but not set", key)
	}
	return v, nil
}

func envBool(key string, def bool) (bool, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean, got %q: %w", key, raw, err)
	}
	return v, nil
}

func envInt(key string, def int) (int, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer, got %q: %w", key, raw, err)
	}
	return v, nil
}

func envInt64(key string, def int64) (int64, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer, got %q: %w", key, raw, err)
	}
	return v, nil
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def, nil
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration such as 15s, got %q: %w", key, raw, err)
	}
	return v, nil
}

func envLogLevel(key string, def slog.Level) (slog.Level, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def, nil
	}
	var level slog.Level
	// UnmarshalText accepts "debug", "info", "warn", "error" case-insensitively.
	if err := level.UnmarshalText([]byte(raw)); err != nil {
		return 0, fmt.Errorf("%s must be debug, info, warn or error, got %q: %w", key, raw, err)
	}
	return level, nil
}

// APIBaseURLContainer is the compose container name for the API.
func (c ChaosConfig) APIBaseURLContainer() string {
	if name := os.Getenv("API_CONTAINER"); name != "" {
		return name
	}
	return "objstore-api"
}

// NodeContainerPrefix is the shared prefix of node containers.
func (c ChaosConfig) NodeContainerPrefix() string {
	if name := os.Getenv("NODE_CONTAINER_PREFIX"); name != "" {
		return name
	}
	return "objstore-node"
}
