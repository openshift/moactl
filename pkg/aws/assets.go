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

	"github.com/openshift/rosa/assets"
)

// PolicyDocument models an AWS IAM policy document
type PolicyDocument struct {
	Version   string            `json:"version,omitempty"`
	ID        string            `json:"id,omitempty"`
	Statement []PolicyStatement `json:"statement"`
}

// PolicyStatement models an AWS policy statement entry.
type PolicyStatement struct {
	Sid string `json:"sid,omitempty"`
	// Effect indicates if this policy statement is to Allow or Deny.
	Effect string `json:"effect"`
	// Action describes the particular AWS service actions that should be allowed or denied.
	// (i.e. ec2:StartInstances, iam:ChangePassword)
	Action []string `json:"action"`
	// Resource specifies the object(s) this statement should apply to. (or "*" for all)
	Resource interface{} `json:"resource"`
}

func readPolicyDocument(path string) PolicyDocument {
	file, err := assets.Asset(path)
	if err != nil {
		fmt.Println(fmt.Errorf("Unable to load file: %s", path))
	}

	policyDocument := PolicyDocument{}

	err = json.Unmarshal(file, &policyDocument)
	if err != nil {
		fmt.Println(fmt.Errorf("Error unmarshalling statement: %v", err))
	}

	return policyDocument
}

func readCloudFormationTemplate(path string) (string, error) {
	cfTemplate, err := assets.Asset(path)
	if err != nil {
		return "", fmt.Errorf("Unable to read cloudformation template: %s", err)
	}

	return string(cfTemplate), nil
}
