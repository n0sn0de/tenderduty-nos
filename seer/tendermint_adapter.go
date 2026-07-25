package seer

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/types/bech32"
	slashing "github.com/cosmos/cosmos-sdk/x/slashing/types"
	staking "github.com/cosmos/cosmos-sdk/x/staking/types"
	rpchttp "github.com/tendermint/tendermint/rpc/client/http"
)

const (
	stakingValidatorQuery = "/cosmos.staking.v1beta1.Query/Validator"
	signingInfoQuery      = "/cosmos.slashing.v1beta1.Query/SigningInfo"
	slashingParamsQuery   = "/cosmos.slashing.v1beta1.Query/Params"
)

type tendermintRPCFactory struct{}

func (tendermintRPCFactory) New(endpoint, websocketPath string) (rpcClient, error) {
	return newTendermintRPCClient(endpoint, websocketPath)
}

type tendermintRPCClient struct {
	client *rpchttp.HTTP
}

func newTendermintRPCClient(endpoint, websocketPath string) (*tendermintRPCClient, error) {
	client, err := rpchttp.New(endpoint, websocketPath)
	if err != nil {
		return nil, err
	}
	return &tendermintRPCClient{client: client}, nil
}

func (client *tendermintRPCClient) Status(ctx context.Context) (rpcStatus, error) {
	status, err := client.client.Status(ctx)
	if err != nil {
		return rpcStatus{}, err
	}
	if status == nil {
		return rpcStatus{}, errors.New("got empty status response")
	}
	return rpcStatus{
		Network:    status.NodeInfo.Network,
		CatchingUp: status.SyncInfo.CatchingUp,
	}, nil
}

func (client *tendermintRPCClient) Validator(ctx context.Context, address string) (validatorRecord, error) {
	if strings.Contains(address, "valcons") {
		_, decoded, err := bech32.DecodeAndConvert(address)
		if err != nil {
			return validatorRecord{}, errors.New("could not decode and convert your address" + address)
		}
		return validatorRecord{
			ConsensusAddress: decoded,
			Moniker:          address,
			Bonded:           true,
		}, nil
	}

	request := staking.QueryValidatorRequest{ValidatorAddr: address}
	payload, err := request.Marshal()
	if err != nil {
		return validatorRecord{}, err
	}
	response, err := client.client.ABCIQuery(ctx, stakingValidatorQuery, payload)
	if err != nil {
		return validatorRecord{}, err
	}
	if response == nil || response.Response.Value == nil {
		return validatorRecord{}, errors.New("could not find validator " + address)
	}

	queryResult := &staking.QueryValidatorResponse{}
	if err := queryResult.Unmarshal(response.Response.Value); err != nil {
		return validatorRecord{}, err
	}
	if queryResult.Validator.ConsensusPubkey == nil {
		return validatorRecord{}, errors.New("got invalid consensus pubkey for " + address)
	}

	var consensusAddress []byte
	switch queryResult.Validator.ConsensusPubkey.TypeUrl {
	case "/cosmos.crypto.ed25519.PubKey":
		publicKey := ed25519.PubKey{}
		if err := publicKey.Unmarshal(queryResult.Validator.ConsensusPubkey.Value); err != nil {
			return validatorRecord{}, err
		}
		consensusAddress = publicKey.Address().Bytes()
	case "/cosmos.crypto.secp256k1.PubKey":
		publicKey := secp256k1.PubKey{}
		if err := publicKey.Unmarshal(queryResult.Validator.ConsensusPubkey.Value); err != nil {
			return validatorRecord{}, err
		}
		consensusAddress = publicKey.Address().Bytes()
	}
	if len(consensusAddress) == 0 {
		return validatorRecord{}, errors.New("could not get pubkey for" + address)
	}

	return validatorRecord{
		ConsensusAddress: consensusAddress,
		Moniker:          queryResult.Validator.GetMoniker(),
		Jailed:           queryResult.Validator.Jailed,
		Bonded:           queryResult.Validator.Status == staking.Bonded,
	}, nil
}

func (client *tendermintRPCClient) SigningInfo(ctx context.Context, address string) (signingInfo, error) {
	request := slashing.QuerySigningInfoRequest{ConsAddress: address}
	payload, err := request.Marshal()
	if err != nil {
		return signingInfo{}, err
	}
	response, err := client.client.ABCIQuery(ctx, signingInfoQuery, payload)
	if err != nil {
		return signingInfo{}, err
	}
	if response == nil || response.Response.Value == nil {
		return signingInfo{}, errors.New("could not query validator slashing status, got empty response")
	}
	queryResult := &slashing.QuerySigningInfoResponse{}
	if err := queryResult.Unmarshal(response.Response.Value); err != nil {
		return signingInfo{}, err
	}
	return signingInfo{
		Tombstoned:   queryResult.ValSigningInfo.Tombstoned,
		MissedBlocks: queryResult.ValSigningInfo.MissedBlocksCounter,
	}, nil
}

func (client *tendermintRPCClient) SlashingParams(ctx context.Context) (slashingParams, error) {
	request := &slashing.QueryParamsRequest{}
	payload, err := request.Marshal()
	if err != nil {
		return slashingParams{}, err
	}
	response, err := client.client.ABCIQuery(ctx, slashingParamsQuery, payload)
	if err != nil {
		return slashingParams{}, err
	}
	if response == nil || response.Response.Value == nil {
		return slashingParams{}, errors.New("🛑 could not query slashing params, got empty response")
	}
	queryResult := &slashing.QueryParamsResponse{}
	if err := queryResult.Unmarshal(response.Response.Value); err != nil {
		return slashingParams{}, err
	}
	return slashingParams{SignedBlocksWindow: queryResult.Params.SignedBlocksWindow}, nil
}

func (client *tendermintRPCClient) Remote() string {
	return client.client.Remote()
}

func (client *tendermintRPCClient) Quit() <-chan struct{} {
	return client.client.Quit()
}

type cosmosValidatorAddressCodec struct{}

func (cosmosValidatorAddressCodec) Encode(prefix string, address []byte) (string, error) {
	return bech32.ConvertAndEncode(prefix, address)
}

type stringInt64 string

func (value stringInt64) val() int64 {
	parsed, _ := strconv.ParseInt(string(value), 10, 64)
	return parsed
}

type tendermintBlockWire struct {
	Block struct {
		Header struct {
			Height          stringInt64 `json:"height"`
			ProposerAddress string      `json:"proposer_address"`
		} `json:"header"`
		LastCommit struct {
			Signatures []struct {
				ValidatorAddress string `json:"validator_address"`
			} `json:"signatures"`
		} `json:"last_commit"`
	} `json:"block"`
}

func decodeBlockEvent(payload []byte) (blockEvent, error) {
	wire := tendermintBlockWire{}
	if err := json.Unmarshal(payload, &wire); err != nil {
		return blockEvent{}, err
	}
	event := blockEvent{
		Height:             wire.Block.Header.Height.val(),
		ProposerAddress:    wire.Block.Header.ProposerAddress,
		ValidatorAddresses: make([]string, 0, len(wire.Block.LastCommit.Signatures)),
	}
	for _, signature := range wire.Block.LastCommit.Signatures {
		event.ValidatorAddresses = append(event.ValidatorAddresses, signature.ValidatorAddress)
	}
	return event, nil
}

type tendermintVoteWire struct {
	Vote struct {
		Type             voteType    `json:"type"`
		Height           stringInt64 `json:"height"`
		ValidatorAddress string      `json:"validator_address"`
	} `json:"Vote"`
}

func decodeVoteEvent(payload []byte) (voteEvent, error) {
	wire := tendermintVoteWire{}
	if err := json.Unmarshal(payload, &wire); err != nil {
		return voteEvent{}, err
	}
	return voteEvent{
		Type:             wire.Vote.Type,
		Height:           wire.Vote.Height.val(),
		ValidatorAddress: wire.Vote.ValidatorAddress,
	}, nil
}
