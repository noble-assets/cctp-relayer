package integration_testing

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"cosmossdk.io/math"
	nobletypes "github.com/circlefin/noble-cctp/x/cctp/types"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	libclient "github.com/cometbft/cometbft/rpc/jsonrpc/client"
	sdkClient "github.com/cosmos/cosmos-sdk/client"
	clientTx "github.com/cosmos/cosmos-sdk/client/tx"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/types/bech32"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	xauthsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	xauthtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/strangelove-ventures/noble-cctp-relayer/cmd"
	"github.com/strangelove-ventures/noble-cctp-relayer/cmd/noble"
	"github.com/strangelove-ventures/noble-cctp-relayer/types"
	"github.com/stretchr/testify/require"
)

// TestNobleBurnToEthMint generates and broadcasts a depositForBurn on Noble
// and broadcasts on Ethereum Goerli
func TestNobleBurnToEthMint(t *testing.T) {
	setupTest()
	p := cmd.NewProcessor()

	cfg.Networks.Source.Ethereum.Enabled = false

	// start up relayer
	cfg.Networks.Source.Noble.StartBlock = getNobleLatestBlockHeight()

	fmt.Println("Starting relayer...")
	processingQueue := make(chan *types.MessageState, 10)
	go noble.StartListener(cfg, logger, processingQueue)
	go p.StartProcessor(cfg, logger, processingQueue, sequenceMap)

	fmt.Println("Building Noble depositForBurn txn...")
	ethDestinationAddress := "0x971c54a6Eb782fAccD00bc3Ed5E934Cc5bD8e3Ef"
	fmt.Println("Minting on Ethereum to https://goerli.etherscan.io/address/" + ethDestinationAddress)

	// verify ethereum usdc amount
	client, _ := ethclient.Dial(testCfg.Networks.Ethereum.RPC)
	defer client.Close()
	originalEthBalance := getEthBalance(client, ethDestinationAddress)

	// deposit for burn

	// set up sdk context
	interfaceRegistry := codectypes.NewInterfaceRegistry()
	nobletypes.RegisterInterfaces(interfaceRegistry)
	cdc := codec.NewProtoCodec(interfaceRegistry)
	sdkContext := sdkClient.Context{
		TxConfig: xauthtx.NewTxConfig(cdc, xauthtx.DefaultSignModes),
	}
	txBuilder := sdkContext.TxConfig.NewTxBuilder()
	// get priv key
	keyBz, _ := hex.DecodeString(testCfg.Networks.Noble.PrivateKey)
	privKey := secp256k1.PrivKey{Key: keyBz}
	nobleAddress, err := bech32.ConvertAndEncode("noble", privKey.PubKey().Address())
	require.Nil(t, err)

	mintRecipient := make([]byte, 32)
	copy(mintRecipient[12:], common.FromHex(ethDestinationAddress))
	var burnAmount = math.NewInt(1)

	// deposit for burn on noble
	burnMsg := nobletypes.NewMsgDepositForBurn(
		nobleAddress,
		burnAmount,
		uint32(0),
		mintRecipient,
		"uusdc",
	)
	err = txBuilder.SetMsgs(burnMsg)
	require.Nil(t, err)

	txBuilder.SetGasLimit(cfg.Networks.Destination.Noble.GasLimit)

	// sign + broadcast txn
	rpcClient, err := NewRPCClient(testCfg.Networks.Noble.RPC, 10*time.Second)
	require.Nil(t, err)

	accountNumber, accountSequence, err := GetNobleAccountNumberSequence(cfg.Networks.Destination.Noble.API, nobleAddress)
	require.Nil(t, err)

	sigV2 := signing.SignatureV2{
		PubKey: privKey.PubKey(),
		Data: &signing.SingleSignatureData{
			SignMode:  sdkContext.TxConfig.SignModeHandler().DefaultMode(),
			Signature: nil,
		},
		Sequence: uint64(accountSequence),
	}

	signerData := xauthsigning.SignerData{
		ChainID:       cfg.Networks.Destination.Noble.ChainId,
		AccountNumber: uint64(accountNumber),
		Sequence:      uint64(accountSequence),
	}

	txBuilder.SetSignatures(sigV2)
	sigV2, err = clientTx.SignWithPrivKey(
		sdkContext.TxConfig.SignModeHandler().DefaultMode(),
		signerData,
		txBuilder,
		&privKey,
		sdkContext.TxConfig,
		uint64(accountSequence),
	)

	err = txBuilder.SetSignatures(sigV2)
	require.Nil(t, err)

	// Generated Protobuf-encoded bytes.
	txBytes, err := sdkContext.TxConfig.TxEncoder()(txBuilder.GetTx())
	require.Nil(t, err)

	rpcResponse, err := rpcClient.BroadcastTxSync(context.Background(), txBytes)
	require.Nil(t, err)
	fmt.Printf("Update pending: https://testnet.mintscan.io/noble-testnet/txs/%s\n", rpcResponse.Hash.String())

	fmt.Println("Checking eth wallet...")
	for i := 0; i < 60; i++ {
		if originalEthBalance+burnAmount.Uint64() == getEthBalance(client, ethDestinationAddress) {
			fmt.Println("Successfully minted at https://goerli.etherscan.io/address/" + ethDestinationAddress)
			return
		}
		time.Sleep(1 * time.Second)
	}
	// verify eth balance
	require.Equal(t, originalEthBalance+burnAmount.Uint64(), getEthBalance(client, ethDestinationAddress))
}

func getEthBalance(client *ethclient.Client, address string) uint64 {
	accountAddress := common.HexToAddress(address)
	tokenAddress := common.HexToAddress("0x07865c6e87b9f70255377e024ace6630c1eaa37f") // USDC goerli
	erc20ABI := `[{"constant":true,"inputs":[{"name":"_owner","type":"address"}],"name":"balanceOf","outputs":[{"name":"balance","type":"uint256"}],"payable":false,"stateMutability":"view","type":"function"}]`
	parsedABI, err := abi.JSON(strings.NewReader(erc20ABI))
	if err != nil {
		log.Fatalf("Failed to parse contract ABI: %v", err)
	}

	data, err := parsedABI.Pack("balanceOf", accountAddress)
	if err != nil {
		log.Fatalf("Failed to pack data into ABI interface: %v", err)
	}

	result, err := client.CallContract(context.Background(), ethereum.CallMsg{To: &tokenAddress, Data: data}, nil)
	if err != nil {
		log.Fatalf("Failed to call contract: %v", err)
	}

	balance := new(big.Int)
	err = parsedABI.UnpackIntoInterface(&balance, "balanceOf", result)
	if err != nil {
		log.Fatalf("Failed to unpack data from ABI interface: %v", err)
	}

	// Convert to uint64
	return balance.Uint64()
}

// NewRPCClient initializes a new tendermint RPC client connected to the specified address.
func NewRPCClient(addr string, timeout time.Duration) (*rpchttp.HTTP, error) {
	httpClient, err := libclient.DefaultHTTPClient(addr)
	if err != nil {
		return nil, err
	}
	httpClient.Timeout = timeout
	rpcClient, err := rpchttp.NewWithClient(addr, "/websocket", httpClient)
	if err != nil {
		return nil, err
	}
	return rpcClient, nil
}

func GetNobleAccountNumberSequence(urlBase string, address string) (int64, int64, error) {
	rawResp, err := http.Get(fmt.Sprintf("%s/cosmos/auth/v1beta1/accounts/%s", urlBase, address))
	if err != nil {
		return 0, 0, errors.New("unable to fetch account number, sequence")
	}
	body, _ := io.ReadAll(rawResp.Body)
	var resp types.AccountResp
	err = json.Unmarshal(body, &resp)
	if err != nil {
		return 0, 0, errors.New("unable to parse account number, sequence")
	}
	accountNumber, _ := strconv.ParseInt(resp.AccountNumber, 10, 0)
	accountSequence, _ := strconv.ParseInt(resp.Sequence, 10, 0)

	return accountNumber, accountSequence, nil
}
