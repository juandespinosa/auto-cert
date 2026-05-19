package notify

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	sestypes "github.com/aws/aws-sdk-go-v2/service/sesv2/types"

	"auto-certs/internal/model"
)

// SES sends alerts via AWS SES v2. From identity must be verified in SES
// console (domain identity is preferred — gives SPF/DKIM "for free"). Use
// this in Lambda; for local dev SMTP or DryRun is simpler.
type SES struct {
	From   string
	To     []string
	client *sesv2.Client
	ctx    context.Context
}

func NewSES(ctx context.Context, from string, to []string, region string) (*SES, error) {
	if from == "" || len(to) == 0 {
		return nil, errors.New("ses: from and at least one to required")
	}
	opts := []func(*awsconfig.LoadOptions) error{}
	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("aws config: %w", err)
	}
	return &SES{
		From:   from,
		To:     to,
		client: sesv2.NewFromConfig(cfg),
		ctx:    ctx,
	}, nil
}

func (s *SES) Notify(alerts []model.Alert, summary Summary) error {
	if len(alerts) == 0 {
		return nil
	}
	now := time.Now()
	subject, plain := Render(alerts, summary, now)
	htmlBody := RenderHTML(alerts, summary, now)

	_, err := s.client.SendEmail(s.ctx, &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(s.From),
		Destination: &sestypes.Destination{
			ToAddresses: s.To,
		},
		Content: &sestypes.EmailContent{
			Simple: &sestypes.Message{
				Subject: &sestypes.Content{
					Data:    aws.String(subject),
					Charset: aws.String("UTF-8"),
				},
				Body: &sestypes.Body{
					Text: &sestypes.Content{
						Data:    aws.String(plain),
						Charset: aws.String("UTF-8"),
					},
					Html: &sestypes.Content{
						Data:    aws.String(htmlBody),
						Charset: aws.String("UTF-8"),
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("ses send: %w", err)
	}
	return nil
}
