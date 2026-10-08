package http

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/alcamerone/pocket2s/server/http/rooms"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/go-chi/chi/v5"
)

// APIGatewayProxyResponseWriter implements http.ResponseWriter
type APIGatewayProxyResponseWriter struct {
	resp   events.APIGatewayProxyResponse
	body   bytes.Buffer
	header http.Header
}

func (rw *APIGatewayProxyResponseWriter) Header() http.Header {
	if rw.header == nil {
		rw.header = make(map[string][]string)
	}
	return rw.header
}

func (rw *APIGatewayProxyResponseWriter) Write(data []byte) (int, error) {
	return rw.body.Write(data)
}

func (rw *APIGatewayProxyResponseWriter) WriteHeader(status int) {
	rw.resp.StatusCode = status
}

// GetResponse converts the http.Response object to a events.APIGatewayProxyResponse
func (rw *APIGatewayProxyResponseWriter) GetResponse() (events.APIGatewayProxyResponse, error) {
	rw.resp.Body = rw.body.String()
	rw.resp.Headers = make(map[string]string, len(rw.header))
	for k, v := range rw.header {
		rw.resp.Headers[k] = strings.Join(v, ",")
	}
	return rw.resp, nil
}

func handler(req events.APIGatewayProxyRequest, handler http.Handler) (events.APIGatewayProxyResponse, error) {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	// Convert the incoming request to a http.Request
	host := req.Headers["Host"]
	goHttpReq, err := http.NewRequest(
		req.HTTPMethod,
		fmt.Sprintf("https://%s%s", host, req.Path),
		bytes.NewBuffer([]byte(req.Body)))
	if err != nil {
		return events.APIGatewayProxyResponse{}, err
	}
	goHttpReq.Host = host
	goHttpReq.URL.Host = host
	goHttpReq.URL.Scheme = req.Headers["CloudFront-Forwarded-Proto"]
	goHttpReq.RemoteAddr = req.RequestContext.Identity.SourceIP
	for k, v := range req.Headers {
		goHttpReq.Header.Add(k, v)
	}

	// Create a router and attach the routes as in the server program
	roomsCtx := &rooms.Context{
		// TODO Inject stores
	}
	router := chi.NewRouter()
	rooms.AttachRoomRoutes(router, roomsCtx)

	rw := APIGatewayProxyResponseWriter{}
	router.ServeHTTP(&rw, goHttpReq)
	return rw.GetResponse()
}

func main() {
	lambda.Start(handler)
}
