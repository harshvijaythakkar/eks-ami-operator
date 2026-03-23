package awsutils

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
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

// package-level RNG, seeded once
var rng = rand.New(rand.NewSource(time.Now().UnixNano()))

// AL2UnsupportedMinorCutoff defines the EKS minor version at/after which AL2
// optimized AMIs are not published by AWS (e.g., 1.33 => 33). Adjust if AWS policy changes.
const AL2UnsupportedMinorCutoff = 33

// ErrAL2UnsupportedOnCluster indicates the requested AL2 AMI type is not
// published for the current EKS cluster version.
var ErrAL2UnsupportedOnCluster = errors.New("al2 ami not published for this eks version")

// UpdateOutcome is a generic view of an EKS update lifecycle state.
type UpdateOutcome struct {
	ID        string
	Status    types.UpdateStatus // typed: InProgress | Successful | Failed | Cancelled
	Errors    []string           // "<code>: <message>" pairs
	CreatedAt *time.Time
	Type      string // e.g., "VersionUpdate"
}

// DescribeNodegroup wraps retry logic.
func DescribeNodegroup(ctx context.Context, eksClient *eks.Client, clusterName, nodegroupName string) (*eks.DescribeNodegroupOutput, error) {
	return retryDescribeNodegroup(ctx, eksClient, &eks.DescribeNodegroupInput{
		ClusterName:   &clusterName,
		NodegroupName: &nodegroupName,
	})
}

// DescribeCluster returns EKS version (without 'v').
func DescribeCluster(ctx context.Context, eksClient *eks.Client, clusterName string) (string, error) {
	clusterOutput, err := eksClient.DescribeCluster(ctx, &eks.DescribeClusterInput{
		Name: &clusterName,
	})
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(*clusterOutput.Cluster.Version, "v"), nil
}

// ResolveLatestAMI builds SSM path based on AMI type + EKS version, and returns (imageID, releaseVersion).
func ResolveLatestAMI(ctx context.Context, ssmClient *ssm.Client, amiType types.AMITypes, eksVersion string) (string, string, error) {
	var ssmPath string
	isAL2 := amiType == types.AMITypesAl2X8664 || amiType == types.AMITypesAl2Arm64
	minor, _ := eksMinor(eksVersion) // best-effort; if parse fails, minor == 0 and guards below won’t trigger

	// Guard: AL2 is not published for newer EKS minors (e.g., >= 1.33).
	if isAL2 && minor >= AL2UnsupportedMinorCutoff {
		return "", "", ErrAL2UnsupportedOnCluster
	}

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
		// Default path: for older minors (< cutoff), AL2; for newer minors (>= cutoff), prefer AL2023 x86_64 standard.
		if minor >= AL2UnsupportedMinorCutoff {
			ssmPath = fmt.Sprintf("/aws/service/eks/optimized-ami/%s/amazon-linux-2023/x86_64/standard/recommended", eksVersion)
		} else {
			ssmPath = fmt.Sprintf("/aws/service/eks/optimized-ami/%s/amazon-linux-2/recommended", eksVersion)
		}
	}

	ssmInput := &ssm.GetParameterInput{
		Name:           &ssmPath,
		WithDecryption: aws.Bool(false),
	}
	ssmOutput, err := retryGetParameter(ctx, ssmClient, ssmInput)
	if err != nil {
		// If SSM returns ParameterNotFound and we're requesting AL2 for a newer minor, map to sentinel error.
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "ParameterNotFound" {
			if isAL2 && minor >= AL2UnsupportedMinorCutoff {
				return "", "", ErrAL2UnsupportedOnCluster
			}
		}
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

// func retryDescribeNodegroup(ctx context.Context, eksClient *eks.Client, input *eks.DescribeNodegroupInput) (*eks.DescribeNodegroupOutput, error) {
// 	var output *eks.DescribeNodegroupOutput

// 	operation := func() error {
// 		var err error
// 		output, err = eksClient.DescribeNodegroup(ctx, input)
// 		if err != nil {
// 			var apiErr smithy.APIError
// 			if errors.As(err, &apiErr) {
// 				switch apiErr.ErrorCode() {
// 				case "ResourceNotFoundException", "InvalidParameterException":
// 					logf.FromContext(ctx).Error(err, "Permanent error during DescribeNodegroup, skipping retries", "errorCode", apiErr.ErrorCode(), "clusterName", *input.ClusterName, "nodegroupName", *input.NodegroupName)
// 					// Return backoff.Permanent to stop retrying
// 					return backoff.Permanent(err)
// 				}
// 			}
// 		}
// 		return err
// 	}

// 	expBackoff := backoff.NewExponentialBackOff()
// 	expBackoff.InitialInterval = 2 * time.Second
// 	expBackoff.MaxElapsedTime = 30 * time.Second

// 	err := backoff.Retry(operation, expBackoff)
// 	return output, err
// }

// func retryGetParameter(ctx context.Context, ssmClient *ssm.Client, input *ssm.GetParameterInput) (*ssm.GetParameterOutput, error) {
// 	var output *ssm.GetParameterOutput

// 	operation := func() error {
// 		var err error
// 		output, err = ssmClient.GetParameter(ctx, input)
// 		if err != nil {
// 			var apiErr smithy.APIError
// 			if errors.As(err, &apiErr) {
// 				switch apiErr.ErrorCode() {
// 				case "ParameterNotFound", "InvalidParameter":
// 					logf.FromContext(ctx).Error(err, "Permanent error during GetParameter, skipping retries", "errorCode", apiErr.ErrorCode(), "parameterName", *input.Name)
// 					return backoff.Permanent(err)
// 				}
// 			}
// 		}
// 		return err
// 	}

// 	expBackoff := backoff.NewExponentialBackOff()
// 	expBackoff.InitialInterval = 2 * time.Second
// 	expBackoff.MaxElapsedTime = 30 * time.Second

// 	err := backoff.Retry(operation, expBackoff)
// 	return output, err
// }

// --- Retry helpers with jittered backoff ---
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
					logf.FromContext(ctx).Error(err, "Permanent error during DescribeNodegroup, skipping retries",
						"errorCode", apiErr.ErrorCode(), "clusterName", *input.ClusterName, "nodegroupName", *input.NodegroupName)
					return backoff.Permanent(err)
				}
			}
		}
		return err
	}
	expBackoff := newJitterBackoff(2*time.Second, 30*time.Second)
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
					logf.FromContext(ctx).Error(err, "Permanent error during GetParameter, skipping retries",
						"errorCode", apiErr.ErrorCode(), "parameterName", *input.Name)
					return backoff.Permanent(err)
				}
			}
		}
		return err
	}
	expBackoff := newJitterBackoff(2*time.Second, 30*time.Second)
	err := backoff.Retry(operation, expBackoff)
	return output, err
}

// newJitterBackoff creates an exponential backoff with slight randomized initial delay.
func newJitterBackoff(initial, maxElapsed time.Duration) *backoff.ExponentialBackOff {
	exp := backoff.NewExponentialBackOff()
	// Add up to 500ms jitter to the initial interval to avoid lockstep starts
	exp.InitialInterval = initial + time.Duration(rng.Intn(500))*time.Millisecond
	exp.MaxElapsedTime = maxElapsed
	return exp
}

// DescribeNodegroupUpdate returns the current outcome for a managed node group update.
func DescribeNodegroupUpdate(ctx context.Context, eksClient *eks.Client, cluster, nodegroup, updateID string) (*UpdateOutcome, error) {
	out, err := eksClient.DescribeUpdate(ctx, &eks.DescribeUpdateInput{
		Name:          aws.String(cluster),
		NodegroupName: aws.String(nodegroup),
		UpdateId:      aws.String(updateID),
	})
	if err != nil {
		return nil, err
	}
	u := out.Update
	// var errs []string
	errs := make([]string, 0, len(u.Errors))
	for _, e := range u.Errors {
		code := strings.TrimSpace(string(e.ErrorCode))
		msg := strings.TrimSpace(aws.ToString(e.ErrorMessage))
		if code == "" && msg == "" {
			continue
		}
		if code == "" {
			code = "Unknown"
		}
		errs = append(errs, fmt.Sprintf("%s: %s", code, msg))
	}
	return &UpdateOutcome{
		ID:        aws.ToString(u.Id),
		Status:    u.Status,
		Errors:    errs,
		CreatedAt: u.CreatedAt,
		Type:      string(u.Type),
	}, nil
}
