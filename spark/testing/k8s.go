package sparktesting

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	namespace = "spark"
)

// SparkOperatorController provides functionality to temporarily disable Spark operator services
// by scaling their deployments to 0
type SparkOperatorController struct {
	client    kubernetes.Interface
	operators map[int]*sparkOperatorState
	mu        sync.RWMutex
}

// sparkOperatorState tracks the state of a single operator
type sparkOperatorState struct {
	deploymentName string
	disabled       bool
}

// NewSparkOperatorController creates a new SparkOperatorController for managing multiple operators
func NewSparkOperatorController(t *testing.T) (*SparkOperatorController, error) {
	client := getKubernetesClient(t)

	numOperators := operatorCount(t)

	controller := &SparkOperatorController{
		client:    client,
		operators: make(map[int]*sparkOperatorState, numOperators),
		mu:        sync.RWMutex{},
	}

	// Initialize all operators
	// Deployment names are 0-indexed (regtest-spark-rpc-0, regtest-spark-rpc-1, ...)
	// but operatorNum is 1-indexed (1, 2, 3...) for user-facing API
	for i := range numOperators {
		controller.operators[i+1] = &sparkOperatorState{
			deploymentName: fmt.Sprintf("regtest-spark-rpc-%d", i),
			disabled:       false,
		}
	}

	// Set up cleanup to automatically re-enable all services when test finishes
	t.Cleanup(func() {
		controller.mu.Lock()
		defer controller.mu.Unlock()

		//nolint: usetesting // Use the background context to ensure this will run even if the test is cancelled, since
		// it deals with external resources. t.Context is canceled just before Cleanup-registered functions are called,
		// so it's no help here.
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		for operatorNum := range controller.operators {
			if controller.operators[operatorNum].disabled {
				if err := controller.enableOperator(ctx, operatorNum); err != nil {
					t.Errorf("Failed to re-enable operator %d during cleanup: %v", operatorNum, err)
				}
			}
		}
	})

	return controller, nil
}

func (s *SparkOperatorController) EnableOperator(t *testing.T, operatorNum int) error {
	return s.enableOperator(t.Context(), operatorNum)
}

func (s *SparkOperatorController) DisableOperator(t *testing.T, operatorNum int) error {
	return s.disableOperator(t.Context(), operatorNum)
}

// IsOperatorDisabled returns whether the specified operator is currently disabled
func (s *SparkOperatorController) IsOperatorDisabled(operatorNum int) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	operator, exists := s.operators[operatorNum]
	if !exists {
		return false
	}
	return operator.disabled
}

// GetDisabledOperators returns a slice of operator numbers that are currently disabled
func (s *SparkOperatorController) GetDisabledOperators() []int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var disabled []int
	for i := 1; i <= len(s.operators); i++ {
		if s.operators[i].disabled {
			disabled = append(disabled, i)
		}
	}
	return disabled
}

// GetEnabledOperators returns a slice of operator numbers that are currently enabled
func (s *SparkOperatorController) GetEnabledOperators() []int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var enabled []int
	for i := 1; i <= len(s.operators); i++ {
		if !s.operators[i].disabled {
			enabled = append(enabled, i)
		}
	}
	return enabled
}

// getKubernetesClient creates a Kubernetes client using kubeconfig
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

// enableOperator scales the deployment back to 1 replica and waits for pod readiness
func (s *SparkOperatorController) enableOperator(ctx context.Context, operatorNum int) error {
	operator, exists := s.operators[operatorNum]
	if !exists {
		return fmt.Errorf("operator %d does not exist (valid range: 1-%d)", operatorNum, len(s.operators))
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !operator.disabled {
		return fmt.Errorf("operator %d is not disabled", operatorNum)
	}

	ctx, cancelFunc := context.WithTimeout(ctx, 60*time.Second)
	defer cancelFunc()

	// Scale deployment to 1
	scale, err := s.client.AppsV1().Deployments(namespace).GetScale(ctx, operator.deploymentName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get deployment scale for %s: %w", operator.deploymentName, err)
	}

	scale.Spec.Replicas = 1
	_, err = s.client.AppsV1().Deployments(namespace).UpdateScale(ctx, operator.deploymentName, scale, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to scale up deployment %s: %w", operator.deploymentName, err)
	}

	// Wait for the pod to be ready
	if err := s.waitForPodReady(ctx, operator.deploymentName); err != nil {
		return fmt.Errorf("failed waiting for pod ready: %w", err)
	}

	operator.disabled = false
	return nil
}

// disableOperator scales the deployment to 0 replicas and waits for pod termination
func (s *SparkOperatorController) disableOperator(ctx context.Context, operatorNum int) error {
	operator, exists := s.operators[operatorNum]
	if !exists {
		return fmt.Errorf("operator %d does not exist (valid range: 1-%d)", operatorNum, len(s.operators))
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if operator.disabled {
		return fmt.Errorf("operator %d is already disabled", operatorNum)
	}

	ctx, cancelFunc := context.WithTimeout(ctx, 60*time.Second)
	defer cancelFunc()

	// Scale deployment to 0
	scale, err := s.client.AppsV1().Deployments(namespace).GetScale(ctx, operator.deploymentName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get deployment scale for %s: %w", operator.deploymentName, err)
	}

	scale.Spec.Replicas = 0
	_, err = s.client.AppsV1().Deployments(namespace).UpdateScale(ctx, operator.deploymentName, scale, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to scale down deployment %s: %w", operator.deploymentName, err)
	}

	// Wait for the pod to be terminated
	if err := s.waitForPodTerminated(ctx, operator.deploymentName); err != nil {
		return fmt.Errorf("failed waiting for pod termination: %w", err)
	}

	operator.disabled = true
	return nil
}

// waitForPodTerminated waits for all pods of the deployment to be terminated
func (s *SparkOperatorController) waitForPodTerminated(ctx context.Context, deploymentName string) error {
	labelSelector := fmt.Sprintf("app.kubernetes.io/name=rpc,app.kubernetes.io/instance=regtest,lightspark.com/operator=%s", deploymentName[len("regtest-spark-rpc-"):])

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		pods, err := s.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: labelSelector,
		})
		if err != nil {
			return fmt.Errorf("failed to list pods: %w", err)
		}

		if len(pods.Items) == 0 {
			return nil
		}

		time.Sleep(500 * time.Millisecond)
	}
}

// waitForPodReady waits for the deployment's pod to be ready
func (s *SparkOperatorController) waitForPodReady(ctx context.Context, deploymentName string) error {
	labelSelector := fmt.Sprintf("app.kubernetes.io/name=rpc,app.kubernetes.io/instance=regtest,lightspark.com/operator=%s", deploymentName[len("regtest-spark-rpc-"):])

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		pods, err := s.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: labelSelector,
		})
		if err != nil {
			return fmt.Errorf("failed to list pods: %w", err)
		}

		for _, pod := range pods.Items {
			for _, cond := range pod.Status.Conditions {
				if cond.Type == "Ready" && cond.Status == "True" {
					// Give a bit more time for connections to be established
					time.Sleep(2 * time.Second)
					return nil
				}
			}
		}

		time.Sleep(500 * time.Millisecond)
	}
}
