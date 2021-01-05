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

package cluster

import (
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go/aws/arn"
	"github.com/spf13/cobra"

	cmv1 "github.com/openshift-online/ocm-sdk-go/clustersmgmt/v1"
	"github.com/openshift/rosa/pkg/aws"
	clusterprovider "github.com/openshift/rosa/pkg/cluster"
	"github.com/openshift/rosa/pkg/logging"
	"github.com/openshift/rosa/pkg/ocm"
	"github.com/openshift/rosa/pkg/ocm/properties"
	"github.com/openshift/rosa/pkg/ocm/upgrades"
	rprtr "github.com/openshift/rosa/pkg/reporter"
)

const (
	StageURL      = "https://qaprodauth.cloud.redhat.com/openshift/details/"
	ProductionURL = "https://cloud.redhat.com/openshift/details/"
	StageEnv      = "https://api.stage.openshift.com"
	ProductionEnv = "https://api.openshift.com"
)

var args struct {
	clusterKey string
}

var Cmd = &cobra.Command{
	Use:   "cluster [ID|NAME]",
	Short: "Show details of a cluster",
	Long:  "Show details of a cluster",
	Example: `  # Describe a cluster named "mycluster"
  rosa describe cluster mycluster

  # Describe a cluster using the --cluster flag
  rosa describe cluster --cluster=mycluster`,
	Run: run,
}

func init() {
	flags := Cmd.Flags()

	flags.StringVarP(
		&args.clusterKey,
		"cluster",
		"c",
		"",
		"Name or ID of the cluster to describe.",
	)
}

func run(_ *cobra.Command, argv []string) {
	reporter := rprtr.CreateReporterOrExit()
	logger := logging.CreateLoggerOrExit(reporter)

	// Check command line arguments:
	clusterKey := args.clusterKey
	if clusterKey == "" {
		if len(argv) != 1 {
			reporter.Errorf(
				"Expected exactly one command line argument or flag containing the name " +
					"or identifier of the cluster",
			)
			os.Exit(1)
		}
		clusterKey = argv[0]
	}

	// Check that the cluster key (name, identifier or external identifier) given by the user
	// is reasonably safe so that there is no risk of SQL injection:
	if !clusterprovider.IsValidClusterKey(clusterKey) {
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
	cluster, err := clusterprovider.GetCluster(ocmClient.Clusters(), clusterKey, awsCreator.ARN)
	if err != nil {
		reporter.Errorf(fmt.Sprintf("Failed to get cluster '%s': %v", clusterKey, err))
		os.Exit(1)
	}

	creatorARN, err := arn.Parse(cluster.Properties()[properties.CreatorARN])
	if err != nil {
		reporter.Errorf("Failed to parse creator ARN for cluster '%s'", clusterKey)
		os.Exit(1)
	}
	phase := ""

	if cluster.State() == cmv1.ClusterStatePending {
		phase = "(Preparing account)"
	}

	if cluster.State() == cmv1.ClusterStateInstalling {
		if !cluster.Status().DNSReady() {
			phase = "(DNS setup in progress)"
		}
		if cluster.Status().ProvisionErrorMessage() != "" {
			errorCode := ""
			if cluster.Status().ProvisionErrorCode() != "" {
				errorCode = cluster.Status().ProvisionErrorCode() + " - "
			}
			phase = "(" + errorCode + "Install is taking longer than expected)"
		}
	}

	clusterName := cluster.DisplayName()
	if clusterName == "" {
		clusterName = cluster.Name()
	}

	isPrivate := "No"
	ingresses, err := ocm.GetIngresses(ocmClient.Clusters(), cluster.ID())
	for _, ingress := range ingresses {
		if ingress.Default() && ingress.Listening() == cmv1.ListeningMethodInternal {
			isPrivate = "Yes"
		}
	}

	scheduledUpgrade, err := upgrades.GetScheduledUpgrade(ocmClient, cluster.ID())
	if err != nil {
		reporter.Errorf("Failed to get scheduled upgrades for cluster '%s': %v", clusterKey, err)
		os.Exit(1)
	}

	detailsPage := getDetailsLink(ocmConnection.URL())

	var nodesStr string
	if cluster.Nodes().AutoscaleCompute() != nil {
		nodesStr = fmt.Sprintf(""+
			"Nodes:                      Master: %d, Infra: %d, Compute (Autoscaled): %d-%d\n",
			cluster.Nodes().Master(), cluster.Nodes().Infra(),
			cluster.Nodes().AutoscaleCompute().MinReplicas(),
			cluster.Nodes().AutoscaleCompute().MaxReplicas(),
		)
	} else {
		nodesStr = fmt.Sprintf(""+
			"Nodes:                      Master: %d, Infra: %d, Compute: %d\n",
			cluster.Nodes().Master(), cluster.Nodes().Infra(), cluster.Nodes().Compute(),
		)
	}

	// Print short cluster description:
	str := fmt.Sprintf(""+
		"Name:                       %s\n"+
		"DNS:                        %s.%s\n"+
		"ID:                         %s\n"+
		"External ID:                %s\n"+
		"AWS Account:                %s\n"+
		"API URL:                    %s\n"+
		"Console URL:                %s\n"+
		"%s"+
		"Region:                     %s\n"+
		"State:                      %s %s\n"+
		"Channel Group:              %s\n"+
		"Private:                    %s\n"+
		"Created:                    %s\n",
		clusterName,
		cluster.Name(), cluster.DNS().BaseDomain(),
		cluster.ID(),
		cluster.ExternalID(),
		creatorARN.AccountID,
		cluster.API().URL(),
		cluster.Console().URL(),
		nodesStr,
		cluster.Region().ID(),
		cluster.State(), phase,
		cluster.Version().ChannelGroup(),
		isPrivate,
		cluster.CreationTimestamp().Format("Jan _2 2006 15:04:05 MST"),
	)

	if detailsPage != "" {
		str = fmt.Sprintf("%s"+
			"Details Page:               %s%s\n", str,
			detailsPage, cluster.ID())
	}
	if scheduledUpgrade != nil {
		str = fmt.Sprintf("%s"+
			"Scheduled upgrade:          %s on %s\n",
			str,
			scheduledUpgrade.Version(),
			scheduledUpgrade.NextRun().Format("2006-01-02 15:04 MST"),
		)
	}
	if cluster.Status().State() == cmv1.ClusterStateError {
		str = fmt.Sprintf("%s"+
			"Provisioning Error Code:    %s\n"+
			"Provisioning Error Message: %s\n",
			str,
			cluster.Status().ProvisionErrorCode(),
			cluster.Status().ProvisionErrorMessage(),
		)
	}
	// Print short cluster description:
	fmt.Print(str)
	fmt.Println()
}

func getDetailsLink(environment string) string {
	switch environment {
	case StageEnv:
		return StageURL
	case ProductionEnv:
		return ProductionURL
	default:
		return ""
	}
}
