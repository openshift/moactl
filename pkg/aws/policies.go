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

package aws

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/service/iam"

	"github.com/openshift/rosa/assets"
)

type Operator struct {
	Name      string
	Namespace string
}

var CredentialRequests map[string]Operator = map[string]Operator{
	"machine_api_aws_cloud_credentials": {
		Name:      "aws-cloud-credentials",
		Namespace: "openshift-machine-api",
	},
	"cloud_credential_operator_cloud_credential_operator_iam_ro_creds": {
		Name:      "cloud-credential-operator-iam-ro-creds",
		Namespace: "openshift-cloud-credential-operator",
	},
	"image_registry_installer_cloud_credentials": {
		Name:      "installer-cloud-credentials",
		Namespace: "openshift-image-registry",
	},
	"ingress_operator_cloud_credentials": {
		Name:      "cloud-credentials",
		Namespace: "openshift-ingress-operator",
	},
	"cluster_csi_drivers_ebs_cloud_credentials": {
		Name:      "ebs-cloud-credentials",
		Namespace: "openshift-cluster-csi-drivers",
	},
}

var AccountRoles map[string]string = map[string]string{
	"installer":             "Installer",
	"instance_controlplane": "ControlPlane",
	"instance_worker":       "Worker",
	"support":               "Support",
}

// PolicyDocument models an AWS IAM policy document
type PolicyDocument struct {
	ID string `json:"Id,omitempty"`
	// Specify the version of the policy language that you want to use.
	// As a best practice, use the latest 2012-10-17 version.
	Version string `json:"Version,omitempty"`
	// Use this main policy element as a container for the following elements.
	// You can include more than one statement in a policy.
	Statement []PolicyStatement `json:"Statement"`
}

// PolicyStatement models an AWS policy statement entry.
type PolicyStatement struct {
	// Include an optional statement ID to differentiate between your statements.
	Sid string `json:"Sid,omitempty"`
	// Use `Allow` or `Deny` to indicate whether the policy allows or denies access.
	Effect string `json:"Effect"`
	// If you create a resource-based policy, you must indicate the account, user, role, or
	// federated user to which you would like to allow or deny access. If you are creating an
	// IAM permissions policy to attach to a user or role, you cannot include this element.
	// The principal is implied as that user or role.
	Principal []PolicyStatementPrincipal `json:"Principal"`
	// Include a list of actions that the policy allows or denies.
	// (i.e. ec2:StartInstances, iam:ChangePassword)
	Action []string `json:"Action"`
	// If you create an IAM permissions policy, you must specify a list of resources to which
	// the actions apply. If you create a resource-based policy, this element is optional. If
	// you do not include this element, then the resource to which the action applies is the
	// resource to which the policy is attached.
	Resource []string `json:"Resource"`
}

type PolicyStatementPrincipal struct {
	// A service principal is an identifier that is used to grant permissions to a service.
	// The identifier for a service principal includes the service name, and is usually in the
	// following format: service-name.amazonaws.com
	Service []string `json:"Service"`
	// You can specify an individual IAM role ARN (or array of role ARNs) as the principal.
	// In IAM roles, the Principal element in the role's trust policy specifies who can assume the role.
	// When you specify more than one principal in the element, you grant permissions to each principal.
	AWS []string `json:"AWS"`
}

func (c *awsClient) EnsureRole(name string, policy string, tagList map[string]string) error {
	_, err := c.iamClient.CreateRole(&iam.CreateRoleInput{
		RoleName:                 aws.String(name),
		AssumeRolePolicyDocument: aws.String(policy),
		Tags:                     getTags(tagList),
	})
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case iam.ErrCodeEntityAlreadyExistsException:
				return nil
			}
		}
		return err
	}
	return nil
}

func (c *awsClient) PutRolePolicy(roleName string, policyName string, policy string) error {
	_, err := c.iamClient.PutRolePolicy(&iam.PutRolePolicyInput{
		RoleName:       aws.String(roleName),
		PolicyName:     aws.String(policyName),
		PolicyDocument: aws.String(policy),
	})
	if err != nil {
		return err
	}
	return nil
}

func (c *awsClient) EnsurePolicy(name string, document string, tagList map[string]string) error {
	_, err := c.iamClient.CreatePolicy(&iam.CreatePolicyInput{
		PolicyName:     aws.String(name),
		PolicyDocument: aws.String(document),
		Tags:           getTags(tagList),
	})
	if err != nil {
		if aerr, ok := err.(awserr.Error); ok {
			switch aerr.Code() {
			case iam.ErrCodeEntityAlreadyExistsException:
				return nil
			}
		}
		return err
	}
	return nil
}

func getTags(tagList map[string]string) []*iam.Tag {
	iamTags := []*iam.Tag{}
	for k, v := range tagList {
		iamTags = append(iamTags, &iam.Tag{
			Key:   aws.String(k),
			Value: aws.String(v),
		})
	}
	return iamTags
}

func ReadPolicyDocument(path string, args ...map[string]string) ([]byte, error) {
	bytes, err := assets.Asset(path)
	if err != nil {
		return nil, fmt.Errorf("Unable to load file %s: %s", path, err)
	}
	file := string(bytes)
	if len(args) > 0 {
		for key, val := range args[0] {
			file = strings.Replace(file, fmt.Sprintf("%%{%s}", key), val, -1)
		}
	}
	return []byte(file), nil
}

func parsePolicyDocument(path string) (PolicyDocument, error) {
	doc := PolicyDocument{}

	file, err := ReadPolicyDocument(path)
	if err != nil {
		return doc, err
	}

	err = json.Unmarshal(file, &doc)
	if err != nil {
		return doc, fmt.Errorf("Error unmarshalling statement: %s", err)
	}

	return doc, nil
}
