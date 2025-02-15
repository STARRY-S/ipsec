package server

import (
	"fmt"
	"net/http"

	"github.com/rancher/ipsec/backend"
	// "github.com/rancher/log"
	log "github.com/Sirupsen/logrus"
)

// Server structure is used to the store backend information
type Server struct {
	Backend backend.Backend
}

// ListenAndServe is used to setup ping and reload handlers and
// start listening on the specified port
func (s *Server) ListenAndServe(listen string) error {
	http.HandleFunc("/ping", s.ping)
	http.HandleFunc("/v1/reload", s.reload)
	log.Infof("Listening on %s", listen)
	err := http.ListenAndServe(listen, nil)
	if err != nil {
		log.Errorf("got error while ListenAndServe: %v", err)
	}
	return err
}

func (s *Server) ping(rw http.ResponseWriter, req *http.Request) {
	log.Debugf("Received ping request")
	rw.Write([]byte("OK"))
}

func (s *Server) reload(rw http.ResponseWriter, req *http.Request) {
	log.Debugf("Received reload request")
	msg := "Reloaded Configuration\n"
	if err := s.Backend.Reload(); err != nil {
		rw.WriteHeader(500)
		msg = fmt.Sprintf("Failed to reload configuration: %v\n", err)
	}

	rw.Write([]byte(msg))
}
