package seer

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/types/bech32"
	slashing "github.com/cosmos/cosmos-sdk/x/slashing/types"
	staking "github.com/cosmos/cosmos-sdk/x/staking/types"
	rpchttp "github.com/tendermint/tendermint/rpc/client/http"
)

// ValInfo holds most of the stats/info used for secondary alarms. It is refreshed roughly every minute.
type ValInfo struct {
	Moniker    string `json:"moniker"`
	Bonded     bool   `json:"bonded"`
	Jailed     bool   `json:"jailed"`
	Tombstoned bool   `json:"tombstoned"`
	Missed     int64  `json:"missed"`
	Window     int64  `json:"window"`
	Conspub    []byte `json:"conspub"`
	Valcons    string `json:"valcons"`
}

// GetValInfo refreshes validator data. The first bool controls startup-only detail logging.
func (cc *ChainConfig) GetValInfo(parent context.Context, first bool) (err error) {
	// Serialize endpoint selection and validator refresh so one refresh uses one
	// client throughout its network queries. Published state remains lock-free
	// during the I/O and is swapped atomically only after a complete refresh.
	cc.rpcMux.Lock()
	defer cc.rpcMux.Unlock()

	client, current, _ := cc.monitoringSnapshot()
	if client == nil {
		return errors.New("nil rpc client")
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()

	next := &ValInfo{}
	if current != nil {
		next.Window = current.Window
	}

	// Fetch info from /cosmos.staking.v1beta1.Query/Validator
	// it's easier to ask people to provide valoper since it's readily available on
	// explorers, so make it easy and lookup the consensus key for them.
	next.Conspub, next.Moniker, next.Jailed, next.Bonded, err = getVal(ctx, client, cc.ValAddress)
	if err != nil {
		return
	}
	if first && next.Bonded {
		l(fmt.Sprintf("⚙️ found %s (%s) in validator set", cc.ValAddress, next.Moniker))
	} else if first && !next.Bonded {
		l(fmt.Sprintf("❌ %s (%s) is INACTIVE", cc.ValAddress, next.Moniker))
	}

	if strings.Contains(cc.ValAddress, "valcons") {
		// no need to change prefix for signing info query
		next.Valcons = cc.ValAddress
	} else {
		// need to know the prefix for when we serialize the slashing info query, this is too fragile.
		// for now, we perform specific chain overrides based on known values because the valoper is used
		// in so many places.
		var prefix string
		split := strings.Split(cc.ValAddress, "valoper")
		if len(split) != 2 {
			if pre, ok := altValopers.getAltPrefix(cc.ValAddress); ok {
				next.Valcons, err = bech32.ConvertAndEncode(pre, next.Conspub[:20])
				if err != nil {
					return
				}
			} else {
				err = errors.New("❓ could not determine bech32 prefix from valoper address: " + cc.ValAddress)
				return
			}
		} else {
			prefix = split[0] + "valcons"
			next.Valcons, err = bech32.ConvertAndEncode(prefix, next.Conspub[:20])
			if err != nil {
				return
			}
		}
		if first {
			l("⚙️", cc.ValAddress[:20], "... is using consensus key:", next.Valcons)
		}

	}

	// get current signing information (tombstoned, missed block count)
	qSigning := slashing.QuerySigningInfoRequest{ConsAddress: next.Valcons}
	b, err := qSigning.Marshal()
	if err != nil {
		return err
	}
	resp, err := client.ABCIQuery(ctx, "/cosmos.slashing.v1beta1.Query/SigningInfo", b)
	if err != nil {
		return err
	}
	if resp == nil || resp.Response.Value == nil {
		return errors.New("could not query validator slashing status, got empty response")
	}
	slash := &slashing.QuerySigningInfoResponse{}
	if err = slash.Unmarshal(resp.Response.Value); err != nil {
		return err
	}
	next.Tombstoned = slash.ValSigningInfo.Tombstoned
	if next.Tombstoned {
		l(fmt.Sprintf("❗️☠️ %s (%s) is tombstoned 🪦❗️", cc.ValAddress, next.Moniker))
	}
	next.Missed = slash.ValSigningInfo.MissedBlocksCounter

	// finally get the signed blocks window
	if next.Window == 0 {
		qParams := &slashing.QueryParamsRequest{}
		b, err = qParams.Marshal()
		if err != nil {
			return err
		}
		resp, err = client.ABCIQuery(ctx, "/cosmos.slashing.v1beta1.Query/Params", b)
		if err != nil {
			return err
		}
		if resp == nil || resp.Response.Value == nil {
			return errors.New("🛑 could not query slashing params, got empty response")
		}
		params := &slashing.QueryParamsResponse{}
		if err = params.Unmarshal(resp.Response.Value); err != nil {
			return err
		}
		next.Window = params.Params.SignedBlocksWindow
	}

	cc.publishValidatorInfo(next, !first)
	if td.Prom {
		td.emitStat(ctx, cc.mkUpdate(metricWindowMissed, float64(next.Missed), ""))
		if first {
			td.emitStat(ctx, cc.mkUpdate(metricWindowSize, float64(next.Window), ""))
			td.emitStat(ctx, cc.mkUpdate(metricTotalNodes, float64(len(cc.Nodes)), ""))
		}
	}
	return nil
}

// getVal returns the public key, moniker, and if the validator is jailed.
func getVal(ctx context.Context, client *rpchttp.HTTP, valoper string) (pub []byte, moniker string, jailed, bonded bool, err error) {
	if strings.Contains(valoper, "valcons") {
		_, bz, err := bech32.DecodeAndConvert(valoper)
		if err != nil {
			return nil, "", false, false, errors.New("could not decode and convert your address" + valoper)
		}

		hexAddress := fmt.Sprintf("%X", bz)
		return ToBytes(hexAddress), valoper, false, true, nil
	}

	q := staking.QueryValidatorRequest{
		ValidatorAddr: valoper,
	}
	b, err := q.Marshal()
	if err != nil {
		return
	}
	resp, err := client.ABCIQuery(ctx, "/cosmos.staking.v1beta1.Query/Validator", b)
	if err != nil {
		return
	}
	if resp.Response.Value == nil {
		return nil, "", false, false, errors.New("could not find validator " + valoper)
	}
	val := &staking.QueryValidatorResponse{}
	err = val.Unmarshal(resp.Response.Value)
	if err != nil {
		return
	}
	if val.Validator.ConsensusPubkey == nil {
		return nil, "", false, false, errors.New("got invalid consensus pubkey for " + valoper)
	}

	pubBytes := make([]byte, 0)
	switch val.Validator.ConsensusPubkey.TypeUrl {
	case "/cosmos.crypto.ed25519.PubKey":
		pk := ed25519.PubKey{}
		err = pk.Unmarshal(val.Validator.ConsensusPubkey.Value)
		if err != nil {
			return
		}
		pubBytes = pk.Address().Bytes()
	case "/cosmos.crypto.secp256k1.PubKey":
		pk := secp256k1.PubKey{}
		err = pk.Unmarshal(val.Validator.ConsensusPubkey.Value)
		if err != nil {
			return
		}
		pubBytes = pk.Address().Bytes()
	}
	if len(pubBytes) == 0 {
		return nil, "", false, false, errors.New("could not get pubkey for" + valoper)
	}

	return pubBytes, val.Validator.GetMoniker(), val.Validator.Jailed, val.Validator.Status == 3, nil
}

func ToBytes(address string) []byte {
	bz, _ := hex.DecodeString(strings.ToLower(address))
	return bz
}
