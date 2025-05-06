package controller

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/go-logr/logr"
	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	// NodeENIFinalizer is the finalizer added to NodeENI resources
	NodeENIFinalizer = "nodeeni.networking.k8s.aws/finalizer"
)

// NodeENIReconciler reconciles a NodeENI object
type NodeENIReconciler struct {
	client.Client
	Log      logr.Logger
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	EC2      *ec2.EC2
}

// NewNodeENIReconciler creates a new NodeENI controller
func NewNodeENIReconciler(mgr manager.Manager) (*NodeENIReconciler, error) {
	// Get AWS region from environment variable
	// This should be set in the deployment manifest
	awsRegion := os.Getenv("AWS_REGION")
	if awsRegion == "" {
		// Default to us-east-1 if not specified, but log a warning
		awsRegion = "us-east-1"
		fmt.Fprintf(os.Stderr, "WARNING: AWS_REGION environment variable not set, defaulting to %s\n", awsRegion)
	}

	// Create AWS session
	sess, err := session.NewSession(&aws.Config{
		Region: aws.String(awsRegion),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create AWS session: %v", err)
	}

	return &NodeENIReconciler{
		Client:   mgr.GetClient(),
		Log:      ctrl.Log.WithName("controllers").WithName("NodeENI"),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorderFor("nodeeni-controller"),
		EC2:      ec2.New(sess),
	}, nil
}

// SetupWithManager sets up the controller with the Manager
func (r *NodeENIReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&networkingv1alpha1.NodeENI{}).
		Watches(
			&source.Kind{Type: &corev1.Node{}},
			handler.EnqueueRequestsFromMapFunc(r.findNodeENIsForNode),
		).
		WithOptions(controller.Options{MaxConcurrentReconciles: 5}).
		Complete(r)
}

// findNodeENIsForNode maps a Node to NodeENI resources that match its labels
func (r *NodeENIReconciler) findNodeENIsForNode(obj client.Object) []reconcile.Request {
	nodeENIList := &networkingv1alpha1.NodeENIList{}
	err := r.Client.List(context.Background(), nodeENIList)
	if err != nil {
		r.Log.Error(err, "Failed to list NodeENIs")
		return nil
	}

	node, ok := obj.(*corev1.Node)
	if !ok {
		r.Log.Error(nil, "Failed to convert to Node", "object", obj)
		return nil
	}

	var requests []reconcile.Request
	for _, nodeENI := range nodeENIList.Items {
		selector := labels.SelectorFromSet(nodeENI.Spec.NodeSelector)
		if selector.Matches(labels.Set(node.Labels)) {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: nodeENI.Name,
				},
			})
		}
	}

	return requests
}

// Reconcile handles NodeENI resources
// +kubebuilder:rbac:groups=networking.k8s.aws,resources=nodeenis,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.aws,resources=nodeenis/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=core,resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
func (r *NodeENIReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("nodeeni", req.NamespacedName)

	// Fetch the NodeENI instance
	nodeENI := &networkingv1alpha1.NodeENI{}
	err := r.Get(ctx, req.NamespacedName, nodeENI)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request
		return ctrl.Result{}, err
	}

	// Check if the NodeENI is being deleted
	if !nodeENI.DeletionTimestamp.IsZero() {
		// The object is being deleted
		if controllerutil.ContainsFinalizer(nodeENI, NodeENIFinalizer) {
			// Our finalizer is present, so we need to clean up resources
			log.Info("Cleaning up resources for NodeENI being deleted", "name", nodeENI.Name)

			// Clean up all ENI attachments
			for _, attachment := range nodeENI.Status.Attachments {
				log.Info("Detaching and deleting ENI", "node", attachment.NodeID, "eniID", attachment.ENIID)

				// Detach the ENI if it's attached
				if attachment.AttachmentID != "" {
					err := r.detachENI(ctx, attachment.AttachmentID)
					if err != nil {
						log.Error(err, "Failed to detach ENI", "attachmentID", attachment.AttachmentID)
						r.Recorder.Eventf(nodeENI, corev1.EventTypeWarning, "ENIDetachmentFailed",
							"Failed to detach ENI %s from node %s: %v", attachment.ENIID, attachment.NodeID, err)
						// Don't return an error, try to delete other ENIs
					}
				}

				// Wait longer for the detachment to complete
				log.Info("Waiting for ENI detachment to complete", "eniID", attachment.ENIID)
				time.Sleep(15 * time.Second)

				// Check if the ENI is detached
				if attachment.ENIID != "" {
					// Try to describe the ENI to check its status
					describeInput := &ec2.DescribeNetworkInterfacesInput{
						NetworkInterfaceIds: []*string{aws.String(attachment.ENIID)},
					}

					describeResult, describeErr := r.EC2.DescribeNetworkInterfaces(describeInput)
					if describeErr != nil {
						log.Error(describeErr, "Failed to describe ENI", "eniID", attachment.ENIID)
						continue
					}

					if len(describeResult.NetworkInterfaces) == 0 {
						log.Info("ENI no longer exists", "eniID", attachment.ENIID)
						continue
					}

					eni := describeResult.NetworkInterfaces[0]
					if eni.Attachment != nil && *eni.Status != "available" {
						log.Info("ENI is still attached, waiting longer", "eniID", attachment.ENIID, "status", *eni.Status)
						time.Sleep(15 * time.Second)
					}

					// Delete the ENI
					err := r.deleteENI(ctx, attachment.ENIID)
					if err != nil {
						log.Error(err, "Failed to delete ENI", "eniID", attachment.ENIID)
						r.Recorder.Eventf(nodeENI, corev1.EventTypeWarning, "ENIDeletionFailed",
							"Failed to delete ENI %s: %v", attachment.ENIID, err)
						// Don't return an error, try to delete other ENIs
					} else {
						log.Info("Successfully deleted ENI", "eniID", attachment.ENIID)
						r.Recorder.Eventf(nodeENI, corev1.EventTypeNormal, "ENIDeleted",
							"Successfully deleted ENI %s", attachment.ENIID)
					}
				}
			}

			// Remove our finalizer from the list and update it
			controllerutil.RemoveFinalizer(nodeENI, NodeENIFinalizer)
			if err := r.Update(ctx, nodeENI); err != nil {
				return ctrl.Result{}, err
			}
		}

		// Stop reconciliation as the item is being deleted
		return ctrl.Result{}, nil
	}

	// Add finalizer if it doesn't exist
	if !controllerutil.ContainsFinalizer(nodeENI, NodeENIFinalizer) {
		controllerutil.AddFinalizer(nodeENI, NodeENIFinalizer)
		if err := r.Update(ctx, nodeENI); err != nil {
			return ctrl.Result{}, err
		}
		// Return here to avoid processing the rest of the reconciliation
		// The update will trigger another reconciliation
		return ctrl.Result{}, nil
	}

	// List all nodes that match the selector
	nodeList := &corev1.NodeList{}
	selector := labels.SelectorFromSet(nodeENI.Spec.NodeSelector)
	err = r.List(ctx, nodeList, client.MatchingLabelsSelector{Selector: selector})
	if err != nil {
		log.Error(err, "Failed to list nodes")
		return ctrl.Result{}, err
	}

	// Initialize status if it's nil
	if nodeENI.Status.Attachments == nil {
		nodeENI.Status.Attachments = []networkingv1alpha1.ENIAttachment{}
	}

	// Track current attachments to detect stale ones
	currentAttachments := make(map[string]bool)

	// Process each matching node
	for _, node := range nodeList.Items {
		// Skip nodes that don't have the provider ID (not ready yet)
		if node.Spec.ProviderID == "" {
			log.Info("Node doesn't have provider ID yet, skipping", "node", node.Name)
			continue
		}

		// Extract EC2 instance ID from provider ID (format: aws:///us-east-1a/i-0123456789abcdef0)
		instanceID := getInstanceIDFromProviderID(node.Spec.ProviderID)
		if instanceID == "" {
			log.Error(nil, "Failed to extract instance ID from provider ID", "providerID", node.Spec.ProviderID)
			continue
		}

		currentAttachments[node.Name] = true

		// Check if we already have an attachment for this node
		attachmentExists := false
		for _, attachment := range nodeENI.Status.Attachments {
			if attachment.NodeID == node.Name {
				attachmentExists = true
				// TODO: Verify the attachment is still valid
				break
			}
		}

		if !attachmentExists {
			// Create and attach a new ENI
			log.Info("Creating and attaching ENI for node", "node", node.Name, "instanceID", instanceID)

			// Create the ENI
			eniID, err := r.createENI(ctx, nodeENI, node)
			if err != nil {
				log.Error(err, "Failed to create ENI", "node", node.Name)
				r.Recorder.Eventf(nodeENI, corev1.EventTypeWarning, "ENICreationFailed",
					"Failed to create ENI for node %s: %v", node.Name, err)
				continue
			}

			// Attach the ENI
			attachmentID, err := r.attachENI(ctx, eniID, instanceID, nodeENI.Spec.DeviceIndex)
			if err != nil {
				log.Error(err, "Failed to attach ENI", "node", node.Name, "eniID", eniID)
				r.Recorder.Eventf(nodeENI, corev1.EventTypeWarning, "ENIAttachmentFailed",
					"Failed to attach ENI %s to node %s: %v", eniID, node.Name, err)
				continue
			}

			// Add the attachment to the status
			nodeENI.Status.Attachments = append(nodeENI.Status.Attachments, networkingv1alpha1.ENIAttachment{
				NodeID:       node.Name,
				InstanceID:   instanceID,
				ENIID:        eniID,
				AttachmentID: attachmentID,
				Status:       "attached",
				LastUpdated:  metav1.Now(),
			})

			r.Recorder.Eventf(nodeENI, corev1.EventTypeNormal, "ENIAttached",
				"Successfully attached ENI %s to node %s", eniID, node.Name)
		}
	}

	// Remove stale attachments (nodes that no longer exist or don't match the selector)
	var updatedAttachments []networkingv1alpha1.ENIAttachment
	for _, attachment := range nodeENI.Status.Attachments {
		if currentAttachments[attachment.NodeID] {
			updatedAttachments = append(updatedAttachments, attachment)
		} else {
			// Detach and delete the ENI
			log.Info("Detaching and deleting ENI", "node", attachment.NodeID, "eniID", attachment.ENIID)

			if attachment.AttachmentID != "" {
				err := r.detachENI(ctx, attachment.AttachmentID)
				if err != nil {
					log.Error(err, "Failed to detach ENI", "attachmentID", attachment.AttachmentID)
					r.Recorder.Eventf(nodeENI, corev1.EventTypeWarning, "ENIDetachmentFailed",
						"Failed to detach ENI %s from node %s: %v", attachment.ENIID, attachment.NodeID, err)
					// Keep the attachment in the list if we failed to detach it
					updatedAttachments = append(updatedAttachments, attachment)
					continue
				}
			}

			// Wait longer for the detachment to complete
			log.Info("Waiting for ENI detachment to complete", "eniID", attachment.ENIID)
			time.Sleep(15 * time.Second)

			if attachment.ENIID != "" {
				// Try to describe the ENI to check its status
				describeInput := &ec2.DescribeNetworkInterfacesInput{
					NetworkInterfaceIds: []*string{aws.String(attachment.ENIID)},
				}

				describeResult, describeErr := r.EC2.DescribeNetworkInterfaces(describeInput)
				if describeErr != nil {
					log.Error(describeErr, "Failed to describe ENI", "eniID", attachment.ENIID)
					// Keep the attachment in the list if we failed to describe it
					updatedAttachments = append(updatedAttachments, attachment)
					continue
				}

				if len(describeResult.NetworkInterfaces) == 0 {
					log.Info("ENI no longer exists", "eniID", attachment.ENIID)
					continue
				}

				eni := describeResult.NetworkInterfaces[0]
				if eni.Attachment != nil && *eni.Status != "available" {
					log.Info("ENI is still attached, waiting longer", "eniID", attachment.ENIID, "status", *eni.Status)
					time.Sleep(15 * time.Second)
				}

				// Delete the ENI
				err := r.deleteENI(ctx, attachment.ENIID)
				if err != nil {
					log.Error(err, "Failed to delete ENI", "eniID", attachment.ENIID)
					r.Recorder.Eventf(nodeENI, corev1.EventTypeWarning, "ENIDeletionFailed",
						"Failed to delete ENI %s: %v", attachment.ENIID, err)
					// Keep the attachment in the list if we failed to delete it
					updatedAttachments = append(updatedAttachments, attachment)
					continue
				}
			}

			r.Recorder.Eventf(nodeENI, corev1.EventTypeNormal, "ENIDetached",
				"Successfully detached and deleted ENI %s from node %s", attachment.ENIID, attachment.NodeID)
		}
	}

	nodeENI.Status.Attachments = updatedAttachments

	// Update the NodeENI status
	err = r.Status().Update(ctx, nodeENI)
	if err != nil {
		log.Error(err, "Failed to update NodeENI status")
		return ctrl.Result{}, err
	}

	// Requeue to periodically check the status
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// Helper functions for AWS operations

// getInstanceIDFromProviderID extracts the EC2 instance ID from the provider ID
func getInstanceIDFromProviderID(providerID string) string {
	// Provider ID format: aws:///zone/i-0123456789abcdef0
	// We need to extract the i-0123456789abcdef0 part
	parts := strings.Split(providerID, "/")
	if len(parts) < 2 {
		return ""
	}
	// The instance ID should be the last part
	return parts[len(parts)-1]
}

// getSubnetIDByName looks up a subnet ID by its Name tag
func (r *NodeENIReconciler) getSubnetIDByName(ctx context.Context, subnetName string) (string, error) {
	input := &ec2.DescribeSubnetsInput{
		Filters: []*ec2.Filter{
			{
				Name:   aws.String("tag:Name"),
				Values: []*string{aws.String(subnetName)},
			},
		},
	}

	result, err := r.EC2.DescribeSubnets(input)
	if err != nil {
		return "", fmt.Errorf("failed to describe subnets: %v", err)
	}

	if len(result.Subnets) == 0 {
		return "", fmt.Errorf("no subnet found with name: %s", subnetName)
	}

	if len(result.Subnets) > 1 {
		r.Log.Info("Multiple subnets found with the same name, using the first one", "subnetName", subnetName)
	}

	return *result.Subnets[0].SubnetId, nil
}

// createENI creates a new ENI in AWS
func (r *NodeENIReconciler) createENI(ctx context.Context, nodeENI *networkingv1alpha1.NodeENI, node corev1.Node) (string, error) {
	description := nodeENI.Spec.Description
	if description == "" {
		description = fmt.Sprintf("ENI created by nodeeni-controller for node %s", node.Name)
	}

	// Determine the subnet ID to use
	subnetID := nodeENI.Spec.SubnetID
	if subnetID == "" && nodeENI.Spec.SubnetName != "" {
		// Look up subnet ID by name
		var err error
		subnetID, err = r.getSubnetIDByName(ctx, nodeENI.Spec.SubnetName)
		if err != nil {
			return "", fmt.Errorf("failed to get subnet ID from name %s: %v", nodeENI.Spec.SubnetName, err)
		}
		r.Log.Info("Resolved subnet name to ID", "subnetName", nodeENI.Spec.SubnetName, "subnetID", subnetID)
	}

	if subnetID == "" {
		return "", fmt.Errorf("neither subnetID nor subnetName provided")
	}

	input := &ec2.CreateNetworkInterfaceInput{
		Description: aws.String(description),
		SubnetId:    aws.String(subnetID),
		Groups:      aws.StringSlice(nodeENI.Spec.SecurityGroupIDs),
		TagSpecifications: []*ec2.TagSpecification{
			{
				ResourceType: aws.String("network-interface"),
				Tags: []*ec2.Tag{
					{
						Key:   aws.String("Name"),
						Value: aws.String(fmt.Sprintf("nodeeni-%s-%s", nodeENI.Name, node.Name)),
					},
					{
						Key:   aws.String("NodeENI"),
						Value: aws.String(nodeENI.Name),
					},
					{
						Key:   aws.String("Node"),
						Value: aws.String(node.Name),
					},
					{
						Key:   aws.String("kubernetes.io/cluster/managed-by"),
						Value: aws.String("nodeeni-controller"),
					},
					{
						Key:   aws.String("no-manage"),
						Value: aws.String("true"),
					},
				},
			},
		},
	}

	result, err := r.EC2.CreateNetworkInterface(input)
	if err != nil {
		return "", err
	}

	return *result.NetworkInterface.NetworkInterfaceId, nil
}

// attachENI attaches an ENI to an EC2 instance
func (r *NodeENIReconciler) attachENI(ctx context.Context, eniID, instanceID string, deviceIndex int) (string, error) {
	input := &ec2.AttachNetworkInterfaceInput{
		DeviceIndex:        aws.Int64(int64(deviceIndex)),
		InstanceId:         aws.String(instanceID),
		NetworkInterfaceId: aws.String(eniID),
	}

	result, err := r.EC2.AttachNetworkInterface(input)
	if err != nil {
		return "", err
	}

	// Set delete on termination attribute
	_, err = r.EC2.ModifyNetworkInterfaceAttribute(&ec2.ModifyNetworkInterfaceAttributeInput{
		NetworkInterfaceId: aws.String(eniID),
		Attachment: &ec2.NetworkInterfaceAttachmentChanges{
			AttachmentId:        result.AttachmentId,
			DeleteOnTermination: aws.Bool(true),
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to set delete on termination: %v", err)
	}

	return *result.AttachmentId, nil
}

// detachENI detaches an ENI from an EC2 instance
func (r *NodeENIReconciler) detachENI(ctx context.Context, attachmentID string) error {
	input := &ec2.DetachNetworkInterfaceInput{
		AttachmentId: aws.String(attachmentID),
		Force:        aws.Bool(true),
	}

	_, err := r.EC2.DetachNetworkInterface(input)
	return err
}

// deleteENI deletes an ENI
func (r *NodeENIReconciler) deleteENI(ctx context.Context, eniID string) error {
	input := &ec2.DeleteNetworkInterfaceInput{
		NetworkInterfaceId: aws.String(eniID),
	}

	_, err := r.EC2.DeleteNetworkInterface(input)
	return err
}
