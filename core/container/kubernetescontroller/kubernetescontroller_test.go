/*
Copyright 2018 Figure Technoclogies Inc. All Rights Reserved.

SPDX-License-Identifier: BSD-3-Clause-Attribution

-------------------------------------------------------------------------------
Copyright 2018 The Kubernetes Authors.

SPDX-License-Identifier: Apache 2.0
*/

package kubernetescontroller

import (
	"context"
	"testing"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
)

func TestFabric(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Fabric Suite")
}

var _ = Describe("Extract Root", func() {

	It("Extracts a root path correctly", func() {

		api := KubernetesAPI{
			client:    nil, //kubernetes.Clientset(client),
			PeerID:    "peer",
			Namespace: "namespace",
		}
		testFiles := make(map[string][]byte, 3)
		testFiles["/root/sub/one"] = []byte("onedata")
		testFiles["/root/sub/two"] = []byte("twodata")
		testFiles["/root/sub/three"] = []byte("threedata")
		rPath, responseFiles := api.extractCommonRoot(testFiles)

		Expect(rPath).To(Equal("/root/sub/"))
		Expect(responseFiles).To(HaveLen(3))
		Expect(responseFiles).To(HaveKey("one"))
		// Expect(vmProvider.NewVMCallCount()).To(Equal(1))
		// Expect(err).NotTo(HaveOccurred())
	})
})

// TestFakeClient demonstrates how to use a fake client with SharedInformerFactory in tests.
func TestFakeClient(t *testing.T) {
	// Use a timeout to keep the test from hanging.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

	// Create the fake client.
	client := fake.NewSimpleClientset()

	// We will create an informer that writes added pods to a channel.
	pods := make(chan *apiv1.Pod, 1)
	informers := informers.NewSharedInformerFactory(client, 0)
	podInformer := informers.Core().V1().Pods().Informer()
	podInformer.AddEventHandler(&cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			pod := obj.(*apiv1.Pod)
			t.Logf("pod added: %s/%s", pod.Namespace, pod.Name)
			pods <- pod
			cancel()
		},
	})

	// Make sure informers are running.
	informers.Start(ctx.Done())

	// This is not required in tests, but it serves as a proof-of-concept by
	// ensuring that the informer goroutine have warmed up and called List before
	// we send any events to it.
	for !podInformer.HasSynced() {
		time.Sleep(10 * time.Millisecond)
	}

	// Inject an event into the fake client.
	p := &apiv1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "my-pod"}}
	_, err := client.Core().Pods("test-ns").Create(p)
	if err != nil {
		t.Errorf("error injecting pod add: %v", err)
	}

	// Wait and check result.
	<-ctx.Done()
	select {
	case pod := <-pods:
		t.Logf("Got pod from channel: %s/%s", pod.Namespace, pod.Name)
	default:
		t.Error("Informer did not get the added pod")
	}
}
