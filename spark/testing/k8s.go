package sparktesting

import (
	"context"
	"fmt"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	numOperators = 3 // Adjust this to match your setup
	namespace    = "spark"
)

// SparkOperatorController provides functionality to temporarily disable Spark operator services
// by adding a non-matching label to their selectors
type SparkOperatorController struct {
	client    kubernetes.Interface
	operators map[int]*sparkOperatorState
}

// sparkOperatorState tracks the state of a single operator service
type sparkOperatorState struct {
	deploymentName   string
	originalReplicas *int32
	disabled         bool
}

// NewSparkOperatorController creates a new SparkOperatorController for managing multiple operators
func NewSparkOperatorController(t *testing.T) *SparkOperatorController {
	client := getKubernetesClient(t)

	controller := &SparkOperatorController{
		client:    client,
		operators: make(map[int]*sparkOperatorState),
	}

	// Initialize all operators
	for i := 1; i <= numOperators; i++ {
		controller.operators[i] = &sparkOperatorState{
			deploymentName: fmt.Sprintf("regtest-spark-rpc-%d", i),
			disabled:       false,
		}
	}

	// Set up cleanup to automatically re-enable all services when test finishes
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // nolint: usetesting
		defer cancel()

		for operatorNum := range controller.operators {
			if controller.operators[operatorNum].disabled {
				if err := controller.enableOperator(ctx, operatorNum); err != nil {
					t.Errorf("Failed to re-enable operator %d during cleanup: %v", operatorNum, err)
				}
			}
		}
	})

	return controller
}

func (s *SparkOperatorController) EnableOperator(t *testing.T, operatorNum int) error {
	return s.enableOperator(t.Context(), operatorNum)
}

func (s *SparkOperatorController) DisableOperator(t *testing.T, operatorNum int) error {
	return s.disableOperator(t.Context(), operatorNum)
}

// EnableAllOperators enables all operators at once
func (s *SparkOperatorController) EnableAllOperators(t *testing.T) error {
	for i := 1; i <= numOperators; i++ {
		if s.operators[i].disabled {
			if err := s.enableOperator(t.Context(), i); err != nil {
				return fmt.Errorf("failed to enable operator %d: %w", i, err)
			}
		}
	}
	return nil
}

// DisableAllOperators disables all operators at once
func (s *SparkOperatorController) DisableAllOperators(t *testing.T) error {
	for i := 1; i <= numOperators; i++ {
		if err := s.disableOperator(t.Context(), i); err != nil {
			return fmt.Errorf("failed to disable operator %d: %w", i, err)
		}
	}
	return nil
}

// IsOperatorDisabled returns whether the specified operator is currently disabled
func (s *SparkOperatorController) IsOperatorDisabled(operatorNum int) bool {
	operator, exists := s.operators[operatorNum]
	if !exists {
		return false
	}
	return operator.disabled
}

// GetDisabledOperators returns a slice of operator numbers that are currently disabled
func (s *SparkOperatorController) GetDisabledOperators() []int {
	var disabled []int
	for i := 1; i <= numOperators; i++ {
		if s.operators[i].disabled {
			disabled = append(disabled, i)
		}
	}
	return disabled
}

// GetEnabledOperators returns a slice of operator numbers that are currently enabled
func (s *SparkOperatorController) GetEnabledOperators() []int {
	var enabled []int
	for i := 1; i <= numOperators; i++ {
		if !s.operators[i].disabled {
			enabled = append(enabled, i)
		}
	}
	return enabled
}

// getKubernetesClient creates a Kubernetes client, preferring in-cluster config
// but falling back to kubeconfig
func getKubernetesClient(t *testing.T) kubernetes.Interface {
	var config *rest.Config
	var err error

	// We should never be doing this in-cluster, so only check kubeconfig.
	kubeconfigPath := clientcmd.NewDefaultClientConfigLoadingRules().GetDefaultFilename()
	config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		t.Fatalf("Failed to create Kubernetes config: %v", err)
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatalf("Failed to create Kubernetes client: %v", err)
	}

	return client
}

// EnableOperator scales the specified operator's deployment back to its original replica count
func (s *SparkOperatorController) enableOperator(ctx context.Context, operatorNum int) error {
	operator, exists := s.operators[operatorNum]
	if !exists {
		return fmt.Errorf("operator %d does not exist (valid range: 1-%d)", operatorNum, numOperators)
	}

	if !operator.disabled {
		return fmt.Errorf("operator %d is not disabled", operatorNum)
	}

	// Get the current deployment
	deployment, err := s.client.AppsV1().Deployments(namespace).Get(ctx, operator.deploymentName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get deployment %s: %w", operator.deploymentName, err)
	}

	// Restore the original replica count
	deployment.Spec.Replicas = operator.originalReplicas

	// Update the deployment
	_, err = s.client.AppsV1().Deployments(namespace).Update(ctx, deployment, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to scale up deployment %s: %w", operator.deploymentName, err)
	}

	err = s.waitForDeploymentReplicas(ctx, operatorNum, int(*operator.originalReplicas))
	if err != nil {
		return fmt.Errorf("error waiting for deployment %s to scale up: %w", operator.deploymentName, err)
	}

	operator.disabled = false
	operator.originalReplicas = nil
	return nil
}

func (s *SparkOperatorController) disableOperator(ctx context.Context, operatorNum int) error {
	operator, exists := s.operators[operatorNum]
	if !exists {
		return fmt.Errorf("operator %d does not exist (valid range: 1-%d)", operatorNum, numOperators)
	}

	if operator.disabled {
		return fmt.Errorf("operator %d is already disabled", operatorNum)
	}

	// Get the current deployment
	deployment, err := s.client.AppsV1().Deployments(namespace).Get(ctx, operator.deploymentName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get deployment %s: %w", operator.deploymentName, err)
	}

	// Store the original replica count for restoration
	if deployment.Spec.Replicas != nil {
		originalReplicas := *deployment.Spec.Replicas
		operator.originalReplicas = &originalReplicas
	} else {
		// Default to 1 if replicas is nil
		defaultReplicas := int32(1)
		operator.originalReplicas = &defaultReplicas
	}

	// Scale down to 0 replicas
	zeroReplicas := int32(0)
	deployment.Spec.Replicas = &zeroReplicas

	// Update the deployment
	_, err = s.client.AppsV1().Deployments(namespace).Update(ctx, deployment, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to scale down deployment %s: %w", operator.deploymentName, err)
	}

	err = s.waitForDeploymentReplicas(ctx, operatorNum, 0)
	if err != nil {
		return fmt.Errorf("error waiting for deployment %s to scale down: %w", operator.deploymentName, err)
	}

	operator.disabled = true
	return nil
}
func (s *SparkOperatorController) waitForDeploymentReplicas(ctx context.Context, operatorNum int, expectedReplicas int) error {
	operator := s.operators[operatorNum]
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	ticker := time.NewTicker(500 * time.Millisecond) // Check every 500ms for pod changes
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for deployment %s to have %d ready replicas", operator.deploymentName, expectedReplicas)
		case <-ticker.C:
			deployment, err := s.client.AppsV1().Deployments(namespace).Get(ctx, operator.deploymentName, metav1.GetOptions{})
			if err != nil {
				continue
			}

			readyReplicas := int(deployment.Status.ReadyReplicas)

			if readyReplicas == expectedReplicas {
				return nil
			}
		}
	}
}
