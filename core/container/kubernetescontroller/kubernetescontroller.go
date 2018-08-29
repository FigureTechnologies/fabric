/*
Copyright Figure Technoclogies Inc. All Rights Reserved.
---
The Kubernetes Controller is derived from the dockercontroller.go file which is subject
to the following copyright statement

Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package kubernetescontroller

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"regexp"

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
	clientSet getClient

	PeerID    string
	NetworkID string
}

// NewKubernetesAPI creates an instance using the environmental Kubernetes configuration
func NewKubernetesAPI(peerID, networkID string) *KubernetesAPI {
	// Empty or host networks map to default kubernetes namespace.
	if networkID == "" || networkID == "host" {
		networkID = apiv1.NamespaceDefault
	}

	api := KubernetesAPI{
		PeerID:    peerID,
		NetworkID: networkID,
	}

	api.clientSet = getKubernetesClient
	return &api
}

// InCluster returns true if the process is running in a pod inside a kubernetes cluster (and configuration can be accessed)
func InCluster() bool {
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
	//p, err := clientset.Core().Pods("fabric").Get("asdf", metav1.GetOptions{})
}

// Start a pod in kubernetes for the chaincode
func (api *KubernetesAPI) Start(ctxt context.Context, ccid ccintf.CCID,
	args []string, env []string, filesToUpload map[string][]byte, builder container.Builder) error {

	podName := api.GetPodName(ccid)

	client, err := api.clientSet()
	if err != nil {
		kubernetesLogger.Debugf("start - cannot create kubernetes client %s", err)
		return err
	}

	// Clean up any existing deployments
	api.stopAllInternal(ccid)

	kubernetesLogger.Info("Start chaincode %s", podName)
	api.createChaincodePod(ccid)

	pod := &apiv1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: api.NetworkID,
			Labels: map[string]string{
				"peer-owner": api.PeerID,
				"ccname":     ccid.Name,
				"ccver":      ccid.Version,
			},
		},
		Spec: apiv1.PodSpec{
			Containers: []apiv1.Container{
				apiv1.Container{
					Name: "chaincode",
				},
			},
		},
	}
	podInstance, err := client.CoreV1().Pods(api.NetworkID).Create(pod)
	if err != nil {
		kubernetesLogger.Errorf("start - cannot create chaincode pod %s", err)
		return err
	}

	kubernetesLogger.Infof("Started chaincode peer pod %s", podInstance.GetName())
	// example image name -
	// dev-peer-0-loanledger-1535563072-1a8492a38dda35606bbdc1bff7ec06b51eb270ac1fdf36453091dc8d226f726e

	return err
}

// Stop a running pod in kubernetes
func (api *KubernetesAPI) Stop(ctxt context.Context, ccid ccintf.CCID, timeout uint, dontkill bool, dontremove bool) error {
	return nil
}

func (api *KubernetesAPI) createChaincodePod(ccid ccintf.CCID) {

}

// stopAllInternal stops any running pods associated with this peer and the given chaincode.
func (api *KubernetesAPI) stopAllInternal(ccid ccintf.CCID) error {
	client, err := api.clientSet()
	if err != nil {
		kubernetesLogger.Debugf("stop chaincode pods - cannot create kubernetes client %s", err)
		return err
	}
	grace := int64(0)
	ccpods, err := api.FindPeerCCPod(ccid)
	for _, pod := range ccpods.Items {
		kubernetesLogger.Info("Removing existing chaincode pod %s", pod.Name)
		err := client.CoreV1().Pods(api.NetworkID).Delete(pod.Name, &metav1.DeleteOptions{
			GracePeriodSeconds: &grace,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// FindPeerCCPod looks for pods associated with this peer assigned to the given chaincode
func (api *KubernetesAPI) FindPeerCCPod(ccid ccintf.CCID) (*apiv1.PodList, error) {

	client, err := api.clientSet()
	if err != nil {
		return nil, err
	}

	labelExp := fmt.Sprintf("peer=%s, ccname=%s, ccver=%s", api.PeerID, ccid.Name, ccid.Version)

	listOptions := metav1.ListOptions{
		LabelSelector: labelExp,
	}

	return client.Core().Pods(api.NetworkID).List(listOptions)
}

// GetPodName composes a name for a chaincode pod based on available
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
