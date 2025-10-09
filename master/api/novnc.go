package api

import (
	"512SvMan/protocol"
	"512SvMan/virsh"
	"net/http"

	grpcVirsh "github.com/Maruqes/512SvMan/api/proto/virsh"
	"github.com/Maruqes/512SvMan/logger"
	"github.com/evangwt/go-vncproxy"
	"github.com/go-chi/chi/v5"
	"golang.org/x/net/websocket"
)

//uses https://github.com/evangwt/go-vncproxy

var vp *vncproxy.Proxy

// http://localhost:9595/novnc/vnc.html?path=/novnc/ws?token=vm1
// http://localhost:9595/novnc/vnc.html?path=/novnc/ws%3Fvm%3Dplsfunfa%26slave%3Dslave1    change plsfunfa and slave1
func initNoVNC() {
	vp = vncproxy.New(&vncproxy.Config{
		LogLevel: vncproxy.DebugLevel,
		TokenHandler: func(r *http.Request) (string, error) {
			// map token -> VNC backend
			// e.g., token=vm1 -> localhost:5901 (adjust as needed)
			vmName := r.URL.Query().Get("vm")
			slaveName := r.URL.Query().Get("slave")
			if vmName == "" || slaveName == "" {
				logger.Error("novnc: missing vm or slave parameter")
				return "", http.ErrNoLocation
			}

			slaveMachine := protocol.GetConnectionByMachineName(slaveName)
			if slaveMachine == nil {
				logger.Error("novnc: slave machine not found")
				return "", http.ErrNoLocation
			}

			vm, err := virsh.GetVmByName(slaveMachine.Connection, &grpcVirsh.GetVmByNameRequest{Name: vmName})
			if err != nil {
				logger.Error("novnc: failed to get VM by name")
				return "", http.ErrNoLocation
			}
			if vm == nil {
				logger.Error("novnc: VM not found or VNC not configured")
				return "", http.ErrNoLocation
			}

			return slaveMachine.Addr + ":" + vm.NovncPort, nil
		},
	})
}

func serveNoVNCWebSocket(w http.ResponseWriter, r *http.Request) {
	websocket.Handler(vp.ServeWS).ServeHTTP(w, r)
}

func serveNoVNC(w http.ResponseWriter, r *http.Request) {
	http.StripPrefix("/novnc", http.FileServer(http.Dir("./novnc"))).ServeHTTP(w, r)
}

func setupNoVNCAPI(r chi.Router) chi.Router {
	return r.Route("/novnc", func(r chi.Router) {
		r.Get("/ws", serveNoVNCWebSocket)
		r.Get("/*", serveNoVNC)
	})
}
