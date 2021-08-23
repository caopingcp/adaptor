package main

import (
	"adaptor/client"
	"adaptor/types"
	"context"
	"log"
	"net/http"
	"time"

	"github.com/33cn/chain33/common"
	ty "github.com/33cn/chain33/types"
	"github.com/ant0ine/go-json-rest/rest"
)

func main() {
	conf := types.ReadConf("conf.yaml")
	log.Println("limiter:", conf.Limiter)
	client.InitLimiter(conf.Limiter)
	client.InitPrivKey(conf.Privkey)
	msgChan := make(chan string, conf.Limiter)
	cli := client.NewClient(conf.JsonRpc, conf.GRpc,conf.Async)

	api := rest.NewApi()
	api.Use(rest.DefaultDevStack...)
	router, err := rest.MakeRouter(
		rest.Get("/getBlockHeight", cli.GetBlockHeight),
		rest.Get("/getNodeCount", cli.GetNodeCount),
		rest.Get("/getTxCountAccepted", cli.GetTxAccepted),
		rest.Get("/getTxCountConfirmed", cli.GetTxConfirmed),
		rest.Post("/getTxInfo", cli.GetTxInfo),
		rest.Post("/getBalance", cli.GetBalance),
		rest.Post("/getBlockInfo", cli.GetBlockInfo),
		rest.Post("/createTx", cli.CreateTx),
		rest.Post("/sendTx", cli.SendTx),
	)
	if err != nil {
		log.Fatal(err)
	}
	if conf.Async{
		//异步转发失败消息补偿机制
		go func() {
			for {
				client.UnSendedTxMap.Range(
					func(key, value interface{}) bool {
						tx := value.(ty.Transaction)
						grcClient, err := cli.GetClient()
						if err == nil {
							go func(c ty.Chain33Client,tx *ty.Transaction) {
								reply, err :=c.SendTransaction(context.Background(), tx)
								if err == nil && reply.IsOk {
									msgChan <- common.ToHex(tx.Hash())
								}
							}(grcClient,&tx)

						}
						//go func() {
						//	reply, err := cli.GetGrpcClient().SendTransaction(context.Background(), &tx)
						//	if err == nil && reply.IsOk {
						//		msgChan <- common.ToHex(tx.Hash())
						//	}
						//	limiter.Release()
						//}()
						return true
					})
				time.Sleep(100 * time.Millisecond)
			}
		}()
		go func() {
			for hash := range msgChan {
				client.UnSendedTxMap.Delete(hash)
			}
		}()
	}
	api.SetApp(router)
	log.Fatal(http.ListenAndServe(":8999", api.MakeHandler()))

}
