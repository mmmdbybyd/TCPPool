package TCPPool

import (
	"errors"
	"net"
	"sync"
	"time"
)

type idleConn struct {
	net.Conn
	// createdTime 记录连接放回池中的时间，用于空闲超时淘汰。
	createdTime time.Time
}

// Pool 是一个简单的 TCP 连接池。
// openingConns 表示“池中已有连接 + 正在创建中的连接”总数，
// 这样在并发补连接时可以避免超过 MaxConn。
type Pool struct {
	// MaxConn 为池允许维护的最大连接数。
	MaxConn int
	// MinConn 为低水位；Get 取走连接后如果低于该值会异步补连接。
	MinConn int
	// DialSpeed > 0 时表示每秒最多补建多少个连接；<= 0 表示尽快补满。
	DialSpeed int
	// IdleSec 为连接在池中的最大空闲秒数，0 表示不按空闲时间淘汰。
	IdleSec int64
	// Dial 负责创建新的底层连接。
	Dial func() (net.Conn, error)
	// Check 用于在取出连接时做健康检查；返回非 nil 表示连接失效。
	Check func(net.Conn) error

	isRelease bool
	idleTime  time.Duration
	// openingConns 统计池当前“持有/预留”的连接数量。
	openingConns int
	connsChannel chan *idleConn
	mutex        sync.Mutex
}

var (
	errorIsReleased error = errors.New("pool is released")
)

// Start 初始化连接池并启动后台补连接协程。
func (pool *Pool) Start() {
	pool.isRelease = false
	if pool.MaxConn > 0 {
		pool.connsChannel = make(chan *idleConn, pool.MaxConn<<1)
		pool.idleTime = time.Duration(pool.IdleSec) * time.Second
		go pool.GenerateConnections()
	}
}

// Restart 清空现有空闲连接，并按 newPool 的配置重新启动连接池。
// 如果 newPool 为 nil，则仅复用当前配置重新拉起。
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

// GenerateConnections 按配置持续补连接。
// 它不会直接根据旧快照循环，而是通过 reserveDialSlot 动态预留名额，
// 从而避免并发补连接时超过 MaxConn。
func (pool *Pool) GenerateConnections() {
	pool.mutex.Lock()
	if pool.isRelease {
		pool.mutex.Unlock()
		return
	}
	dialSpeed := pool.DialSpeed
	pool.mutex.Unlock()

	if dialSpeed > 0 {
		for {
			for dialCount := 0; dialCount < dialSpeed; dialCount++ {
				if !pool.generateOne() {
					return
				}
			}
			time.Sleep(time.Second)
		}
	}

	for {
		if !pool.generateOne() {
			return
		}
	}
}

// generateOne 负责补建一个连接。
// 先预留一个名额，再拨号，成功后放回池；失败时回滚预留计数。
func (pool *Pool) generateOne() bool {
	dialer, ok := pool.reserveDialSlot()
	if !ok {
		return false
	}
	conn, _ := pool.dial(dialer)
	if conn == nil {
		pool.releaseOpeningConn()
		return false
	}
	return pool.put(conn, true)
}

// Get 从池中取出一个可用连接。
// 如果池为空，则直接调用 Dial 现建现用；
// 如果取出的连接超时或健康检查失败，会继续尝试读取下一个。
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
		if pool.openingConns <= pool.MinConn {
			if pool.openingConns < 0 {
				pool.openingConns = 0
			}
			go pool.GenerateConnections()
		}
		if pool.idleTime > 0 && iConn.createdTime.Add(pool.idleTime).Before(time.Now()) {
			iConn.Close()
			goto READ_CONN_CHANNEL
		}
		if pool.Check != nil && pool.Check(iConn) != nil {
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

// Put 将连接放回池中，供下次复用。
func (pool *Pool) Put(conn net.Conn) bool {
	return pool.put(conn, false)
}

// put 是 Put 的内部实现。
// reserved=true 表示该连接对应的 openingConns 名额已经在拨号前预留过，
// 因此入池时不应再次递增；失败时要回滚计数并关闭连接。
func (pool *Pool) put(conn net.Conn, reserved bool) bool {
	if conn == nil {
		return false
	}
	pool.mutex.Lock()
	if pool.isRelease {
		if reserved && pool.openingConns > 0 {
			pool.openingConns--
		}
		pool.mutex.Unlock()
		conn.Close()
		return false
	}
	select {
	case pool.connsChannel <- &idleConn{
		Conn:        conn,
		createdTime: time.Now(),
	}:
		if !reserved {
			pool.openingConns++
		}
		pool.mutex.Unlock()
		return true
	default:
		if reserved && pool.openingConns > 0 {
			pool.openingConns--
		}
		pool.mutex.Unlock()
		conn.Close()
		return false
	}
}

// reserveDialSlot 为一次即将发生的拨号预留名额。
// 这样多个补连接协程并发运行时，也能保证总连接数不超过 MaxConn。
func (pool *Pool) reserveDialSlot() (func() (net.Conn, error), bool) {
	pool.mutex.Lock()
	defer pool.mutex.Unlock()
	if pool.isRelease || pool.openingConns >= pool.MaxConn {
		return nil, false
	}
	pool.openingConns++
	return pool.Dial, true
}

// releaseOpeningConn 在拨号失败或结果作废时回滚预留名额。
func (pool *Pool) releaseOpeningConn() {
	pool.mutex.Lock()
	if pool.openingConns > 0 {
		pool.openingConns--
	}
	pool.mutex.Unlock()
}

// Release 关闭并释放连接池中的全部空闲连接。
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

// dial 调用外部 Dial，并对 TCP 连接补充 keepalive 设置。
func (pool *Pool) dial(dialer func() (net.Conn, error)) (net.Conn, error) {
	conn, err := dialer()
	if err != nil {
		return nil, err
	}
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		tcpConn.SetKeepAlive(true)
		tcpConn.SetKeepAlivePeriod(time.Minute >> 2)
	}

	return conn, nil
}
