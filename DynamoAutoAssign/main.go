package main

import (
	"fmt"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/dynamodb/dynamodbattribute"
	"github.com/aws/aws-sdk-go/service/identitystore"
	"github.com/aws/aws-sdk-go/service/organizations"
	"github.com/aws/aws-sdk-go/service/ssoadmin"
	"os"
	"strings"
)

var (
	maxReturnPermissionSet = os.Getenv("MAX_RETURN_PERMISSION_SET")
	ssoInstanceARN         = os.Getenv("SSO_INSTANCE_ARN")
	directoryServiceID     = os.Getenv("DIRECTORY_SERVICE_ID")
)

type AWSOrgAccount struct {
	Name string
	ID   string
}

type OrgSSOGroup struct {
	GroupID string
}

func getAccountIDDynamo(accountName string) string {
	dynamoDB := dynamodb.New(session.New())
	table := "AWSOrgAccounts"

	queryInput := &dynamodb.QueryInput{
		TableName: &table,
		IndexName: aws.String("AccountNameIndex"),
		KeyConditions: map[string]*dynamodb.Condition{
			"Name": {
				ComparisonOperator: aws.String("EQ"),
				AttributeValueList: []*dynamodb.AttributeValue{
					{
						S: aws.String(accountName),
					},
				},
			},
		},
	}

	response, err := dynamoDB.Query(queryInput)
	if err != nil {
		fmt.Println("Error querying DynamoDB:", err)
		return ""
	}

	var account AWSOrgAccount
	err = dynamodbattribute.UnmarshalMap(response.Items[0], &account)
	if err != nil {
		fmt.Println("Error unmarshalling DynamoDB response:", err)
		return ""
	}

	return account.ID
}

func getPermIDFromName(perms *ssoadmin.ListPermissionSetsOutput, permissionSetName string, ssoAdminClient *ssoadmin.SSOAdmin) string {
	for _, perm := range perms.PermissionSets {
		permDetailsInput := &ssoadmin.DescribePermissionSetInput{
			InstanceArn:      aws.String(ssoInstanceARN),
			PermissionSetArn: perm,
		}
		permDetails, err := ssoAdminClient.DescribePermissionSet(permDetailsInput)
		if err != nil {
			fmt.Println("Error describing permission set:", err)
			continue
		}

		if *permDetails.PermissionSet.Name == permissionSetName {
			fmt.Println("Permission set ARN:", *permDetails.PermissionSet.PermissionSetArn)
			return *permDetails.PermissionSet.PermissionSetArn
		}

		fmt.Println("Permission set details:")
		fmt.Println(permDetails)
	}

	return ""
}

func getAllAccountIDs(accountsList []*organizations.Account) []string {
	var allAccountsOrg []string
	for _, account := range accountsList {
		allAccountsOrg = append(allAccountsOrg, *account.Id)
	}
	return allAccountsOrg
}

func getGroupByGroupName(groupName string) string {
	identityServicesClient := identitystore.New(session.New())

	groupInfoInput := &identitystore.ListGroupsInput{
		IdentityStoreId: aws.String(directoryServiceID),
		Filters: []*identitystore.Filter{
			{
				AttributePath:  aws.String("DisplayName"),
				AttributeValue: aws.String(groupName),
			},
		},
	}

	groupInfo, err := identityServicesClient.ListGroups(groupInfoInput)
	if err != nil {
		fmt.Println("Error listing groups:", err)
		return ""
	}

	fmt.Println(groupInfo)

	return *groupInfo.Groups[0].GroupId
}

func getGroupsByGroupName(groupName string) []string {
	var groupIDs []string

	identityServicesClient := identitystore.New(session.New())

	groupInfoInput := &identitystore.ListGroupsInput{
		IdentityStoreId: aws.String(directoryServiceID),
		Filters: []*identitystore.Filter{
			{
				AttributePath:  aws.String("DisplayName"),
				AttributeValue: aws.String(groupName),
			},
		},
	}

	groupInfo, err := identityServicesClient.ListGroups(groupInfoInput)
	if err != nil {
		fmt.Println("Error listing groups:", err)
		return nil
	}

	fmt.Println(groupInfo)
	groupCount := len(groupInfo.Groups)

	for groupListNumber := 0; groupListNumber <= groupCount; groupListNumber++ {
		groupIDs = append(groupIDs, *groupInfo.Groups[groupListNumber].GroupId)
	}

	return groupIDs
}

func associateSSO(ssoInstanceARN, accountID, permSetARN, groupID string, channel chan *ssoadmin.CreateAccountAssignmentOutput) *ssoadmin.CreateAccountAssignmentOutput {
	ssoAdminClient := ssoadmin.New(session.New())

	response, err := ssoAdminClient.CreateAccountAssignment(&ssoadmin.CreateAccountAssignmentInput{
		InstanceArn:      aws.String(ssoInstanceARN),
		TargetId:         aws.String(accountID),
		TargetType:       aws.String("AWS_ACCOUNT"),
		PermissionSetArn: aws.String(permSetARN),
		PrincipalType:    aws.String("GROUP"),
		PrincipalId:      aws.String(groupID),
	})

	if err != nil {
		fmt.Println("Error creating account assignment:", err)
		return nil
	}

	channel <- response
}

func getDynamoOrgGroups() []OrgSSOGroup {
	dynamoDB := dynamodb.New(session.New())
	table := "ORGSSOGroups"

	scanInput := &dynamodb.ScanInput{
		TableName: aws.String(table),
	}

	response, err := dynamoDB.Scan(scanInput)
	if err != nil {
		fmt.Println("Error scanning DynamoDB:", err)
		return nil
	}

	var orgGroups []OrgSSOGroup
	err = dynamodbattribute.UnmarshalListOfMaps(response.Items, &orgGroups)
	if err != nil {
		fmt.Println("Error unmarshalling DynamoDB response:", err)
		return nil
	}

	return orgGroups
}

func lambdaHandler(event events.CloudWatchEvent) {
	eventName := event.Detail["eventName"].(string)

	if eventName == "CreateGroup" {
		ssoAdminClient := ssoadmin.New(session.New())
		organizationsClient := organizations.New(session.New())

		getPermSetsOutput, err := ssoAdminClient.ListPermissionSets(&ssoadmin.ListPermissionSetsInput{
			InstanceArn: aws.String(ssoInstanceARN),
			MaxResults:  aws.Int64(100),
		})

		if err != nil {
			fmt.Println("Error listing permission sets:", err)
			return
		}

		group, ok := event.Detail["responseElements"].(map[string]interface{})["group"].(map[string]interface{})
		if !ok {
			return
		}

		groupName := group["displayName"].(string)
		groupNameSplit := strings.Split(groupName, "-")

		if groupNameSplit[1] == "A" {
			fmt.Println("Printing A group")
			accountName := groupNameSplit[2]
			permissionSet := groupNameSplit[3]
			fmt.Printf("Account Name: %s, Permission Set: %s, GroupName: %s\n", accountName, permissionSet, groupName)
			accountID := getAccountIDDynamo(accountName)
			groupID := getGroupByGroupName(groupName)

			permSetARN := getPermIDFromName(getPermSetsOutput, permissionSet, ssoAdminClient)
			fmt.Printf("The permission set arn is %s\n", permSetARN)
			associateChan := make(chan *ssoadmin.CreateAccountAssignmentOutput)
			go associateSSO(ssoInstanceARN, accountID, permSetARN, groupID, associateChan)
			processedItem := <-associateChan
			fmt.Println(processedItem)
		}

		if groupNameSplit[1] == "O" {
			permissionSet := groupNameSplit[2]
			accountsOutput, err := organizationsClient.ListAccounts(nil)
			if err != nil {
				fmt.Println("Error listing accounts:", err)
				return
			}

			accountIDs := getAllAccountIDs(accountsOutput.Accounts)
			groupID := getGroupByGroupName(groupName)

			permSetARN := getPermIDFromName(getPermSetsOutput, permissionSet, ssoAdminClient)
			associateChan := make(chan *ssoadmin.CreateAccountAssignmentOutput)
			for _, account := range accountIDs {
				go associateSSO(ssoInstanceARN, account, permSetARN, groupID, associateChan)
				processedItem := <-associateChan
				fmt.Println(processedItem)
			}
		}
	} else if eventName == "CreateManagedAccount" {
		fmt.Println("MANAGED ACCOUNT")
		ssoAdminClient := ssoadmin.New(session.New())

		getPermSetsOutput, err := ssoAdminClient.ListPermissionSets(&ssoadmin.ListPermissionSetsInput{
			InstanceArn: aws.String(ssoInstanceARN),
			MaxResults:  aws.Int64(100),
		})

		if err != nil {
			fmt.Println("Error listing permission sets:", err)
			return
		}

		groupNames := getDynamoOrgGroups()
		accountInfo, ok := event.Detail["serviceEventDetails"].(map[string]interface{})["createManagedAccountStatus"].(map[string]interface{})
		if !ok {
			return
		}

		accountID := accountInfo["account"].(map[string]interface{})["accountId"].(string)
		associateChan := make(chan *ssoadmin.CreateAccountAssignmentOutput)
		for _, group := range groupNames {
			groupName := group.GroupID
			groupID := getGroupByGroupName(groupName)
			groupToID := strings.Split(groupName, "-")
			permName := groupToID[2]

			permSetARN := getPermIDFromName(getPermSetsOutput, permName, ssoAdminClient)
			go associateSSO(ssoInstanceARN, accountID, permSetARN, groupID, associateChan)
			ssoAssociateResponse := <-associateChan
			fmt.Println(ssoAssociateResponse)
		}
	} else {
		fmt.Println("Nothing I can do here")
	}
}
