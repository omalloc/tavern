package server

import (
	"net"
	"net/http"
	"strconv"
	"strings"
)

var (
	bodyBytes = []byte(http.StatusText(http.StatusInternalServerError))
	bodyLen   = strconv.FormatInt(int64(len(bodyBytes)), 10)
)

func (s *HTTPServer) listen() error {
	if s.listener == nil {
		var (
			ln  net.Listener
			err error
		)

		// normal listen
		listen := net.Listen
		if s.flip != nil {
			// graceful listen
			listen = s.flip.Listen
		}

		// normal network
		network := "tcp"
		if strings.HasSuffix(s.Server.Addr, ".sock") {
			// unix socket
			network = "unix"
		}

		ln, err = listen(network, s.Server.Addr)
		if err != nil {
			return err
		}

		s.listener = ln
		return nil
	}

	return nil
}
