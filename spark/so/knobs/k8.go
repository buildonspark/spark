package knobs

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/goccy/go-yaml"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

type knobsK8ValuesProvider struct {
	context context.Context
	logger  *slog.Logger
	lock    *sync.RWMutex
	values  map[string]float64
}

func NewKnobsK8ValuesProvider(ctx context.Context) (*knobsK8ValuesProvider, error) {
	provider := knobsK8ValuesProvider{
		context: ctx,
		logger:  slog.Default().With("component", "knobs"),
		lock:    &sync.RWMutex{},
		values:  make(map[string]float64),
	}

	if err := provider.fetchAndUpdate(); err != nil {
		return nil, fmt.Errorf("failed to fetch and update knobs: %w", err)
	}

	return &provider, nil
}

func (k *knobsK8ValuesProvider) GetValue(key string, defaultValue float64) float64 {
	k.lock.RLock()
	defer k.lock.RUnlock()

	if value, exists := k.values[key]; exists {
		return value
	}
	return defaultValue
}

// fetchAndUpdate continuously fetches and updates knob values from a Kubernetes ConfigMap.
// This function sets up a Kubernetes informer to watch for ConfigMap changes in real-time.
//
// Parameters:
//   - ctx: Context for cancellation and timeout control
//
// Returns:
//   - error: Returns an error if Kubernetes setup fails, nil if context is cancelled
//
// Behavior:
// 1. Attempts to get Kubernetes configuration (uses in-cluster config, no kubeconfig fallback)
// 2. Creates a Kubernetes clientset for API communication
// 3. Sets up a ConfigMap informer with custom ListerWatcher to avoid LIST permission requirement
// 4. Configures event handlers for ConfigMap add/update events
// 5. Starts the informer goroutine and waits for initial cache sync
//
// ConfigMap Processing:
//   - Watches ConfigMaps in the "knobs" namespace with name "knobs"
//   - Supports both simple values (key: "100.0") and target-specific values (key: "ENV: 50.0")
//   - Automatically parses YAML format for complex configurations
//   - Updates internal knob values in real-time when ConfigMap changes
//
// Permissions Required:
//   - WATCH permission on ConfigMaps in "knobs" namespace (LIST permission not required)
func (k *knobsK8ValuesProvider) fetchAndUpdate() error {
	// Get Kubernetes config
	config, err := rest.InClusterConfig()
	if err != nil {
		// Fall back to kubeconfig
		kubeconfig := clientcmd.NewDefaultClientConfigLoadingRules().GetDefaultFilename()
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return fmt.Errorf("failed to get kubernetes config: %w", err)
		}
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	// Create custom ListerWatcher that only uses Watch (no List permission required)
	watchOnlyLW := &cache.ListWatch{
		ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
			options.FieldSelector = "metadata.name=knobs"
			return clientset.CoreV1().ConfigMaps("knobs").List(context.Background(), options)
		},
		WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
			options.FieldSelector = "metadata.name=knobs"
			return clientset.CoreV1().ConfigMaps("knobs").Watch(context.Background(), options)
		},
	}

	informer := cache.NewSharedIndexInformer(
		watchOnlyLW,
		&corev1.ConfigMap{},
		0,
		cache.Indexers{},
	)

	_, err = informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			k.handleConfigMap(obj.(*corev1.ConfigMap))
		},
		UpdateFunc: func(_, newObj any) {
			k.handleConfigMap(newObj.(*corev1.ConfigMap))
		},
	})
	if err != nil {
		return fmt.Errorf("failed to add event handler: %w", err)
	}

	go informer.RunWithContext(k.context)

	// Wait for the informer to sync before returning,
	// to ensure that all feature flags are loaded before the first request.
	syncCtx, cancel := context.WithTimeout(k.context, 10*time.Second)
	defer cancel()

	if !cache.WaitForCacheSync(syncCtx.Done(), informer.HasSynced) {
		return fmt.Errorf("failed to sync informer")
	}

	return nil
}

func (k *knobsK8ValuesProvider) log(level slog.Level, msg string, args ...any) {
	if k.logger != nil {
		k.logger.Log(k.context, level, msg, args...)
	}
}

func (k *knobsK8ValuesProvider) handleConfigMap(configMap *corev1.ConfigMap) {
	k.log(slog.LevelDebug, "Processing ConfigMap", "configMap", configMap.Data)

	k.lock.Lock()
	defer k.lock.Unlock()

	clear(k.values)

	// If no data, nothing to do.
	if configMap == nil || configMap.Data == nil {
		k.log(slog.LevelInfo, "No knobs found in ConfigMap")
		return
	}

	for name, value := range configMap.Data {
		var parsedFloat float64
		if err := yaml.Unmarshal([]byte(value), &parsedFloat); err == nil {
			k.values[name] = parsedFloat
			continue
		}

		var parsedMap map[string]float64
		if err := yaml.Unmarshal([]byte(value), &parsedMap); err == nil {
			for target, targetValue := range parsedMap {
				key := fmt.Sprintf("%s@%s", name, target)
				k.values[key] = targetValue
			}
			continue
		}

		k.log(slog.LevelWarn, "Unknown knob value type", "name", name, "value", value)
	}
	k.log(slog.LevelInfo, "Updated knobs", "knobs", k.values)
}
