/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package main

import (
	"flag"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/hyperledger/fabric/common/crypto"
	"github.com/hyperledger/fabric/common/localmsp"
	"github.com/hyperledger/fabric/common/tools/configtxgen/provisional"
	mspmgmt "github.com/hyperledger/fabric/msp/mgmt"
	"github.com/hyperledger/fabric/orderer/common/localconfig"
	cb "github.com/hyperledger/fabric/protos/common"
	ab "github.com/hyperledger/fabric/protos/orderer"
	"github.com/hyperledger/fabric/protos/utils"

	"golang.org/x/net/context"
	"google.golang.org/grpc"
)

type broadcastClient struct {
	client  ab.AtomicBroadcast_BroadcastClient
	signer  crypto.LocalSigner
	chainID string
}

// newBroadcastClient creates a simple instance of the broadcastClient interface
func newBroadcastClient(client ab.AtomicBroadcast_BroadcastClient, chainID string, signer crypto.LocalSigner) *broadcastClient {
	return &broadcastClient{client: client, chainID: chainID, signer: signer}
}

func (s *broadcastClient) broadcast(transaction []byte) error {
	env, err := utils.CreateSignedEnvelope(cb.HeaderType_MESSAGE, s.chainID, s.signer, &cb.Envelope{Signature: transaction}, 0, 0)
	if err != nil {
		panic(err)
	}
	time.Sleep(time.Second)
	return s.client.Send(env)
}

func (s *broadcastClient) getAck() error {
	msg, err := s.client.Recv()
	if err != nil {
		return err
	}
	if msg.Status != cb.Status_SUCCESS {
		return fmt.Errorf("Got unexpected status: %v - %s", msg.Status, msg.Info)
	}
	return nil
}

func main() {
	config := config.Load()

	// Load local MSP
	err := mspmgmt.LoadLocalMsp(config.General.LocalMSPDir, config.General.BCCSP, config.General.LocalMSPID)
	if err != nil { // Handle errors reading the config file
		fmt.Println("Failed to initialize local MSP:", err)
		os.Exit(0)
	}

	signer := localmsp.NewSigner()

	var chainID string
	var serverAddr string
	var messages uint64
	var goroutines uint64
	var msgSize uint64

	flag.StringVar(&serverAddr, "server", fmt.Sprintf("%s:%d", config.General.ListenAddress, config.General.ListenPort), "The RPC server to connect to.")
	flag.StringVar(&chainID, "chainID", provisional.TestChainID, "The chain ID to broadcast to.")
	flag.Uint64Var(&messages, "messages", 1, "The number of messages to broadcast.")
	flag.Uint64Var(&goroutines, "goroutines", 1, "The number of concurrent go routines to broadcast the messages on")
	flag.Uint64Var(&msgSize, "size", 1024, "The size in bytes of the data section for the payload")
	flag.Parse()

	conn, err := grpc.Dial(serverAddr, grpc.WithInsecure())
	defer func() {
		_ = conn.Close()
	}()
	if err != nil {
		fmt.Println("Error connecting:", err)
		return
	}

	msgsPerGo := messages / goroutines
	roundMsgs := msgsPerGo * goroutines
	if roundMsgs != messages {
		fmt.Println("Rounding messages to", roundMsgs)
	}

	msgData := make([]byte, msgSize)

	var wg sync.WaitGroup
	wg.Add(int(goroutines))
	for i := uint64(0); i < goroutines; i++ {
		go func(i uint64) {
			client, err := ab.NewAtomicBroadcastClient(conn).Broadcast(context.TODO())
			time.Sleep(10 * time.Second)
			if err != nil {
				fmt.Println("Error connecting:", err)
				return
			}

			s := newBroadcastClient(client, chainID, signer)
			done := make(chan (struct{}))
			go func() {
				for i := uint64(0); i < msgsPerGo; i++ {
					err = s.getAck()
				}
				if err != nil {
					fmt.Printf("\nError: %v\n", err)
				}
				close(done)
			}()
			for i := uint64(0); i < msgsPerGo; i++ {
				if err := s.broadcast(msgData); err != nil {
					panic(err)
				}
			}
			<-done
			wg.Done()
			client.CloseSend()
			fmt.Println("Go routine", i, "exiting")
		}(i)
	}

	wg.Wait()
}
