package pluginserver

import (
	"aster/billing/plugins/server.aster-cloud/legacy/pluginapi"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"sync"
)

type sessionKey struct{}
type session struct {
	nonce string
	used  bool
}

type boundedListener struct {
	net.Listener
	clientUID uint32
	slots     chan struct{}
}
type boundedConn struct {
	*net.UnixConn
	slots chan struct{}
	once  sync.Once
}

func (c *boundedConn) Close() error {
	err := c.UnixConn.Close()
	c.once.Do(func() { <-c.slots })
	return err
}
func (l *boundedListener) Accept() (net.Conn, error) {
	for {
		connection, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		unix, ok := connection.(*net.UnixConn)
		if !ok {
			connection.Close()
			continue
		}
		uid, err := pluginapi.PeerUID(unix)
		if err != nil || uid != l.clientUID {
			connection.Close()
			continue
		}
		select {
		case l.slots <- struct{}{}:
			return &boundedConn{UnixConn: unix, slots: l.slots}, nil
		default:
			connection.Close()
		}
	}
}

func readJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	if r.Method != "POST" || r.Host != "plugin.local" || r.Header.Get("Content-Type") != "application/json" || len(r.URL.RawQuery) != 0 || r.URL.ForceQuery {
		http.Error(w, "invalid request", 400)
		return false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, pluginapi.MaxRequestBytes))
	if err != nil || pluginapi.DecodeStrict(raw, pluginapi.MaxRequestBytes, target) != nil {
		http.Error(w, "invalid request", 400)
		return false
	}
	return true
}

func reply(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
