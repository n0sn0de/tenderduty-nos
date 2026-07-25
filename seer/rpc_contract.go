package seer

import (
	"context"
	"encoding/hex"
	"strings"
)

// rpcStatus is the monitoring subset of a consensus RPC status response.
type rpcStatus struct {
	Network    string
	CatchingUp bool
}

// validatorRecord is the monitoring subset of a Cosmos validator lookup.
type validatorRecord struct {
	ConsensusAddress []byte
	Moniker          string
	Jailed           bool
	Bonded           bool
}

// signingInfo is the monitoring subset of a Cosmos slashing signing-info query.
type signingInfo struct {
	Tombstoned   bool
	MissedBlocks int64
}

// slashingParams is the monitoring subset of a Cosmos slashing params query.
type slashingParams struct {
	SignedBlocksWindow int64
}

// rpcClient is the dependency seam consumed by core monitoring. Implementations
// translate dependency-specific RPC and protobuf types into the DTOs above.
type rpcClient interface {
	Status(context.Context) (rpcStatus, error)
	Validator(context.Context, string) (validatorRecord, error)
	SigningInfo(context.Context, string) (signingInfo, error)
	SlashingParams(context.Context) (slashingParams, error)
	Remote() string
	Quit() <-chan struct{}
}

type rpcClientFactory interface {
	New(string, string) (rpcClient, error)
}

type validatorAddressCodec interface {
	Encode(string, []byte) (string, error)
}

func (cc *ChainConfig) openRPCClient(endpoint, websocketPath string) (rpcClient, error) {
	factory := cc.clientFactory
	if factory == nil {
		factory = cometBFTRPCFactory{}
	}
	return factory.New(endpoint, websocketPath)
}

func (cc *ChainConfig) encodeConsensusAddress(prefix string, address []byte) (string, error) {
	codec := cc.addressCodec
	if codec == nil {
		codec = cosmosValidatorAddressCodec{}
	}
	return codec.Encode(prefix, address)
}

type blockEvent struct {
	Height             int64
	ProposerAddress    string
	ValidatorAddresses []string
}

type voteType int32

const (
	voteTypePrevote   voteType = 1
	voteTypePrecommit voteType = 2
	voteTypeProposal  voteType = 32
)

type voteEvent struct {
	Type             voteType
	Height           int64
	ValidatorAddress string
}

func normalizeConsensusAddress(address []byte) string {
	return strings.ToUpper(hex.EncodeToString(address))
}

// ToBytes decodes a hexadecimal consensus address.
// Deprecated: retained for source compatibility; monitoring uses first-party DTO bytes directly.
func ToBytes(address string) []byte {
	decoded, _ := hex.DecodeString(strings.ToLower(address))
	return decoded
}

func classifyBlockEvent(event blockEvent, validatorAddress string) StatusUpdate {
	update := StatusUpdate{Height: event.Height, Status: Statusmissed, Final: true}
	if event.ProposerAddress == validatorAddress {
		update.Status = StatusProposed
		return update
	}
	for _, signer := range event.ValidatorAddresses {
		if signer == validatorAddress {
			update.Status = StatusSigned
			break
		}
	}
	return update
}

func classifyVoteEvent(event voteEvent, validatorAddress string) (StatusUpdate, bool) {
	if event.ValidatorAddress != validatorAddress {
		return StatusUpdate{}, false
	}
	update := StatusUpdate{Height: event.Height}
	switch event.Type {
	case voteTypePrevote:
		update.Status = StatusPrevote
	case voteTypePrecommit:
		update.Status = StatusPrecommit
	case voteTypeProposal:
		update.Status = StatusProposed
	}
	return update, true
}
