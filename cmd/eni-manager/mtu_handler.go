package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
)

// MTUHandler handles MTU configuration for ENIs
type MTUHandler struct {
	config      *config.ENIManagerConfig
	k8sClient   *kubernetes.Clientset
	restClient  *rest.RESTClient
	nodeName    string
	initialized bool
}

// NewMTUHandler creates a new MTU handler
func NewMTUHandler(cfg *config.ENIManagerConfig) *MTUHandler {
	return &MTUHandler{
		config:      cfg,
		initialized: false,
	}
}

// Initialize initializes the MTU handler
func (h *MTUHandler) Initialize() error {
	// Get the node name from the environment
	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		// Try to get the node name from the hostname
		var err error
		nodeName, err = os.Hostname()
		if err != nil {
			return fmt.Errorf("failed to get hostname: %v", err)
		}
	}
	h.nodeName = nodeName

	// Create a Kubernetes client
	config, err := rest.InClusterConfig()
	if err != nil {
		// Try to use kubeconfig
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			kubeconfig = os.Getenv("HOME") + "/.kube/config"
		}
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return fmt.Errorf("failed to create Kubernetes client config: %v", err)
		}
	}

	// Create a clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create Kubernetes clientset: %v", err)
	}
	h.k8sClient = clientset

	// Create a REST client for the NodeENI CRD
	scheme := runtime.NewScheme()
	schemeBuilder := runtime.NewSchemeBuilder(func(scheme *runtime.Scheme) error {
		scheme.AddKnownTypes(
			schema.GroupVersion{Group: "networking.k8s.aws", Version: "v1alpha1"},
			&networkingv1alpha1.NodeENI{},
			&networkingv1alpha1.NodeENIList{},
		)
		metav1.AddToGroupVersion(scheme, schema.GroupVersion{Group: "networking.k8s.aws", Version: "v1alpha1"})
		return nil
	})
	if err := schemeBuilder.AddToScheme(scheme); err != nil {
		return fmt.Errorf("failed to add NodeENI to scheme: %v", err)
	}

	crdConfig := *config
	crdConfig.GroupVersion = &schema.GroupVersion{Group: "networking.k8s.aws", Version: "v1alpha1"}
	crdConfig.APIPath = "/apis"
	crdConfig.NegotiatedSerializer = serializer.NewCodecFactory(scheme)
	crdConfig.UserAgent = rest.DefaultKubernetesUserAgent()

	restClient, err := rest.RESTClientFor(&crdConfig)
	if err != nil {
		return fmt.Errorf("failed to create REST client for NodeENI CRD: %v", err)
	}
	h.restClient = restClient

	h.initialized = true
	return nil
}

// UpdateMTUConfig updates the MTU configuration based on NodeENI resources
func (h *MTUHandler) UpdateMTUConfig(ctx context.Context) error {
	if !h.initialized {
		if err := h.Initialize(); err != nil {
			return err
		}
	}

	// Get all NodeENI resources
	var nodeENIList networkingv1alpha1.NodeENIList
	err := h.restClient.Get().
		Resource("nodeenis").
		VersionedParams(&metav1.ListOptions{}, metav1.ParameterCodec).
		Do(ctx).
		Into(&nodeENIList)
	if err != nil {
		return fmt.Errorf("failed to list NodeENI resources: %v", err)
	}

	// Process each NodeENI resource
	for _, nodeENI := range nodeENIList.Items {
		// Check if this NodeENI applies to this node
		for _, attachment := range nodeENI.Status.Attachments {
			if attachment.NodeID == h.nodeName {
				// This NodeENI has an attachment for this node
				if attachment.MTU > 0 {
					// Get the interface name from the device index
					ifaceName := getInterfaceNameFromDeviceIndex(nodeENI.Spec.DeviceIndex)
					if ifaceName != "" {
						// Set the MTU for this interface
						h.config.InterfaceMTUs[ifaceName] = attachment.MTU
						log.Printf("Set MTU for interface %s to %d from NodeENI %s",
							ifaceName, attachment.MTU, nodeENI.Name)
					}
				}
			}
		}
	}

	return nil
}

// getInterfaceNameFromDeviceIndex gets the interface name from the device index
// This is a simple implementation that assumes eth0 is the primary interface
// and eth1, eth2, etc. are the secondary interfaces
func getInterfaceNameFromDeviceIndex(deviceIndex int) string {
	// AWS uses device index 0 for the primary interface
	// and 1, 2, etc. for secondary interfaces
	// We map these to eth0, eth1, eth2, etc.
	return fmt.Sprintf("eth%d", deviceIndex)
}

// StartMTUUpdater starts a goroutine that periodically updates the MTU configuration
func StartMTUUpdater(ctx context.Context, cfg *config.ENIManagerConfig) {
	handler := NewMTUHandler(cfg)

	// Initialize the handler
	if err := handler.Initialize(); err != nil {
		log.Printf("Failed to initialize MTU handler: %v", err)
		return
	}

	// Start a goroutine to periodically update the MTU configuration
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		// Do an initial update
		if err := handler.UpdateMTUConfig(ctx); err != nil {
			log.Printf("Failed to update MTU configuration: %v", err)
		}

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := handler.UpdateMTUConfig(ctx); err != nil {
					log.Printf("Failed to update MTU configuration: %v", err)
				}
			}
		}
	}()
}
