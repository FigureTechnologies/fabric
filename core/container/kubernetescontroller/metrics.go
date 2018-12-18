/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

/*
No idea what to do with this for the kubernetes controller ... but copying it anyways so we can
replace the docker controller seamlessly.
*/
package kubernetescontroller

import "github.com/hyperledger/fabric/common/metrics"

var (
	chaincodeImageBuildDuration = metrics.HistogramOpts{
		Namespace:    "kubernetescontroller",
		Name:         "chaincode_container_build_duration",
		Help:         "The time to build a chaincode image in seconds.",
		LabelNames:   []string{"chaincode", "success"},
		StatsdFormat: "%{#fqname}.%{chaincode}.%{success}",
	}
)

type BuildMetrics struct {
	ChaincodeImageBuildDuration metrics.Histogram
}

func NewBuildMetrics(p metrics.Provider) *BuildMetrics {
	return &BuildMetrics{
		ChaincodeImageBuildDuration: p.NewHistogram(chaincodeImageBuildDuration),
	}
}
