// Package secrets loads secret values into the process environment so the
// existing ${VAR} placeholder mechanism in configs/config.yaml keeps working
// without changes. Two backends: dotenv (local) and SSM Parameter Store
// (Lambda). The right one is picked automatically via IsLambda().
package secrets

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/joho/godotenv"
)

// IsLambda reports whether the current process is running inside AWS Lambda.
// Lambda always sets AWS_LAMBDA_FUNCTION_NAME for the function code.
func IsLambda() bool {
	return os.Getenv("AWS_LAMBDA_FUNCTION_NAME") != ""
}

// LoadDotenv reads .env (if present) into os.Environ. Missing file is not an
// error — useful for local dev where the user may rely entirely on real env
// vars exported by their shell.
func LoadDotenv(path string) error {
	if path == "" {
		path = ".env"
	}
	if err := godotenv.Load(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("dotenv load %s: %w", path, err)
	}
	return nil
}

// LoadFromSSM reads every parameter under prefix and sets each as an env var.
// The env-var name is the parameter name minus the prefix (last "/" segment).
// Example: prefix="/auto-certs/prod/", param "/auto-certs/prod/SMTP_PASSWORD"
// becomes env var "SMTP_PASSWORD". SecureString parameters are decrypted in
// transit via WithDecryption=true; the Lambda role needs kms:Decrypt on the
// key that protects them.
func LoadFromSSM(ctx context.Context, prefix, region string) error {
	if prefix == "" {
		return errors.New("ssm load: prefix required")
	}
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	opts := []func(*awsconfig.LoadOptions) error{}
	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return fmt.Errorf("ssm aws config: %w", err)
	}
	client := ssm.NewFromConfig(cfg)

	p := ssm.NewGetParametersByPathPaginator(client, &ssm.GetParametersByPathInput{
		Path:           aws.String(prefix),
		WithDecryption: aws.Bool(true),
		Recursive:      aws.Bool(false),
	})
	count := 0
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("ssm GetParametersByPath %s: %w", prefix, err)
		}
		for _, param := range page.Parameters {
			if param.Name == nil || param.Value == nil {
				continue
			}
			name := strings.TrimPrefix(*param.Name, prefix)
			if name == "" || strings.Contains(name, "/") {
				continue
			}
			if err := os.Setenv(name, *param.Value); err != nil {
				return fmt.Errorf("setenv %s: %w", name, err)
			}
			count++
		}
	}
	slog.Info("secrets loaded from ssm", "prefix", prefix, "count", count)
	return nil
}
