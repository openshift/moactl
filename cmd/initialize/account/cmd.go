/*
Copyright (c) 2021 Red Hat, Inc.

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

package account

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/openshift/rosa/cmd/create/accountroles"
	"github.com/openshift/rosa/cmd/login"
	"github.com/openshift/rosa/cmd/verify/oc"
	"github.com/openshift/rosa/cmd/verify/quota"

	"github.com/openshift/rosa/pkg/arguments"
	"github.com/openshift/rosa/pkg/aws"
	"github.com/openshift/rosa/pkg/logging"
	"github.com/openshift/rosa/pkg/ocm"
	rprtr "github.com/openshift/rosa/pkg/reporter"
)

var Cmd = &cobra.Command{
	Use:   "account",
	Short: "Initialize AWS account with IAM roles",
	Long:  "Initialize AWS account with IAM roles before creating clusters with STS/role-based authentication.",
	Example: `  # Initialize default account roles for OpenShift 4.8.x
  rosa init account --version 4.8

  # Initialize default account roles manually with IAM roles prefixed with "foo-"
  rosa init account --mode manual --prefix foo`,
	Run: run,
}

func init() {
	flags := Cmd.Flags()

	// Force-load all flags from `account-roles` into `init`
	flags.AddFlagSet(accountroles.Cmd.Flags())
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

	// Validate AWS quota
	// Call `verify quota` as part of init
	quota.Cmd.Run(cmd, argv)

	// Verify version of `oc`
	oc.Cmd.Run(cmd, argv)

	// Run the `create account-roles` command
	accountroles.Cmd.Run(cmd, argv)
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
