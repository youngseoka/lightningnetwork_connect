package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	
	"strings"
	"time"
	

	"github.com/lightningnetwork/lnd/keychain"
	"github.com/lightningnetwork/lnd/lntypes"

	"github.com/lightningnetwork/lnd/lnrpc"
	"github.com/lightningnetwork/lnd/routing/route"
	"google.golang.org/grpc"

	
	"github.com/lightningnetwork/lnd/lnrpc/routerrpc"
	"github.com/lightningnetwork/lnd/lnrpc/signrpc"
	"github.com/urfave/cli"
)

var sendCommand = cli.Command{
	Name:      "send",
	Category:  "Send",
	ArgsUsage: "recipient_pubkey",
	Usage:     "Use lnd as a p2p messenger application.",
	Action:    actionDecorator(chat),
	Flags: []cli.Flag{
		cli.Uint64Flag{
			Name:  "amt_msat",
			Usage: "payment amount per chat message",
			Value: 1000,
		},
	},
}

var byteOrder = binary.BigEndian

const (
	tlvMsgRecord    = 34349334
	tlvSigRecord    = 34349337
	tlvSenderRecord = 34349339
	tlvTimeRecord   = 34349343

	// TODO: Reference lnd master constant when available.
	tlvKeySendRecord = 5482373484
)

type messageState uint8

const (
	statePending messageState = iota

	stateDelivered

	stateFailed
)

type chatLine struct {
	text      string
	sender    route.Vertex
	recipient *route.Vertex
	state     messageState
	fee       uint64
	timestamp time.Time
}

var (
	msgLines       []chatLine
	destination    *route.Vertex
	runningBalance map[route.Vertex]int64 = make(map[route.Vertex]int64)

	keyToAlias = make(map[route.Vertex]string)
	aliasToKey = make(map[string]route.Vertex)

	self route.Vertex

	Message string		//insert youngseok
)

func initAliasMaps(conn *grpc.ClientConn) error {
	client := lnrpc.NewLightningClient(conn)

	graph, err := client.DescribeGraph(
		context.Background(),
		&lnrpc.ChannelGraphRequest{},
	)
	if err != nil {
		return err
	}

	aliasCount := make(map[string]int)
	for _, node := range graph.Nodes {
		alias := node.Alias
		aliasCount[alias]++
	}

	for _, node := range graph.Nodes {
		alias := node.Alias

		key, err := route.NewVertexFromStr(node.PubKey)
		if err != nil {
			return err
		}

		if aliasCount[alias] > 1 {
			alias += "-" + node.PubKey[:6]
		}

		aliasToKey[alias] = key
		aliasToKey[key.String()] = key

		keyToAlias[key] = alias
	}

	info, err := client.GetInfo(context.Background(), &lnrpc.GetInfoRequest{})
	if err != nil {
		return err
	}
	
	self, err = route.NewVertexFromStr(info.IdentityPubkey)
	if err != nil {
		return err
	}

	return nil
}

func setDest(destStr string) {
	dest, err := route.NewVertexFromStr(destStr)
	if err == nil {
		destination = &dest
	}

	if dest, ok := aliasToKey[destStr]; ok {
		destination = &dest
	}
}

func setMessage(messStr []string){					//insert youngseok

	Message = strings.Join(messStr,"")

}

func chat(ctx *cli.Context) error {
	chatMsgAmt := int64(ctx.Uint64("amt_msat"))

	conn := getClientConn(ctx, false)
	defer conn.Close()

	err := initAliasMaps(conn)
	if err != nil {
		return err
	}

	if ctx.NArg() != 0 {
		destStr := ctx.Args().First()
		setDest(destStr)
		messStr := ctx.Args().Tail()
		setMessage(messStr)
		
	}

	mainRpc := lnrpc.NewLightningClient(conn)
	client := routerrpc.NewRouterClient(conn)
	signClient := signrpc.NewSignerClient(conn)

	req := &lnrpc.InvoiceSubscription{}
	rpcCtx := context.Background()
	stream, err := mainRpc.SubscribeInvoices(rpcCtx, req)
	if err != nil {
		return err
	}

	

	addMsg := func(line chatLine) int {
		msgLines = append(msgLines, line)
		return len(msgLines) - 1
	}

	go func() {
		returnErr := func(err error) {
		
		}

		for {
			invoice, err := stream.Recv()
			if err != nil {
				returnErr(err)
				return
			}

			if invoice.State != lnrpc.Invoice_SETTLED {
				continue
			}

			var customRecords map[uint64][]byte
			for _, htlc := range invoice.Htlcs {
				if htlc.State == lnrpc.InvoiceHTLCState_SETTLED {
					customRecords = htlc.CustomRecords
					break
				}
			}
			if customRecords == nil {
				continue
			}

			msg, ok := customRecords[tlvMsgRecord]
			if !ok {
				continue
			}

			signature, ok := customRecords[tlvSigRecord]
			if !ok {
				continue
			}

			timestampBytes, ok := customRecords[tlvTimeRecord]
			if !ok {
				continue
			}
			timestamp := time.Unix(
				0,
				int64(byteOrder.Uint64(timestampBytes)),
			)

			senderBytes, ok := customRecords[tlvSenderRecord]
			if !ok {
				continue
			}
			sender, err := route.NewVertexFromBytes(senderBytes)
			if err != nil {
				// Invalid sender pubkey
				continue
			}

			signData, err := getSignData(sender, self, timestampBytes, msg)
			if err != nil {
				returnErr(err)
				return
			}

			verifyResp, err := signClient.VerifyMessage(
				context.Background(),
				&signrpc.VerifyMessageReq{
					Msg:       signData,
					Signature: signature,
					Pubkey:    sender[:],
				})
			if err != nil {
				returnErr(err)
				return
			}

			if !verifyResp.Valid {
				continue
			}

			if destination == nil {
				destination = &sender
			}

			addMsg(chatLine{
				sender:    sender,
				text:      string(msg),
				timestamp: timestamp,
			})
			

			amt := invoice.AmtPaid
			runningBalance[*destination] += amt
		}
	}()

	

	sendMessage:= func() error {
		
		newMsg := Message

		

		if destination == nil {
			return nil
		}

		d := *destination
		msgIdx := addMsg(chatLine{
			sender:    self,
			text:      newMsg,
			recipient: &d,
		})

		

		payAmt := runningBalance[*destination]
		if payAmt < chatMsgAmt {
			payAmt = chatMsgAmt
		}
		if payAmt > 10*chatMsgAmt {
			payAmt = 10 * chatMsgAmt
		}

		var preimage lntypes.Preimage
		if _, err := rand.Read(preimage[:]); err != nil {
			return err
		}
		hash := preimage.Hash()

		// Message sending time stamp
		timestamp := time.Now().UnixNano()
		var timeBuffer [8]byte
		byteOrder.PutUint64(timeBuffer[:], uint64(timestamp))

		// Sign all data.
		signData, err := getSignData(
			self, *destination, timeBuffer[:], []byte(newMsg),
		)
		if err != nil {
			return err
		}

		signResp, err := signClient.SignMessage(context.Background(), &signrpc.SignMessageReq{
			Msg: signData,
			KeyLoc: &signrpc.KeyLocator{
				KeyFamily: int32(keychain.KeyFamilyNodeKey),
				KeyIndex:  0,
			},
		})
		if err != nil {
			return err
		}
		signature := signResp.Signature

		customRecords := map[uint64][]byte{
			tlvMsgRecord:     []byte(newMsg),
			tlvSenderRecord:  self[:],
			tlvTimeRecord:    timeBuffer[:],
			tlvSigRecord:     signature,
			tlvKeySendRecord: preimage[:],
		}

		req := routerrpc.SendPaymentRequest{
			PaymentHash:       hash[:],
			AmtMsat:           payAmt,
			FinalCltvDelta:    40,
			Dest:              destination[:],
			FeeLimitMsat:      chatMsgAmt * 10,
			TimeoutSeconds:    30,
			DestCustomRecords: customRecords,
		}

		
			
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stream, err := client.SendPayment(ctx, &req)
			if err != nil {
				
				return nil
			}

			for {
				status, err := stream.Recv()
				if err != nil {
					break
				}

				switch status.State {
				case routerrpc.PaymentState_SUCCEEDED:
					msgLines[msgIdx].fee = uint64(status.Route.TotalFeesMsat)
					runningBalance[*destination] -= payAmt
					msgLines[msgIdx].state = stateDelivered
					
					break

				case routerrpc.PaymentState_IN_FLIGHT:

				default:
					msgLines[msgIdx].state = stateFailed
					
					break
				}
			}
		

		return nil 
	}
	sendMessage()
	


	

	

	return nil
}






func getSignData(sender, recipient route.Vertex, timestamp []byte, msg []byte) ([]byte, error) {
	var signData bytes.Buffer

	// Write sender.
	if _, err := signData.Write(sender[:]); err != nil {
		return nil, err
	}

	// Write recipient.
	if _, err := signData.Write(recipient[:]); err != nil {
		return nil, err
	}

	// Write time.
	if _, err := signData.Write(timestamp); err != nil {
		return nil, err
	}

	// Write message.
	if _, err := signData.Write(msg); err != nil {
		return nil, err
	}

	return signData.Bytes(), nil
}
