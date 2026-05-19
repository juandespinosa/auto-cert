package rdap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// S3Cache stores the RDAP cache as a single JSON object in S3. Lambda is
// the only writer (one invocation at a time via EventBridge), so no locking.
// Load happens once in NewS3Cache; Flush writes only when dirty.
type S3Cache struct {
	Bucket string
	Key    string
	client *s3.Client
	ctx    context.Context
	*memBackend
}

func NewS3Cache(ctx context.Context, bucket, key, region string, ttl time.Duration) (*S3Cache, error) {
	if bucket == "" || key == "" {
		return nil, errors.New("rdap s3 cache: bucket and key required")
	}
	opts := []func(*awsconfig.LoadOptions) error{}
	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}
	c := &S3Cache{
		Bucket:     bucket,
		Key:        key,
		client:     s3.NewFromConfig(cfg),
		ctx:        ctx,
		memBackend: newMemBackend(ttl),
	}
	c.load()
	return c, nil
}

func (c *S3Cache) load() {
	out, err := c.client.GetObject(c.ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.Bucket),
		Key:    aws.String(c.Key),
	})
	if err != nil {
		var nsk *s3types.NoSuchKey
		if errors.As(err, &nsk) {
			return
		}
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "NoSuchKey" {
			return
		}
		slog.Warn("rdap s3 cache load failed", "bucket", c.Bucket, "key", c.Key, "err", err)
		return
	}
	defer out.Body.Close()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		slog.Warn("rdap s3 cache read failed", "err", err)
		return
	}
	var cf cacheFile
	if err := json.Unmarshal(data, &cf); err != nil {
		slog.Warn("rdap s3 cache parse failed", "err", err)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range cf.Entries {
		c.entries[e.Domain] = e
	}
	slog.Info("rdap cache loaded from s3", "key", c.Key, "entries", len(c.entries))
}

func (c *S3Cache) Flush(ctx context.Context) error {
	if !c.isDirty() {
		return nil
	}
	entries := c.snapshot()
	sort.Slice(entries, func(i, j int) bool { return entries[i].Domain < entries[j].Domain })
	data, err := json.MarshalIndent(cacheFile{Entries: entries}, "", "  ")
	if err != nil {
		return fmt.Errorf("rdap cache marshal: %w", err)
	}
	_, err = c.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(c.Bucket),
		Key:         aws.String(c.Key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String("application/json"),
	})
	if err != nil {
		return fmt.Errorf("rdap s3 cache put %s/%s: %w", c.Bucket, c.Key, err)
	}
	c.markClean()
	slog.Info("rdap cache flushed to s3", "key", c.Key, "entries", len(entries))
	return nil
}
