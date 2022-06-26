# TCPPool
TCP连接池  
  - 达到最小空闲连接数(MinConn)自动扩张到最大空闲连接数(MaxConn)  
  - 超出最大空闲时间则自动丢弃  
  - 可设置生成TCP连接的速度，避免被服务器误判为攻击流量  
  - 协程安全，可多协程共用一个连接池  
  - 可自定义Check方法来检测连接的有效性  
  - 连接池无可用空闲连接时，调用自定义Dial方法获取新的连接  
  - 支持将用完的Conn放回连接池中复用  

## 用法示例：
```go
//创建一个连接池
pool := TCPPool.Pool{
	MaxConn: 30, //最大空闲连接数
	MinConn: 2, //最小空闲连接数
	DialSpeed: 10, //生成连接速度(每秒生成N个连接)
	IdleSec: 60, //连接最大空闲时间，超过该时间连接则将失效（如果不设置则永不失效）
	Dial: func() (net.Conn, error) {
		return net.DialTimeout("tcp", "119.29.29.29:80", time.Minute>>2)
	},
	Check: nil, //检查连接是否有效的方法（如果不设置则不检测）
}

//启动连接池
pool.Start()

//从连接池中获取一个连接
conn, err := pool.Get()
if err != nil {
	//handle error
}

//释放连接池中的所有连接
pool.Release()

```