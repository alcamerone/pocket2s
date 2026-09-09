/*    package "server/main" defines the Pocket2s server.
 *    Copyright (C) 2020 Cameron Ekblad.
 *    Email: al.camerone@gmail.com
 *
 *    This program is free software: you can redistribute it and/or modify
 *    it under the terms of the GNU Affero General Public License as published
 *    by the Free Software Foundation, either version 3 of the License, or
 *    (at your option) any later version.
 *
 *    This program is distributed in the hope that it will be useful,
 *    but WITHOUT ANY WARRANTY; without even the implied warranty of
 *    MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 *    GNU Affero General Public License for more details.
 *
 *    You should have received a copy of the GNU Affero General Public License
 *    along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/alcamerone/pocket2s/cmap"
	"github.com/alcamerone/pocket2s/db"
	pocket2shttp "github.com/alcamerone/pocket2s/server/http"
	"github.com/alcamerone/pocket2s/server/http/rooms"
	"github.com/alcamerone/pocket2s/types"
	"github.com/gocraft/web"
)

const (
	MAX_PLAYERS         = 6
	DEFAULT_BUY_IN      = 2000
	DEFAULT_BIG_BLIND   = 20
	DEFAULT_SMALL_BLIND = 10
	DEFAULT_ANTE        = 0
	ENV_LOCAL           = "local"
)

var (
	router                *web.Router
	roomStore             = db.NewInMemoryRoomStore()
	conns                 = db.NewInMemoryConnectionStore()
	cancelSelfDestructChs = make(map[string]chan struct{})
)

func main() {
	var err error

	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	// TODO default room for dev. Remove before prod
	roomStore.NewRoom(context.Background(), &types.Room{
		Id:        "pocket2s",
		PlayerMap: cmap.New[string, *types.Player](MAX_PLAYERS),
		Opts: types.RoomOpts{
			BuyIn:      DEFAULT_BUY_IN,
			BigBlind:   DEFAULT_BIG_BLIND,
			SmallBlind: DEFAULT_SMALL_BLIND,
			Ante:       DEFAULT_ANTE,
		},
	})
	router = web.New(pocket2shttp.Context{}).
		Get("/healthcheck", handleHealthcheck)
	router.Middleware(func(ctx *pocket2shttp.Context, rw web.ResponseWriter, req *web.Request, next web.NextMiddlewareFunc) {
		// Inject stores
		ctx.Connections = conns
		ctx.Rooms = roomStore
		next(rw, req)
	})
	rooms.AddRoomRoutes(router)
	router.Middleware(func(ctx *pocket2shttp.Context, rw web.ResponseWriter, req *web.Request, next web.NextMiddlewareFunc) {
		// If the call was to connect,
		// ensure that the room in question is not erroneously cleaned up
		if strings.Contains(req.URL.Path, "connect") {
			rId := req.PathParams["roomId"]
			if cancelSelfDestructChs[rId] != nil {
				cancelSelfDestructChs[rId] <- struct{}{}
			} else {
				cancelSelfDestructChs[rId] = make(chan struct{}, 1)
			}
		}
		next(rw, req)
	})

	go func() {
		// Periodically check for empty rooms and close them if found
		for {
			// Just do this every five minutes until the server is shut down externally
			<-time.After(5 * time.Minute)

			// Can safely ignore error here as in-memory store never errors
			allRooms, _ := roomStore.GetAllRooms(context.Background())
		roomLoop:
			for _, r := range allRooms {
				for p := range r.PlayerMap.Values() {
					if _, err := conns.GetConnection(fmt.Sprintf("%s-%s", r.Id, p.Id)); err != nil {
						// This room has an active connection, continue to the next one
						continue roomLoop
					}
				}
				select {
				case <-cancelSelfDestructChs[r.Id]:
					continue
				case <-time.After(30 * time.Second):
				}
				log.Printf("room %s destroyed due to inactivity", r.Id)
				// For dev just reset the room
				// For prod, destroy it
				if r.Id == "pocket2s" {
					r.GameTable = nil
					r.PlayerMap = cmap.New[string, *types.Player](MAX_PLAYERS)
					continue
				}
				roomStore.DeleteRoom(context.Background(), r.Id)
			}
		}
	}()

	if os.Getenv("ENVIRONMENT") == ENV_LOCAL {
		log.Println("starting server on port 2222")
		err = http.ListenAndServe(":2222", router)
	} else {
		// redirect HTTP to HTTPS
		httpSrv := &http.Server{
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
			IdleTimeout:  5 * time.Second,
			Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Connection", "close")
				url := "https://" + req.Host + req.URL.String()
				http.Redirect(w, req, url, http.StatusMovedPermanently)
			}),
		}

		go func() {
			log.Printf("error in HTTP upgrade server: %s", httpSrv.ListenAndServe())
		}()

		tlsConfig := &tls.Config{
			// Causes servers to use Go's default ciphersuite preferences,
			// which are tuned to avoid attacks. Does nothing on clients.
			PreferServerCipherSuites: true,
			// Only use curves which have assembly implementations
			CurvePreferences: []tls.CurveID{
				tls.CurveP256,
				tls.X25519, // Go 1.8 only
			},
		}

		srv := &http.Server{
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 10 * time.Second,
			IdleTimeout:  120 * time.Second,
			TLSConfig:    tlsConfig,
			Handler:      router,
		}

		log.Println("starting HTTPS server")
		err = srv.ListenAndServeTLS(
			"/etc/letsencrypt/live/api.pocket2s.com/fullchain.pem",
			"/etc/letsencrypt/live/api.pocket2s.com/privkey.pem")
	}
	if err != nil {
		log.Fatal("error in main loop: " + err.Error())
	}
}

// TODO maybe create a new context that embeds the pocket2shttp Context?
func handleHealthcheck(ctx *pocket2shttp.Context, rw web.ResponseWriter, req *web.Request) {
	// TODO for now just return 200 to say the server is alive
	rw.WriteHeader(http.StatusOK)
}
