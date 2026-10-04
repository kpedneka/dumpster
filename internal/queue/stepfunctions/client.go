package stepfunctions

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	"github.com/aws/aws-sdk-go-v2/service/sfn/types"
)

// realClient implements Client against the real AWS Step Functions API.
type realClient struct {
	sfn *sfn.Client
}

// NewClient builds a Client using the standard AWS credential chain (env
// vars for local dev, or the ECS task role/Lambda execution role in every
// real deployment), matching internal/queue/sqs.NewClient.
func NewClient(ctx context.Context, region string) (Client, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("stepfunctions: load AWS config: %w", err)
	}
	return &realClient{sfn: sfn.NewFromConfig(cfg)}, nil
}

// StartExecution implements Client, translating the SDK's
// ExecutionAlreadyExists into ErrExecutionAlreadyExists.
func (c *realClient) StartExecution(ctx context.Context, stateMachineARN, name, input string) error {
	_, err := c.sfn.StartExecution(ctx, &sfn.StartExecutionInput{
		StateMachineArn: aws.String(stateMachineARN),
		Name:            aws.String(name),
		Input:           aws.String(input),
	})
	var exists *types.ExecutionAlreadyExists
	if errors.As(err, &exists) {
		return fmt.Errorf("%w: %s", ErrExecutionAlreadyExists, name)
	}
	if err != nil {
		return fmt.Errorf("stepfunctions: start execution: %w", err)
	}
	return nil
}

var _ Client = (*realClient)(nil)
