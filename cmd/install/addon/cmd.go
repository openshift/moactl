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

package addon

import (
	"net"
	"os"
	"regexp"
	"strconv"

	cmv1 "github.com/openshift-online/ocm-sdk-go/clustersmgmt/v1"
	"github.com/spf13/cobra"

	"github.com/openshift/rosa/pkg/aws"
	clusterprovider "github.com/openshift/rosa/pkg/cluster"
	"github.com/openshift/rosa/pkg/confirm"
	"github.com/openshift/rosa/pkg/interactive"
	"github.com/openshift/rosa/pkg/logging"
	"github.com/openshift/rosa/pkg/ocm"
	rprtr "github.com/openshift/rosa/pkg/reporter"
)

var args struct {
	clusterKey string
}

var Cmd = &cobra.Command{
	Use:     "addon",
	Aliases: []string{"addons", "add-on", "add-ons"},
	Short:   "Install add-ons on cluster",
	Long:    "Install Red Hat managed add-ons on a cluster",
	Example: `  # Add the CodeReady Workspaces add-on installation to the cluster
  rosa install addon --cluster=mycluster codeready-workspaces`,
	Run: run,
}

func init() {
	flags := Cmd.Flags()
	confirm.AddFlag(flags)

	flags.StringVarP(
		&args.clusterKey,
		"cluster",
		"c",
		"",
		"Name or ID of the cluster to add the IdP to (required).",
	)
	Cmd.MarkFlagRequired("cluster")
}

func run(_ *cobra.Command, argv []string) {
	reporter := rprtr.CreateReporterOrExit()
	logger := logging.CreateLoggerOrExit(reporter)

	// Check command line arguments:
	if len(argv) != 1 {
		reporter.Errorf("Expected exactly one command line parameters containing the identifier of the add-on.")
		os.Exit(1)
	}

	addOnID := argv[0]
	if addOnID == "" {
		reporter.Errorf("Add-on ID is required.")
		os.Exit(1)
	}

	// Check that the cluster key (name, identifier or external identifier) given by the user
	// is reasonably safe so that there is no risk of SQL injection:
	clusterKey := args.clusterKey
	if !ocm.IsValidClusterKey(clusterKey) {
		reporter.Errorf(
			"Cluster name, identifier or external identifier '%s' isn't valid: it "+
				"must contain only letters, digits, dashes and underscores",
			clusterKey,
		)
		os.Exit(1)
	}

	// Create the AWS client:
	awsClient, err := aws.NewClient().
		Logger(logger).
		Build()
	if err != nil {
		reporter.Errorf("Failed to create AWS client: %v", err)
		os.Exit(1)
	}

	awsCreator, err := awsClient.GetCreator()
	if err != nil {
		reporter.Errorf("Failed to get AWS creator: %v", err)
		os.Exit(1)
	}

	// Create the client for the OCM API:
	ocmConnection, err := ocm.NewConnection().
		Logger(logger).
		Build()
	if err != nil {
		reporter.Errorf("Failed to create OCM connection: %v", err)
		os.Exit(1)
	}
	defer func() {
		err = ocmConnection.Close()
		if err != nil {
			reporter.Errorf("Failed to close OCM connection: %v", err)
		}
	}()

	// Get the client for the OCM collection of clusters:
	ocmClient := ocmConnection.ClustersMgmt().V1()

	// Try to find the cluster:
	reporter.Debugf("Loading cluster '%s'", clusterKey)
	cluster, err := ocm.GetCluster(ocmClient.Clusters(), clusterKey, awsCreator.ARN)
	if err != nil {
		reporter.Errorf("Failed to get cluster '%s': %v", clusterKey, err)
		os.Exit(1)
	}

	if cluster.State() != cmv1.ClusterStateReady {
		reporter.Errorf("Cluster '%s' is not yet ready", clusterKey)
		os.Exit(1)
	}

	if !confirm.Confirm("install add-on '%s' on cluster '%s'", addOnID, clusterKey) {
		os.Exit(0)
	}

	parameters, err := clusterprovider.GetAddOnParameters(ocmClient.Addons(), addOnID)
	if err != nil {
		reporter.Errorf("Failed to get add-on '%s' parameters: %v", addOnID, err)
		os.Exit(1)
	}

	var params []clusterprovider.AddOnParam
	if parameters.Len() > 0 {
		parameters.Each(func(param *cmv1.AddOnParameter) bool {
			input := interactive.Input{
				Question: param.Name(),
				Help:     param.Description(),
				Default:  param.DefaultValue(),
				Required: param.Required(),
			}

			var val string
			switch param.ValueType() {
			case "boolean":
				var boolVal bool
				boolVal, err = interactive.GetBool(input)
				if boolVal {
					val = "true"
				} else {
					val = "false"
				}
			case "cidr":
				var cidrVal net.IPNet
				cidrVal, err = interactive.GetIPNet(input)
				val = cidrVal.String()
			case "number":
				var numVal int
				numVal, err = interactive.GetInt(input)
				val = strconv.Itoa(numVal)
			case "string":
				val, err = interactive.GetString(input)
			}
			if err != nil {
				reporter.Errorf("Expected a valid value for '%s': %v", param.ID(), err)
				os.Exit(1)
			}

			if param.Validation() != "" {
				isValid, err := regexp.MatchString(param.Validation(), val)
				if err != nil || !isValid {
					reporter.Errorf("Expected %v to match /%s/", val, param.Validation())
					os.Exit(1)
				}
			}

			params = append(params, clusterprovider.AddOnParam{Key: param.ID(), Val: val})

			return true
		})
	}

	reporter.Debugf("Installing add-on '%s' on cluster '%s'", addOnID, clusterKey)
	err = clusterprovider.InstallAddOn(ocmClient.Clusters(), clusterKey, awsCreator.ARN, addOnID, params)
	if err != nil {
		reporter.Errorf("Failed to add add-on installation '%s' for cluster '%s': %v", addOnID, clusterKey, err)
		os.Exit(1)
	}
	reporter.Infof("Add-on '%s' is now installing. To check the status run 'rosa list addons -c %s'", addOnID, clusterKey)
}
