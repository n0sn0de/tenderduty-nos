package seer

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gorilla/websocket"
	dash "github.com/n0sn0de/tenderduty-nos/seer/dashboard"
	pbtypes "github.com/tendermint/tendermint/proto/tendermint/types"
	"log"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	rpchttp "github.com/tendermint/tendermint/rpc/client/http"
)

const (
	QueryNewBlock string = `tm.event='NewBlock'`
	QueryVote     string = `tm.event='Vote'`
)

// StatusType represents the various possible end states. Prevote and Precommit are special cases, where the node
// monitoring for misses did see them, but the proposer did not include in the block.
type StatusType int

const (
	Statusmissed StatusType = iota
	StatusPrevote
	StatusPrecommit
	StatusSigned
	StatusProposed
)

// StatusUpdate is passed over a channel from the websocket client indicating the current state, it is immediate in the
// case of prevotes etc, and the highest value seen is used in the final determination (which is how we tag
// prevote/precommit + missed blocks.
type StatusUpdate struct {
	Height int64
	Status StatusType
	Final  bool
}

// WsReply is a trimmed down version of the JSON sent from a tendermint websocket subscription.
type WsReply struct {
	Id     int64 `json:"id"`
	Result struct {
		Query string `json:"query"`
		Data  struct {
			Type  string          `json:"type"`
			Value json.RawMessage `json:"value"`
		} `json:"data"`
	} `json:"result"`
}

// Type is the abci message type
func (wsr WsReply) Type() string {
	return wsr.Result.Data.Type
}

// Value returns the JSON encoded raw bytes from the response. Unlike an ABCI RPC query, these are not protobuf.
func (wsr WsReply) Value() []byte {
	if wsr.Result.Data.Value == nil {
		return make([]byte, 0)
	}
	return wsr.Result.Data.Value
}

// WsRun is our main entrypoint for the websocket listener. It blocks until the parent is canceled or the client exits.
func (cc *ChainConfig) WsRun(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	var workers sync.WaitGroup
	defer func() {
		cancel()
		cc.closeWebSocket()
		workers.Wait()
	}()

	var client *rpchttp.HTTP
	started := time.Now()
	for {
		client = cc.client
		if client != nil && cc.valInfo != nil && cc.valInfo.Conspub != nil {
			break
		}
		if started.Before(time.Now().Add(-2 * time.Minute)) {
			l(cc.name, "websocket client timed out waiting for a working rpc endpoint, restarting")
			return
		}
		l("⏰ waiting for a healthy client for", cc.ChainId)
		if !waitForContext(ctx, 30*time.Second) {
			return
		}
	}

	connection, err := NewClientContext(ctx, client.Remote(), true)
	if err != nil {
		l(err)
		return
	}
	cc.connectionMux.Lock()
	cc.wsclient = connection
	cc.connectionMux.Unlock()
	if err := connection.SetCompressionLevel(3); err != nil {
		log.Println(err)
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		<-ctx.Done()
		_ = connection.Close()
	}()

	resultChan := make(chan StatusUpdate)
	workers.Add(1)
	go func() {
		defer workers.Done()
		var signState StatusType = -1
		for {
			select {
			case update := <-resultChan:
				if update.Final && update.Height%20 == 0 {
					l(fmt.Sprintf("🧊 %-12s block %d", cc.ChainId, update.Height))
				}
				if update.Status > signState && cc.valInfo.Bonded {
					signState = update.Status
				}
				if !update.Final {
					continue
				}
				cc.lastBlockNum = update.Height
				now := time.Now()
				previousBlockTime := cc.recordFinalBlock(now)
				if td.Prom {
					if !td.emitStat(ctx, cc.mkUpdate(metricLastBlockSeconds, now.Sub(previousBlockTime).Seconds(), "")) {
						return
					}
				}
				info := getAlarms(cc.name)
				blocks := cc.recordBlockResult(int(signState))
				if signState < StatusSigned && cc.valInfo.Bonded {
					warn := fmt.Sprintf("❌ warning      %s missed block %d on %s", cc.valInfo.Moniker, update.Height, cc.ChainId)
					info += warn + "\n"
					cc.lastError = time.Now().UTC().String() + " " + info
					l(warn)
				}
				switch signState {
				case Statusmissed:
					cc.statTotalMiss++
					cc.statConsecutiveMiss++
				case StatusPrecommit:
					cc.statPrecommitMiss++
					cc.statTotalMiss++
					cc.statConsecutiveMiss++
				case StatusPrevote:
					cc.statPrevoteMiss++
					cc.statTotalMiss++
					cc.statConsecutiveMiss++
				case StatusSigned:
					cc.statTotalSigns++
					cc.statConsecutiveMiss = 0
				case StatusProposed:
					cc.statTotalProps++
					cc.statTotalSigns++
					cc.statConsecutiveMiss = 0
				}
				signState = -1
				healthyNodes := 0
				for _, node := range cc.Nodes {
					state := cc.nodeState(node)
					if !state.down {
						healthyNodes++
					} else if !td.HideLogs {
						info += "\n - " + state.lastMsg
					}
				}
				switch {
				case cc.valInfo.Tombstoned:
					info += "- validator is tombstoned\n"
				case cc.valInfo.Jailed:
					info += "- validator is jailed\n"
				}
				cc.activeAlerts = td.alarmState().getCount(cc.name)
				if td.EnableDash {
					td.updateChan <- &dash.ChainStatus{
						MsgType:      "status",
						Name:         cc.name,
						ChainId:      cc.ChainId,
						Moniker:      cc.valInfo.Moniker,
						Bonded:       cc.valInfo.Bonded,
						Jailed:       cc.valInfo.Jailed,
						Tombstoned:   cc.valInfo.Tombstoned,
						Missed:       cc.valInfo.Missed,
						Window:       cc.valInfo.Window,
						Nodes:        len(cc.Nodes),
						HealthyNodes: healthyNodes,
						ActiveAlerts: cc.activeAlerts,
						Height:       update.Height,
						LastError:    info,
						Blocks:       blocks,
					}
				}
				if td.Prom {
					updates := []*promUpdate{
						cc.mkUpdate(metricSigned, cc.statTotalSigns, ""),
						cc.mkUpdate(metricProposed, cc.statTotalProps, ""),
						cc.mkUpdate(metricMissed, cc.statTotalMiss, ""),
						cc.mkUpdate(metricPrevote, cc.statPrevoteMiss, ""),
						cc.mkUpdate(metricPrecommit, cc.statPrecommitMiss, ""),
						cc.mkUpdate(metricConsecutive, cc.statConsecutiveMiss, ""),
						cc.mkUpdate(metricUnealthyNodes, float64(len(cc.Nodes)-healthyNodes), ""),
					}
					for _, metric := range updates {
						if !td.emitStat(ctx, metric) {
							return
						}
					}
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	consensusAddress := strings.ToUpper(hex.EncodeToString(append([]byte(nil), cc.valInfo.Conspub...)))
	voteChan := make(chan *WsReply)
	workers.Add(1)
	go func() {
		defer workers.Done()
		handleVotes(ctx, voteChan, resultChan, consensusAddress)
	}()
	blockChan := make(chan *WsReply)
	workers.Add(1)
	go func() {
		defer workers.Done()
		if err := handleBlocks(ctx, blockChan, resultChan, consensusAddress); err != nil {
			l("🛑", cc.ChainId, err)
			cancel()
		}
	}()

	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			_, message, err := connection.ReadMessage()
			if err != nil {
				if ctx.Err() == nil {
					l(err)
				}
				cancel()
				return
			}
			reply := &WsReply{}
			if err := json.Unmarshal(message, reply); err != nil {
				continue
			}
			var destination chan *WsReply
			switch reply.Type() {
			case `tendermint/event/NewBlock`:
				destination = blockChan
			case `tendermint/event/Vote`:
				destination = voteChan
			default:
				continue
			}
			select {
			case destination <- reply:
			case <-ctx.Done():
				return
			}
		}
	}()

	for _, subscribe := range []string{QueryNewBlock, QueryVote} {
		query := fmt.Sprintf(`{"jsonrpc":"2.0","method":"subscribe","id":1,"params":{"query":"%s"}}`, subscribe)
		if err := connection.WriteMessage(websocket.TextMessage, []byte(query)); err != nil {
			l(err)
			cancel()
			return
		}
	}
	l(fmt.Sprintf("⚙️ %-12s watching for NewBlock and Vote events via %s", cc.ChainId, client.Remote()))
	select {
	case <-client.Quit():
		cancel()
	case <-ctx.Done():
	}
}

func (cc *ChainConfig) closeWebSocket() {
	cc.connectionMux.Lock()
	defer cc.connectionMux.Unlock()
	if cc.wsclient != nil {
		_ = cc.wsclient.Close()
		cc.wsclient = nil
	}
}

func (c *Config) closeWebSockets() {
	for _, chain := range c.Chains {
		chain.closeWebSocket()
	}
}

type stringInt64 string

// helper to make the "everything is a string" issue less painful.
func (si stringInt64) val() int64 {
	i, _ := strconv.ParseInt(string(si), 10, 64)
	return i
}

type signature struct {
	ValidatorAddress string `json:"validator_address"`
}

// rawBlock is a trimmed down version of the block subscription result, it contains only what we need.
type rawBlock struct {
	Block struct {
		Header struct {
			Height          stringInt64 `json:"height"`
			ProposerAddress string      `json:"proposer_address"`
		} `json:"header"`
		LastCommit struct {
			Signatures []signature `json:"signatures"`
		} `json:"last_commit"`
	} `json:"block"`
}

// find determines if a validator's pre-commit was included in a finalized block.
func (rb rawBlock) find(val string) bool {
	if rb.Block.LastCommit.Signatures == nil {
		return false
	}
	for _, v := range rb.Block.LastCommit.Signatures {
		if v.ValidatorAddress == val {
			return true
		}
	}
	return false
}

// handleBlocks consumes the channel for new blocks and when it sees one sends a status update. It's also
// responsible for stalled chain detection and will shutdown the client if there are no blocks for a minute.
func handleBlocks(ctx context.Context, blocks chan *WsReply, results chan StatusUpdate, address string) error {
	live := time.NewTicker(time.Minute)
	defer live.Stop()
	lastBlock := time.Now()
	for {
		select {
		case <-live.C:
			// no block for a full minute likely means we have either a dead chain, or a dead client.
			if lastBlock.Before(time.Now().Add(-time.Minute)) {
				return errors.New("websocket idle for 1 minute, exiting")
			}
		case block := <-blocks:
			lastBlock = time.Now()
			b := &rawBlock{}
			err := json.Unmarshal(block.Value(), b)
			if err != nil {
				l("could not decode block", err)
				continue
			}
			upd := StatusUpdate{
				Height: b.Block.Header.Height.val(),
				Status: Statusmissed,
				Final:  true,
			}
			if b.Block.Header.ProposerAddress == address {
				upd.Status = StatusProposed
			} else if b.find(address) {
				upd.Status = StatusSigned
			}
			select {
			case results <- upd:
			case <-ctx.Done():
				return nil
			}
		case <-ctx.Done():
			return nil
		}
	}
}

// rawVote is a trimmed down version of the vote response.
type rawVote struct {
	Vote struct {
		Type             pbtypes.SignedMsgType `json:"type"`
		Height           stringInt64           `json:"height"`
		ValidatorAddress string                `json:"validator_address"`
	} `json:"Vote"`
}

// handleVotes consumes the channel for precommits and prevotes, tracking where in the process a validator is.
func handleVotes(ctx context.Context, votes chan *WsReply, results chan StatusUpdate, address string) {
	for {
		select {
		case reply := <-votes:
			vote := &rawVote{}
			err := json.Unmarshal(reply.Value(), vote)
			if err != nil {
				l(err)
				continue
			}
			if vote.Vote.ValidatorAddress == address {
				upd := StatusUpdate{Height: vote.Vote.Height.val()}
				switch vote.Vote.Type.String() {
				case "":
					continue
				case "SIGNED_MSG_TYPE_PREVOTE":
					upd.Status = StatusPrevote
				case "SIGNED_MSG_TYPE_PRECOMMIT":
					upd.Status = StatusPrecommit
				case "SIGNED_MSG_TYPE_PROPOSAL":
					upd.Status = StatusProposed
				}
				select {
				case results <- upd:
				case <-ctx.Done():
					return
				}
			}

		case <-ctx.Done():
			return
		}
	}
}

// TmConn is the websocket client. This is probably not necessary since I expected more complexity.
type TmConn struct {
	*websocket.Conn
}

// NewClient returns a websocket client.
func NewClient(u string, allowInsecure bool) (*TmConn, error) {
	return NewClientContext(context.Background(), u, allowInsecure)
}

// NewClientContext returns a websocket client whose dial is canceled with ctx.
func NewClientContext(ctx context.Context, u string, allowInsecure bool) (*TmConn, error) {
	// FIXME: need to handle UDS and insecure TLS
	// dialUnix is used to determine if the connection is to a UDS and requires a custom dialer.
	var dialUnix bool

	// normalize the path, some public rpcs prefix with /rpc or similar.
	u = strings.TrimRight(u, "/")
	if !strings.HasSuffix(u, "/websocket") {
		u += "/websocket"
	}

	endpoint, err := url.Parse(u)
	if err != nil {
		return nil, fmt.Errorf("parsing url in NewWsClient %s: %s", u, err.Error())
	}

	// normalize scheme to ws or wss
	switch endpoint.Scheme {
	case "http", "tcp", "ws":
		endpoint.Scheme = "ws"
	case "unix":
		dialUnix = true
		endpoint.Scheme = "ws"
	case "https", "wss":
		endpoint.Scheme = "wss"
	default:
		return nil, fmt.Errorf("protocol %s is unknown, valid choices are http, https, tcp, unix, ws, and wss", endpoint.Scheme)
	}

	// allowInsecure is primarily intended for self-signed certs, but it doesn't make sense to allow yes to for non-tls
	if endpoint.Scheme == "ws" && !allowInsecure {
		return nil, errors.New("allowInsecure must be true if protocol is not using TLS")
	}

	conn := &websocket.Conn{}

	switch {

	// TODO: add custom UDS dialer
	case dialUnix:

	// TODO: add custom TLS dialer to allow self-signed certs.
	// case allowInsecure && endpoint.Scheme == "wss":

	default:
		conn, _, err = websocket.DefaultDialer.DialContext(ctx, endpoint.String(), nil)
		if err != nil {
			return nil, fmt.Errorf("could not dial ws client to %s: %s", endpoint.String(), err.Error())
		}
	}
	return &TmConn{Conn: conn}, nil
}
