package sw

import (
	"encoding/hex"

	"github.com/hyperledger/fabric/bccsp"
	"github.com/hyperledger/fabric/bccsp/utils"

	"github.com/hashicorp/vault/api"
	"github.com/pkg/errors"
)

// NewVaultKeyStore returns a bccsp compatible keystore interface backed by an
// instance of a Vault secret store.
func NewVaultKeyStore(c *api.Client, config *VaultOptions) (bccsp.KeyStore, error) {
	// If a client is not provided, make one.
	var client *api.Client
	if c == nil {
		var err error
		client, err = InitializeClient(*config)
		if err != nil {
			return nil, err
		}
	}

	ks := &VaultKeyStore{
		client: client,
		config: config,
	}

	// check to see if vault has been mounted or not ... if not make one?
	mounted, err := ks.hasSecretMount()
	if err == nil && !mounted {
		logger.Infof("Secret mount not found, attempting to mount as %s", ks.config.Path)
		err = ks.createSecretMount()
	}

	return ks, err
}

type VaultKeyStore struct {
	readOnly bool

	client *api.Client
	config *VaultOptions
}

// ReadOnly returns true if this KeyStore is read only, false otherwise.
// If ReadOnly is true then StoreKey will fail.
func (vault *VaultKeyStore) ReadOnly() bool {
	return vault.readOnly
}

// GetKey returns a key object whose SKI is the one passed.
func (vault *VaultKeyStore) GetKey(ski []byte) (bccsp.Key, error) {
	if len(ski) < 3 {
		return nil, errors.New("invalid SKI; must be at least 3 length")
	}

	api := vault.client.Logical()

	key := hex.EncodeToString(ski)
	secret, err := api.Read(vault.keyPath(key))
	if err != nil {
		return nil, errors.Wrapf(err, "attempt to retrieve key from vault for [%s] failed", key)
	}

	if secret == nil {
		return nil, errors.Errorf("no key found for ski %s", key)
	}
	return KeyFromSecret(secret.Data)
}

// StoreKey stores the key k in this KeyStore.
// If this KeyStore is read only then the method will fail.
func (vault *VaultKeyStore) StoreKey(k bccsp.Key) (err error) {
	if vault.readOnly {
		return errors.New("read only KeyStore; can not store key")
	}

	if k == nil {
		return errors.New("key to store can not be nil")
	}

	ski := k.SKI()
	if len(ski) == 0 {
		return errors.New("invalid SKI; cannot be of zero length")
	}

	if len(ski) < 3 {
		return errors.New("invalid SKI; must be at least 3 length")
	}

	api := vault.client.Logical()
	key := hex.EncodeToString(ski)

	existing, _ := api.Read(vault.keyPath(key))
	if existing != nil {
		return errors.Errorf("ski %s already exists in the keystore", key)
	}

	vk, err := NewVaultKey(&k)
	if err != nil {
		return err
	}

	pem, err := vk.GetSecretJSON(vault.config.Version)
	if err != nil {
		return err
	}
	_, err = api.Write(vault.keyPath(key), pem)
	return err
}

// keyPath makes an adjustment to the key path based on the version of the keystore we are talking to.
func (vault *VaultKeyStore) keyPath(key string) string {
	if vault.config.Version == 2 {
		return vault.config.Path + "data/" + key
	}
	return vault.config.Path + key
}

func (vault *VaultKeyStore) hasSecretMount() (bool, error) {
	list, err := vault.client.Sys().ListMounts()
	return (list[vault.config.Path] != nil), err
}

// createSecretMount mounts a secret engine in the vault server
func (vault *VaultKeyStore) createSecretMount() error {
	mountConfig := api.MountInput{
		Type: "kv",
		Config: api.MountConfigInput{
			DefaultLeaseTTL: "16h",
			MaxLeaseTTL:     "60h",
			Options: map[string]string{
				"version":      string(vault.config.Version),
				"cas_required": "true",
			},
		},
	}
	return vault.client.Sys().Mount(vault.config.Path, &mountConfig)
}

// ExtractKey performs type conversion and validation on values from the vault secret
func ExtractKey(key interface{}, pass interface{}) (interface{}, error) {
	k, ok := key.([]byte)
	if !ok {
		return nil, errors.Errorf("could not extract key from vault secret value [%s]", key)
	}
	p, ok := pass.([]byte)
	return utils.PEMtoPrivateKey(k, p)
}

/*

Example of API usage.

var VClient *api.Client // global variable

func InitVault(token string) error {
    conf := &api.Config{
        Address: "http://127.0.0.1:8200",
    }

    client, err := api.NewClient(conf)
    if err != nil {
        return err
    }
    VClient = client

    VClient.SetToken(token)
    return nil
}


func main() {
    err := InitVault("root token")
    if err != nil {
        log.Println(err)
    }
    c := VClient.Logical()

    secret, err = c.Write("kv/hi",
    map[string]interface{}{
        "name":     name,
        "username": username,
        "password": password,
    })
    if err != nil {
        log.Println(err)
    }
    log.Println(secret)
}

*/
