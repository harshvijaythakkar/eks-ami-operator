package awsutils

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/smithy-go"
	"github.com/cenkalti/backoff/v4"
)

func DescribeNodegroup(ctx context.Context, eksClient *eks.Client, clusterName, nodegroupName string) (*eks.DescribeNodegroupOutput, error) {
	return retryDescribeNodegroup(ctx, eksClient, &eks.DescribeNodegroupInput{
		ClusterName:   &clusterName,
		NodegroupName: &nodegroupName,
	})
}

func DescribeCluster(ctx context.Context, eksClient *eks.Client, clusterName string) (string, error) {
	clusterOutput, err := eksClient.DescribeCluster(ctx, &eks.DescribeClusterInput{
		Name: &clusterName,
	})
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(*clusterOutput.Cluster.Version, "v"), nil
}

func ResolveLatestAMI(ctx context.Context, ssmClient *ssm.Client, amiType types.AMITypes, eksVersion string) (string, string, error) {
	var ssmPath string
	switch amiType {
	case "AL2_x86_64":
		ssmPath = fmt.Sprintf("/aws/service/eks/optimized-ami/%s/amazon-linux-2/recommended", eksVersion)
	case "AL2_ARM_64":
		ssmPath = fmt.Sprintf("/aws/service/eks/optimized-ami/%s/amazon-linux-2-arm64/recommended", eksVersion)
	case "AL2023_x86_64_STANDARD":
		ssmPath = fmt.Sprintf("/aws/service/eks/optimized-ami/%s/amazon-linux-2023/x86_64/standard/recommended", eksVersion)
	case "AL2023_ARM_64_STANDARD":
		ssmPath = fmt.Sprintf("/aws/service/eks/optimized-ami/%s/amazon-linux-2023/arm64/standard/recommended", eksVersion)
	case "CUSTOM":
		return "", "", nil
	default:
		ssmPath = fmt.Sprintf("/aws/service/eks/optimized-ami/%s/amazon-linux-2/recommended", eksVersion)
	}

	ssmOutput, err := retryGetParameter(ctx, ssmClient, &ssm.GetParameterInput{
		Name:           &ssmPath,
		WithDecryption: aws.Bool(false),
	})
	if err != nil {
		return "", "", err
	}

	var amiMetadata struct {
		ImageID        string `json:"image_id"`
		ReleaseVersion string `json:"release_version"`
	}
	if err := json.Unmarshal([]byte(*ssmOutput.Parameter.Value), &amiMetadata); err != nil {
		return "", "", err
	}

	return amiMetadata.ImageID, amiMetadata.ReleaseVersion, nil
}

func retryDescribeNodegroup(ctx context.Context, eksClient *eks.Client, input *eks.DescribeNodegroupInput) (*eks.DescribeNodegroupOutput, error) {
	var output *eks.DescribeNodegroupOutput

	operation := func() error {
		var err error
		output, err = eksClient.DescribeNodegroup(ctx, input)
		if err != nil {
			var apiErr smithy.APIError
			if errors.As(err, &apiErr) {
				switch apiErr.ErrorCode() {
				case "ResourceNotFoundException", "InvalidParameterException":
					logf.FromContext(ctx).Error(err, "Permanent error during DescribeNodegroup, skipping retries", "errorCode", apiErr.ErrorCode(), "clusterName", *input.ClusterName, "nodegroupName", *input.NodegroupName)
					// Return backoff.Permanent to stop retrying
					return backoff.Permanent(err)
				}
			}
		}
		return err
	}

	expBackoff := backoff.NewExponentialBackOff()
	expBackoff.InitialInterval = 2 * time.Second
	expBackoff.MaxElapsedTime = 30 * time.Second

	err := backoff.Retry(operation, expBackoff)
	return output, err
}

func retryGetParameter(ctx context.Context, ssmClient *ssm.Client, input *ssm.GetParameterInput) (*ssm.GetParameterOutput, error) {
	var output *ssm.GetParameterOutput

	operation := func() error {
		var err error
		output, err = ssmClient.GetParameter(ctx, input)
		if err != nil {
			var apiErr smithy.APIError
			if errors.As(err, &apiErr) {
				switch apiErr.ErrorCode() {
				case "ParameterNotFound", "InvalidParameter":
					logf.FromContext(ctx).Error(err, "Permanent error during GetParameter, skipping retries", "errorCode", apiErr.ErrorCode(), "parameterName", *input.Name)
					return backoff.Permanent(err)
				}
			}
		}
		return err
	}

	expBackoff := backoff.NewExponentialBackOff()
	expBackoff.InitialInterval = 2 * time.Second
	expBackoff.MaxElapsedTime = 30 * time.Second

	err := backoff.Retry(operation, expBackoff)
	return output, err
}
