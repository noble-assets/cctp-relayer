package cmd

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"cosmossdk.io/log"
	"github.com/spf13/cobra"
	"github.com/strangelove-ventures/noble-cctp-relayer/cmd/circle"
	"github.com/strangelove-ventures/noble-cctp-relayer/cmd/ethereum"
	"github.com/strangelove-ventures/noble-cctp-relayer/cmd/noble"
	"github.com/strangelove-ventures/noble-cctp-relayer/config"
	"github.com/strangelove-ventures/noble-cctp-relayer/types"
)

type Processor struct {
	Mu sync.RWMutex
}

func NewProcessor() *Processor {
	return &Processor{}
}

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start relaying CCTP transactions from Ethereum to Noble",
	Run:   Start,
}

// State and Store map the iris api lookup id -> MessageState
// State represents all in progress burns/mints
// Store represents terminal states
var State = types.NewStateMap()

// SequenceMap maps the domain -> the equivalent minter account sequence or nonce
var sequenceMap = types.NewSequenceMap()

func Start(cmd *cobra.Command, args []string) {

	p := NewProcessor()

	var wg sync.WaitGroup
	wg.Add(1)

	// initialize minter account sequences
	for key, _ := range Cfg.Networks.Minters {
		switch key {
		case 0:
			ethNonce, err := ethereum.GetEthereumAccountNonce(
				Cfg.Networks.Destination.Ethereum.RPC,
				Cfg.Networks.Minters[0].MinterAddress)

			if err != nil {
				Logger.Error("Error retrieving Ethereum account nonce")
				os.Exit(1)
			}
			sequenceMap.Put(key, ethNonce)
		case 4:
			_, nextMinterSequence, err := noble.GetNobleAccountNumberSequence(
				Cfg.Networks.Destination.Noble.API,
				Cfg.Networks.Minters[4].MinterAddress)

			if err != nil {
				Logger.Error("Error retrieving Noble account sequence")
				os.Exit(1)
			}
			sequenceMap.Put(key, nextMinterSequence)
		}

		// ...initialize more here
	}

	// messageState processing queue
	var processingQueue = make(chan *types.MessageState, 10000)

	// spin up Processor worker pool
	for i := 0; i < int(Cfg.ProcessorWorkerCount); i++ {
		go p.StartProcessor(Cfg, Logger, processingQueue, sequenceMap)
	}

	// listeners listen for events, parse them, and enqueue them to processingQueue
	if Cfg.Networks.Source.Ethereum.Enabled {
		ethereum.StartListener(Cfg, Logger, processingQueue)
	}
	if Cfg.Networks.Source.Noble.Enabled {
		noble.StartListener(Cfg, Logger, processingQueue)
	}
	// ...register more chain listeners here

	wg.Wait()
}

// StartProcessor is the main processing pipeline.
func (p *Processor) StartProcessor(cfg config.Config, logger log.Logger, processingQueue chan *types.MessageState, sequenceMap *types.SequenceMap) {
	for {
		dequeuedMsg := <-processingQueue

		var batchPosition int
		// if this is the first time seeing this tx hash, add the message to State
		// msg, ok := State.Load(LookupKey(dequeuedMsg.SourceTxHash, dequeuedMsg.Type, sha256.Sum256(dequeuedMsg.MsgSentBytes)))
		msg, ok := State.Load(dequeuedMsg.SourceTxHash)
		if !ok {
			batchPosition = 0
			State.Store(dequeuedMsg.SourceTxHash, []*types.MessageState{dequeuedMsg})
			msg, _ = State.Load(dequeuedMsg.SourceTxHash)
			p.Mu.Lock()
			msg[batchPosition].Status = types.Created
			p.Mu.Unlock()

		} else {
			// if we have seen this transaction hash, check if we have seen this message
			// if not, add it to the messageState slice tied to the tx hash
			found := false
			for pos, s := range msg {
				if s == dequeuedMsg {
					batchPosition = pos
					found = true
					break
				}
			}
			if !found {
				msg = append(msg, dequeuedMsg)
				State.Store(dequeuedMsg.SourceTxHash, msg)
				msg, ok = State.Load(dequeuedMsg.SourceTxHash)
				if !ok {
					logger.Error("error adding msg to state", "tx-hash", dequeuedMsg.SourceTxHash)
					continue
				}
				batchPosition = len(msg) - 1
			}
		}

		// if a filter's condition is met, mark as filtered
		if filterDisabledCCTPRoutes(cfg, logger, msg[batchPosition]) ||
			filterInvalidDestinationCallers(cfg, logger, msg[batchPosition]) ||
			filterNonWhitelistedChannels(cfg, logger, msg[batchPosition]) {
			p.Mu.Lock()
			msg[batchPosition].Status = types.Filtered
			p.Mu.Unlock()
		}

		// if the message is burned or pending, check for an attestation
		if msg[batchPosition].Status == types.Created || msg[batchPosition].Status == types.Pending {
			response := circle.CheckAttestation(cfg, logger, msg[batchPosition].IrisLookupId)
			if response != nil {
				if msg[batchPosition].Status == types.Created && response.Status == "pending_confirmations" {
					p.Mu.Lock()
					msg[batchPosition].Status = types.Pending
					msg[batchPosition].Updated = time.Now()
					p.Mu.Unlock()
					time.Sleep(10 * time.Second)
					processingQueue <- msg[batchPosition]
					continue
				} else if response.Status == "pending_confirmations" {
					time.Sleep(10 * time.Second)
					processingQueue <- msg[batchPosition]
					continue
				} else if response.Status == "complete" {
					p.Mu.Lock()
					msg[batchPosition].Status = types.Attested
					msg[batchPosition].Attestation = response.Attestation
					msg[batchPosition].Updated = time.Now()
					p.Mu.Unlock()
				}
			} else {
				// add attestation retry intervals per domain here
				logger.Debug("Attestation is still processing for 0x" + msg[batchPosition].IrisLookupId + ".  Retrying...")
				time.Sleep(10 * time.Second)
				// retry
				processingQueue <- msg[batchPosition]
				continue
			}
		}
		// if the message is attested to, try to broadcast
		if msg[batchPosition].Status == types.Attested {
			p.Mu.RLock()
			destDomain := msg[batchPosition].DestDomain
			p.Mu.RUnlock()
			switch destDomain {
			case 0: // ethereum
				response, err := ethereum.Broadcast(cfg, logger, msg[batchPosition], sequenceMap)
				if err != nil {
					logger.Error("unable to mint on Ethereum", "err", err)
					processingQueue <- msg[batchPosition]
					continue
				}
				msg[batchPosition].DestTxHash = response.Hash().Hex()
				logger.Info(fmt.Sprintf("Successfully broadcast %s to Ethereum.  Tx hash: %s", msg[batchPosition].SourceTxHash, msg[batchPosition].DestTxHash))
			case 4: // noble
				response, err := noble.Broadcast(cfg, logger, msg[batchPosition], sequenceMap)
				if err != nil {
					logger.Error("unable to mint on Noble", "err", err)
					processingQueue <- msg[batchPosition]
					continue
				}
				if response.Code != 0 {
					logger.Error("nonzero response code received", "err", err)
					processingQueue <- msg[batchPosition]
					continue
				}
				// success!
				p.Mu.Lock()
				msg[batchPosition].DestTxHash = response.Hash.String()
				p.Mu.Unlock()
			}
			// ...add minters for different domains here
			p.Mu.Lock()
			msg[batchPosition].Status = types.Complete
			msg[batchPosition].Updated = time.Now()
			p.Mu.Unlock()
		}
	}
}

// filterDisabledCCTPRoutes returns true if we haven't enabled relaying from a source domain to a destination domain
func filterDisabledCCTPRoutes(cfg config.Config, logger log.Logger, msg *types.MessageState) bool {
	val, ok := cfg.Networks.EnabledRoutes[msg.SourceDomain]
	result := !(ok && val == msg.DestDomain)
	if result {
		logger.Info(fmt.Sprintf("Filtered tx %s because relaying from %d to %d is not enabled",
			msg.SourceTxHash, msg.SourceDomain, msg.DestDomain))
	}
	return result
}

// filterInvalidDestinationCallers returns true if the minter is not the destination caller for the specified domain
func filterInvalidDestinationCallers(cfg config.Config, logger log.Logger, msg *types.MessageState) bool {
	zeroByteArr := make([]byte, 32)
	result := false

	switch msg.DestDomain {
	case 4:
		bech32DestinationCaller, err := types.DecodeDestinationCaller(msg.DestinationCaller)
		if err != nil {
			result = true
		}
		if !bytes.Equal(msg.DestinationCaller, zeroByteArr) &&
			bech32DestinationCaller != cfg.Networks.Minters[msg.DestDomain].MinterAddress {
			result = true
		}
		if result {
			logger.Info(fmt.Sprintf("Filtered tx %s because the destination caller %s is specified and it's not the minter %s",
				msg.SourceTxHash, msg.DestinationCaller, cfg.Networks.Minters[msg.DestDomain].MinterAddress))
		}

	default: // minting to evm
		decodedMinter, err := hex.DecodeString(strings.ReplaceAll(cfg.Networks.Minters[0].MinterAddress, "0x", ""))
		if err != nil {
			return !bytes.Equal(msg.DestinationCaller, zeroByteArr)
		}

		decodedMinterPadded := make([]byte, 32)
		copy(decodedMinterPadded[12:], decodedMinter)

		if !bytes.Equal(msg.DestinationCaller, zeroByteArr) && !bytes.Equal(msg.DestinationCaller, decodedMinterPadded) {
			result = true
		}
	}

	return result
}

// filterNonWhitelistedChannels is a Noble specific filter that returns true
// if the channel is not in the forwarding_channel_whitelist
func filterNonWhitelistedChannels(cfg config.Config, logger log.Logger, msg *types.MessageState) bool {
	if !cfg.Networks.Destination.Noble.FilterForwardsByIbcChannel {
		return false
	}
	for _, channel := range cfg.Networks.Destination.Noble.ForwardingChannelWhitelist {
		if msg.Channel == channel {
			return false
		}
	}
	logger.Info(fmt.Sprintf("Filtered tx %s because channel whitelisting is enabled and the tx's channel is not in the whitelist: %s",
		msg.SourceTxHash, msg.Channel))
	return true
}

func init() {
	cobra.OnInitialize(func() {})
}
