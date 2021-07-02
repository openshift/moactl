package aws

import (
	"fmt"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/arn"
	"github.com/aws/aws-sdk-go/service/iam"
)

// SimulateParams captures any additional details that should be used
// when simulating permissions.
type SimulateParams struct {
	Region string
}

// ValidateSCP attempts to validate SCP policies by ensuring we have the correct permissions
func (c *awsClient) ValidateSCP(target *string) (bool, error) {
	scpPolicyPath := "templates/policies/osd_scp_policy.json"

	sParams := &SimulateParams{
		Region: *c.awsSession.Config.Region,
	}

	// Read installer permissions and OSD SCP Policy permissions
	osdPolicyDocument := readPolicyDocument(scpPolicyPath)
	policyDocuments := []PolicyDocument{osdPolicyDocument}

	// Get Creator details
	creator, err := c.GetCreator()
	if err != nil {
		return false, err
	}

	// Find target user
	var targetUserARN arn.ARN
	if target == nil {
		var err error
		callerIdentity, _, err := getClientDetails(c)
		if err != nil {
			return false, fmt.Errorf("getClientDetails: %v\n"+
				"Run 'rosa init' and try again", err)
		}
		targetUserARN, err = arn.Parse(*callerIdentity.Arn)
		if err != nil {
			return false, fmt.Errorf("unable to parse caller ARN %v", err)
		}
		// If the client is using STS credentials want to validate the role
		// the user has assumed. GetCreator() resolves that for us and updates
		// the ARN
		if creator.IsSTS {
			targetUserARN, err = arn.Parse(creator.ARN)
			if err != nil {
				return false, err
			}
		}
	} else {
		targetIAMOutput, err := c.iamClient.GetUser(&iam.GetUserInput{UserName: target})
		if err != nil {
			return false, fmt.Errorf("iamClient.GetUser: %v\n"+
				"To reset the '%s' account, run 'rosa init --delete-stack' and try again", *target, err)
		}
		targetUserARN, err = arn.Parse(*targetIAMOutput.User.Arn)
		if err != nil {
			return false, fmt.Errorf("unable to parse caller ARN %v", err)
		}
	}

	// Validate permissions
	hasPermissions, err := validatePolicyDocuments(c, targetUserARN.String(), policyDocuments, sParams)
	if err != nil {
		return false, err
	}
	if !hasPermissions {
		return false, err
	}

	return true, nil
}

// checkPermissionsUsingQueryClient will use queryClient to query whether the credentials in targetClient can perform
// the actions listed in the statementEntries. queryClient will need
// sts:GetCallerIdentity and iam:SimulatePrincipalPolicy
func checkPermissionsUsingQueryClient(queryClient *awsClient, targetUserARN string, policyDocument PolicyDocument,
	params *SimulateParams) (bool, error) {
	// Ignoring isRoot here since we only warn the user that its not best practice to use it.
	// TODO: Add a check for isRoot in the initialize
	allowList := []*string{}
	for _, statement := range policyDocument.Statement {
		for _, action := range statement.Action {
			allowList = append(allowList, aws.String(action))
		}
	}

	input := &iam.SimulatePrincipalPolicyInput{
		PolicySourceArn: aws.String(targetUserARN),
		ActionNames:     allowList,
		ContextEntries:  []*iam.ContextEntry{},
	}

	if params != nil {
		if params.Region != "" {
			input.ContextEntries = append(input.ContextEntries, &iam.ContextEntry{
				ContextKeyName:   aws.String("aws:RequestedRegion"),
				ContextKeyType:   aws.String("stringList"),
				ContextKeyValues: []*string{aws.String(params.Region)},
			})
		}
	}

	// Either all actions are allowed and we'll return 'true', or it's a failure
	allClear := true
	// Collect all failed actions
	var failedActions []string

	err := queryClient.iamClient.SimulatePrincipalPolicyPages(input,
		func(response *iam.SimulatePolicyResponse, lastPage bool) bool {
			for _, result := range response.EvaluationResults {
				if *result.EvalDecision != "allowed" {
					// Don't bail out after the first failure, so we can log the full list
					// of failed/denied actions
					failedActions = append(failedActions, *result.EvalActionName)
					allClear = false
				}
			}
			return !lastPage
		})
	if err != nil {
		return false, fmt.Errorf("Error simulating policy: %v", err)
	}

	if !allClear {
		return false, fmt.Errorf("Actions not allowed with tested credentials: %v", failedActions)
	}

	return true, nil
}

func validatePolicyDocuments(queryClient *awsClient, targetUserARN string, policyDocuments []PolicyDocument,
	sParams *SimulateParams) (bool, error) {
	for _, policyDocument := range policyDocuments {
		permissionsOk, err := checkPermissionsUsingQueryClient(queryClient, targetUserARN, policyDocument, sParams)
		if err != nil {
			return false, err
		}
		if !permissionsOk {
			return false, fmt.Errorf("Unable to validate permissions in %s", policyDocument.ID)
		}
	}

	return true, nil
}
