package observe

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// NewLogger creates a production-grade structured logger.
// In dev mode: human-readable console output.
// In prod mode: JSON structured output for log aggregation.
func NewLogger(dev bool, level string) (*zap.Logger, error) {
	var cfg zap.Config

	if dev {
		cfg = zap.NewDevelopmentConfig()
		cfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	} else {
		cfg = zap.NewProductionConfig()
	}

	// Parse level
	if err := cfg.Level.UnmarshalText([]byte(level)); err != nil {
		cfg.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	}

	// Always include caller location for debugging
	cfg.EncoderConfig.CallerKey = "caller"

	return cfg.Build(
		zap.AddCaller(),
		zap.AddCallerSkip(0),
		// Add OpenFabric version as a static field on every log line
		zap.Fields(zap.String("service", "openfabric")),
	)
}

// WithComponent returns a child logger scoped to a component name.
func WithComponent(log *zap.Logger, component string) *zap.Logger {
	return log.With(zap.String("component", component))
}

// WithTask returns a child logger scoped to a specific task.
func WithTask(log *zap.Logger, taskID, nodeID string) *zap.Logger {
	return log.With(
		zap.String("task_id", taskID),
		zap.String("node_id", nodeID),
	)
}

// WithNode returns a child logger scoped to a cluster node.
func WithNode(log *zap.Logger, nodeID string) *zap.Logger {
	return log.With(zap.String("node_id", nodeID))
}
