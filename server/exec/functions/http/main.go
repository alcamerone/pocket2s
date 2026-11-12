package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/alcamerone/pocket2s/server/db"
	"github.com/alcamerone/pocket2s/server/http/rooms"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/go-chi/chi/middleware"
	"github.com/go-chi/chi/v5"
)

const requestSchemeHttps = "https"

var router chi.Router

func mustEnv(v string) (envVar string) {
	if envVar = os.Getenv(v); envVar == "" {
		log.Fatalf("Error: No value for %s", v)
	}
	return envVar
}

// APIGatewayV2HTTPResponseWriter implements http.ResponseWriter
type APIGatewayV2HTTPResponseWriter struct {
	resp   events.APIGatewayV2HTTPResponse
	body   bytes.Buffer
	header http.Header
}

func (rw *APIGatewayV2HTTPResponseWriter) Header() http.Header {
	if rw.header == nil {
		rw.header = make(map[string][]string)
	}
	return rw.header
}

func (rw *APIGatewayV2HTTPResponseWriter) Write(data []byte) (int, error) {
	return rw.body.Write(data)
}

func (rw *APIGatewayV2HTTPResponseWriter) WriteHeader(status int) {
	rw.resp.StatusCode = status
}

// GetResponse converts the http.Response object to a events.APIGatewayV2HTTPResponse
func (rw *APIGatewayV2HTTPResponseWriter) GetResponse() (events.APIGatewayV2HTTPResponse, error) {
	if rw.resp.StatusCode == 0 {
		rw.resp.StatusCode = http.StatusOK
	}
	rw.resp.Body = rw.body.String()
	rw.resp.Headers = make(map[string]string, len(rw.header))
	for k, v := range rw.header {
		rw.resp.Headers[k] = strings.Join(v, ",")
	}
	return rw.resp, nil
}

func handler(ctx context.Context, req events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	log.Printf("Host: %s Path: %s", req.RequestContext.DomainName, req.RawPath)

	// Convert the incoming request to a http.Request
	body := []byte(req.Body)
	if req.IsBase64Encoded {
		var err error
		body, err = base64.StdEncoding.DecodeString(req.Body)
		if err != nil {
			return events.APIGatewayV2HTTPResponse{StatusCode: http.StatusBadRequest}, nil
		}
	}

	host := req.RequestContext.DomainName
	goHttpReq, err := http.NewRequest(
		req.RequestContext.HTTP.Method,
		fmt.Sprintf("https://%s%s", host, req.RawPath),
		bytes.NewBuffer(body))
	if err != nil {
		return events.APIGatewayV2HTTPResponse{StatusCode: http.StatusInternalServerError}, err
	}
	goHttpReq.Host = host
	goHttpReq.URL.Host = host
	goHttpReq.URL.Scheme = requestSchemeHttps
	goHttpReq.RemoteAddr = req.RequestContext.HTTP.SourceIP
	goHttpReq.URL.RawQuery = req.RawQueryString
	for k, v := range req.Headers {
		goHttpReq.Header.Add(k, v)
	}

	rw := APIGatewayV2HTTPResponseWriter{}
	router.ServeHTTP(&rw, goHttpReq)
	return rw.GetResponse()
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	// Instantiate room store
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatalf("Error initialising AWS context: %s", err.Error())
	}
	ddbClient := dynamodb.NewFromConfig(cfg)
	roomStore := db.NewDDBRoomStore(ddbClient, mustEnv("DDB_ROOM_STATE_TABLE"))

	// Create a router and attach the routes as in the server program
	roomsCtx := &rooms.Context{Rooms: roomStore}
	router = chi.NewRouter()
	router.Use(middleware.Logger)
	rooms.AttachRoomRoutes(router, roomsCtx)

	lambda.Start(handler)
}
