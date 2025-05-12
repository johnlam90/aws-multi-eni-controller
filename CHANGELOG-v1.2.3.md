# AWS Multi-ENI Controller v1.2.3

## New Features
- Improved detection of manually detached ENIs to avoid manual intervention
- Enhanced ENI attachment verification to be more reliable
- Added more robust checks for ENI attachment status

## Bug Fixes
- Fixed issue where controller would not detect manually detached ENIs without manual intervention
- Fixed issue where controller would keep stale ENI attachments in the NodeENI status

## Technical Changes
- Enhanced the `DescribeENI` method to more accurately check the attachment status
- Modified the `verifyENIAttachments` method to be more aggressive in detecting detached ENIs
- Added a direct check against the AWS API for the attachment status
- Updated the `DetachENI` method to handle the case where we're just checking if an attachment exists
- Added DryRun mode for attachment verification to avoid accidentally detaching ENIs

## Improvements
- Added more detailed logging for ENI attachment status
- Updated the last updated timestamp for ENI attachments
- Added additional checks to verify that ENIs are attached to the correct instance

## Compatibility
- No breaking changes
- Existing NodeENI resources will be automatically updated with the improved detection logic
- No changes to the CRD schema

## Upgrade Instructions
1. Update your deployment to use the v1.2.3 tag:
   ```yaml
   image: ghcr.io/johnlam90/aws-multi-eni-controller:v1.2.3
   ```
2. Apply the updated deployment:
   ```bash
   kubectl apply -f deploy/deployment.yaml
   ```
   
   Or using Helm:
   ```bash
   helm upgrade --install aws-multi-eni-controller aws-multi-eni-controller/aws-multi-eni-controller
   ```
