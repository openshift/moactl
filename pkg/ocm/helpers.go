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

package ocm

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"

	amsv1 "github.com/openshift-online/ocm-sdk-go/accountsmgmt/v1"
	cmv1 "github.com/openshift-online/ocm-sdk-go/clustersmgmt/v1"
	ocmerrors "github.com/openshift-online/ocm-sdk-go/errors"
)

const (
	ANY                 = "any"
	HibernateCapability = "capability.organization.hibernate_cluster"
)

// Regular expression to used to make sure that the identifier or name given by the user is
// safe and that it there is no risk of SQL injection:
var clusterKeyRE = regexp.MustCompile(`^(\w|-)+$`)

// Cluster names must be valid DNS-1035 labels, so they must consist of lower case alphanumeric
// characters or '-', start with an alphabetic character, and end with an alphanumeric character
var clusterNameRE = regexp.MustCompile(`^[a-z]([-a-z0-9]{0,13}[a-z0-9])?$`)

var badUsernameRE = regexp.MustCompile(`^(~|\.?\.|.*[:\/%].*)$`)

func IsValidClusterKey(clusterKey string) bool {
	return clusterKeyRE.MatchString(clusterKey)
}

func IsValidClusterName(clusterName string) bool {
	return clusterNameRE.MatchString(clusterName)
}

func IsValidUsername(username string) bool {
	return !badUsernameRE.MatchString(username)
}

func IsEmptyCIDR(cidr net.IPNet) bool {
	return cidr.String() == "<nil>"
}

// Determine whether a resources is compatible with ROSA clusters in general
func isCompatible(relatedResource *amsv1.RelatedResource) bool {
	product := strings.ToLower(relatedResource.Product())
	cloudProvider := strings.ToLower(relatedResource.CloudProvider())
	byoc := strings.ToLower(relatedResource.BYOC())

	// nolint:goconst
	return (product == ANY || product == "rosa" || product == "moa") &&
		(cloudProvider == ANY || cloudProvider == "aws") &&
		(byoc == ANY || byoc == "byoc")
}

func handleErr(res *ocmerrors.Error, err error) error {
	msg := res.Reason()
	if msg == "" {
		msg = err.Error()
	}
	// Hack to always display the correct terms and conditions message
	if res.Code() == "CLUSTERS-MGMT-451" {
		msg = "You must accept the Terms and Conditions in order to continue.\n" +
			"Go to https://www.redhat.com/wapps/tnc/ackrequired?site=ocm&event=register\n" +
			"Once you accept the terms, you will need to retry the action that was blocked."
	}
	return errors.New(msg)
}

func (c *Client) GetDefaultClusterFlavors(flavour string) (dMachinecidr *net.IPNet, dPodcidr *net.IPNet,
	dServicecidr *net.IPNet, dhostPrefix int) {
	flavourGetResponse, err := c.ocm.ClustersMgmt().V1().Flavours().Flavour(flavour).Get().Send()
	if err != nil {
		flavourGetResponse, _ = c.ocm.ClustersMgmt().V1().Flavours().Flavour("osd-4").Get().Send()
	}
	network, ok := flavourGetResponse.Body().GetNetwork()
	if !ok {
		return nil, nil, nil, 0
	}
	_, dMachinecidr, err = net.ParseCIDR(network.MachineCIDR())
	if err != nil {
		dMachinecidr = nil
	}
	_, dPodcidr, err = net.ParseCIDR(network.PodCIDR())
	if err != nil {
		dPodcidr = nil
	}
	_, dServicecidr, err = net.ParseCIDR(network.ServiceCIDR())
	if err != nil {
		dServicecidr = nil
	}
	dhostPrefix, _ = network.GetHostPrefix()
	return dMachinecidr, dPodcidr, dServicecidr, dhostPrefix
}

func (c *Client) LogEvent(key string) {
	event, err := cmv1.NewEvent().Key(key).Build()
	if err == nil {
		_, _ = c.ocm.ClustersMgmt().V1().
			Events().
			Add().
			Body(event).
			Send()
	}
}

func (c *Client) GetCurrentAccount() (*amsv1.Account, error) {
	response, err := c.ocm.AccountsMgmt().V1().
		CurrentAccount().
		Get().
		Send()
	if err != nil {
		if response.Status() == http.StatusNotFound {
			return nil, nil
		}
		return nil, handleErr(response.Error(), err)
	}
	return response.Body(), nil
}

func (c *Client) GetCurrentOrganization() (string, error) {
	acctResponse, err := c.GetCurrentAccount()

	if err != nil {
		return "", err
	}
	return acctResponse.Organization().ID(), nil
}

func (c *Client) IsHibernateCapabilityEnabled() error {
	organizationID, err := c.GetCurrentOrganization()
	if err != nil {
		return err
	}
	isCapabilityEnable, err := c.IsCapabilityEnabled(HibernateCapability, organizationID)

	if err != nil {
		return err
	}
	if !isCapabilityEnable {
		return fmt.Errorf("The '%s' capability is not set for org '%s'", HibernateCapability, organizationID)
	}
	return nil
}

func (c *Client) IsCapabilityEnabled(capabilityName string, orgID string) (bool, error) {
	capabilityResponse, err := c.ocm.AccountsMgmt().V1().Organizations().
		Organization(orgID).Get().Parameter("fetchCapabilities", true).Send()

	if err != nil {
		return false, handleErr(capabilityResponse.Error(), err)
	}
	if len(capabilityResponse.Body().Capabilities()) > 0 {
		for _, capability := range capabilityResponse.Body().Capabilities() {
			if capability.Name() == capabilityName {
				return capability.Value() == "true", nil
			}
		}
	}
	return false, nil
}
