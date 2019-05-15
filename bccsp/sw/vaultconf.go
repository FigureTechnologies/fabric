package sw

import (
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	vault "github.com/hashicorp/vault/api"
	"github.com/hyperledger/fabric/bccsp/utils"

	"github.com/hyperledger/fabric/bccsp"
	"github.com/pkg/errors"
)

// VaultOptions for configuring a Hashicorp Vault back BCCSP
type VaultOptions struct {
	Host      string `mapstructure:"host" yaml:"Host" json:"host"`
	Port      int    `mapstructure:"port" yaml:"Port" json:"port"`
	Token     string `mapstructure:"token" yaml:"Token" json:"token"`
	TLS       bool   `mapstructure:"usetls" yaml:"UseTLS" json:"UseTLS"`
	VerifyTLS bool   `mapstructure:"verifytls" yaml:"VerifyTLS" json:"verifytls"`
	Version   int    `mapstructure:"version" yaml:"Version" json:"version"`
	Path      string `mapstructure:"path" yaml:"Path" json:"path"`
	Timeout   int    `mapstructure:"timeout" yaml:"Timeout" json:"timeout"`
}

// InitializeClient returns a new Vault Client with the current configuration
func InitializeClient(cfg VaultOptions) (*vault.Client, error) {

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: !cfg.VerifyTLS},
	}

	protocol := "http"
	if cfg.TLS {
		protocol = "https"
	}

	var timeout time.Duration
	if cfg.Timeout > 0 {
		timeout = time.Duration(cfg.Timeout)
	} else {
		timeout = 3
	}

	config := vault.Config{
		Address:    fmt.Sprintf("%v://%v:%v", protocol, cfg.Host, cfg.Port),
		HttpClient: &http.Client{Transport: tr},
		Timeout:    timeout * time.Second,
	}

	vc, err := vault.NewClient(&config)
	if err != nil {
		return vc, err
	}

	vc.SetToken(cfg.Token)
	vc.Auth()
	return vc, nil
}

type VaultKey struct {
	bccspKey bccsp.Key

	sourceType string
	ski        []byte
}

// VaultSecret structure is used for persisting bccsp.Key objects in Hashicorp Vault
type VaultSecret struct {
	Options struct {
		Cas int `json:"cas"`
	} `json:"options"`

	Data struct {
		KeyBytes   string `json:"pem"`
		SourceType string `json:"type"`

		Ski string `json:"ski"`
	} `json:"data"`
}

// Method set for making existing BCCSP keys serializable

// Serialize converts this privatekey to its byte representation,
func (k *rsaPrivateKey) Serialize() ([]byte, error) {
	return utils.PrivateKeyToPEM(k.privKey, nil)
}

// Serialize converts this privatekey to its byte representation,
func (k *ecdsaPrivateKey) Serialize() ([]byte, error) {
	return utils.PrivateKeyToPEM(k.privKey, nil)
}

// Serialize converts this privatekey to its byte representation,
func (k *aesPrivateKey) Serialize() ([]byte, error) {
	return utils.PrivateKeyToPEM(k.privKey, nil)
}

// Serialize converts this publickey to its byte representation,
func (k *rsaPublicKey) Serialize() ([]byte, error) {
	return utils.PublicKeyToPEM(k.pubKey, nil)
}

// Serialize converts this publickey to its byte representation,
func (k *ecdsaPublicKey) Serialize() ([]byte, error) {
	return utils.PublicKeyToPEM(k.pubKey, nil)
}

// serialize takes the current vault key instance and returns a pem representation (in bytes)
func (vk *VaultKey) serialize() ([]byte, error) {
	switch vk.bccspKey.(type) {
	case *ecdsaPrivateKey:
		kk := vk.bccspKey.(*ecdsaPrivateKey)
		vk.sourceType = "ecdsaPrivateKey"
		return kk.Serialize()

	case *ecdsaPublicKey:
		kk := vk.bccspKey.(*ecdsaPublicKey)
		vk.sourceType = "ecdsaPublicKey"
		return kk.Serialize()

	case *rsaPrivateKey:
		kk := vk.bccspKey.(*rsaPrivateKey)
		vk.sourceType = "rsaPrivateKey"
		return kk.Serialize()

	case *rsaPublicKey:
		kk := vk.bccspKey.(*rsaPublicKey)
		vk.sourceType = "rsaPublicKey"
		return kk.Serialize()

	case *aesPrivateKey:
		kk := vk.bccspKey.(*aesPrivateKey)
		vk.sourceType = "aesPrivateKey"
		return kk.Serialize()

	default:
		return nil, fmt.Errorf("Key type not reconigned [%T]", vk.bccspKey)
	}
}

// deserialize sets the internal VaultKey bccsp.Key instance to the given encoded pem.
func (vk *VaultKey) deserialize(raw []byte) error {
	switch vk.sourceType {
	case "ecdsaPrivateKey":
		privateKey, err := utils.PEMtoPrivateKey(raw, nil)
		vk.bccspKey = &ecdsaPrivateKey{privateKey.(*ecdsa.PrivateKey)}
		return err

	case "ecdsaPublicKey":
		publicKey, err := utils.PEMtoPublicKey(raw, nil)
		vk.bccspKey = &ecdsaPublicKey{publicKey.(*ecdsa.PublicKey)}
		return err

	case "rsaPrivateKey":
		privateKey, err := utils.PEMtoPrivateKey(raw, nil)
		vk.bccspKey = &rsaPrivateKey{privateKey.(*rsa.PrivateKey)}
		return err

	case "rsaPublicKey":
		publicKey, err := utils.PEMtoPublicKey(raw, nil)
		vk.bccspKey = &rsaPublicKey{publicKey.(*rsa.PublicKey)}
		return err

	case "aesPrivateKey":
		privateKey, err := utils.PEMtoAES(raw, nil)
		vk.bccspKey = &aesPrivateKey{privateKey, false}
		return err

	default:
		return fmt.Errorf("Key type not reconigned [%T]", vk.bccspKey)
	}
}

// NewVaultKey returns a VaultKey instance encapsulating the given bccsp.Key
func NewVaultKey(bKey *bccsp.Key) (*VaultKey, error) {
	if bKey == nil {
		return nil, errors.New("source bccsp key can not be nil")
	}

	vk := &VaultKey{
		bccspKey:   *bKey,
		sourceType: fmt.Sprintf("%T", bKey),
		ski:        (*bKey).SKI(),
	}

	// Make sure we are able to serialize this keytype.
	_, err := vk.serialize()
	if err != nil {
		return nil, err
	}
	return vk, nil
}

// KeyFromSecret unpacks the stored information in a Vault Secret into a VaultKey
func KeyFromSecret(data map[string]interface{}) (*VaultKey, error) {

	// If there isnt any data to parse then complain.
	if len(data) == 0 || data == nil {
		return nil, errors.New("Cannot deserialize secret from null")
	}

	// is this version 2?
	if data["data"] != nil {
		dataStr, err := json.Marshal(data)
		if err != nil {
			return nil, err
		}
		var vs VaultSecret
		err = json.Unmarshal(dataStr, &vs)
		if err != nil {
			return nil, err
		}

		skiBytes, err := hex.DecodeString(vs.Data.Ski)
		if err != nil {
			return nil, err
		}

		vk := &VaultKey{
			sourceType: vs.Data.SourceType,
			ski:        skiBytes,
		}

		pem, err := hex.DecodeString(vs.Data.KeyBytes)
		if err != nil {
			return nil, err
		}

		return vk, vk.deserialize(pem)
	}
	// version 1 vault data, no JSON decoding...
	if data["ski"] == nil {
		return nil, errors.New("secret malformed, no ski found")
	}

	// Decoding the SKI must succeed, it is what we are using to identify the key in the vault.
	skiBytes, err := hex.DecodeString(data["ski"].(string))
	if err != nil {
		return nil, err
	}

	vk := &VaultKey{
		sourceType: data["sourceType"].(string),
		ski:        skiBytes,
	}

	return vk, vk.deserialize(data["raw"].([]byte))
}

// Bytes converts this key to its byte representation, if this operation is allowed.
func (vk *VaultKey) Bytes() ([]byte, error) {
	return vk.bccspKey.Bytes()
}

// SKI returns the subject key identifier of this key.
func (vk *VaultKey) SKI() []byte {
	return vk.bccspKey.SKI()
}

// Symmetric returns true if this key is a symmetric key, false is this key is asymmetric
func (vk *VaultKey) Symmetric() bool {
	return vk.bccspKey.Symmetric()
}

// Private returns true if this key is a private key,  false otherwise.
func (vk *VaultKey) Private() bool {
	return vk.bccspKey.Private()
}

// PublicKey returns the corresponding public key part of an asymmetric public/private key pair.
// This method returns an error in symmetric key schemes.
func (vk *VaultKey) PublicKey() (bccsp.Key, error) {
	return vk.bccspKey.PublicKey()
}

// VaultPathName is the value used in the Vault resource path for this secret instance
func (vk *VaultKey) VaultPathName() (string, error) {
	return hex.EncodeToString(vk.bccspKey.SKI()), nil
}

// GetSecretJSON returns a structure of data suitable to represent a VaultKey in a Vault Secret
func (vk *VaultKey) GetSecretJSON(version int) (map[string]interface{}, error) {
	pem, err := vk.serialize()
	if err != nil {
		return nil, err
	}

	vs := &VaultSecret{}
	vs.Data.KeyBytes = hex.EncodeToString(pem)
	vs.Data.SourceType = vk.sourceType
	vs.Data.Ski = hex.EncodeToString(vk.ski)

	if version > 1 {
		vs.Options.Cas = 0 // Set 'check and set' flag to zero to allow writing only if key does not exist.
	}

	// encode structure into JSON
	vaultSecretJSON, err := json.Marshal(vs)
	if err != nil {
		return nil, err
	}

	// use JSON structure to build generic map for Vault's client.
	vaultSecretMap := make(map[string]interface{})
	err = json.Unmarshal(vaultSecretJSON, &vaultSecretMap)
	if err != nil {
		return nil, err
	}

	return vaultSecretMap, nil
}
