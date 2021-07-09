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

package accountroles

import (
	"fmt"
	"os"
	"strings"

	semver "github.com/hashicorp/go-version"
	"github.com/spf13/cobra"

	"github.com/openshift/rosa/pkg/aws"
	"github.com/openshift/rosa/pkg/aws/tags"
	"github.com/openshift/rosa/pkg/interactive"
	"github.com/openshift/rosa/pkg/interactive/confirm"
	"github.com/openshift/rosa/pkg/logging"
	"github.com/openshift/rosa/pkg/ocm"
	rprtr "github.com/openshift/rosa/pkg/reporter"
)

var modes []string = []string{"auto", "manual"}

var args struct {
	version string
	prefix  string
	mode    string
}

var Cmd = &cobra.Command{
	Use:     "account-roles",
	Aliases: []string{"accountroles", "roles", "policies"},
	Short:   "Create account-wide IAM roles before creating your cluster.",
	Long:    "Create account-wide IAM roles before creating your cluster.",
	Example: `  # Create default account roles for OpenShift 4.8.x
  rosa create account-roles --version 4.8`,
	Run: run,
}

func init() {
	flags := Cmd.Flags()

	flags.StringVar(
		&args.version,
		"version",
		"",
		"Version of the cluster to generate the IAM roles for",
	)

	flags.StringVar(
		&args.prefix,
		"prefix",
		"ManagedOpenShift",
		"User-defined prefix for all generated AWS resources",
	)

	flags.StringVar(
		&args.mode,
		"mode",
		modes[0],
		"How to perform the operation. Valid options are:\n"+
			"auto: Roles and policies will be created using the current AWS account\n"+
			"manual: Policy documents will be saved in the current directory",
	)
	Cmd.RegisterFlagCompletionFunc("mode", modeCompletion)

	confirm.AddFlag(flags)
	interactive.AddFlag(flags)
}

func modeCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return modes, cobra.ShellCompDirectiveDefault
}

func run(cmd *cobra.Command, _ []string) {
	reporter := rprtr.CreateReporterOrExit()
	logger := logging.CreateLoggerOrExit(reporter)

	// Create the AWS client:
	awsClient, err := aws.NewClient().
		Logger(logger).
		Build()
	if err != nil {
		reporter.Errorf("Failed to create AWS client: %v", err)
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
	defer func() {
		err = ocmClient.Close()
		if err != nil {
			reporter.Errorf("Failed to close OCM connection: %v", err)
		}
	}()

	env, err := ocm.GetEnv()
	if err != nil {
		reporter.Errorf("Failed to determine OCM environment: %v", err)
		os.Exit(1)
	}

	// Determine if interactive mode is needed
	if !interactive.Enabled() && (!cmd.Flags().Changed("version") || !cmd.Flags().Changed("mode")) {
		interactive.Enable()
	}

	// OpenShift version:
	version := args.version
	versionList, err := getVersionList(ocmClient)
	if err != nil {
		reporter.Errorf("%s", err)
		os.Exit(1)
	}
	if interactive.Enabled() {
		if version == "" {
			version = versionList[0]
		}
		version, err = interactive.GetOption(interactive.Input{
			Question: "OpenShift version",
			Help:     cmd.Flags().Lookup("version").Usage,
			Options:  versionList,
			Default:  version,
			Required: true,
		})
		if err != nil {
			reporter.Errorf("Expected a valid OpenShift version: %s", err)
			os.Exit(1)
		}
	}
	version, err = validateVersion(version, versionList)
	if err != nil {
		reporter.Errorf("Expected a valid OpenShift version: %s", err)
		os.Exit(1)
	}

	prefix := args.prefix
	if interactive.Enabled() {
		prefix, err = interactive.GetString(interactive.Input{
			Question: "Role prefix",
			Help:     cmd.Flags().Lookup("prefix").Usage,
			Default:  prefix,
			Required: true,
		})
		if err != nil {
			reporter.Errorf("Expected a valid role prefix: %s", err)
			os.Exit(1)
		}
	}
	if len(prefix) > 32 {
		reporter.Errorf("Expected a prefix with no more than 32 characters")
		os.Exit(1)
	}

	mode := args.mode
	if interactive.Enabled() {
		mode, err = interactive.GetOption(interactive.Input{
			Question: "Role creation mode",
			Help:     cmd.Flags().Lookup("mode").Usage,
			Default:  mode,
			Options:  modes,
			Required: true,
		})
		if err != nil {
			reporter.Errorf("Expected a valid role creation mode: %s", err)
			os.Exit(1)
		}
	}

	switch mode {
	case "auto":
		creator, err := awsClient.GetCreator()
		if err != nil {
			reporter.Errorf("Unable to get IAM credentials: %s", err)
			os.Exit(1)
		}
		reporter.Infof("Creating roles using '%s'", creator.ARN)
		err = createRoles(reporter, awsClient, prefix, version, env)
		if err != nil {
			reporter.Errorf("There was an error creating the account roles: %s", err)
			os.Exit(1)
		}
	case "manual":
		err = generatePolicyFiles(reporter, version, env)
		if err != nil {
			reporter.Errorf("There was an error generating the policy files: %s", err)
			os.Exit(1)
		}

		reporter.Infof("All policy files saved to the current directory")
		reporter.Infof("Run the following commands to create the account roles and policies:\n")

		commands := buildCommands(prefix, version)
		fmt.Println(commands)
	}
}

// Get a list of all STS-supported minor versions
func getVersionList(ocmClient *ocm.Client) (versionList []string, err error) {
	vs, err := ocmClient.GetVersions("")
	if err != nil {
		err = fmt.Errorf("Failed to retrieve versions: %s", err)
		return
	}

	// Make a set-map of all minors
	minorSet := make(map[string]struct{})
	for _, v := range vs {
		if !ocm.HasSTSSupport(v.RawID(), v.ChannelGroup()) {
			continue
		}
		ver, errv := semver.NewVersion(v.RawID())
		if err != nil {
			return versionList, errv
		}
		segments := ver.Segments64()
		minor := fmt.Sprintf("%d.%d", segments[0], segments[1])
		minorSet[minor] = struct{}{}
	}

	// Extract minor keys into a slice
	for m := range minorSet {
		versionList = append(versionList, m)
	}

	return
}

// Validate OpenShift versions
func validateVersion(version string, versionList []string) (string, error) {
	if version != "" {
		// Check and set the cluster version
		hasVersion := false
		for _, v := range versionList {
			if v == version {
				hasVersion = true
			}
		}
		if !hasVersion {
			allVersions := strings.Join(versionList, " ")
			err := fmt.Errorf("A valid version number must be specified\nValid versions: %s", allVersions)
			return version, err
		}

		if !ocm.HasSTSSupportMinor(version) {
			err := fmt.Errorf("Version '%s' is not supported for STS clusters", version)
			return version, err
		}
	}

	return version, nil
}

func generatePolicyFiles(reporter *rprtr.Object, version string, env string) error {
	for role := range aws.AccountRoles {
		filename := fmt.Sprintf("sts_%s_trust_policy.json", role)
		path := fmt.Sprintf("templates/policies/%s", filename)

		policy, err := aws.ReadPolicyDocument(path, map[string]string{
			"aws_account_id": aws.JumpAccounts[env],
		})
		if err != nil {
			return err
		}

		reporter.Debugf("Saving '%s' to the current directory", filename)
		err = saveDocument(policy, filename)
		if err != nil {
			return err
		}

		filename = fmt.Sprintf("sts_%s_permission_policy.json", role)
		path = fmt.Sprintf("templates/policies/%s/%s", version, filename)

		policy, err = aws.ReadPolicyDocument(path)
		if err != nil {
			return err
		}

		reporter.Debugf("Saving '%s' to the current directory", filename)
		err = saveDocument(policy, filename)
		if err != nil {
			return err
		}
	}

	for credrequest := range aws.CredentialRequests {
		filename := fmt.Sprintf("openshift_%s_policy.json", credrequest)
		path := fmt.Sprintf("templates/policies/%s/%s", version, filename)

		policy, err := aws.ReadPolicyDocument(path)
		if err != nil {
			return err
		}

		reporter.Debugf("Saving '%s' to the current directory", filename)
		err = saveDocument(policy, filename)
		if err != nil {
			return err
		}
	}

	return nil
}

func saveDocument(doc []byte, filename string) error {
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = file.Write(doc)
	if err != nil {
		return err
	}

	return nil
}

func buildCommands(prefix string, version string) string {
	commands := []string{}

	for file, role := range aws.AccountRoles {
		name := getRoleName(prefix, role)
		iamTags := fmt.Sprintf(
			"Key=%s,Value=%s Key=%s,Value=%s Key=%s,Value=%s",
			tags.OpenShiftVersion, version,
			tags.RolePrefix, prefix,
			tags.RoleType, file,
		)
		createRole := fmt.Sprintf("aws iam create-role \\\n"+
			"\t--role-name %s \\\n"+
			"\t--assume-role-policy-document file://sts_%s_trust_policy.json \\\n"+
			"\t--tags %s",
			name, file, iamTags)
		putRolePolicy := fmt.Sprintf("aws iam put-role-policy \\\n"+
			"\t--role-name %s \\\n"+
			"\t--policy-name %s-Policy \\\n"+
			"\t--policy-document file://sts_%s_permission_policy.json",
			name, name, file)
		commands = append(commands, createRole, putRolePolicy)
	}

	for credrequest, operator := range aws.CredentialRequests {
		name := getPolicyName(prefix, operator.Namespace, operator.Name)
		iamTags := fmt.Sprintf(
			"Key=%s,Value=%s Key=%s,Value=%s Key=%s,Value=%s Key=%s,Value=%s",
			tags.OpenShiftVersion, version,
			tags.RolePrefix, prefix,
			"operator_namespace", operator.Namespace,
			"operator_name", operator.Name,
		)
		createPolicy := fmt.Sprintf("aws iam create-policy \\\n"+
			"\t--policy-name %s \\\n"+
			"\t--policy-document file://openshift_%s_policy.json \\\n"+
			"\t--tags %s",
			name, credrequest, iamTags)
		commands = append(commands, createPolicy)
	}

	return strings.Join(commands, "\n\n")
}

func createRoles(reporter *rprtr.Object, awsClient aws.Client, prefix string, version string, env string) error {
	for file, role := range aws.AccountRoles {
		name := getRoleName(prefix, role)

		if !confirm.Confirm("create the '%s' role", name) {
			continue
		}

		filename := fmt.Sprintf("sts_%s_trust_policy.json", file)
		path := fmt.Sprintf("templates/policies/%s", filename)

		policy, err := aws.ReadPolicyDocument(path, map[string]string{
			"aws_account_id": aws.JumpAccounts[env],
		})
		if err != nil {
			return err
		}

		reporter.Infof("Creating role '%s'", name)
		err = awsClient.EnsureRole(name, string(policy), map[string]string{
			tags.OpenShiftVersion: version,
			tags.RolePrefix:       prefix,
			tags.RoleType:         file,
		})
		if err != nil {
			return err
		}

		filename = fmt.Sprintf("sts_%s_permission_policy.json", file)
		path = fmt.Sprintf("templates/policies/%s/%s", version, filename)

		policy, err = aws.ReadPolicyDocument(path)
		if err != nil {
			return err
		}

		reporter.Debugf("Attaching permission policy to role '%s'", filename)
		err = awsClient.PutRolePolicy(name, fmt.Sprintf("%s-Policy", name), string(policy))
		if err != nil {
			return err
		}
	}

	if !confirm.Confirm("create the operator policies for OpenShift %s", version) {
		return nil
	}

	for credrequest, operator := range aws.CredentialRequests {
		name := getPolicyName(prefix, operator.Namespace, operator.Name)

		filename := fmt.Sprintf("openshift_%s_policy.json", credrequest)
		path := fmt.Sprintf("templates/policies/%s/%s", version, filename)

		policy, err := aws.ReadPolicyDocument(path)
		if err != nil {
			return err
		}

		reporter.Infof("Creating policy '%s'", name)
		err = awsClient.EnsurePolicy(name, string(policy), map[string]string{
			tags.OpenShiftVersion: version,
			tags.RolePrefix:       prefix,
			"operator_namespace":  operator.Namespace,
			"operator_name":       operator.Name,
		})
		if err != nil {
			return err
		}
	}

	return nil
}

func getRoleName(prefix string, role string) string {
	name := fmt.Sprintf("%s-%s-Role", prefix, role)
	if len(name) > 64 {
		name = name[0:64]
	}
	return name
}

func getPolicyName(prefix string, namespace string, name string) string {
	policy := fmt.Sprintf("%s-%s-%s", prefix, namespace, name)
	if len(policy) > 64 {
		policy = policy[0:64]
	}
	return policy
}
