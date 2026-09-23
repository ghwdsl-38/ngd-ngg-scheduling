package algorithm

import (
	"fmt"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// NewLoggerFromEnv builds the production command-line logger used by Algorithm.
// LOG_FORMAT accepts json or console; LOG_LEVEL accepts debug, info, warn or error.
func NewLoggerFromEnv() (*zap.Logger, error) {
	levelText := strings.ToLower(strings.TrimSpace(env("LOG_LEVEL", "info")))
	var level zapcore.Level
	if err := level.Set(levelText); err != nil {
		return nil, fmt.Errorf("invalid LOG_LEVEL %q: %w", levelText, err)
	}
	format := strings.ToLower(strings.TrimSpace(env("LOG_FORMAT", "json")))
	if format != "json" && format != "console" {
		return nil, fmt.Errorf("invalid LOG_FORMAT %q: must be json or console", format)
	}

	config := zap.NewProductionConfig()
	config.Level = zap.NewAtomicLevelAt(level)
	config.Encoding = format
	config.OutputPaths = []string{"stderr"}
	config.ErrorOutputPaths = []string{"stderr"}
	config.EncoderConfig.TimeKey = "timestamp"
	config.EncoderConfig.MessageKey = "message"
	config.EncoderConfig.EncodeTime = zapcore.RFC3339NanoTimeEncoder
	return config.Build()
}
