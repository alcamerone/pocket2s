package pocket2shttp

import (
	"log"
	"net/http"

	"github.com/alcamerone/pocket2s/db"
	"github.com/gocraft/web"
)

type Context struct {
	Rooms       db.RoomStore
	Connections db.ConnectionStore
}

func SetHeaders(ctx *Context, rw web.ResponseWriter, req *web.Request, next web.NextMiddlewareFunc) {
	reqOrigin := req.Request.Header.Get("Origin")
	if reqOrigin == "" {
		log.Printf("received request with no origin header from %s", req.Request.RemoteAddr)
		rw.WriteHeader(http.StatusBadRequest)
		return
	}
	rw.Header().Add("Access-Control-Allow-Origin", reqOrigin)
	rw.Header().Add("Access-Control-Allow-Headers", "Content-Type")
	next(rw, req)
}
