package seer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
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
func (cc *ChainConfig) GetValInfo(parent context.Context, first bool) error {
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

	// Fetch validator identity through the first-party RPC seam. Operators can
	// provide valoper because it is readily available on explorers; the adapter
	// resolves the consensus key used by monitoring.
	validator, err := client.Validator(ctx, cc.ValAddress)
	if err != nil {
		return err
	}
	next.Conspub = append([]byte(nil), validator.ConsensusAddress...)
	next.Moniker = validator.Moniker
	next.Jailed = validator.Jailed
	next.Bonded = validator.Bonded
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
				next.Valcons, err = cc.encodeConsensusAddress(pre, next.Conspub[:20])
				if err != nil {
					return err
				}
			} else {
				return errors.New("❓ could not determine bech32 prefix from valoper address: " + cc.ValAddress)
			}
		} else {
			prefix = split[0] + "valcons"
			next.Valcons, err = cc.encodeConsensusAddress(prefix, next.Conspub[:20])
			if err != nil {
				return err
			}
		}
		if first {
			l("⚙️", cc.ValAddress[:20], "... is using consensus key:", next.Valcons)
		}
	}

	signing, err := client.SigningInfo(ctx, next.Valcons)
	if err != nil {
		return err
	}
	next.Tombstoned = signing.Tombstoned
	if next.Tombstoned {
		l(fmt.Sprintf("❗️☠️ %s (%s) is tombstoned 🪦❗️", cc.ValAddress, next.Moniker))
	}
	next.Missed = signing.MissedBlocks

	if next.Window == 0 {
		params, err := client.SlashingParams(ctx)
		if err != nil {
			return err
		}
		next.Window = params.SignedBlocksWindow
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
