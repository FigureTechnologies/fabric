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

// KubernetesAPI instance for a peer to schedule chaincodes.
type KubernetesAPI struct {
	client *kubernetes.Clientset

	PeerID    string
	Namespace string
}

// NewKubernetesAPI creates an instance using the environmental Kubernetes configuration
func NewKubernetesAPI(peerID, networkID string) *KubernetesAPI {
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
func (api *KubernetesAPI) Start(ctxt context.Context, ccid ccintf.CCID,
	args []string, env []string, filesToUpload map[string][]byte, builder container.Builder) error {

	// Clean up any existing deployments (why do this?)
	api.stopAllInternal(ccid)

	deploy, err := api.createChaincodePodDeployment(ccid, args, env, filesToUpload)
	if err != nil {
		kubernetesLogger.Errorf("start - cannot create chaincode deploy %s", err)
		return err
	}

	kubernetesLogger.Infof("Started chaincode peer deployment %s", deploy.GetName())
	return nil
}

// Stop a running pod in kubernetes
func (api *KubernetesAPI) Stop(ctxt context.Context, ccid ccintf.CCID, timeout uint, dontkill bool, dontremove bool) error {
	// Remove any existing deployments by matching labels
	return api.stopAllInternal(ccid)
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
		ss := strings.Split(v, "=")
		kubernetesLogger.Debugf("create chaincode deployment: add env %s = %s", ss[0], ss[1])
		envvars = append(envvars, apiv1.EnvVar{Name: ss[0], Value: ss[1]})
	}

	weight := int32(50)
	labelExp, err := metav1.ParseToLabelSelector(fmt.Sprintf("Name == %s", api.PeerID))
	pod := &apiv1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: podName,
			Labels: map[string]string{
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
					Name:  "fabric-chaincode-mycc-container",
					Image: api.GetChainCodeImageName(ccid),
					Args:  args,
					Env:   envvars,
					VolumeMounts: []apiv1.VolumeMount{
						{
							Name:      "uploadedfiles-volume",
							MountPath: mountPoint,
						},
					},
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

// deleteChainCodeFilesConfigMap removes the configuration map files assocaited with the peer chaincode deployment
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
		kubernetesLogger.Info("Removing existing chaincode pod %s", pod.Name)
		err := api.client.Core().Pods(api.Namespace).Delete(pod.Name, &metav1.DeleteOptions{
			GracePeriodSeconds: &grace,
		})
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
	name := ccid.GetName()

	if api.PeerID != "" {
		name = fmt.Sprintf("cc-%s-%s", api.PeerID, name)
	} else {
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
