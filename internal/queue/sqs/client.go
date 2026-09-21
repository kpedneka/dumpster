package sqs

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
)

// realClient implements Client against the real AWS SQS API.
type realClient struct {
	sqs *awssqs.Client
}

// NewClient builds a Client using the standard AWS credential chain (env
// vars for local dev, or the ECS task role/Lambda execution role in every
// real deployment — whichever the environment provides), matching
// internal/awsbatch.NewClient's pattern.
func NewClient(ctx context.Context, region string) (Client, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("queue/sqs: load AWS config: %w", err)
	}
	return &realClient{sqs: awssqs.NewFromConfig(cfg)}, nil
}

func (c *realClient) SendMessage(ctx context.Context, queueURL, body string) error {
	_, err := c.sqs.SendMessage(ctx, &awssqs.SendMessageInput{
		QueueUrl:    aws.String(queueURL),
		MessageBody: aws.String(body),
	})
	if err != nil {
		return fmt.Errorf("queue/sqs: send message: %w", err)
	}
	return nil
}

var _ Client = (*realClient)(nil)
