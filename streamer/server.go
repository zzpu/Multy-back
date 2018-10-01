package streamer

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/KristinaEtc/slf"
	_ "github.com/KristinaEtc/slflog"
	"github.com/Multy-io/Multy-BTC-node-service/btc"
	pb "github.com/Multy-io/Multy-BTC-node-service/node-streamer"
	"github.com/Multy-io/Multy-back/store"
	"github.com/blockcypher/gobcy"
	"github.com/parnurzeal/gorequest"
)

var log = slf.WithContext("streamer")

// Server implements streamer interface and is a gRPC server
type Server struct {
	UsersData *sync.Map
	// UsersData sync.Map
	BtcAPI *gobcy.API
	BtcCli *btc.Client
	M      *sync.Mutex
	Info   *store.ServiceInfo
}

func (s *Server) ServiceInfo(c context.Context, in *pb.Empty) (*pb.ServiceVersion, error) {
	return &pb.ServiceVersion{
		Branch:    s.Info.Branch,
		Commit:    s.Info.Commit,
		Buildtime: s.Info.Buildtime,
		Lasttag:   "",
	}, nil
}

// EventInitialAdd us used to add initial pairs of watch addresses
func (s *Server) EventInitialAdd(c context.Context, ud *pb.UsersData) (*pb.ReplyInfo, error) {
	log.Debugf("EventInitialAdd - %v", ud.Map)

	udMap := sync.Map{}

	for addr, ex := range ud.GetMap() {
		udMap.Store(addr, store.AddressExtended{
			UserID:       ex.GetUserID(),
			WalletIndex:  int(ex.GetWalletIndex()),
			AddressIndex: int(ex.GetAddressIndex()),
		})
	}

	*s.UsersData = udMap

	return &pb.ReplyInfo{
		Message: "ok",
	}, nil
}

// EventAddNewAddress us used to add new watch address to existing pairs
func (s *Server) EventAddNewAddress(c context.Context, wa *pb.WatchAddress) (*pb.ReplyInfo, error) {
	newMap := *s.UsersData
	// if newMap == nil {
	// 	newMap = map[string]store.AddressExtended{}
	// }
	//TODO: binded address fix
	_, ok := newMap.Load(wa.Address)
	if ok {
		return &pb.ReplyInfo{
			Message: "err: Address already binded",
		}, nil
	}
	newMap.Store(wa.Address, store.AddressExtended{
		UserID:       wa.UserID,
		WalletIndex:  int(wa.WalletIndex),
		AddressIndex: int(wa.AddressIndex),
	})

	*s.UsersData = newMap

	return &pb.ReplyInfo{
		Message: "ok",
	}, nil

}

func (s *Server) EventGetBlockHeight(ctx context.Context, in *pb.Empty) (*pb.BlockHeight, error) {
	h, err := s.BtcCli.RpcClient.GetBlockCount()
	if err != nil {
		return &pb.BlockHeight{}, err
	}
	return &pb.BlockHeight{
		Height: h,
	}, nil
}

// EventAddNewAddress us used to add new watch address to existing pairs
func (s *Server) EventGetAllMempool(_ *pb.Empty, stream pb.NodeCommuunications_EventGetAllMempoolServer) error {
	mp, err := s.BtcCli.GetAllMempool()
	if err != nil {
		return err
	}

	for _, rec := range mp {
		stream.Send(&pb.MempoolRecord{
			Category: int32(rec.Category),
			HashTX:   rec.HashTX,
		})
	}
	return nil
}

func (s *Server) SyncState(ctx context.Context, in *pb.BlockHeight) (*pb.ReplyInfo, error) {
	// s.BtcCli.RpcClient.GetTxOut()
	// var blocks []*chainhash.Hash
	currentH, err := s.BtcCli.RpcClient.GetBlockCount()
	if err != nil {
		log.Errorf("s.BtcCli.RpcClient.GetBlockCount: %v ", err.Error())
	}

	log.Debugf("currentH %v lastH %v", currentH, in.GetHeight())

	// for lastH := int64(in.GetHeight()); lastH < currentH; lastH++ {
	// 	hash, err := s.BtcCli.RpcClient.GetBlockHash(lastH)
	// 	if err != nil {
	// 		log.Errorf("s.BtcCli.RpcClient.GetBlockHash: %v", err.Error())
	// 	}
	// 	log.Debugf("currentH %v lastH %v cur %v", currentH, lastH, int64(in.GetHeight()))
	// 	fmt.Println("hash", hash.String())
	// 	go s.BtcCli.BlockTransactions(hash)
	// }

	// for i := in.GetHeight(); i <= currentH; i++ {
	// hash, err := s.BtcCli.RpcClient.GetBlockHash(i)
	// if err != nil {
	// 	log.Errorf("s.BtcCli.RpcClient.GetBlockHash: %v", err.Error())
	// }
	// go s.BtcCli.BlockTransactions(hash)
	// }

	return &pb.ReplyInfo{
		Message: "ok",
	}, nil
}

func (s *Server) EventResyncAddress(c context.Context, address *pb.AddressToResync) (*pb.ReplyInfo, error) {
	log.Debugf("EventResyncAddress")
	allResync := []store.ResyncTx{}
	delFromResyncQ := ""
	requestTimes := 0
	if s.BtcAPI.Chain == "test3" {
		addrInfo, err := s.BtcAPI.GetAddrFull(address.Address, map[string]string{"limit": "50"})
		if err != nil {
			return nil, fmt.Errorf("EventResyncAddress: s.BtcAPI.GetAddrFull : %s", err.Error())
		}

		log.Debugf("EventResyncAddress:s.BtcAPI.GetAddrFull")
		if addrInfo.FinalNumTX > 50 {
			requestTimes = int(float64(addrInfo.FinalNumTX) / 50.0)
		}

		if addrInfo.FinalNumTX == 0 {
			delFromResyncQ = address.Address
		}

		for _, tx := range addrInfo.TXs {
			allResync = append(allResync, store.ResyncTx{
				Hash:        tx.Hash,
				BlockHeight: tx.BlockHeight,
			})
		}
		for i := 0; i < requestTimes; i++ {
			addrInfo, err := s.BtcAPI.GetAddrFull(address.Address, map[string]string{"limit": "50", "before": strconv.Itoa(allResync[len(allResync)-1].BlockHeight)})
			if err != nil {
				return nil, fmt.Errorf("[ERR] EventResyncAddress: s.BtcAPI.GetAddrFull : %s", err.Error())
			}
			for _, tx := range addrInfo.TXs {
				allResync = append(allResync, store.ResyncTx{
					Hash:        tx.Hash,
					BlockHeight: tx.BlockHeight,
				})
			}
		}

	}

	if s.BtcAPI.Chain == "main" {

		url := "https://chain.api.btc.com/v3/address/" + address.Address + "/tx?page=1"
		dbl := sync.Map{}

		request := gorequest.New()
		resp, _, errs := request.Get(url).Retry(10, 10*time.Second, http.StatusForbidden, http.StatusBadRequest, http.StatusInternalServerError).End()
		if len(errs) > 0 {
			log.Errorf("EventResyncAddress:request.Get: %v", errs)
		}
		respBody, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			log.Errorf("EventResyncAddress:ioutil.ReadAll: %v", err.Error())
		}

		reTx := store.BtcComResp{}
		if err := json.Unmarshal(respBody, &reTx); err != nil {
			log.Errorf("EventResyncAddress:json.Unmarshal: %v", err.Error())
		}

		if reTx.Data.TotalCount > 50 {
			requestTimes = int(float64(reTx.Data.TotalCount)/50.0) + 2
		}

		if reTx.Data.TotalCount < 50 {
			for _, tx := range reTx.Data.List {
				_, ok := dbl.LoadOrStore(tx, true)
				if !ok {
					allResync = append(allResync, store.ResyncTx{
						Hash:        tx.Hash,
						BlockHeight: tx.BlockHeight,
					})
				}
			}
		}

		if reTx.Data.TotalCount > 50 {
			for index := 1; index < requestTimes; index++ {
				url := "https://chain.api.btc.com/v3/address/" + address.Address + "/tx?page=" + strconv.Itoa(index)
				resp, _, errs := request.Get(url).Retry(2, 2*time.Second, http.StatusForbidden, http.StatusBadRequest, http.StatusInternalServerError).End()
				if len(errs) > 0 {
					log.Errorf("EventResyncAddress:request.Get: %v", errs)
				}

				respBody, err := ioutil.ReadAll(resp.Body)
				if err != nil {
					log.Errorf("EventResyncAddress:ioutil.ReadAll: %v", err.Error())
				}

				reTx := store.BtcComResp{}
				if err := json.Unmarshal(respBody, &reTx); err != nil {
					log.Errorf("EventResyncAddress:json.Unmarshal: %v", err.Error())
				}

				for _, tx := range reTx.Data.List {
					_, ok := dbl.LoadOrStore(tx, true)
					if !ok {
						allResync = append(allResync, store.ResyncTx{
							Hash:        tx.Hash,
							BlockHeight: tx.BlockHeight,
						})
					}
				}
			}
		}

		if reTx.Data.TotalCount == 0 {
			delFromResyncQ = address.Address
		}

	}
	reverseResyncTx(allResync)
	log.Debugf("EventResyncAddress:reverseResyncTx %d", len(allResync))

	s.BtcCli.ResyncAddresses(allResync, address, delFromResyncQ)

	return &pb.ReplyInfo{
		Message: "ok",
	}, nil
}

func (s *Server) EventSendRawTx(c context.Context, tx *pb.RawTx) (*pb.ReplyInfo, error) {
	hash, err := s.BtcCli.RpcClient.SendCyberRawTransaction(tx.Transaction, true)
	if err != nil {
		return &pb.ReplyInfo{
			Message: "err: wrong raw tx",
		}, fmt.Errorf("err: wrong raw tx %s", err.Error())

	}

	return &pb.ReplyInfo{
		Message: hash.String(),
	}, nil

}

func (s *Server) EventDeleteMempool(_ *pb.Empty, stream pb.NodeCommuunications_EventDeleteMempoolServer) error {
	for del := range s.BtcCli.DeleteMempool {
		err := stream.Send(&del)
		if err != nil {
			//HACK:
			log.Errorf("Delete mempool record %s", err.Error())
			i := 0
			for {
				err := stream.Send(&del)
				if err != nil {
					i++
					log.Errorf("Delete mempool record resend attempt(%d) err = %s", i, err.Error())
					time.Sleep(time.Second * 2)
					if i == 3 {
						break
					}
				} else {
					log.Debugf("Delete mempool record resend success on %d attempt", i)
					break
				}
			}
		}
	}
	return nil
}

func (s *Server) EventAddMempoolRecord(_ *pb.Empty, stream pb.NodeCommuunications_EventAddMempoolRecordServer) error {
	for add := range s.BtcCli.AddToMempool {
		err := stream.Send(&add)
		if err != nil {
			//HACK:
			log.Errorf("Add mempool record %s", err.Error())
			i := 0
			for {
				err := stream.Send(&add)
				if err != nil {
					i++
					log.Errorf("Add mempool record resend attempt(%d) err = %s", i, err.Error())
					time.Sleep(time.Second * 2)
					if i == 3 {
						break
					}
				} else {
					log.Debugf("Add mempool record resend success on %d attempt", i)
					break
				}
			}
		}
	}
	return nil
}

func (s *Server) EventDeleteSpendableOut(_ *pb.Empty, stream pb.NodeCommuunications_EventDeleteSpendableOutServer) error {
	for delSp := range s.BtcCli.DelSpOut {
		log.Infof("Delete spendable out %v", delSp.String())
		err := stream.Send(&delSp)
		if err != nil {
			//HACK:
			log.Errorf("Delete spendable out %s", err.Error())
			i := 0
			for {
				err := stream.Send(&delSp)
				if err != nil {
					i++
					log.Errorf("Delete spendable out resend attempt(%d) err = %s", i, err.Error())
					time.Sleep(time.Second * 2)
					if i == 3 {
						break
					}
				} else {
					log.Debugf("EventDeleteSpendableOut history resend success on %d attempt", i)
					break
				}
			}
		}

	}
	return nil
}
func (s *Server) EventAddSpendableOut(_ *pb.Empty, stream pb.NodeCommuunications_EventAddSpendableOutServer) error {

	for addSp := range s.BtcCli.AddSpOut {
		log.Infof("Add spendable out %v", addSp.String())
		err := stream.Send(&addSp)
		if err != nil {
			//HACK:
			log.Errorf("Add spendable out %s", err.Error())
			i := 0
			for {
				err := stream.Send(&addSp)
				if err != nil {
					i++
					log.Errorf("Add spendable out resend attempt(%d) err = %s", i, err.Error())
					time.Sleep(time.Second * 2)
				} else {
					log.Debugf("Add spendable out resend success on %d attempt", i)
					break
				}
			}

		}

	}

	return nil
}
func (s *Server) NewTx(_ *pb.Empty, stream pb.NodeCommuunications_NewTxServer) error {

	for tx := range s.BtcCli.TransactionsCh {
		log.Infof("NewTx history - %v", tx.String())
		err := stream.Send(&tx)
		if err != nil {
			//HACK:
			log.Errorf("NewTx history %s", err.Error())
			i := 0
			for {
				err := stream.Send(&tx)
				if err != nil {
					i++
					log.Errorf("NewTx history resend attempt(%d) err = %s", i, err.Error())
					time.Sleep(time.Second * 2)
				} else {
					log.Debugf("NewTx history resend success on %d attempt", i)
					break
				}
			}

		}
	}
	return nil
}

func (s *Server) EventNewBlock(_ *pb.Empty, stream pb.NodeCommuunications_EventNewBlockServer) error {
	for h := range s.BtcCli.Block {
		log.Infof("New block height - %v", h.GetHeight())
		err := stream.Send(&h)
		if err != nil {
			//HACK:
			log.Errorf("New block %s", err.Error())
			i := 0
			for {
				err := stream.Send(&h)
				if err != nil {
					i++
					log.Errorf("New block resend attempt(%d) err = %s", i, err.Error())
					time.Sleep(time.Second * 2)
				} else {
					log.Debugf("New block resend success on %d attempt", i)
					break
				}
			}

		}
	}
	return nil
}

func (s *Server) ResyncAddress(_ *pb.Empty, stream pb.NodeCommuunications_ResyncAddressServer) error {
	for res := range s.BtcCli.ResyncCh {
		log.Infof("Resync address - %v", res.String())
		err := stream.Send(&res)
		if err != nil {
			//HACK:
			log.Errorf("Resync address %s", err.Error())
			i := 0
			for {
				err := stream.Send(&res)
				if err != nil {
					i++
					log.Errorf("Resync address resend attempt(%d) err = %s", i, err.Error())
					time.Sleep(time.Second * 2)
				} else {
					log.Debugf("Resync address resend success on %d attempt", i)
					break
				}
			}

		}
	}
	return nil
}
