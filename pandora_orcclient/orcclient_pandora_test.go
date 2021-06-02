/*
* Generated: 5/31/21
* This file is generated to support Lukso pandora module.
* Purpose:
 */
package pandora_orcclient

import (
	"context"
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rpc"
)

const (
	Pending Status = iota
	Verified
	Invalid
)

const (
	MockedHashInvalid = "0xc9a190eb52c18df5ffcb1d817214ecb08f025f8583805cd12064d30e3f9bd9d5"
	MockedHashPending = "0xa99c69a301564970956edd897ff0590f4c0f1031daa464ded655af65ad0906df"
)

// In process rpc server related test

// TestOrcClient_GetConfirmedPanBlockHashes tests in process pandora block confirmation
func TestOrcClient_GetConfirmedPanBlockHashes(t *testing.T) {
	orchestrator := DialInProcRPCClient()
	var request []*BlockHash
	request = append(request, &BlockHash{Hash: common.HexToHash(MockedHashInvalid), Slot: 1}, &BlockHash{Hash: common.HexToHash(MockedHashPending), Slot: 2})
	response, err := orchestrator.GetConfirmedPanBlockHashes(context.Background(), request)
	if err != nil {
		t.Fatalf("error found while getting confirmed pending block hashes")
	}
	t.Log("received block confirmations from orchestrator")
	for _, hash := range response {
		t.Logf("received %v", hash)
	}
}

// DialInProcRPCClient creates an in process RPC mock client
func DialInProcRPCClient() *OrcClient {
	server := NewMockOrchestratorServer()
	rpcClient := rpc.DialInProc(server)
	return NewOrcClient(rpcClient)
}

// testing mock orchestrator service
type mockOrchestratorService struct{}

// NewMockOrchestratorServer method mock pandora chain apis
func NewMockOrchestratorServer() *rpc.Server {
	server := rpc.NewServer()
	if err := server.RegisterName("orc", new(mockOrchestratorService)); err != nil {
		panic(err)
	}
	return server
}

// ConfirmPanBlockHashes confirms block confirmation
func (OrcClient *mockOrchestratorService) ConfirmPanBlockHashes(ctx context.Context,
	request []*BlockHash) (response []*BlockStatus, err error) {

	if len(request) < 1 {
		err = fmt.Errorf("request has empty slice")

		return
	}
	response = make([]*BlockStatus, 0)

	for _, blockRequest := range request {
		status := Verified

		if MockedHashInvalid == blockRequest.Hash.String() {
			status = Invalid
		}

		if MockedHashPending == blockRequest.Hash.String() {
			status = Pending
		}

		response = append(response, &BlockStatus{
			BlockHash: BlockHash{
				Slot: blockRequest.Slot,
				Hash: blockRequest.Hash,
			},
			Status: status,
		})
	}

	return
}
