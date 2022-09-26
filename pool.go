package TCPPool

import (
	"errors"
	"net"
	"sync"
	"time"
)

type idleConn struct {
	net.Conn
	createdTime time.Time
}

// Pool 连接池主要结构
type Pool struct {
	//最大空闲连接数
	MaxConn int
	//最小空闲连接数（达到最小空闲连接数时自动扩张到最大空闲连接数）
	MinConn int
	//生成连接速度(每秒生成N个连接)
	DialSpeed int
	//连接最大空闲时间，超过该时间连接则将失效（如果不设置则永不失效）
	IdleSec int64
	//生成连接的方法
	Dial func() (net.Conn, error)
	//检查连接是否有效的方法（如果不设置则不检测）
	Check func(net.Conn) error

	isRelease    bool
	idleTime     time.Duration
	openingConns int
	connsChannel chan *idleConn
	mutex        sync.Mutex
}

var (
	errorIsReleased error = errors.New("pool is released")
)

// Start 启动TCP连接池
func (pool *Pool) Start() {
	pool.isRelease = false
	if pool.MaxConn > 0 {
		pool.connsChannel = make(chan *idleConn, pool.MaxConn<<1)
		pool.idleTime = time.Duration(pool.IdleSec) * time.Second
		go pool.GenerateConnections()
	}
}

// Restart 重启TCP连接池，如果newPool不为nil则将newPool覆盖到当前pool
func (pool *Pool) Restart(newPool *Pool) {
	pool.mutex.Lock()
	for {
		select {
		case iConn := <-pool.connsChannel:
			iConn.Conn.Close()
			pool.openingConns--
		default:
			goto CLOSE_END
		}
	}
CLOSE_END:
	if newPool != nil {
		pool.MaxConn = newPool.MaxConn
		pool.MinConn = newPool.MinConn
		pool.DialSpeed = newPool.DialSpeed
		pool.IdleSec = newPool.IdleSec
		pool.Dial = newPool.Dial
		pool.Check = newPool.Check
	}
	pool.Start()
	pool.mutex.Unlock()
}

// GenerateConnections 生成多个TCP连接并加入连接池队列
func (pool *Pool) GenerateConnections() {
	var (
		conn net.Conn
	)

	pool.mutex.Lock()
	if pool.isRelease {
		pool.mutex.Unlock()
		return
	}
	dialSpeed := pool.DialSpeed
	openingConns := pool.openingConns
	maxConn := pool.MaxConn
	dialer := pool.Dial
	pool.mutex.Unlock()
	if dialSpeed > 0 {
		/* 按照设定的速度创建TCP连接 */
		for openingConns < maxConn {
			dialCount := dialSpeed
			if dialCount > maxConn-openingConns {
				dialCount = maxConn - openingConns
			}
			for ; dialCount > 0; dialCount-- {
				conn, _ = pool.dial(dialer)
				if !pool.Put(conn) {
					return
				}
				openingConns++
			}
			time.Sleep(time.Second)
		}
	} else {
		for openingConns < maxConn {
			conn, _ = pool.dial(dialer)
			if !pool.Put(conn) {
				return
			}
			openingConns++
		}
	}
}

// Get 从pool中取一个连接
func (pool *Pool) Get() (conn net.Conn, err error) {
	var iConn *idleConn

	pool.mutex.Lock()
	if pool.isRelease {
		pool.mutex.Unlock()
		return nil, errorIsReleased
	}
READ_CONN_CHANNEL:
	select {
	case iConn = <-pool.connsChannel:
		pool.openingConns--
		if pool.openingConns <= pool.MinConn { //连接池达到最小连接数，生成新的连接
			if pool.openingConns < 0 {
				pool.openingConns = 0
			}
			go pool.GenerateConnections()
		}
		if pool.idleTime > 0 && iConn.createdTime.Add(pool.idleTime).Before(time.Now()) { //超时则关闭连接重新读取新的连接
			iConn.Close()
			goto READ_CONN_CHANNEL
		}
		if pool.Check != nil && pool.Check(iConn) != nil { //检查连接是否有效
			iConn.Close()
			goto READ_CONN_CHANNEL
		}
		pool.mutex.Unlock()
		conn = iConn.Conn
	default:
		dialer := pool.Dial
		pool.mutex.Unlock()
		conn, err = pool.dial(dialer)
	}

	return
}

// Put 将连接放到pool中
func (pool *Pool) Put(conn net.Conn) bool {
	if conn == nil {
		return false
	}
	pool.mutex.Lock()
	if pool.isRelease {
		pool.mutex.Unlock()
		return false
	}
	select {
	case pool.connsChannel <- &idleConn{
		Conn:        conn,
		createdTime: time.Now(),
	}:
		pool.openingConns++
		pool.mutex.Unlock()
		return true
	default:
		pool.mutex.Unlock()
		return false
	}
}

// Release 释放连接池中所有连接
func (pool *Pool) Release() {
	pool.mutex.Lock()
	if pool.isRelease {
		pool.mutex.Unlock()
		return
	}
	for {
		select {
		case iConn := <-pool.connsChannel:
			iConn.Conn.Close()
			pool.openingConns--
		default:
			goto CLOSE_END
		}
	}
CLOSE_END:
	close(pool.connsChannel)
	pool.isRelease = true
	pool.mutex.Unlock()
}

func (pool *Pool) dial(dialer func() (net.Conn, error)) (net.Conn, error) {
	conn, err := dialer()
	if err != nil {
		return nil, err
	}
	/* 如果返回net.TCPConn则打开TCP Keep Alive */
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		tcpConn.SetKeepAlive(true)
		tcpConn.SetKeepAlivePeriod(time.Minute >> 2)
	}

	return conn, nil
}
