package sw

import (
	"errors"

	"github.com/hashicorp/vault/api"
	"github.com/hyperledger/fabric/bccsp"
)

type PKIService struct {
	conf   *api.Config
	client *api.Client
}


func (pkiService *PKIService) KeyGen(opts bccsp.KeyGenOpts) (k bccsp.Key, err error) {
	//newKey := bccsp.Key()

	//client.
	return nil, errors.New("not implemented")
}
