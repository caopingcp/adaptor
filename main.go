package main

import (
	"adaptor/client"
	"adaptor/types"
	"github.com/ant0ine/go-json-rest/rest"
	"log"
	"net/http"
)

func main() {
	conf := types.ReadConf("conf.yaml")
	log.Println("limiter:", conf.Limiter)
	cli := client.NewClient(conf)

	api := rest.NewApi()
	api.Use(rest.DefaultDevStack...)
	router, err := rest.MakeRouter(
		rest.Get("/getBlockHeight", cli.GetBlockHeight),
		rest.Get("/getNodeCount", cli.GetNodeCount),
		rest.Get("/getTxCountAccepted", cli.GetTxAccepted),
		rest.Get("/getTxCountConfirmed", cli.GetTxConfirmed),
		rest.Post("/getTxInfo", cli.GetTxInfo),
		rest.Post("/getBalance", cli.GetBalance),
		rest.Post("/getPayload", cli.GetPayload),
		rest.Post("/getGreet", cli.GetGreet),
		rest.Post("/getBlockInfo", cli.GetBlockInfo),
		rest.Post("/createTx", cli.CreateTx),
		rest.Post("/sendTx", cli.SendTx),
	)
	if err != nil {
		log.Fatal(err)
	}
	if conf.Async {
		//异步发送
		go cli.SendTransaction()
		go cli.RetrySendTransaction()
	}
	api.SetApp(router)
	log.Fatal(http.ListenAndServe(":8999", api.MakeHandler()))

}
