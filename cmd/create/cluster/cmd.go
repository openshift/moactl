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
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	cmv1 "github.com/openshift-online/ocm-sdk-go/clustersmgmt/v1"
	"github.com/spf13/cobra"

	clusterlogs "github.com/openshift/moactl/cmd/logs/cluster"

	"github.com/openshift/moactl/pkg/aws"
	clusterprovider "github.com/openshift/moactl/pkg/cluster"
	"github.com/openshift/moactl/pkg/interactive"
	"github.com/openshift/moactl/pkg/logging"
	"github.com/openshift/moactl/pkg/ocm"
	"github.com/openshift/moactl/pkg/ocm/machines"
	"github.com/openshift/moactl/pkg/ocm/regions"
	"github.com/openshift/moactl/pkg/ocm/versions"
	rprtr "github.com/openshift/moactl/pkg/reporter"
)

var args struct {
	// Watch logs during cluster installation
	watch bool

	// Basic options
	private            bool
	multiAZ            bool
	expirationDuration time.Duration
	expirationTime     string
	name               string
	region             string
	version            string

	// Scaling options
	computeMachineType string
	computeNodes       int

	// Networking options
	hostPrefix  int
	machineCIDR net.IPNet
	serviceCIDR net.IPNet
	podCIDR     net.IPNet
}

var Cmd = &cobra.Command{
	Use:   "cluster",
	Short: "Create cluster",
	Long:  "Create cluster.",
	Example: `  # Create a cluster named "mycluster"
  moactl create cluster --name=mycluster

  # Create a cluster in the us-east-2 region
  moactl create cluster --name=mycluster --region=us-east-2`,
	Run: run,
}

func init() {
	flags := Cmd.Flags()
	flags.SortFlags = false

	// Basic options
	flags.StringVarP(
		&args.name,
		"name",
		"n",
		"",
		"Name of the cluster. This will be used when generating a sub-domain for your cluster on openshiftapps.com.",
	)
	flags.StringVarP(
		&args.region,
		"region",
		"r",
		"",
		"AWS region where your worker pool will be located. (overrides the AWS_REGION environment variable)",
	)
	flags.StringVar(
		&args.version,
		"version",
		"",
		"Version of OpenShift that will be used to install the cluster, for example \"4.3.10\"",
	)
	flags.BoolVar(
		&args.multiAZ,
		"multi-az",
		false,
		"Deploy to multiple data centers.",
	)
	flags.StringVar(
		&args.expirationTime,
		"expiration-time",
		"",
		"Specific time when cluster should expire (RFC3339). Only one of expiration-time / expiration may be used.",
	)
	flags.DurationVar(
		&args.expirationDuration,
		"expiration",
		0,
		"Expire cluster after a relative duration like 2h, 8h, 72h. Only one of expiration-time / expiration may be used.\n",
	)
	// Cluster expiration is not supported in production
	flags.MarkHidden("expiration-time")
	flags.MarkHidden("expiration")

	// Scaling options
	flags.StringVar(
		&args.computeMachineType,
		"compute-machine-type",
		"",
		"Instance type for the compute nodes. Determines the amount of memory and vCPU allocated to each compute node.",
	)
	flags.IntVar(
		&args.computeNodes,
		"compute-nodes",
		0,
		"Number of worker nodes to provision per zone. Single zone clusters need at least 4 nodes, "+
			"while multizone clusters need at least 9 nodes (3 per zone) for resiliency.\n",
	)

	flags.IPNetVar(
		&args.machineCIDR,
		"machine-cidr",
		net.IPNet{},
		"Block of IP addresses used by OpenShift while installing the cluster, for example \"10.0.0.0/16\".",
	)
	flags.IPNetVar(
		&args.serviceCIDR,
		"service-cidr",
		net.IPNet{},
		"Block of IP addresses for services, for example \"172.30.0.0/16\".",
	)
	flags.IPNetVar(
		&args.podCIDR,
		"pod-cidr",
		net.IPNet{},
		"Block of IP addresses from which Pod IP addresses are allocated, for example \"10.128.0.0/14\".",
	)
	flags.IntVar(
		&args.hostPrefix,
		"host-prefix",
		0,
		"Subnet prefix length to assign to each individual node. For example, if host prefix is set "+
			"to \"23\", then each node is assigned a /23 subnet out of the given CIDR.",
	)
	flags.BoolVar(
		&args.private,
		"private",
		false,
		"Restrict master API endpoint and application routes to direct, private connectivity.\n",
	)

	flags.BoolVar(
		&args.watch,
		"watch",
		false,
		"Watch cluster installation logs.",
	)
}

func run(cmd *cobra.Command, _ []string) {
	reporter := rprtr.CreateReporterOrExit()
	logger := logging.CreateLoggerOrExit(reporter)
	var err error

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
	ocmClient := ocmConnection.ClustersMgmt().V1()

	if interactive.Enabled() {
		reporter.Infof("Interactive mode enabled.\n" +
			"Any optional fields can be left empty and a default will be selected.")
	}

	// Get cluster name
	name := args.name
	if interactive.Enabled() {
		name, err = interactive.GetString(interactive.Input{
			Question: "Cluster name",
			Help:     cmd.Flags().Lookup("name").Usage,
			Default:  name,
			Required: true,
		})
		if err != nil {
			reporter.Errorf("Expected a valid cluster name: %s", err)
			os.Exit(1)
		}
	}
	if !clusterprovider.IsValidClusterKey(name) {
		reporter.Errorf("Expected a valid cluster name")
		os.Exit(1)
	}

	// Get AWS region
	region, err := aws.GetRegion(args.region)
	if err != nil {
		reporter.Errorf("Error getting region: %v", err)
		os.Exit(1)
	}
	regionList, err := getRegionList(ocmClient)
	if err != nil {
		reporter.Errorf(fmt.Sprintf("%s", err))
		os.Exit(1)
	}
	if interactive.Enabled() {
		region, err = interactive.GetOption(interactive.Input{
			Question: "AWS region",
			Help:     cmd.Flags().Lookup("region").Usage,
			Options:  regionList,
			Default:  region,
			Required: true,
		})
		if err != nil {
			reporter.Errorf("Expected a valid AWS region: %s", err)
			os.Exit(1)
		}
	}
	if region == "" {
		reporter.Errorf("Expected a valid AWS region")
		os.Exit(1)
	}

	// OpenShift version:
	version := args.version
	versionList, err := getVersionList(ocmClient)
	if err != nil {
		reporter.Errorf(fmt.Sprintf("%s", err))
		os.Exit(1)
	}
	if interactive.Enabled() {
		version, err = interactive.GetOption(interactive.Input{
			Question: "OpenShift version",
			Help:     cmd.Flags().Lookup("version").Usage,
			Options:  versionList,
			Default:  version,
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

	// Multi-AZ:
	multiAZ := args.multiAZ
	if interactive.Enabled() {
		multiAZ, err = interactive.GetBool(interactive.Input{
			Question: "Multiple availability zones",
			Help:     cmd.Flags().Lookup("multi-az").Usage,
			Default:  multiAZ,
		})
		if err != nil {
			reporter.Errorf("Expected a valid multi-AZ value: %s", err)
			os.Exit(1)
		}
	}

	// Compute node instance type:
	computeMachineType := args.computeMachineType
	computeMachineTypeList, err := getMachineTypeList(ocmClient)
	if err != nil {
		reporter.Errorf(fmt.Sprintf("%s", err))
		os.Exit(1)
	}
	if interactive.Enabled() {
		computeMachineType, err = interactive.GetOption(interactive.Input{
			Question: "Compute nodes instance type",
			Help:     cmd.Flags().Lookup("compute-machine-type").Usage,
			Options:  computeMachineTypeList,
			Default:  computeMachineType,
		})
		if err != nil {
			reporter.Errorf("Expected a valid machine type: %s", err)
			os.Exit(1)
		}
	}
	computeMachineType, err = validateMachineType(computeMachineType, computeMachineTypeList)
	if err != nil {
		reporter.Errorf("Expected a valid machine type: %s", err)
		os.Exit(1)
	}

	// Compute nodes:
	computeNodes := args.computeNodes
	if interactive.Enabled() {
		computeNodes, err = interactive.GetInt(interactive.Input{
			Question: "Compute nodes",
			Help:     cmd.Flags().Lookup("compute-nodes").Usage,
			Default:  computeNodes,
		})
		if err != nil {
			reporter.Errorf("Expected a valid number of compute nodes: %s", err)
			os.Exit(1)
		}
	}

	// Validate all remaining flags:
	expiration, err := validateExpiration()
	if err != nil {
		reporter.Errorf(fmt.Sprintf("%s", err))
		os.Exit(1)
	}

	// Machine CIDR:
	machineCIDR := args.machineCIDR
	if interactive.Enabled() {
		machineCIDR, err = interactive.GetIPNet(interactive.Input{
			Question: "Machine CIDR",
			Help:     cmd.Flags().Lookup("machine-cidr").Usage,
			Default:  machineCIDR,
		})
		if err != nil {
			reporter.Errorf("Expected a valid CIDR value: %s", err)
			os.Exit(1)
		}
	}

	// Service CIDR:
	serviceCIDR := args.serviceCIDR
	if interactive.Enabled() {
		serviceCIDR, err = interactive.GetIPNet(interactive.Input{
			Question: "Service CIDR",
			Help:     cmd.Flags().Lookup("service-cidr").Usage,
			Default:  serviceCIDR,
		})
		if err != nil {
			reporter.Errorf("Expected a valid CIDR value: %s", err)
			os.Exit(1)
		}
	}

	// Pod CIDR:
	podCIDR := args.podCIDR
	if interactive.Enabled() {
		podCIDR, err = interactive.GetIPNet(interactive.Input{
			Question: "Pod CIDR",
			Help:     cmd.Flags().Lookup("pod-cidr").Usage,
			Default:  podCIDR,
		})
		if err != nil {
			reporter.Errorf("Expected a valid CIDR value: %s", err)
			os.Exit(1)
		}
	}

	// Host prefix:
	hostPrefix := args.hostPrefix
	if interactive.Enabled() {
		hostPrefix, err = interactive.GetInt(interactive.Input{
			Question: "Host prefix",
			Help:     cmd.Flags().Lookup("host-prefix").Usage,
			Default:  hostPrefix,
		})
		if err != nil {
			reporter.Errorf("Expected a valid host prefix value: %s", err)
			os.Exit(1)
		}
	}

	// Cluster privacy:
	private := args.private
	if interactive.Enabled() {
		private, err = interactive.GetBool(interactive.Input{
			Question: "Private cluster",
			Help:     cmd.Flags().Lookup("private").Usage,
			Default:  private,
		})
		if err != nil {
			reporter.Errorf("Expected a valid private value: %s", err)
			os.Exit(1)
		}
	}

	clusterConfig := clusterprovider.Spec{
		Name:               name,
		Region:             region,
		MultiAZ:            multiAZ,
		Version:            version,
		Expiration:         expiration,
		ComputeMachineType: computeMachineType,
		ComputeNodes:       computeNodes,
		MachineCIDR:        machineCIDR,
		ServiceCIDR:        serviceCIDR,
		PodCIDR:            podCIDR,
		HostPrefix:         hostPrefix,
		Private:            &private,
	}

	cluster, err := clusterprovider.CreateCluster(ocmClient.Clusters(), clusterConfig)
	if err != nil {
		reporter.Errorf("Failed to create cluster: %v", err)
		os.Exit(1)
	}

	clusterID := cluster.ID()
	clusterName := cluster.Name()
	reporter.Infof("Cluster '%s' has been created.", clusterName)
	reporter.Infof(
		"Once the cluster is installed you will need to add an Identity Provider " +
			"before you can login into the cluster. See 'moactl create idp --help' " +
			"for more information.")

	if args.watch {
		clusterlogs.Cmd.Run(cmd, []string{clusterID})
	} else {
		reporter.Infof(
			"To determine when your cluster is Ready, run 'moactl describe cluster -c %s'.",
			clusterName,
		)
		reporter.Infof(
			"To watch your cluster installation logs, run 'moactl logs cluster -c %s --watch'.",
			clusterName,
		)
	}
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

		version = "openshift-v" + version
	}

	return version, nil
}

func getVersionList(client *cmv1.Client) (versionList []string, err error) {
	versions, err := versions.GetVersions(client)
	if err != nil {
		err = fmt.Errorf("Failed to retrieve versions: %s", err)
		return
	}

	for _, v := range versions {
		versionList = append(versionList, strings.Replace(v.ID(), "openshift-v", "", 1))
	}

	return
}

func validateExpiration() (expiration time.Time, err error) {
	// Validate options
	if len(args.expirationTime) > 0 && args.expirationDuration != 0 {
		err = errors.New("At most one of 'expiration-time' or 'expiration' may be specified")
		return
	}

	// Parse the expiration options
	if len(args.expirationTime) > 0 {
		t, err := parseRFC3339(args.expirationTime)
		if err != nil {
			err = fmt.Errorf("Failed to parse expiration-time: %s", err)
			return expiration, err
		}

		expiration = t
	}
	if args.expirationDuration != 0 {
		// round up to the nearest second
		expiration = time.Now().Add(args.expirationDuration).Round(time.Second)
	}

	return
}

// Validate AWS machine types
func validateMachineType(machineType string, machineTypeList []string) (string, error) {
	if machineType != "" {
		// Check and set the cluster machineType
		hasMachineType := false
		for _, v := range machineTypeList {
			if v == machineType {
				hasMachineType = true
			}
		}
		if !hasMachineType {
			allMachineTypes := strings.Join(machineTypeList, " ")
			err := fmt.Errorf("A valid machine type number must be specified\nValid machine types: %s", allMachineTypes)
			return machineType, err
		}
	}

	return machineType, nil
}

func getMachineTypeList(client *cmv1.Client) (machineTypeList []string, err error) {
	machineTypes, err := machines.GetMachineTypes(client)
	if err != nil {
		err = fmt.Errorf("Failed to retrieve machine types: %s", err)
		return
	}

	for _, v := range machineTypes {
		machineTypeList = append(machineTypeList, v.ID())
	}

	return
}

func getRegionList(client *cmv1.Client) (regionList []string, err error) {
	regions, err := regions.GetRegions(client)
	if err != nil {
		err = fmt.Errorf("Failed to retrieve AWS regions: %s", err)
		return
	}

	for _, v := range regions {
		regionList = append(regionList, v.ID())
	}

	return
}

// parseRFC3339 parses an RFC3339 date in either RFC3339Nano or RFC3339 format.
func parseRFC3339(s string) (time.Time, error) {
	if t, timeErr := time.Parse(time.RFC3339Nano, s); timeErr == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}
