package sw

import (
	"testing"

	"github.com/hashicorp/vault/api"
	"github.com/hashicorp/vault/vault"
)

var vc *api.Client
var cluster *vault.TestCluster
var vConfig *VaultOptions

// SetupTestEnvironment creates a vault test backend
func SetupTestEnvironment(t *testing.T) (VaultOptions, *api.Client, error) {

	cluster = vault.NewTestCluster(t, nil, nil)
	cluster.Start()

	// make it easy to get access to the active
	cores := cluster.Cores
	vault.TestWaitActive(t, cores[0].Core)

	vConfig = &VaultOptions{
		Host:    cores[0].CoreConfig.ClusterAddr,
		Path:    "test/",
		Token:   cluster.RootToken,
		Version: 1,
	}

	client := cores[0].Client
	client.SetToken(cluster.RootToken)
	client.Auth()

	mountConfig := api.MountInput{
		Type: "kv",
		Config: api.MountConfigInput{
			DefaultLeaseTTL: "16h",
			MaxLeaseTTL:     "60h",
			Options: map[string]string{
				"version": string(vConfig.Version),
			},
		},
	}
	vc = client
	err := client.Sys().Mount(vConfig.Path, &mountConfig)

	return *vConfig, client, err
}

// TeardownTestEnvironment removes the test environment from vault.
func TeardownTestEnvironment(t *testing.T) error {
	logger.Info("Tearing down test environment")
	defer cluster.Cleanup()
	return vc.Sys().Unmount(vConfig.Path)
}
