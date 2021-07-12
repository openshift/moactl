/*
Copyright (c) 2020 Red Hat, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

  http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package aws

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/arn"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/client"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/cloudformation"
	"github.com/aws/aws-sdk-go/service/cloudformation/cloudformationiface"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/ec2/ec2iface"
	"github.com/aws/aws-sdk-go/service/iam"
	"github.com/aws/aws-sdk-go/service/iam/iamiface"
	"github.com/aws/aws-sdk-go/service/organizations"
	"github.com/aws/aws-sdk-go/service/organizations/organizationsiface"
	"github.com/aws/aws-sdk-go/service/servicequotas"
	"github.com/aws/aws-sdk-go/service/servicequotas/servicequotasiface"
	"github.com/aws/aws-sdk-go/service/sts"
	"github.com/aws/aws-sdk-go/service/sts/stsiface"
	"github.com/sirupsen/logrus"

	"github.com/openshift/rosa/pkg/aws/profile"
	"github.com/openshift/rosa/pkg/aws/tags"
	"github.com/openshift/rosa/pkg/logging"
)

// Name of the AWS user that will be used to create all the resources of the cluster:
const (
	AdminUserName        = "osdCcsAdmin"
	OsdCcsAdminStackName = "osdCcsAdminIAMUser"

	// Since CloudFormation stacks are region-dependent, we hard-code OCM's default region and
	// then use it to ensure that the user always gets the stack from the same region.
	DefaultRegion = "us-east-1"
)

// Client defines a client interface
type Client interface {
	CheckAdminUserNotExisting(userName string) (err error)
	CheckStackReadyOrNotExisting(stackName string) (stackReady bool, stackStatus *string, err error)
	GetIAMCredentials() (credentials.Value, error)
	GetRegion() string
	ValidateCredentials() (isValid bool, isSTS bool, err error)
	EnsureOsdCcsAdminUser(stackName string, adminUserName string, awsRegion string) (bool, error)
	DeleteOsdCcsAdminUser(stackName string) error
	GetAWSAccessKeys() (*AccessKey, error)
	GetCreator() (*Creator, error)
	ValidateSCP(*string) (bool, error)
	GetSubnetIDs() ([]*ec2.Subnet, error)
	ValidateQuota() (bool, error)
	TagUserRegion(username string, region string) error
	GetClusterRegionTagForUser(username string) (string, error)
}

// ClientBuilder contains the information and logic needed to build a new AWS client.
type ClientBuilder struct {
	logger      *logrus.Logger
	region      *string
	credentials *AccessKey
}

type awsClient struct {
	logger              *logrus.Logger
	iamClient           iamiface.IAMAPI
	ec2Client           ec2iface.EC2API
	orgClient           organizationsiface.OrganizationsAPI
	stsClient           stsiface.STSAPI
	cfClient            cloudformationiface.CloudFormationAPI
	servicequotasClient servicequotasiface.ServiceQuotasAPI
	awsSession          *session.Session
	awsAccessKeys       *AccessKey
}

// NewClient creates a builder that can then be used to configure and build a new AWS client.
func NewClient() *ClientBuilder {
	return &ClientBuilder{}
}

func New(
	logger *logrus.Logger,
	iamClient iamiface.IAMAPI,
	ec2Client ec2iface.EC2API,
	orgClient organizationsiface.OrganizationsAPI,
	stsClient stsiface.STSAPI,
	cfClient cloudformationiface.CloudFormationAPI,
	servicequotasClient servicequotasiface.ServiceQuotasAPI,
	awsSession *session.Session,
	awsAccessKeys *AccessKey,

) Client {
	return &awsClient{
		logger,
		iamClient,
		ec2Client,
		orgClient,
		stsClient,
		cfClient,
		servicequotasClient,
		awsSession,
		awsAccessKeys,
	}
}

// Logger sets the logger that the AWS client will use to send messages to the log.
func (b *ClientBuilder) Logger(value *logrus.Logger) *ClientBuilder {
	b.logger = value
	return b
}

func (b *ClientBuilder) Region(value string) *ClientBuilder {
	b.region = aws.String(value)
	return b
}

func (b *ClientBuilder) AccessKeys(value *AccessKey) *ClientBuilder {
	// fmt.Printf("Using new access key %s\n", value.AccessKeyID)
	b.credentials = value
	return b
}

// Create AWS session with a specific set of credentials
func (b *ClientBuilder) BuildSessionWithOptionsCredentials(value *AccessKey) (*session.Session, error) {
	return session.NewSessionWithOptions(session.Options{
		Config: aws.Config{
			CredentialsChainVerboseErrors: aws.Bool(true),
			Region:                        b.region,
			Credentials:                   credentials.NewStaticCredentials(value.AccessKeyID, value.SecretAccessKey, ""),
		},
	})
}

func (b *ClientBuilder) BuildSessionWithOptions() (*session.Session, error) {
	return session.NewSessionWithOptions(session.Options{
		SharedConfigState: session.SharedConfigEnable,
		Profile:           profile.Profile(),
		Config: aws.Config{
			CredentialsChainVerboseErrors: aws.Bool(true),
			Region:                        b.region,
		},
	})
}

// Build uses the information stored in the builder to build a new AWS client.
func (b *ClientBuilder) Build() (Client, error) {
	// Check parameters:
	if b.logger == nil {
		return nil, fmt.Errorf("logger is mandatory")
	}

	// Create the AWS logger:
	logger, err := logging.NewAWSLogger().
		Logger(b.logger).
		Build()
	if err != nil {
		return nil, err
	}

	var sess *session.Session

	// Create the AWS session:
	if b.credentials != nil {
		sess, err = b.BuildSessionWithOptionsCredentials(b.credentials)
	} else {
		sess, err = b.BuildSessionWithOptions()
	}
	if err != nil {
		return nil, err
	}

	if profile.Profile() != "" {
		b.logger.Debugf("Using AWS profile: %s", profile.Profile())
	}

	// Check that the AWS credentials are available:
	// TODO: No need to do this twice, we're essentially doing the
	// same thing in getClientDetails()
	// We should implement getClientDetails() here or a new validation func
	_, err = sess.Config.Credentials.Get()
	if err != nil {
		b.logger.Debugf("Failed to find credentials: %v", err)
		return nil, fmt.Errorf("failed to find credentials. Check your AWS configuration and try again")
	}

	// Check that the region is set:
	region := aws.StringValue(sess.Config.Region)
	if region == "" {
		return nil, fmt.Errorf("region is not set. Use --region to set the region")
	}

	// Update session config
	sess = sess.Copy(&aws.Config{
		// MaxRetries to limit the number of attempts on failed API calls
		MaxRetries: aws.Int(25),
		// Set MinThrottleDelay to 1 second
		Retryer: client.DefaultRetryer{
			NumMaxRetries:    5,
			MinThrottleDelay: 1 * time.Second,
		},
		Logger: logger,
		HTTPClient: &http.Client{
			Transport: http.DefaultTransport,
		},
	})

	if b.logger.IsLevelEnabled(logrus.DebugLevel) {
		var dumper http.RoundTripper
		dumper, err = logging.NewRoundTripper().
			Logger(b.logger).
			Next(sess.Config.HTTPClient.Transport).
			Build()
		if err != nil {
			return nil, err
		}
		sess.Config.HTTPClient.Transport = dumper
	}

	// Create and populate the object:
	c := &awsClient{
		logger:              b.logger,
		iamClient:           iam.New(sess),
		ec2Client:           ec2.New(sess),
		orgClient:           organizations.New(sess),
		stsClient:           sts.New(sess),
		cfClient:            cloudformation.New(sess),
		servicequotasClient: servicequotas.New(sess),
		awsSession:          sess,
	}

	_, root, err := getClientDetails(c)
	if err != nil {
		return nil, err
	}

	if root {
		return nil, errors.New("Using a root account is not supported, please use an IAM user instead.")
	}

	return c, err
}

func (c *awsClient) GetIAMCredentials() (credentials.Value, error) {
	return c.awsSession.Config.Credentials.Get()
}

func (c *awsClient) GetRegion() string {
	return aws.StringValue(c.awsSession.Config.Region)
}

// GetSubnetIDs will return the list of subnetsIDs supported for the region picked.
func (c *awsClient) GetSubnetIDs() ([]*ec2.Subnet, error) {
	res, err := c.ec2Client.DescribeSubnets(&ec2.DescribeSubnetsInput{})
	if err != nil {
		return nil, err
	}
	return res.Subnets, nil
}

type Creator struct {
	ARN       string
	AccountID string
	IsSTS     bool
}

func (c *awsClient) GetCreator() (*Creator, error) {
	getCallerIdentityOutput, err := c.stsClient.GetCallerIdentity(&sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, err
	}

	creatorARN := aws.StringValue(getCallerIdentityOutput.Arn)

	// Extract the account identifier from the ARN of the user:
	creatorParsedARN, err := arn.Parse(creatorARN)
	if err != nil {
		return nil, err
	}

	// If the user is STS resolve the Role the user has assumed
	var stsRole *string
	if isSTS(creatorParsedARN) {
		stsRole, err = resolveSTSRole(creatorParsedARN)
		if err != nil {
			return nil, err
		}

		// resolveSTSRole esures a parsed valid ARN before
		// returning it so we don't need to parse it again
		creatorARN = *stsRole
	}

	return &Creator{
		ARN:       creatorARN,
		AccountID: creatorParsedARN.AccountID,
		IsSTS:     isSTS(creatorParsedARN),
	}, nil
}

// Checks if given credentials are valid.
func (c *awsClient) ValidateCredentials() (bool, bool, error) {
	// Validate the AWS credentials by calling STS GetCallerIdentity
	// This will fail if the AWS access key and secret key are invalid. This
	// will also work for STS credentials with access key, secret key and session
	// token
	_, err := c.stsClient.GetCallerIdentity(&sts.GetCallerIdentityInput{})
	if err != nil {
		return false, false, err
	}

	return true, false, nil
}

// Ensure osdCcsAdmin IAM user is created
func (c *awsClient) EnsureOsdCcsAdminUser(stackName string, adminUserName string, awsRegion string) (bool, error) {
	// Check already existing cloudformation stack status
	stackReady, stackStatus, err := c.CheckStackReadyOrNotExisting(stackName)
	if err != nil {
		return false, err
	}

	// Read cloudformation template
	cfTemplateBody, err := readCFTemplate()
	if err != nil {
		return false, err
	}

	// If stack CREATE_COMPLETE or UPGRADE_COMPLETE the stack is already create
	// try to update it in case the cloudformation template has changed
	if stackStatus != nil {
		if (*stackStatus == cloudformation.StackStatusCreateComplete) ||
			(*stackStatus == cloudformation.StackStatusUpdateComplete) {
			_, err = c.UpdateStack(cfTemplateBody, stackName)
			if err != nil {
				return false, err
			}

			return false, nil
		}
	}

	// If the Cloudformation stack isn't ready, make sure the IAM user
	// doesn't exist or the Cloudformation stack create will fail
	if !stackReady {
		err = c.CheckAdminUserNotExisting(adminUserName)
		if err != nil {
			return false, err
		}
	}

	// Create stack
	_, err = c.CreateStack(cfTemplateBody, stackName)
	if err != nil {
		return false, err
	}

	err = c.TagUserRegion(adminUserName, awsRegion)
	if err != nil {
		return false, err
	}
	return true, nil
}

func (c *awsClient) CreateStack(cfTemplateBody, stackName string) (bool, error) {
	// Create cloudformation stack
	_, err := c.cfClient.CreateStack(buildCreateStackInput(cfTemplateBody, stackName))
	if err != nil {
		return false, err
	}

	// Wait until cloudformation stack creates
	err = c.cfClient.WaitUntilStackCreateComplete(&cloudformation.DescribeStacksInput{
		StackName: aws.String(stackName),
	})
	if err != nil {
		switch typed := err.(type) {
		case awserr.Error:
			// Waiter reached maximum attempts waiting for the resource to be ready
			if typed.Code() == request.WaiterResourceNotReadyErrorCode {
				c.logger.Errorf("Max retries reached waiting for stack to create.")
				return false, err
			}
		}
		return false, err
	}

	return true, nil
}

func (c *awsClient) UpdateStack(cfTemplateBody, stackName string) (bool, error) {
	_, err := c.cfClient.UpdateStack(buildUpdateStackInput(cfTemplateBody, stackName))
	if err != nil {
		switch typed := err.(type) {
		case awserr.Error:
			// Exit true if there is no update to be performed on the cloudformation stack
			if typed.Code() == "ValidationError" {
				if typed.Message() == "No updates are to be performed." {
					return true, nil
				}
			}
		}
		return false, err
	}

	// Wait for CloudFormation update to complete
	err = c.cfClient.WaitUntilStackUpdateComplete(&cloudformation.DescribeStacksInput{
		StackName: aws.String(stackName),
	})

	if err != nil {
		switch typed := err.(type) {
		case awserr.Error:
			// Waiter reached maximum attempts waiting for the resource to be ready
			if typed.Code() == request.WaiterResourceNotReadyErrorCode {
				c.logger.Errorf("Max retries reached waiting for stack to create.")
				return false, err
			}
		}
		return false, err
	}

	return true, err
}

func (c *awsClient) CheckStackReadyOrNotExisting(stackName string) (stackReady bool, status *string, err error) {
	stackList, err := c.cfClient.ListStacks(&cloudformation.ListStacksInput{})
	if err != nil {
		return false, nil, err
	}

	for _, summary := range stackList.StackSummaries {
		if *summary.StackName == stackName {
			if (*summary.StackStatus == cloudformation.StackStatusCreateComplete) ||
				(*summary.StackStatus == cloudformation.StackStatusUpdateComplete) {
				return true, summary.StackStatus, nil
			}
			if *summary.StackStatus != cloudformation.StackStatusDeleteComplete {
				return false, summary.StackStatus, fmt.Errorf("Error creating user: Cloudformation stack %s exists "+
					"with status %s. Expected status is %s.\n"+
					"Ensure %s CloudFormation Stack does not exist, then retry with\n"+
					"rosa init --delete-stack; rosa init",
					*summary.StackName, *summary.StackStatus, cloudformation.StackStatusCreateComplete, *summary.StackName)
			}
		}
	}
	return false, nil, nil
}

func (c *awsClient) CheckAdminUserNotExisting(userName string) (err error) {
	userList, err := c.iamClient.ListUsers(&iam.ListUsersInput{})
	if err != nil {
		return err
	}
	for _, user := range userList.Users {
		if *user.UserName == userName {
			return fmt.Errorf("Error creating user: IAM user '%s' already exists.\n"+
				"Ensure user '%s' IAM user does not exist, then retry with\n"+
				"rosa init",
				*user.UserName, *user.UserName)
		}
	}
	return nil
}

func (c *awsClient) DeleteOsdCcsAdminUser(stackName string) error {
	deleteStackInput := &cloudformation.DeleteStackInput{
		StackName: aws.String(stackName),
	}

	// Delete cloudformation stack
	_, err := c.cfClient.DeleteStack(deleteStackInput)
	if err != nil {
		switch typed := err.(type) {
		case awserr.Error:
			if typed.Code() == cloudformation.ErrCodeTokenAlreadyExistsException {
				return nil
			}
		}
		return err
	}

	// Wait until cloudformation stack deletes
	err = c.cfClient.WaitUntilStackDeleteComplete(&cloudformation.DescribeStacksInput{
		StackName: aws.String(stackName),
	})
	if err != nil {
		switch typed := err.(type) {
		case awserr.Error:
			// Waiter reached maximum attempts waiting for the resource to be ready
			if typed.Code() == request.WaiterResourceNotReadyErrorCode {
				c.logger.Errorf("Max retries reached waiting for stack to delete.")
				return err
			}
		}
		return err
	}

	return nil
}

func (c *awsClient) GetClusterRegionTagForUser(username string) (string, error) {
	user, err := c.iamClient.GetUser(&iam.GetUserInput{UserName: aws.String(username)})
	if err != nil {
		return "", err
	}
	for _, tag := range user.User.Tags {
		if *tag.Key == tags.ClusterRegion {
			return *tag.Value, nil
		}
	}
	return "", nil
}

func (c *awsClient) TagUserRegion(username string, region string) error {
	_, err := c.iamClient.TagUser(&iam.TagUserInput{
		UserName: aws.String(username),
		Tags: []*iam.Tag{
			{
				Key:   aws.String(tags.ClusterRegion),
				Value: aws.String(region),
			},
		},
	})
	if err != nil {
		return err
	}
	return nil
}

type AccessKey struct {
	AccessKeyID     string
	SecretAccessKey string
}

// GetAWSAccessKeys uses UpsertAccessKey to delete and create new access keys
// for `osdCcsAdmin` each time we use the client to create a cluster.
// There is no need to permanently store these credentials since they are only used
// on create, the cluster uses a completely different set of IAM credentials
// provisioned by this user.
func (c *awsClient) GetAWSAccessKeys() (*AccessKey, error) {
	if c.awsAccessKeys != nil {
		return c.awsAccessKeys, nil
	}

	accessKey, err := c.UpsertAccessKey(AdminUserName)
	if err != nil {
		return nil, err
	}

	err = c.ValidateAccessKeys(accessKey)
	if err != nil {
		return nil, err
	}

	c.awsAccessKeys = accessKey

	return c.awsAccessKeys, nil
}

// ValidateAccessKeys deals with AWS' eventual consistency, its attempts to call
// GetCallerIdentity and will try again if the error is access denied.
func (c *awsClient) ValidateAccessKeys(AccessKey *AccessKey) error {
	logger, err := logging.NewLogger().
		Build()
	if err != nil {
		return fmt.Errorf("Unable to create AWS logger: %v", err)
	}

	start := time.Now()
	maxAttempts := 15

	// Wait for credentials
	// 15 attempts should be enough, it takes generally around 10 seconds to ready
	// credentials
	for i := 0; i < maxAttempts; i++ {
		// Create the AWS client
		_, err := NewClient().
			Logger(logger).
			Region(DefaultRegion).
			AccessKeys(AccessKey).
			Build()

		if err != nil {
			logger.Debug(fmt.Sprintf("%+v\n", err))
			switch typed := err.(type) {
			case awserr.Error:
				// Waiter reached maximum attempts waiting for the resource to be ready
				if typed.Code() == "InvalidClientTokenId" {
					wait := time.Duration((i * 200)) * time.Millisecond
					waited := time.Since(start)
					logger.Debug(fmt.Sprintf("InvalidClientTokenId, waited %.2f\n", waited.Seconds()))
					time.Sleep(wait)
				}
				if typed.Code() == "AccessDenied" {
					wait := time.Duration((i * 200)) * time.Millisecond
					waited := time.Since(start)
					logger.Debug(fmt.Printf("AccessDenied, waited %.2f\n", waited.Seconds()))
					time.Sleep(wait)
				}
			}

			// If we've still got an error on the last attempt return it
			if i == maxAttempts {
				logger.Error("Error waiting for IAM credentials to become ready.")
				return err
			}
		} else {
			waited := time.Since(start)
			logger.Debug(fmt.Sprintf("\nCredentials ready in %.2fs\n", waited.Seconds()))
			break
		}
	}
	return nil
}

// UpsertAccessKey first deletes all access keys attached to `username` and then creates a
// new access key. DeleteAccessKey ensures we own the user before proceeding to delete
// access keys
func (c *awsClient) UpsertAccessKey(username string) (*AccessKey, error) {
	err := c.DeleteAccessKeys(username)
	if err != nil {
		return nil, err
	}

	createAccessKeyOutput, err := c.CreateAccessKey(username)
	if err != nil {
		return nil, err
	}

	return &AccessKey{
		AccessKeyID:     *createAccessKeyOutput.AccessKey.AccessKeyId,
		SecretAccessKey: *createAccessKeyOutput.AccessKey.SecretAccessKey,
	}, nil
}

// CreateAccessKey creates an IAM access key for `username`
func (c *awsClient) CreateAccessKey(username string) (*iam.CreateAccessKeyOutput, error) {
	// Create access key for IAM user
	createIAMUserAccessKeyOutput, err := c.iamClient.CreateAccessKey(
		&iam.CreateAccessKeyInput{
			UserName: aws.String(username),
		},
	)
	if err != nil {
		return nil, err
	}

	return createIAMUserAccessKeyOutput, nil
}

// DeleteAccessKeys deletes all access keys from `username`. We ensure
// that we own the user before deleting access keys by search for IAM Tags
func (c *awsClient) DeleteAccessKeys(username string) error {
	// List all access keys for user. Result wont be truncated since IAM users
	// can only have 2 access keys
	listAccessKeysOutput, err := c.iamClient.ListAccessKeys(
		&iam.ListAccessKeysInput{
			UserName: aws.String(username),
		},
	)
	if err != nil {
		return err
	}

	// Delete all access keys. Moactl owns this user since the CloudFormation stack
	// at this point is complete and the user is tagged by use on creation
	for _, key := range listAccessKeysOutput.AccessKeyMetadata {
		_, err = c.iamClient.DeleteAccessKey(
			&iam.DeleteAccessKeyInput{
				UserName:    aws.String(username),
				AccessKeyId: key.AccessKeyId,
			},
		)
		if err != nil {
			return err
		}
	}

	// Complete, deleted all accesskeys for `username`
	return nil
}

// ValidateQuota
func (c *awsClient) ValidateQuota() (bool, error) {
	for _, quota := range serviceQuotaServices {
		serviceQuotas, err := ListServiceQuotas(c, quota.ServiceCode)
		if err != nil {
			return false, fmt.Errorf("Error listing AWS service quotas: %s %v", quota.ServiceCode, err)
		}

		serviceQuota, err := GetServiceQuota(serviceQuotas, quota.QuotaCode)
		if err != nil || serviceQuota == nil || (*serviceQuota).Value == nil {
			return false, fmt.Errorf("Error getting AWS service quota: %s %v", quota.ServiceCode, err)
		}

		if *serviceQuota.Value < *quota.DesiredValue {
			return false, fmt.Errorf(
				"Service %s quota code %s %s not valid, expected quota of at least %d, but got %d",
				quota.ServiceCode, quota.QuotaCode, quota.QuotaName,
				int(*quota.DesiredValue), int(*serviceQuota.Value))
		}

		c.logger.Debug(fmt.Sprintf("Service %s quota code %s is ok", quota.ServiceCode, quota.QuotaCode))
	}

	return true, nil
}

// ValidateSCP attempts to validate SCP policies by ensuring we have the correct permissions
func (c *awsClient) ValidateSCP(target *string) (bool, error) {
	scpPolicyPath := "templates/policies/osd_scp_policy.json"

	sParams := &SimulateParams{
		Region: *c.awsSession.Config.Region,
	}

	// Read installer permissions and OSD SCP Policy permissions
	osdPolicyDocument := readSCPPolicy(scpPolicyPath)
	policyDocuments := []PolicyDocument{osdPolicyDocument}

	// Get Creator details
	creator, err := c.GetCreator()
	if err != nil {
		return false, err
	}

	// Find target user
	var targetUserARN arn.ARN
	if target == nil {
		var err error
		callerIdentity, _, err := getClientDetails(c)
		if err != nil {
			return false, fmt.Errorf("getClientDetails: %v\n"+
				"Please run 'rosa init' and try again.", err)
		}
		targetUserARN, err = arn.Parse(*callerIdentity.Arn)
		if err != nil {
			return false, fmt.Errorf("unable to parse caller ARN %v", err)
		}
		// If the client is using STS credentials want to validate the role
		// the user has assumed. GetCreator() resolves that for us and updates
		// the ARN
		if creator.IsSTS {
			targetUserARN, err = arn.Parse(creator.ARN)
			if err != nil {
				return false, err
			}
		}
	} else {
		targetIAMOutput, err := c.iamClient.GetUser(&iam.GetUserInput{UserName: target})
		if err != nil {
			return false, fmt.Errorf("iamClient.GetUser: %v\n"+
				"To reset the '%s' account, run 'rosa init --delete-stack' and try again.", *target, err)
		}
		targetUserARN, err = arn.Parse(*targetIAMOutput.User.Arn)
		if err != nil {
			return false, fmt.Errorf("Unable to parse caller ARN %v", err)
		}
	}

	// Validate permissions
	hasPermissions, err := validatePolicyDocuments(c, targetUserARN.String(), policyDocuments, sParams)
	if err != nil {
		return false, err
	}
	if !hasPermissions {
		return false, err
	}

	return true, nil
}
