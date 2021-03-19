// Copyright 2018 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

// +build none

// This file contains a miner stress test based on the Ethash consensus engine.
package main

import (
	"crypto/ecdsa"
	crand "crypto/rand"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/big"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"time"
	vbls "vuvuzela.io/crypto/bls"

	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/fdlimit"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/eth/downloader"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/miner"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/params"
	"github.com/silesiacoin/bls/herumi"
	"net/http/httptest"
)

func main() {
	log.Root().SetHandler(log.LvlFilterHandler(log.LvlInfo, log.StreamHandler(os.Stderr, log.TerminalFormat(true))))
	fdlimit.Raise(2048)

	// Generate a batch of accounts to seal and fund with
	faucets := make([]*ecdsa.PrivateKey, 128)
	for i := 0; i < len(faucets); i++ {
		faucets[i], _ = crypto.GenerateKey()
	}
	// Pre-generate the ethash mining DAG so we don't race
	ethash.MakeDataset(1, filepath.Join(os.Getenv("HOME"), ".ethash"))

	sealers := [32]*vbls.PublicKey{}
	validatorPrivateList := [32]*vbls.PrivateKey{}

	for i := 0; i < len(sealers); i++ {
		randomReader := crand.Reader
		pubKey, privKey, err := herumi.GenerateKey(randomReader)

		if nil != err {
			panic(fmt.Sprintf("Error during creation of herumi keys: %s", err.Error()))
		}

		sealers[i] = pubKey
		validatorPrivateList[i] = privKey
	}

	// Create an Ethash network based off of the Ropsten config
	genesis := makeGenesis(faucets, sealers)

	notifyUrl, err := makeSealerServer(genesis, sealers, validatorPrivateList)
	notifyUrls := make([]string, 0)
	notifyUrls = append(notifyUrls, notifyUrl)

	if nil != err {
		panic(fmt.Sprintf("Died when starting the sealer, err: %v", err.Error()))
	}

	var (
		nodes  []*eth.Ethereum
		enodes []*enode.Node
	)
	for i := 0; i < 4; i++ {
		// Start the node and wait until it's up
		stack, ethBackend, err := makeMiner(genesis, notifyUrls, sealers)
		if err != nil {
			panic(err)
		}
		defer stack.Close()

		for stack.Server().NodeInfo().Ports.Listener == 0 {
			time.Sleep(250 * time.Millisecond)
		}

		makeRemoteSealer(stack, sealers, validatorPrivateList)

		// Connect the node to all the previous ones
		for _, n := range enodes {
			stack.Server().AddPeer(n)
		}
		// Start tracking the node and its enode
		nodes = append(nodes, ethBackend)
		enodes = append(enodes, stack.Server().Self())

		// Inject the signer key and start sealing with it
		store := stack.AccountManager().Backends(keystore.KeyStoreType)[0].(*keystore.KeyStore)
		if _, err := store.NewAccount(""); err != nil {
			panic(err)
		}
	}

	// Iterate over all the nodes and start mining
	time.Sleep(3 * time.Second)
	for _, node := range nodes {
		if err := node.StartMining(1); err != nil {
			panic(err)
		}
	}
	time.Sleep(3 * time.Second)

	// Start injecting transactions from the faucets like crazy
	nonces := make([]uint64, len(faucets))
	for {
		// Pick a random mining node
		index := rand.Intn(len(faucets))
		backend := nodes[index%len(nodes)]

		// Create a self transaction and inject into the pool
		tx, err := types.SignTx(types.NewTransaction(nonces[index], crypto.PubkeyToAddress(faucets[index].PublicKey), new(big.Int), 21000, big.NewInt(100000000000+rand.Int63n(65536)), nil), types.HomesteadSigner{}, faucets[index])
		if err != nil {
			panic(err)
		}
		if err := backend.TxPool().AddLocal(tx); err != nil {
			panic(err)
		}
		nonces[index]++

		// Wait if we're too saturated
		if pend, _ := backend.TxPool().Stats(); pend > 2048 {
			time.Sleep(100 * time.Millisecond)
		}
	}
}

// makeGenesis creates a custom Ethash genesis block based on some pre-defined
// faucet accounts.
func makeGenesis(faucets []*ecdsa.PrivateKey, sealers [32]*vbls.PublicKey) *core.Genesis {
	genesis := core.DefaultRopstenGenesisBlock()
	genesis.Difficulty = params.MinimumDifficulty
	genesis.GasLimit = 25000000

	genesis.Config.ChainID = big.NewInt(18)
	genesis.Config.EIP150Hash = common.Hash{}
	genesis.Config.SilesiaBlock = big.NewInt(0)

	timeNow := time.Now()
	genesisEpochStart := uint64(timeNow.Unix())

	consensusInfos := [4]*params.MinimalEpochConsensusInfo{}

	for index, consensusInfo := range consensusInfos {
		consensusInfo = &params.MinimalEpochConsensusInfo{
			Epoch:            uint64(index),
			ValidatorsList:   sealers,
			EpochTimeStart:   genesisEpochStart,
			SlotTimeDuration: 6,
		}

		consensusInfos[index] = consensusInfo

		if index < 1 {
			continue
		}

		consensusInfo.EpochTimeStart = genesisEpochStart + uint64(index*6)
	}

	pandoraConfig := params.PandoraConfig{
		ConsensusInfo: consensusInfos[:],
	}

	genesis.Alloc = core.GenesisAlloc{}
	for _, faucet := range faucets {
		genesis.Alloc[crypto.PubkeyToAddress(faucet.PublicKey)] = core.GenesisAccount{
			Balance: new(big.Int).Exp(big.NewInt(2), big.NewInt(128), nil),
		}
	}

	genesis.Config.PandoraConfig = &pandoraConfig

	return genesis
}

func makeMiner(
	genesis *core.Genesis,
	notify []string,
	validators [32]*vbls.PublicKey,
) (*node.Node, *eth.Ethereum, error) {
	// Define the basic configurations for the Ethereum node
	datadir, _ := ioutil.TempDir("", "")

	config := &node.Config{
		Name:    "geth",
		Version: params.Version,
		DataDir: datadir,
		P2P: p2p.Config{
			ListenAddr:  "0.0.0.0:0",
			NoDiscovery: true,
			MaxPeers:    25,
		},
		UseLightweightKDF: true,
	}
	// Create the node and configure a full Ethereum node on it
	stack, err := node.New(config)
	if err != nil {
		return nil, nil, err
	}

	minimalConsensusInfo := ethash.NewMinimalConsensusInfo(0).(*ethash.MinimalEpochConsensusInfo)
	minimalConsensusInfo.AssignEpochStartFromGenesis(time.Unix(
		int64(genesis.Config.PandoraConfig.ConsensusInfo[0].EpochTimeStart),
		0,
	))
	minimalConsensusInfo.AssignValidators(validators)
	ethConfig := &ethconfig.Config{
		Genesis:         genesis,
		NetworkId:       genesis.Config.ChainID.Uint64(),
		SyncMode:        downloader.FullSync,
		DatabaseCache:   256,
		DatabaseHandles: 256,
		TxPool:          core.DefaultTxPoolConfig,
		Ethash:          ethash.Config{PowMode: ethash.ModePandora, Log: log.Root()},
		Miner: miner.Config{
			GasFloor: genesis.GasLimit * 9 / 10,
			GasCeil:  genesis.GasLimit * 11 / 10,
			GasPrice: big.NewInt(1),
			Recommit: time.Second,
		},
	}

	ethBackend, err := eth.New(stack, ethConfig)

	if err != nil {
		return nil, nil, err
	}

	err = stack.Start()
	return stack, ethBackend, err
}

func makeSealerServer(
	genesis *core.Genesis,
	validators [32]*vbls.PublicKey,
	privateKeys [32]*vbls.PrivateKey,
) (url string, err error) {
	vanguardServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		blob, err := ioutil.ReadAll(req.Body)
		if err != nil {
			panic(fmt.Sprintf("failed to read miner notification: %v", err))
		}

		var work [4]string

		if err := json.Unmarshal(blob, &work); err != nil {
			panic(fmt.Sprintf("failed to unmarshal miner notification: %v", err))
		}

		rlpHexHeader := work[2]
		rlpHeader, err := hexutil.Decode(rlpHexHeader)

		if nil != err {
			panic(fmt.Sprintf("failed to encode hex header %v", rlpHexHeader))
		}

		fmt.Printf("\n\n\n\n Elooooo Hex header \n, %s", rlpHeader)
	}))

	url = vanguardServer.URL

	return
}

func makeRemoteSealer(
	stack *node.Node,
	validators [32]*vbls.PublicKey,
	privateKeys [32]*vbls.PrivateKey,
) {
	rpcClient, err := stack.Attach()

	if nil != err {
		panic(fmt.Sprintf("could not attach: %s", err.Error()))
	}

	timeout := time.Duration(6 * time.Second)

	go func() {
		ticker := time.NewTicker(timeout)
		defer ticker.Stop()
		for {
			<-ticker.C
			fmt.Printf("tick")
			var workInfo [4]string
			err = rpcClient.Call(&workInfo, "eth_getWork")

			if nil != err {
				fmt.Printf("\n rpcClient got error: %v", err.Error())
			}

			fmt.Printf("\n ETH GET WORK: %v", &workInfo)
		}
	}()

}
