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

package idp

import (
	"fmt"
	"os"

	cmv1 "github.com/openshift-online/ocm-sdk-go/clustersmgmt/v1"
	"github.com/spf13/cobra"

	"github.com/openshift/rosa/pkg/aws"
	"github.com/openshift/rosa/pkg/interactive/confirm"
	"github.com/openshift/rosa/pkg/logging"
	"github.com/openshift/rosa/pkg/ocm"
	rprtr "github.com/openshift/rosa/pkg/reporter"
)

var args struct {
	clusterKey string
}

var Cmd = &cobra.Command{
	Use:     "idp ID",
	Aliases: []string{"idps"},
	Short:   "Delete cluster IDPs",
	Long:    "Delete a specific identity provider for a cluster.",
	Example: `  # Delete an identity provider named github-1
  rosa delete idp github-1 --cluster=mycluster`,
	Run: run,
	Args: func(_ *cobra.Command, argv []string) error {
		if len(argv) != 1 {
			return fmt.Errorf(
				"Expected exactly one command line parameter containing the name of the identity provider",
			)
		}
		return nil
	},
}

func init() {
	flags := Cmd.Flags()

	flags.StringVarP(
		&args.clusterKey,
		"cluster",
		"c",
		"",
		"Name or ID of the cluster to delete the IdP from (required).",
	)
	Cmd.MarkFlagRequired("cluster")
}

func run(_ *cobra.Command, argv []string) {
	reporter := rprtr.CreateReporterOrExit()
	logger := logging.CreateLoggerOrExit(reporter)

	idpName := argv[0]

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
	clustersCollection := ocmConnection.ClustersMgmt().V1().Clusters()

	// Try to find the cluster:
	reporter.Debugf("Loading cluster '%s'", clusterKey)
	cluster, err := ocm.GetCluster(clustersCollection, clusterKey, awsCreator.ARN)
	if err != nil {
		reporter.Errorf("Failed to get cluster '%s': %v", clusterKey, err)
		os.Exit(1)
	}

	// Try to find the identity provider:
	reporter.Debugf("Loading identity provider '%s'", idpName)
	idps, err := ocm.GetIdentityProviders(clustersCollection, cluster.ID())
	if err != nil {
		reporter.Errorf("Failed to get identity providers for cluster '%s': %v", clusterKey, err)
		os.Exit(1)
	}

	var idp *cmv1.IdentityProvider
	for _, item := range idps {
		if item.Name() == idpName {
			idp = item
		}
	}
	if idp == nil {
		reporter.Errorf("Failed to get identity provider '%s' for cluster '%s'", idpName, clusterKey)
		os.Exit(1)
	}

	if confirm.Confirm("delete identity provider %s on cluster %s", idpName, clusterKey) {
		reporter.Debugf("Deleting identity provider '%s' on cluster '%s'", idpName, clusterKey)
		res, err := clustersCollection.
			Cluster(cluster.ID()).
			IdentityProviders().
			IdentityProvider(idp.ID()).
			Delete().
			Send()
		if err != nil {
			reporter.Debugf(err.Error())
			reporter.Errorf("Failed to delete identity provider '%s' on cluster '%s': %s",
				idpName, clusterKey, res.Error().Reason())
			os.Exit(1)
		}
		reporter.Infof("Successfully deleted identity provider '%s' from cluster '%s'", idpName, clusterKey)
	}
}
