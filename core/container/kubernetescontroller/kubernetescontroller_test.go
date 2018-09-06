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

	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"

	apiv1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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
