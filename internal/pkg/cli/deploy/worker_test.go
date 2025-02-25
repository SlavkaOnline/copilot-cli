// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package deploy

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/copilot-cli/internal/pkg/cli/deploy/mocks"
	"github.com/aws/copilot-cli/internal/pkg/config"
	"github.com/aws/copilot-cli/internal/pkg/deploy"
	"github.com/aws/copilot-cli/internal/pkg/deploy/cloudformation/stack"
	"github.com/aws/copilot-cli/internal/pkg/manifest"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"
)

func TestSvcDeployOpts_stackConfiguration_worker(t *testing.T) {
	mockError := errors.New("some error")
	topic, _ := deploy.NewTopic("arn:aws:sns:us-west-2:0123456789012:mockApp-mockEnv-mockwkld-givesdogs", "mockApp", "mockEnv", "mockwkld")
	const (
		mockAppName = "mockApp"
		mockEnvName = "mockEnv"
		mockName    = "mockwkld"
		mockBucket  = "mockBucket"
	)
	mockResources := &stack.AppRegionalResources{
		S3Bucket: mockBucket,
	}
	mockTopics := []manifest.TopicSubscription{
		{
			Name:    aws.String("givesdogs"),
			Service: aws.String("mockwkld"),
		},
	}
	tests := map[string]struct {
		inAlias        string
		inApp          *config.Application
		inEnvironment  *config.Environment
		inBuildRequire bool

		mock func(m *deployMocks)

		wantErr             error
		wantedSubscriptions []manifest.TopicSubscription
	}{
		"fail to get deployed topics": {
			inEnvironment: &config.Environment{
				Name:   mockEnvName,
				Region: "us-west-2",
			},
			inApp: &config.Application{
				Name:   mockAppName,
				Domain: "mockDomain",
			},
			mock: func(m *deployMocks) {
				m.mockEndpointGetter.EXPECT().ServiceDiscoveryEndpoint().Return("mockApp.local", nil)
				m.mockEnvVersionGetter.EXPECT().Version().Return("v1.42.0", nil)
				m.mockSNSTopicsLister.EXPECT().ListSNSTopics(mockAppName, mockEnvName).Return(nil, mockError)
			},
			wantErr: fmt.Errorf("get SNS topics for app mockApp and environment mockEnv: %w", mockError),
		},
		"success": {
			inEnvironment: &config.Environment{
				Name:   mockEnvName,
				Region: "us-west-2",
			},
			inApp: &config.Application{
				Name:   mockAppName,
				Domain: "mockDomain",
			},
			mock: func(m *deployMocks) {
				m.mockEndpointGetter.EXPECT().ServiceDiscoveryEndpoint().Return("mockEnv.mockApp.local", nil)
				m.mockEnvVersionGetter.EXPECT().Version().Return("v1.42.0", nil)
				m.mockSNSTopicsLister.EXPECT().ListSNSTopics(mockAppName, mockEnvName).Return([]deploy.Topic{
					*topic,
				}, nil)
			},
			wantedSubscriptions: mockTopics,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			m := &deployMocks{
				mockEndpointGetter:   mocks.NewMockendpointGetter(ctrl),
				mockSNSTopicsLister:  mocks.NewMocksnsTopicsLister(ctrl),
				mockEnvVersionGetter: mocks.NewMockversionGetter(ctrl),
			}
			tc.mock(m)

			deployer := workerSvcDeployer{
				svcDeployer: &svcDeployer{
					workloadDeployer: &workloadDeployer{
						name:             mockName,
						app:              tc.inApp,
						env:              tc.inEnvironment,
						resources:        mockResources,
						endpointGetter:   m.mockEndpointGetter,
						envVersionGetter: m.mockEnvVersionGetter,
					},
					newSvcUpdater: func(f func(*session.Session) serviceForceUpdater) serviceForceUpdater {
						return nil
					},
				},
				topicLister: m.mockSNSTopicsLister,
				wsMft: &manifest.WorkerService{
					Workload: manifest.Workload{
						Name: aws.String(mockName),
					},
					WorkerServiceConfig: manifest.WorkerServiceConfig{
						ImageConfig: manifest.ImageWithHealthcheck{
							Image: manifest.Image{
								Build: manifest.BuildArgsOrString{BuildString: aws.String("/Dockerfile")},
							},
						},
						Subscribe: manifest.SubscribeConfig{
							Topics: mockTopics,
						},
					},
				},
			}

			got, gotErr := deployer.stackConfiguration(&StackRuntimeConfiguration{})

			if tc.wantErr != nil {
				require.EqualError(t, gotErr, tc.wantErr.Error())
			} else {
				require.NoError(t, gotErr)
				require.ElementsMatch(t, tc.wantedSubscriptions, got.subscriptions)
			}
		})
	}
}

func Test_validateTopicsExist(t *testing.T) {
	mockApp := "app"
	mockEnv := "env"
	mockAllowedTopics := []string{
		"arn:aws:sqs:us-west-2:123456789012:app-env-database-events",
		"arn:aws:sqs:us-west-2:123456789012:app-env-database-orders",
		"arn:aws:sqs:us-west-2:123456789012:app-env-api-events",
	}
	duration10Hours := 10 * time.Hour
	testGoodTopics := []manifest.TopicSubscription{
		{
			Name:    aws.String("events"),
			Service: aws.String("database"),
		},
		{
			Name:    aws.String("orders"),
			Service: aws.String("database"),
			Queue: manifest.SQSQueueOrBool{
				Advanced: manifest.SQSQueue{
					Retention: &duration10Hours,
				},
			},
		},
	}
	testCases := map[string]struct {
		inTopics    []manifest.TopicSubscription
		inTopicARNs []string

		wantErr string
	}{
		"empty subscriptions": {
			inTopics:    nil,
			inTopicARNs: mockAllowedTopics,
		},
		"topics are valid": {
			inTopics:    testGoodTopics,
			inTopicARNs: mockAllowedTopics,
		},
		"topic is invalid": {
			inTopics:    testGoodTopics,
			inTopicARNs: []string{},
			wantErr:     "SNS topic app-env-database-events does not exist in environment env",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			err := validateTopicsExist(tc.inTopics, tc.inTopicARNs, mockApp, mockEnv)
			if tc.wantErr != "" {
				require.EqualError(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
