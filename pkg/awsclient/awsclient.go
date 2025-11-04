package awsclient

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ctrl "sigs.k8s.io/controller-runtime"
)

var logger = ctrl.Log.WithName("awsclient")

// AWSClients holds initialized AWS service clients
type AWSClients struct {
	EKS *eks.Client
	SSM *ssm.Client
	EC2 *ec2.Client
	// Add more clients here as needed
}

// NewAWSClients initializes AWS clients with region override and default credential chain
func NewAWSClients(ctx context.Context, region string) (*AWSClients, error) {
	logger.Info("Loading AWS config", "region", region)

	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		logger.Error(err, "Failed to load AWS config")
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	logger.Info("Successfully loaded AWS config")

	clients := &AWSClients{
		EKS: eks.NewFromConfig(cfg),
		SSM: ssm.NewFromConfig(cfg),
		EC2: ec2.NewFromConfig(cfg),
	}
	return clients, nil
}
