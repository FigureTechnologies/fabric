/*
Copyright 2018 Figure Technoclogies Inc. All Rights Reserved.

SPDX-License-Identifier: BSD-3-Clause-Attribution

*/

package kubernetescontroller

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"reflect"
	"regexp"
	"strings"
	"sync"

	"github.com/hyperledger/fabric/core/chaincode"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/spf13/viper"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/hyperledger/fabric/common/flogging"
	"github.com/hyperledger/fabric/core/container"
	"github.com/hyperledger/fabric/core/container/ccintf"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ContainerType is the string which the kuberentes container type
// is registered with the container.VMController
const ContainerType = "KUBERNETES"

var (
	kubernetesLogger = flogging.MustGetLogger("kubernetescontroller")
	clusterConfig    *rest.Config
	podRegExp        = regexp.MustCompile("[^a-zA-Z0-9-_.]")
)

type getClient func() (*kubernetes.Clientset, error)

// ExitHandles structure holds a conncurrent hashmap instance of references to channels
type ExitHandles struct {
	mutex      sync.Mutex
	chaincodes map[string]*chan string
}

// GetInstance returns the exit channel associated with the given name
func (handles *ExitHandles) GetInstance(name string) *chan string {
	handles.mutex.Lock()
	defer handles.mutex.Unlock()
	return handles.chaincodes[name]
}

// SetInstance sets a channel associated with the given chaincode name
func (handles *ExitHandles) SetInstance(name string, inst *chan string) {
	handles.mutex.Lock()
	defer handles.mutex.Unlock()
	handles.chaincodes[name] = inst
}

// RemoveInstance removes the exit channel associated with the given chaincode name
func (handles *ExitHandles) RemoveInstance(name string) {
	handles.mutex.Lock()
	defer handles.mutex.Unlock()
	delete(handles.chaincodes, name)
}

// NewExitHandles creates a new ExitHandles registry instance
func NewExitHandles() *ExitHandles {
	return &ExitHandles{
		chaincodes: make(map[string]*chan string),
	}
}

// KubernetesAPI instance for a peer to schedule chaincodes.
type KubernetesAPI struct {
	client *kubernetes.Clientset

	PeerID       string
	Namespace    string
	BuildMetrics *BuildMetrics

	chaincodes *ExitHandles
}

// NewKubernetesAPI creates an instance using the environmental Kubernetes configuration
func NewKubernetesAPI(peerID, networkID string, exitHandles *ExitHandles) *KubernetesAPI {
	// Empty or host networks map to default kubernetes namespace.
	namespace := viper.GetString("vm.kubernetes.namespace")
	if len(namespace) == 0 {
		kubernetesLogger.Warningf("NewKubernetesAPI - 'vm.kubernetes.namespace' not set. Using default namespace %s.", apiv1.NamespaceDefault)
		namespace = apiv1.NamespaceDefault
	}

	api := KubernetesAPI{
		PeerID:    peerID,
		Namespace: namespace,
	}

	client, err := getKubernetesClient()
	if err != nil {
		kubernetesLogger.Debugf("NewKubernetesAPI - cannot create kubernetes client %s", err)
		panic(err)
	}

	api.client = client
	api.chaincodes = exitHandles

	return &api
}

// InCluster returns true if the process is running in a pod inside a kubernetes cluster (and configuration can be accessed)
func InCluster() bool {
	enable := viper.GetBool("vm.kubernetes.enabled")
	if !enable {
		kubernetesLogger.Info("Kubernetes support is disabled.")
		return false
	}

	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if len(host) == 0 || len(port) == 0 {
		kubernetesLogger.Info("Kubernetes service environment variables not found.")
		return false
	}

	token, err := ioutil.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")

	if err != nil {
		kubernetesLogger.Warning("Error accessing kubernetes service account", err)
		return false
	}

	bearer := string(token)
	if len(bearer) < 1 {
		kubernetesLogger.Warning("Kubernetes services account token not accessible.", err)
		return false
	}

	return true
}

func getKubernetesClient() (*kubernetes.Clientset, error) {
	// creates the in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		panic(err.Error())
	}
	// creates the clientset
	return kubernetes.NewForConfig(config)
}

// Start a pod in kubernetes for the chaincode
func (api *KubernetesAPI) Start(ccid ccintf.CCID,
	args []string, env []string, filesToUpload map[string][]byte, builder container.Builder) error {

	kubernetesLogger.Infof("Starting chaincode %s...", api.GetPodName(ccid))

	// Clean up any existing deployments (why do this?)
	api.stopAllInternal(ccid)

	// Inject the peer and version information.
	env = append(env, chaincode.E2eeConfigs(api.PeerID+"."+api.Namespace, ccid.Name, ccid.Version)...)

	deploy, err := api.createChaincodePodDeployment(ccid, args, env, filesToUpload)
	if err != nil {
		kubernetesLogger.Errorf("start - cannot create chaincode deploy %s", err)
		return err
	}

	// Create a stop channel reference
	ccchan := make(chan string, 1)
	api.chaincodes.SetInstance(api.GetPodName(ccid), &ccchan)

	kubernetesLogger.Infof("Chaincode %s started successfully.", deploy.GetName())
	return nil
}

// Stop a running pod in kubernetes
func (api *KubernetesAPI) Stop(ccid ccintf.CCID, timeout uint, dontkill bool, dontremove bool) error {
	kubernetesLogger.Infof("Stop chaincode %s requested. [kill=%t, remove=%t]", ccid.Name, !dontkill, !dontremove)
	// Remove any existing deployments by matching labels
	if !dontremove && !dontremove {
		return api.stopAllInternal(ccid)
	}

	return nil
}

// Wait blocks until the container stops and returns the exit code of the container.
func (api *KubernetesAPI) Wait(ccid ccintf.CCID) (int, error) {
	podName := api.GetPodName(ccid)
	kubernetesLogger.Infof("Waiting for %s to exit...", podName)

	cc := api.chaincodes.GetInstance(podName)
	if cc == nil {
		kubernetesLogger.Errorf("Chaincode %s exit channel handle was not found.", podName)
		return 0, fmt.Errorf("%s not found", podName)
	}

	<-*cc // wait in the chaincode stop channel to return something (or close)

	kubernetesLogger.Infof("Chaincode %s exited.", podName)

	return 0, nil
}

// HealthCheck checks api call used by docker for ensuring endpoint is available...
func (api *KubernetesAPI) HealthCheck(ctx context.Context) error {
	// Decide what kind of check we want to do here... nothing for now.
	return nil
}

func (api *KubernetesAPI) createChaincodePodDeployment(ccid ccintf.CCID, args []string, env []string, filesToUpload map[string][]byte) (*apiv1.Pod, error) {
	podName := api.GetPodName(ccid)
	kubernetesLogger.Info("Starting chaincode", podName)

	mountPoint, configMap, err := api.createChainCodeFilesConfigMap(podName, filesToUpload)
	if err != nil {
		kubernetesLogger.Errorf("Could not create config map for peer chaincode pod. %s", err)
		return nil, err
	}

	envvars := []apiv1.EnvVar{}
	for _, v := range env {
		// Use splitN(.., .., 2) here to handle base64 encoded strings coming in thru env.
		ss := strings.SplitN(v, "=", 2)
		kubernetesLogger.Debugf("create chaincode deployment: add env %s = %s", ss[0], ss[1])
		envvars = append(envvars, apiv1.EnvVar{Name: ss[0], Value: ss[1]})
	}

	weight := int32(50)
	labelExp, err := metav1.ParseToLabelSelector(fmt.Sprintf("Name == %s", api.PeerID))

	// Read in resource limits and requests from config.
	resourceRequest, err := getResourceRequest()
	if err != nil {
		return nil, err
	}

	pod := &apiv1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: podName,
			Labels: map[string]string{
				"service":    "peer-chaincode",
				"peer-owner": api.PeerID,
				"ccname":     ccid.Name,
				"ccver":      ccid.Version,
				"cc":         podName,
			},
		},
		Spec: apiv1.PodSpec{
			RestartPolicy: "Never", // If we exit for any reason rely on the Peer to reschedule.
			Containers: []apiv1.Container{
				{
					Name:  "fabric-chaincode-" + ccid.Name,
					Image: api.GetChainCodeImageName(ccid),
					Args:  args,
					Env:   envvars,
					VolumeMounts: []apiv1.VolumeMount{
						{
							Name:      "uploadedfiles-volume",
							MountPath: mountPoint,
						},
					},
					Resources: resourceRequest,
				},
			},
			Affinity: &apiv1.Affinity{
				PodAffinity: &apiv1.PodAffinity{
					PreferredDuringSchedulingIgnoredDuringExecution: []apiv1.WeightedPodAffinityTerm{
						{
							Weight: weight,
							PodAffinityTerm: apiv1.PodAffinityTerm{
								LabelSelector: labelExp,
								TopologyKey:   "kubernetes.io/hostname",
							},
						},
					},
				},
			},
			Volumes: []apiv1.Volume{
				{
					Name: "uploadedfiles-volume",
					VolumeSource: apiv1.VolumeSource{
						ConfigMap: &apiv1.ConfigMapVolumeSource{
							LocalObjectReference: apiv1.LocalObjectReference{
								Name: configMap.Name,
							},
						},
					},
				},
			},
		},
	}
	// Not already deployed so create it.
	kubernetesLogger.Info("Creating chaincode peer pod deployment")
	return api.client.Core().Pods(api.Namespace).Create(pod)
}

func getResourceQuantity(key string) (*resource.Quantity, error) {
	q := viper.GetString(key)
	if q == "" {
		// Not specified in config.
		return nil, nil
	}

	v, err := resource.ParseQuantity(q)
	if err != nil {
		return nil, err
	}

	return &v, nil
}

func getResourceRequest() (apiv1.ResourceRequirements, error) {
	resourceRequest := apiv1.ResourceRequirements{
		Limits:   apiv1.ResourceList{},
		Requests: apiv1.ResourceList{},
	}

	keyPrefix := "vm.kubernetes.container.%s"
	key := func(k string) string {
		return fmt.Sprintf(keyPrefix, k)
	}

	setQuantityFromConfig := func(k apiv1.ResourceName) error {
		// Read in (possibly non-existent) value from config.
		qty, err := getResourceQuantity(key(k.String()))
		if err != nil {
			return err
		}

		// No quantity provided is not an error, just do nothing.
		if qty == nil {
			return nil
		}

		// If quantity is provided, add to resources request.
		resourceRequest.Requests[k] = *qty
		return nil
	}

	// vm.kubernetes.container.limits.cpu
	if err := setQuantityFromConfig(apiv1.ResourceLimitsCPU); err != nil {
		return apiv1.ResourceRequirements{}, err
	}

	// vm.kubernetes.container.limits.memory
	if err := setQuantityFromConfig(apiv1.ResourceLimitsMemory); err != nil {
		return apiv1.ResourceRequirements{}, err
	}

	// vm.kubernetes.container.requests.cpu
	if err := setQuantityFromConfig(apiv1.ResourceRequestsCPU); err != nil {
		return apiv1.ResourceRequirements{}, err
	}

	// vm.kubernetes.container.requests.memory
	if err := setQuantityFromConfig(apiv1.ResourceRequestsMemory); err != nil {
		return apiv1.ResourceRequirements{}, err
	}

	return resourceRequest, nil
}

// createChainCodeFilesConfigMap return the mount point to use with the create config map or an error if it could not be created.
func (api *KubernetesAPI) createChainCodeFilesConfigMap(podName string, filesToUpload map[string][]byte) (string, *apiv1.ConfigMap, error) {

	rootPath, binaryData := api.extractCommonRoot(filesToUpload)

	configmap := &apiv1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: api.Namespace,
			Labels: map[string]string{
				"peer-owner": api.PeerID,
				"peercc":     podName,
				"service":    "peer-chaincode",
			},
		},
		BinaryData: binaryData,
	}
	// Try to delete any existing configmaps with this same name...
	existing, _ := api.client.CoreV1().ConfigMaps(api.Namespace).List(metav1.ListOptions{
		LabelSelector: fmt.Sprintf("peercc=%s", podName),
		Limit:         1,
	})

	// If this configmap exists already then update it... otherwise do a normal create
	if len(existing.Items) > 0 {
		kubernetesLogger.Infof("Updating existing configmap '%s' for chaincode pod files.", configmap.Name)
		configmap, err := api.client.CoreV1().ConfigMaps(api.Namespace).Update(configmap)
		return rootPath, configmap, err
	}
	kubernetesLogger.Infof("Creating chaincode configmap '%s' for files", configmap.Name)
	configmap, err := api.client.CoreV1().ConfigMaps(api.Namespace).Create(configmap)
	return rootPath, configmap, err
}

// deleteChainCodeFilesConfigMap removes the configuration map files associate with the peer chaincode deployment
func (api *KubernetesAPI) deleteChainCodeFilesConfigMap(podName string) error {
	opt := metav1.DeleteOptions{}
	kubernetesLogger.Infof("Removing config map '%s' for peer chaincode deployment", podName)
	return api.client.CoreV1().ConfigMaps(api.Namespace).Delete(podName, &opt)
}

// extractCommonRoot looks at the list of files and returns the longest matching root path and an updated set of files with it removed.
func (api *KubernetesAPI) extractCommonRoot(filesToUpload map[string][]byte) (string, map[string][]byte) {
	// Check if we need to do anything
	if len(filesToUpload) < 1 {
		return "", nil
	}

	rootPath := reflect.ValueOf(filesToUpload).MapKeys()[0].String() // Start with any key in the set
	foundRoot := strings.LastIndex(rootPath, "/") < 0                // We are done if there isn't a path to match

	if foundRoot { // If there wasn't a root to find then use empty string
		rootPath = ""
	}
	// While there isn't a common root matched in all files
	for !foundRoot && strings.LastIndex(rootPath, "/") > 0 {
		rootPath = rootPath[0 : strings.LastIndex(rootPath, "/")+1]
		all := true
		for k := range filesToUpload {
			// Check to see if the key starts with the rootpath ...
			kubernetesLogger.Debugf("Checking %s for %s", k, rootPath)
			all = strings.HasPrefix(k, rootPath) && all
		}
		foundRoot = all
	}

	// Create a new map using the updated keys with the root path
	binaryData := make(map[string][]byte, len(filesToUpload))
	for k, v := range filesToUpload {
		binaryData[strings.Replace(k, rootPath, "", 1)] = v
	}
	kubernetesLogger.Debugf("Extracted root path '%s' for filemap.", rootPath)
	return rootPath, binaryData
}

// stopAllInternal stops any running pods associated with this peer and the given chaincode.
func (api *KubernetesAPI) stopAllInternal(ccid ccintf.CCID) error {
	grace := int64(0)
	ccPods, err := api.FindPeerCCPods(ccid)
	if err != nil {
		kubernetesLogger.Errorf("stop all - cannot search for existing cc pods %s", err)
		return err
	}
	for _, pod := range ccPods.Items {
		kubernetesLogger.Infof("Removing existing chaincode pod %s", pod.Name)
		err := api.client.Core().Pods(api.Namespace).Delete(pod.Name, &metav1.DeleteOptions{
			GracePeriodSeconds: &grace,
		})
		// look for wait handle and close.
		cc := api.chaincodes.GetInstance(pod.Name)
		if cc != nil {
			close(*cc)
			api.chaincodes.RemoveInstance(pod.Name)
		}

		if err != nil {
			return err
		}
	}
	return api.deleteChainCodeFilesConfigMap(api.GetPodName(ccid))
}

// FindPeerCCPods looks for pods associated with this peer assigned to the given chaincode
func (api *KubernetesAPI) FindPeerCCPods(ccid ccintf.CCID) (*apiv1.PodList, error) {

	labelExp := fmt.Sprintf("peer-owner=%s, ccname=%s, ccver=%s", api.PeerID, ccid.Name, ccid.Version)

	listOptions := metav1.ListOptions{
		LabelSelector: labelExp,
	}

	return api.client.Core().Pods(api.Namespace).List(listOptions)
}

// GetPodName composes a name for a chaincode pod based on available metadata
func (api *KubernetesAPI) GetPodName(ccid ccintf.CCID) string {
	// assetledger-develop-61
	name := ccid.GetName()

	if api.PeerID != "" {
		// cc-peer-0-assetledger-develop-61
		name = fmt.Sprintf("cc-%s-%s", api.PeerID, name)
	} else {
		// cc-assetledger-develop-61
		name = fmt.Sprintf("cc-%s", name)
	}
	// replace any invalid characters with "-"
	return podRegExp.ReplaceAllString(name, "-")
}

// GetChainCodeImageName formats the chaincode image container name based on configuration values in core.yaml
func (api *KubernetesAPI) GetChainCodeImageName(ccid ccintf.CCID) string {
	ns := viper.GetString("chaincode.registry.namespace")
	prefix := viper.GetString("chaincode.registry.prefix")
	return fmt.Sprintf("%s/%s-%s:%s", ns, prefix, ccid.Name, ccid.Version)
}
