// Binary lambda is the AWS Lambda entry point. Triggered by EventBridge on a
// cron schedule. It loads secrets from SSM Parameter Store, reads config from
// a bundled YAML (CONFIG_PATH env var), and runs the same pipeline as
// cmd/monitor — only the storage and notifier backends differ.
//
// Required env vars (set by the SAM template):
//   CONFIG_PATH  — path to YAML inside the deployment package, e.g. /var/task/config.lambda.yaml
//   SSM_PREFIX   — path prefix for parameter store, e.g. /auto-certs/prod/
//
// Optional:
//   AWS_REGION   — picked up automatically by the AWS SDK
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"

	"auto-certs/internal/config"
	"auto-certs/internal/runner"
	"auto-certs/internal/secrets"
)

// handler runs on every EventBridge trigger. The event payload is ignored —
// the schedule itself is the only signal we need.
func handler(ctx context.Context, _ events.CloudWatchEvent) error {
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "config.yaml"
	}

	// SSM loads secret env vars BEFORE config.Load so ${VAR} expansion in
	// the YAML resolves to the real values.
	ssmPrefix := os.Getenv("SSM_PREFIX")
	if ssmPrefix != "" {
		if err := secrets.LoadFromSSM(ctx, ssmPrefix, os.Getenv("AWS_REGION")); err != nil {
			return fmt.Errorf("ssm load: %w", err)
		}
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("config load %s: %w", configPath, err)
	}

	slog.Info("auto-certs lambda starting",
		"thresholds_days", cfg.Thresholds,
		"notifier_backend", cfg.Notifier.Backend,
		"state_backend", cfg.State.Backend,
		"inventory_backend", cfg.Inventory.Backend,
	)

	deps, err := runner.BuildDeps(ctx, cfg, false)
	if err != nil {
		return fmt.Errorf("build deps: %w", err)
	}
	return runner.Run(ctx, deps)
}

func main() {
	// CloudWatch parses JSON logs into structured fields — much better
	// filtering than the default text format.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))
	lambda.Start(handler)
}
