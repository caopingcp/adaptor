package client

import (
	. "adaptor/common"
	. "adaptor/types"
	"context"
	"fmt"
	"github.com/33cn/chain33/common"
	"github.com/33cn/chain33/common/address"
	"github.com/33cn/chain33/common/crypto"
	"github.com/33cn/chain33/types"
	"github.com/ant0ine/go-json-rest/rest"
	"github.com/shimingyah/pool"
	"io/ioutil"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const fee = 1e6
const letterBytes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ123456789-=_+=/<>!@#$%^&"

var (
	r     *rand.Rand
	count uint64
	execAddr    = address.ExecAddress("user.write")
	writeTxPool = sync.Pool{
		New: func() interface{} {
			tx := &types.Transaction{Execer: []byte("user.write")}
			return tx
		},
	}
	coinsExecAddr  = address.ExecAddress("coins")
	transferTxPool = sync.Pool{
		New: func() interface{} {
			tx := &types.Transaction{Execer: []byte("coins")}
			return tx
		},
	}
)

type Client struct {
	JrpcBalancer    []*JSONClient
	GrpcBalancer    []types.Chain33Client
	GrpcConnectPool []pool.Pool
	Async           bool
	//限流器
	Limiter *ChannelLimiter
	//私钥
	Priv crypto.PrivKey
	//缓存通道
	txChan chan *types.Transaction
	//retry tx chan
	retryTxChan chan *types.Transaction
	sync.RWMutex
}

func NewClient(conf *Config) *Client {
	jrpcBalancer := make([]*JSONClient, len(conf.JsonRpc))
	grpcBalancer := make([]types.Chain33Client, len(conf.GRpc))
	grpcConnectPool := make([]pool.Pool, len(conf.GRpc))
	for i, url := range conf.JsonRpc {
		jrpcBalancer[i] = NewJSONClient("", url)
	}
	for i, url := range conf.GRpc {
		gcli := types.NewChain33Client(newGrpcConn(url))
		grpcBalancer[i] = gcli
		p, err := pool.New(url, pool.DefaultOptions)
		if err != nil {
			panic(err)
		}
		grpcConnectPool[i] = p
	}
	txChan := make(chan *types.Transaction, conf.Limiter*10000)
	retryTxChan := make(chan *types.Transaction, conf.Limiter*10000)
	return &Client{JrpcBalancer: jrpcBalancer, GrpcBalancer: grpcBalancer, GrpcConnectPool: grpcConnectPool, Async: conf.Async, Limiter: NewChannelLimiter(conf.Limiter), Priv: getprivkey(conf.Privkey), txChan: txChan, retryTxChan: retryTxChan}
}

//处理txchan
func (c *Client) SendTransaction() {
	for tx := range c.txChan {
		if c.Limiter.Allow() {
			cli, err := c.GetClient()
			if err != nil {
				c.retryTxChan <- tx
				//释放令牌
				c.Limiter.Release()
				time.Sleep(100*time.Millisecond)
				continue
			}
			go func(tr *types.Transaction, grpcClient types.Chain33Client) {
				//释放令牌
				defer c.Limiter.Release()
				reply, _ := grpcClient.SendTransaction(context.Background(), tr)
				if !reply.IsOk {
					c.retryTxChan <- tr
				}
			}(tx, cli)
			continue
		}
		c.retryTxChan <- tx
		time.Sleep(100*time.Millisecond)
	}
}

//retry sendTx
func (c *Client) RetrySendTransaction() {
	for tx := range c.retryTxChan {
        c.txChan<-tx
	}
}

func (c *Client) GetGrpcClient() types.Chain33Client {
	c.RLock()
	grpcClient := c.GrpcBalancer[rand.Intn(len(c.GrpcBalancer))]
	c.RUnlock()
	return grpcClient
}

func (c *Client) GetJrpcClient() *JSONClient {
	c.RLock()
	jrcpClient := c.JrpcBalancer[rand.Intn(len(c.JrpcBalancer))]
	c.RUnlock()
	return jrcpClient
}

func (c *Client) GetClient() (types.Chain33Client, error) {
	c.RLock()
	p := c.GrpcConnectPool[rand.Intn(len(c.GrpcConnectPool))]
	c.RUnlock()
	conn, err := p.Get()
	if err != nil {
		return nil, err
	}
	return types.NewChain33Client(conn.Value()), nil
}

// 获取最新区块高度
func (c *Client) GetBlockHeight(w rest.ResponseWriter, r *rest.Request) {
	header, err := c.GetGrpcClient().GetLastHeader(context.Background(), &types.ReqNil{})
	if err != nil {
		rest.Error(w, err.Error(), http.StatusInternalServerError)
	}
	w.WriteJson(&ReplyHeight{Result: strconv.FormatInt(header.Height, 10)})
}

// 节点总数
func (c *Client) GetNodeCount(w rest.ResponseWriter, r *rest.Request) {
	peerList, err := c.GetGrpcClient().GetPeerInfo(context.Background(), &types.P2PGetPeerReq{})
	if err != nil {
		rest.Error(w, err.Error(), http.StatusInternalServerError)
	}
	w.WriteJson(&ReplyNodeCount{Result: strconv.FormatInt(int64(len(peerList.Peers)), 10)})
}

// 往链交易池推送交易总数
func (c *Client) GetTxAccepted(w rest.ResponseWriter, r *rest.Request) {
	w.WriteJson(&ReplyAcceptedTxCount{Result: strconv.FormatUint(atomic.LoadUint64(&count), 10)})
}

// 已经打包确认交易总数
func (c *Client) GetTxConfirmed(w rest.ResponseWriter, r *rest.Request) {
	header, err := c.GetGrpcClient().GetLastHeader(context.Background(), &types.ReqNil{})
	if err != nil {
		rest.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	txfee, err := c.GetJrpcClient().GetTotalTxCount(header.Height)
	if err != nil {
		rest.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteJson(&ReplyConfirmedTxCount{Result: strconv.FormatInt(txfee.TxCount, 10)})
}

// 获取交易信息
func (c *Client) GetTxInfo(w rest.ResponseWriter, r *rest.Request) {
	//TODO   测试工具接口暂时不可用
	tx, err := ioutil.ReadAll(r.Body)
	if err != nil {
		rest.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data, _ := common.FromHex(string(tx))
	var tr types.Transaction
	types.Decode(data, &tr)
	client, err := c.GetClient()
	if err != nil {
		rest.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	detail, err := client.QueryTransaction(context.Background(), &types.ReqHash{Hash: tr.Hash()})
	if err != nil {
		rest.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteJson(&ReplyTxHash{ID: common.ToHex(detail.Tx.Hash())})
}

// 获取存证信息
func (c *Client) GetWriteInfo(w rest.ResponseWriter, r *rest.Request) {
	//TODO   测试工具接口暂时不可用
	tx, err := ioutil.ReadAll(r.Body)
	if err != nil {
		rest.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data, _ := common.FromHex(string(tx))
	var tr types.Transaction
	types.Decode(data, &tr)
	client, err := c.GetClient()
	if err != nil {
		rest.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	detail, err := client.QueryTransaction(context.Background(), &types.ReqHash{Hash: tr.Hash()})
	if err != nil {
		rest.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteJson(common.ToHex(detail.Tx.Payload))
}

// 获取账户余额
func (c *Client) GetBalance(w rest.ResponseWriter, r *rest.Request) {
	//TODO 这个接口不可用
	tx, err := ioutil.ReadAll(r.Body)
	if err != nil {
		rest.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data, _ := common.FromHex(string(tx))
	var tr types.Transaction
	types.Decode(data, &tr)
	client, err := c.GetClient()
	if err != nil {
		rest.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	addr := tr.From()
	detail, err := client.GetBalance(context.Background(), &types.ReqBalance{Execer: "coins", Addresses: []string{addr}})
	if err != nil {
		rest.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteJson(detail)

}

// 获取payload
func (c *Client) GetPayload(w rest.ResponseWriter, r *rest.Request) {
	//TODO 这个接口不可用
	tx, err := ioutil.ReadAll(r.Body)
	if err != nil {
		rest.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data, _ := common.FromHex(string(tx))
	var tr types.Transaction
	types.Decode(data, &tr)
	client, err := c.GetClient()
	if err != nil {
		rest.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	detail, err := client.QueryTransaction(context.Background(), &types.ReqHash{Hash: tr.Hash()})
	if err != nil {
		rest.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteJson(string(detail.Tx.Payload))

}

//test 测试框架性能
func (c *Client) GetGreet(w rest.ResponseWriter, r *rest.Request) {
	//TODO 这个接口不可用
	tx, err := ioutil.ReadAll(r.Body)
	if err != nil {
		rest.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data, _ := common.FromHex(string(tx))
	var tr types.Transaction
	types.Decode(data, &tr)

	w.WriteJson("hello world")
}

// 获取区块信息
func (c *Client) GetBlockInfo(w rest.ResponseWriter, r *rest.Request) {
	var req RequestH
	if err := r.DecodeJsonPayload(&req); err != nil {
		rest.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	height, err := strconv.ParseInt(req.Height, 10, 64)
	//height, err := strconv.ParseInt(r.Request.FormValue("height"), 10, 64)
	fmt.Println("hegiht:", height)
	if err != nil {
		rest.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	blockInfo, err := c.GetJrpcClient().GetBlockByHeight(height)
	if err != nil {
		rest.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteJson(&BlockInfo{
		Height:  int(blockInfo.Head.Height),
		TxCount: int(blockInfo.Head.TxCount),
		Hash:    blockInfo.Head.Hash,
		PreHash: blockInfo.Head.ParentHash,
		//时间直接字符串化处理,时间戳19位
		CreateTime: strconv.FormatInt(blockInfo.Head.BlockTime*1e9, 10),
		TxHashList: blockInfo.TxHashes,
	})
}

// 构建交易，本地构建
func (c *Client) CreateTx(w rest.ResponseWriter, r *rest.Request) {
	var txType TxType
	if err := r.DecodeJsonPayload(&txType); err != nil {
		rest.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var tx *types.Transaction
	if txType.IsTransfer {
		tx = createTransferTx(c.Priv)
	} else {
		s, _ := strconv.ParseInt(txType.Size, 10, 64)
		tx = createWriteTx(int(s))
	}
	//w.WriteJson(common.ToHex(types.Encode(tx)))
	w.WriteJson(&ReplyTx{
		TxContent: common.ToHex(types.Encode(tx)),
	})
}

// 发送交易
func (c *Client) SendTx(w rest.ResponseWriter, r *rest.Request) {
	if c.Async {
		c.asyncSendTx(w, r)
		return
	}
	c.syncSendTx(w, r)
}

// 同步发送
func (c *Client) syncSendTx(w rest.ResponseWriter, r *rest.Request) {
	tx, err := ioutil.ReadAll(r.Body)
	if err != nil {
		rest.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data, _ := common.FromHex(string(tx))
	var tr types.Transaction
	types.Decode(data, &tr)
	grpcClient, err := c.GetClient()
	if err != nil {
		rest.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	reply, err := grpcClient.SendTransaction(context.Background(), &tr)
	if err != nil {
		rest.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if reply.IsOk {
		//计数器，区块链系统接收了多少条数据
		atomic.AddUint64(&count, 1)
		w.WriteJson(&ReplyTxHash{ID: common.ToHex(reply.Msg)})
	} else {
		rest.Error(w, fmt.Errorf("The service did not handle the request properly!").Error(), http.StatusInternalServerError)
		return
	}
}

// 异步发送
func (c *Client) asyncSendTx(w rest.ResponseWriter, r *rest.Request) {
	tx, err := ioutil.ReadAll(r.Body)
	if err != nil {
		rest.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data, _ := common.FromHex(string(tx))
	var tr types.Transaction
	types.Decode(data, &tr)
	//异步转发处理
	c.txChan <- &tr
	w.WriteJson(&ReplyTxHash{ID: common.ToHex(tr.Hash())})
	//计数器，区块链系统接收了多少条数据
	atomic.AddUint64(&count, 1)
}
//关闭客户端
func (c *Client)Close(){
	close(c.txChan)
	close(c.retryTxChan)
	c.Limiter.Close()
	for _,p :=range c.GrpcConnectPool{
		p.Close()
	}
}