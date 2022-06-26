package TCPPool

import (
	"net"
	"testing"
	"time"
)

// TestPool_Base 基本用法
func TestPool_Base(t *testing.T) {
	pool := Pool{
		MaxConn: 10,
		MinConn: 2,
		IdleSec: 57,
		Dial: func() (net.Conn, error) {
			return net.DialTimeout("tcp", "119.29.29.29:80", time.Minute>>2)
		},
	}
	pool.Start() //启动连接池
	httpRequest := []byte("GET /d?dn=github.com HTTP/1.1\r\nHost: 119.29.29.29\r\nConnection: Close\r\n\r\n")
	conn, err := pool.Get() //从连接池中取一个连接，，如果连接池中没有空闲连接则调用Dial()创建新的连接
	if err != nil {
		t.Error("pool.Get():", err)
		return
	}
	defer conn.Close()
	_, err = conn.Write(httpRequest)
	if err != nil {
		t.Error("conn.Write():", err)
		return
	}
	var rsp [2048]byte
	_, err = conn.Read(rsp[:])
	if err != nil {
		t.Error("conn.Read():", err)
		return
	}
}

// TestPool_Restart 多携程场景修改Dial()函数
func TestPool_Restart(t *testing.T) {
	pool := Pool{
		MaxConn: 10,
		MinConn: 2,
		IdleSec: 57,
		Dial: func() (net.Conn, error) {
			return net.DialTimeout("tcp", "119.29.29.29:80", time.Minute>>2)
		},
	}
	pool.Start() //启动连接池
	/* 访问HTTP连接 */
	go func() {
		for i := 0; i < 5; i++ {
			httpRequest := []byte("GET /d?dn=github.com HTTP/1.1\r\nHost: 119.29.29.29\r\nConnection: Close\r\n\r\n")
			conn, err := pool.Get()
			if err != nil {
				t.Error("pool.Get():", err)
				return
			}
			t.Log("TCP Dest Address:", conn.RemoteAddr().String())
			defer conn.Close()
			_, err = conn.Write(httpRequest)
			if err != nil {
				t.Error("conn.Write():", err)
				return
			}
			var rsp [2048]byte
			_, err = conn.Read(rsp[:])
			if err != nil {
				t.Error("conn.Read():", err)
				return
			}
		}
	}()
	/* 修改Dial的地址，然后重启连接池 */
	time.Sleep(time.Second)
	newPool := Pool{
		MaxConn: 5,
		MinConn:1,
		IdleSec: 60,
		DialSpeed: 2,
		Dial: func() (net.Conn, error) {
			return net.DialTimeout("tcp", "182.254.116.116:80", time.Minute>>2)
		},
	}
	pool.Restart(&newPool)
}
