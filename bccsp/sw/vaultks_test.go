package sw

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"

	"github.com/hyperledger/fabric/bccsp"
	"github.com/stretchr/testify/assert"
)

func TestVault(t *testing.T) {
	config, client, err := SetupTestEnvironment(t)
	defer TeardownTestEnvironment(t)

	assert.NoError(t, err, "Create test environment failed.")

	ks, err := NewVaultKeyStore(client, &config)

	if err != nil {
		fmt.Printf("Failed initiliazing KeyStore [%s]", err)
		os.Exit(-1)
	}

	t.Run("Store Twice", func(ts *testing.T) { storeKeyTwice(ts, ks) })
	t.Run("Invalid Keys", func(ts *testing.T) { invalidVaultKeys(ts, ks) })

}

func TestWithExistingVault(t *testing.T) {
	vaultConfig := VaultOptions{
		Host:    "localhost",
		Port:    8200,
		TLS:     false,
		Path:    "testpath/",
		Token:   "myroot",
		Version: 2,
	}

	client, err := InitializeClient(vaultConfig)
	assert.NoError(t, err, "Initializing client failed.")

	ks, err := NewVaultKeyStore(nil, &vaultConfig)
	defer client.Sys().Unmount(vaultConfig.Path)

	if err != nil {
		fmt.Printf("Failed initiliazing KeyStore [%s]", err)
		os.Exit(-1)
	}

	t.Run("Store Once", func(ts *testing.T) { storeRetrieve(ts, ks) })
	t.Run("Store Twice", func(ts *testing.T) { storeKeyTwice(ts, ks) })
	t.Run("Not Found", func(ts *testing.T) { testNotFound(ts, ks) })
	t.Run("Invalid Keys", func(ts *testing.T) { invalidVaultKeys(ts, ks) })
}

func testNotFound(t *testing.T, ks bccsp.KeyStore) {
	ski := []byte("foo")
	_, err := ks.GetKey(ski)
	assert.EqualError(t, err, fmt.Sprintf("no key found for ski %x", ski))
}

func storeRetrieve(t *testing.T, ks bccsp.KeyStore) {
	//t.Parallel()

	// generate a key for the keystore to find
	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	assert.NoError(t, err)
	cspKey := &ecdsaPrivateKey{privKey}

	// store key
	err = ks.StoreKey(cspKey)
	assert.NoError(t, err)

	privKey2, err := ks.GetKey(cspKey.SKI())
	if err != nil {
		t.Fatalf("failed to retrieve stored key for [%s]", hex.EncodeToString(cspKey.SKI()))
	}
	assert.EqualValues(t, privKey2.SKI(), cspKey.SKI())
}

func storeKeyTwice(t *testing.T, ks bccsp.KeyStore) {
	//t.Parallel()

	// generate a key for the keystore to find
	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	assert.NoError(t, err)
	cspKey := &ecdsaPrivateKey{privKey}

	// store key
	err = ks.StoreKey(cspKey)
	assert.NoError(t, err)

	// store key a second time
	err = ks.StoreKey(cspKey)
	assert.EqualError(t, err, fmt.Sprintf("ski %s already exists in the keystore", hex.EncodeToString(cspKey.SKI())))

	privKey2, err := ks.GetKey(cspKey.SKI())
	if err != nil {
		t.Fatalf("failed to retrieve stored key for [%s]", hex.EncodeToString(cspKey.SKI()))
	}
	assert.EqualValues(t, privKey2.SKI(), cspKey.SKI())
}

func invalidVaultKeys(t *testing.T, ks bccsp.KeyStore) {
	//t.Parallel()

	var ski = []byte{1, 2}
	_, err := ks.GetKey(ski)
	assert.Error(t, err, "expected invalid SKI error")

	err = ks.StoreKey(nil)
	if err == nil {
		t.Fatal("error required for typed but nil key")
	}

	err = ks.StoreKey(&ecdsaPrivateKey{nil})
	if err == nil {
		t.Fatal("error required for typed but nil key")
	}

	err = ks.StoreKey(&ecdsaPublicKey{nil})
	if err == nil {
		t.Fatal("error required for typed but nil key")
	}

	err = ks.StoreKey(&rsaPublicKey{nil})
	if err == nil {
		t.Fatal("error required for typed but nil key")
	}

	err = ks.StoreKey(&rsaPrivateKey{nil})
	if err == nil {
		t.Fatal("error required for typed but nil key")
	}

	err = ks.StoreKey(&aesPrivateKey{nil, false})
	if err == nil {
		t.Fatal("error required for typed but nil key")
	}

	err = ks.StoreKey(&aesPrivateKey{nil, true})
	if err == nil {
		t.Fatal("error required for typed but nil key")
	}
}
