package repository

import (
	"net"
	"testing"
	"time"
)

// An accepting but silent server used to keep startup stuck indefinitely.
// Exercise the real driver handshake without needing a MySQL installation.
func TestOpenBoundsUnresponsiveHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		<-stop
	}()
	result := make(chan error, 1)
	go func() {
		db, err := Open("root@tcp(" + listener.Addr().String() + ")/test?parseTime=true")
		if db != nil {
			db.Close()
		}
		result <- err
	}()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("silent server accepted as ready")
		}
	case <-time.After(7 * time.Second):
		t.Fatal("startup database handshake exceeded bounded timeout")
	}
}
