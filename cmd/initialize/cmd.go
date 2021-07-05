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

package initialize

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/openshift/rosa/cmd/login"
	"github.com/openshift/rosa/cmd/verify/oc"
	"github.com/openshift/rosa/cmd/verify/permissions"
	"github.com/openshift/rosa/cmd/verify/quota"
	"github.com/openshift/rosa/pkg/arguments"

	"github.com/openshift/rosa/pkg/aws"
	"github.com/openshift/rosa/pkg/aws/region"
	"github.com/openshift/rosa/pkg/logging"
	"github.com/openshift/rosa/pkg/ocm"
	rprtr "github.com/openshift/rosa/pkg/reporter"
)

var args struct {
	dlt              bool
	disableSCPChecks bool
	sts              bool
	region           string
}

var Cmd = &cobra.Command{
	Use:   "init",
	Short: "Applies templates to support Red Hat OpenShift Service on AWS",
	Long: "Applies templates to support Red Hat OpenShift Service on AWS. If you are not\n" +
		"yet logged in to OCM, it will prompt you for credentials.",
	Example: `  # Configure your AWS account to allow ROSA clusters
  rosa init

  # Configure a new AWS account using pre-existing OCM credentials
  rosa init --token=$OFFLINE_ACCESS_TOKEN`,
	Run: run,
}

func init() {
	flags := Cmd.Flags()
	flags.SortFlags = false

	flags.BoolVar(
		&args.dlt,
		"delete-stack",
		false,
		"Deletes stack template applied to your AWS account during the 'init' command.\n",
	)
	flags.MarkDeprecated("delete-stack", "use --delete instead")

	flags.BoolVar(
		&args.dlt,
		"delete",
		false,
		"",
	)

	flags.BoolVar(
		&args.disableSCPChecks,
		"disable-scp-checks",
		false,
		"Indicates if cloud permission checks are disabled when attempting installation of the cluster.",
	)

	flags.BoolVar(
		&args.sts,
		"sts",
		false,
		"Indicates if cluster credentials checks should be done using STS Assume Role",
	)

	// Force-load all flags from `login` into `init`
	flags.AddFlagSet(login.Cmd.Flags())

	arguments.AddProfileFlag(flags)
	arguments.AddRegionFlag(flags)
}

func run(cmd *cobra.Command, argv []string) {
	reporter := rprtr.CreateReporterOrExit()
	logger := logging.CreateLoggerOrExit(reporter)

	// If necessary, call `login` as part of `init`. We do this before
	// other validations to get the prompt out of the way before performing
	// longer checks.
	err := ocmLogin(cmd, argv, reporter)
	if err != nil {
		reporter.Errorf("Failed to login to OCM: %v", err)
		os.Exit(1)
	}

	// Create the client for the OCM API:
	ocmClient, err := ocm.NewClient().
		Logger(logger).
		Build()
	if err != nil {
		reporter.Errorf("Failed to create OCM connection: %v", err)
		os.Exit(1)
	}
	defer ocmClient.Close()

	// Get AWS region
	awsRegion, err := aws.GetRegion(arguments.GetRegion())
	if err != nil {
		reporter.Errorf("Error getting region: %v", err)
		os.Exit(1)
	}
	// Create the AWS client:
	client, err := aws.NewClient().
		Logger(logger).
		Region(awsRegion).
		Build()

	if err != nil {
		// FIXME Hack to capture errors due to using STS accounts
		if strings.Contains(fmt.Sprintf("%s", err), "STS") {
			ocmClient.LogEvent("ROSAInitCredentialsSTS")
		}
		reporter.Errorf("Error creating AWS client: %v", err)
		os.Exit(1)
	}

	// Validate AWS credentials for current user
	reporter.Infof("Validating AWS credentials...")
	ok, err := client.ValidateCredentials()
	if err != nil {
		ocmClient.LogEvent("ROSAInitCredentialsFailed")
		reporter.Errorf("Error validating AWS credentials: %v", err)
		os.Exit(1)
	}
	if !ok {
		ocmClient.LogEvent("ROSAInitCredentialsInvalid")
		reporter.Errorf("AWS credentials are invalid")
		os.Exit(1)
	}
	reporter.Infof("AWS credentials are valid!")

	cfClient := aws.GetAWSClientForUserRegion(reporter, logger)

	// Delete CloudFormation stack and exit
	if args.dlt {
		reporter.Infof("Deleting cluster administrator user '%s'...", aws.AdminUserName)
		err = deleteStack(cfClient, ocmClient)
		if err != nil {
			reporter.Errorf("%v", err)
			os.Exit(1)
		}

		reporter.Infof("Admin user '%s' deleted successfully!", aws.AdminUserName)
		os.Exit(0)
	}

	// Validate AWS SCP/IAM Permissions
	// Call `verify permissions` as part of init
	// Skip this check if --disable-scp-checks is true
	// If SCP policies conditions restrict an AWS accounts permissions by region
	// AWS will fail all policy simulation checks
	// Validate AWS credentials for current user
	if !args.disableSCPChecks {
		permissions.Cmd.Run(cmd, argv)
	} else {
		reporter.Infof("Skipping AWS SCP policies check")
	}

	// Validate AWS quota
	// Call `verify quota` as part of init
	quota.Cmd.Run(cmd, argv)

	// Ensure that there is an AWS user to create all the resources needed by the cluster:
	if !args.sts {
		reporter.Infof("Ensuring cluster administrator user '%s'...", aws.AdminUserName)
		created, err := cfClient.EnsureOsdCcsAdminUser(aws.OsdCcsAdminStackName, aws.AdminUserName, awsRegion)
		if err != nil {
			ocmClient.LogEvent("ROSAInitCreateStackFailed")
			reporter.Errorf("Failed to create user '%s': %v", aws.AdminUserName, err)
			os.Exit(1)
		}
		if created {
			reporter.Infof("Admin user '%s' created successfully!", aws.AdminUserName)
		} else {
			reporter.Infof("Admin user '%s' already exists!", aws.AdminUserName)
		}

		// Check if osdCcsAdmin has right permissions
		// Skip this check if --disable-scp-checks is true
		if !args.disableSCPChecks {
			reporter.Infof("Validating SCP policies for '%s'...", aws.AdminUserName)
			target := aws.AdminUserName
			isValid, err := client.ValidateSCP(&target)
			if !isValid {
				ocmClient.LogEvent("ROSAInitSCPPoliciesFailed")
				reporter.Errorf("Failed to verify permissions for user '%s': %v", target, err)
				os.Exit(1)
			}
			reporter.Infof("AWS SCP policies ok")
		} else {
			reporter.Infof("Skipping AWS SCP policies check for '%s'...", aws.AdminUserName)
		}

		// Check whether the user can create a basic cluster
		reporter.Infof("Validating cluster creation...")
		err = simulateCluster(ocmClient, region.Region())
		if err != nil {
			ocmClient.LogEvent("ROSAInitDryRunFailed")
			reporter.Warnf("Cluster creation failed. "+
				"If you create a cluster, it should fail with the following error:\n%s", err)
		} else {
			reporter.Infof("Cluster creation valid")
		}
	}

	// Verify version of `oc`
	oc.Cmd.Run(cmd, argv)
}

func ocmLogin(cmd *cobra.Command, argv []string, reporter *rprtr.Object) error {
	loginFlags := []string{"token-url", "client-id", "client-secret", "scope", "env", "token", "insecure"}
	hasLoginFlags := false
	// Check if the user set login flags
	for _, loginFlag := range loginFlags {
		if cmd.Flags().Changed(loginFlag) {
			hasLoginFlags = true
			break
		}
	}
	if hasLoginFlags {
		// Always force login if user sets login flags
		login.Cmd.Run(cmd, argv)
		return nil
	}

	// Verify if user is already logged in:
	isLoggedIn := false
	cfg, err := ocm.Load()
	if err != nil {
		return fmt.Errorf("Failed to load config file: %v", err)
	}
	if cfg != nil {
		// Check that credentials in the config file are valid
		isLoggedIn, err = cfg.Armed()
		if err != nil {
			return fmt.Errorf("Failed to determine if user is logged in: %v", err)
		}
	}

	if isLoggedIn {
		username, err := cfg.GetData("username")
		if err != nil {
			return fmt.Errorf("Failed to get username: %v", err)
		}

		reporter.Infof("Logged in as '%s' on '%s'", username, cfg.URL)
		return nil
	}

	login.Cmd.Run(cmd, argv)
	return nil
}

func deleteStack(awsClient aws.Client, ocmClient *ocm.Client) error {
	// Get creator ARN to determine existing clusters:
	awsCreator, err := awsClient.GetCreator()
	if err != nil {
		ocmClient.LogEvent("ROSAInitGetCreatorFailed")
		return fmt.Errorf("Failed to get AWS creator: %v", err)
	}

	// Check whether the account has clusters:
	hasClusters, err := ocmClient.HasClusters(awsCreator)
	if err != nil {
		return fmt.Errorf("Failed to check for clusters: %v", err)
	}

	if hasClusters {
		return fmt.Errorf("Failed to delete '%s': User still has clusters", aws.AdminUserName)
	}

	// Delete the CloudFormation stack
	err = awsClient.DeleteOsdCcsAdminUser(aws.OsdCcsAdminStackName)
	if err != nil {
		ocmClient.LogEvent("ROSAInitDeleteStackFailed")
		return fmt.Errorf("Failed to delete user '%s': %v", aws.AdminUserName, err)
	}

	return nil
}

func simulateCluster(ocmClient *ocm.Client, region string) error {
	dryRun := true
	if region == "" {
		region = aws.DefaultRegion
	}
	spec := ocm.Spec{
		Name:   "rosa-init",
		Region: region,
		DryRun: &dryRun,
	}

	_, err := ocmClient.CreateCluster(spec)
	if err != nil {
		return err
	}

	return nil
}
