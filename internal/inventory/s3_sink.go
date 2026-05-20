package inventory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3Sink writes one inventory snapshot per run, dated, under a key prefix.
// Example: prefix="auto-certs/inventory/" → key "auto-certs/inventory/2026-05-19.json".
// Pair with an S3 lifecycle rule to expire old objects (e.g., 90 days).
type S3Sink struct {
	Bucket    string
	KeyPrefix string
	client    *s3.Client
	ctx       context.Context
}

func NewS3Sink(ctx context.Context, bucket, keyPrefix, region string) (*S3Sink, error) {
	if bucket == "" {
		return nil, errors.New("s3 sink: bucket required")
	}
	if keyPrefix != "" && !strings.HasSuffix(keyPrefix, "/") {
		keyPrefix += "/"
	}
	opts := []func(*awsconfig.LoadOptions) error{}
	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}
	return &S3Sink{
		Bucket:    bucket,
		KeyPrefix: keyPrefix,
		client:    s3.NewFromConfig(cfg),
		ctx:       ctx,
	}, nil
}

func (s *S3Sink) Save(snap Snapshot) error {
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("inventory marshal: %w", err)
	}
	base := s.KeyPrefix + snap.GeneratedAt.UTC().Format("2006-01-02")
	if err := s.put(base+".json", data, "application/json"); err != nil {
		return err
	}

	xlsxData, err := MarshalXLSX(snap)
	if err != nil {
		return fmt.Errorf("inventory xlsx: %w", err)
	}
	if err := s.put(base+".xlsx", xlsxData,
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"); err != nil {
		return err
	}
	return nil
}

func (s *S3Sink) put(key string, data []byte, contentType string) error {
	_, err := s.client.PutObject(s.ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.Bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("s3 put %s/%s: %w", s.Bucket, key, err)
	}
	return nil
}
