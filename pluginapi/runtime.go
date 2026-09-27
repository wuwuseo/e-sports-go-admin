package pluginapi

import (
	"context"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const (
	EnvPluginID      = "PLUGIN_ID"
	EnvPluginVersion = "PLUGIN_VERSION"
	EnvAPIVersion    = "PLUGIN_API_VERSION"
	EnvProtocol      = "PLUGIN_PROTOCOL"
	EnvTransport     = "PLUGIN_TRANSPORT"
	EnvAddress       = "PLUGIN_ADDRESS"
	EnvSocket        = "PLUGIN_SOCKET"
	EnvEndpointFile  = "PLUGIN_ENDPOINT_FILE"
	EnvToken         = "PLUGIN_TOKEN"

	APIVersion          = "v1"
	TokenHeader         = "X-Go-Admin-Plugin-Token"
	UserIDHeader        = "X-Go-Admin-Plugin-User-ID"
	APIHeader           = "X-Go-Admin-Plugin-API"
	PluginIDHeader      = "X-Go-Admin-Plugin-ID"
	PluginVersionHeader = "X-Go-Admin-Plugin-Version"
	HealthPath          = "/health"
	HandshakePath       = "/_go-admin/handshake"
	ShutdownPath        = "/_go-admin/shutdown"
	MaxMessageSize      = 16 << 20
	serverStopTimeout   = 5 * time.Second
)

type ProxyHandler func(context.Context, *ProxyRequest) (*ProxyResponse, error)

func ServeHTTP(handler http.Handler) error {
	if handler == nil {
		return errors.New("plugin HTTP handler is nil")
	}
	listener, cleanup, err := listenFromEnvironment()
	if err != nil {
		return err
	}
	defer cleanup()

	token := os.Getenv(EnvToken)
	var server *http.Server
	server = &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !validToken(token, r.Header.Get(TokenHeader)) {
				http.Error(w, "unauthorized plugin request", http.StatusUnauthorized)
				return
			}
			if IsControlPath(r.URL.Path) {
				if r.URL.Path == ShutdownPath && r.Method != http.MethodPost {
					w.WriteHeader(http.StatusMethodNotAllowed)
					return
				}
				setRuntimeHTTPHeaders(w.Header())
				w.Header().Set(TokenHeader, token)
				w.WriteHeader(http.StatusNoContent)
				if r.URL.Path == ShutdownPath {
					go func() {
						ctx, cancel := context.WithTimeout(context.Background(), serverStopTimeout)
						defer cancel()
						_ = server.Shutdown(ctx)
					}()
				}
				return
			}
			handler.ServeHTTP(w, r)
		}),
	}
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func ServeGRPC(handler ProxyHandler, options ...grpc.ServerOption) error {
	if handler == nil {
		return errors.New("plugin gRPC handler is nil")
	}
	listener, cleanup, err := listenFromEnvironment()
	if err != nil {
		return err
	}
	defer cleanup()

	options = append(options, grpc.MaxRecvMsgSize(MaxMessageSize), grpc.MaxSendMsgSize(MaxMessageSize))
	server := grpc.NewServer(options...)
	RegisterPluginServer(server, &proxyServer{
		handler:  handler,
		token:    os.Getenv(EnvToken),
		shutdown: server.GracefulStop,
	})
	return server.Serve(listener)
}

func ServeStdio(ctx context.Context, handler ProxyHandler) error {
	if handler == nil {
		return errors.New("plugin stdio handler is nil")
	}
	token := os.Getenv(EnvToken)
	for {
		request := new(ProxyRequest)
		if err := ReadDelimited(os.Stdin, request); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
		response, err := dispatch(ctx, token, handler, request)
		if err != nil {
			response = &ProxyResponse{StatusCode: http.StatusInternalServerError, Error: err.Error()}
		}
		if err := WriteDelimited(os.Stdout, response); err != nil {
			return err
		}
		if request.GetPath() == ShutdownPath && response.GetStatusCode() == http.StatusNoContent {
			return nil
		}
	}
}

func WriteDelimited(writer io.Writer, message proto.Message) error {
	data, err := proto.Marshal(message)
	if err != nil {
		return err
	}
	if len(data) > MaxMessageSize {
		return fmt.Errorf("plugin message exceeds %d bytes", MaxMessageSize)
	}
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(data)))
	if err := writeAll(writer, size[:]); err != nil {
		return err
	}
	return writeAll(writer, data)
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if written > 0 {
			data = data[written:]
		}
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func ReadDelimited(reader io.Reader, message proto.Message) error {
	var size [4]byte
	if _, err := io.ReadFull(reader, size[:]); err != nil {
		return err
	}
	length := binary.BigEndian.Uint32(size[:])
	if length > MaxMessageSize {
		return fmt.Errorf("plugin message exceeds %d bytes", MaxMessageSize)
	}
	data := make([]byte, int(length))
	if _, err := io.ReadFull(reader, data); err != nil {
		return err
	}
	return proto.Unmarshal(data, message)
}

type proxyServer struct {
	UnimplementedPluginServer
	handler  ProxyHandler
	token    string
	shutdown func()
}

func (server *proxyServer) Proxy(ctx context.Context, request *ProxyRequest) (*ProxyResponse, error) {
	response, err := dispatch(ctx, server.token, server.handler, request)
	if err != nil {
		if status.Code(err) != codes.Unknown {
			return nil, err
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	if request.GetPath() == ShutdownPath && response.GetStatusCode() == http.StatusNoContent && server.shutdown != nil {
		go server.shutdown()
	}
	return response, nil
}

func dispatch(ctx context.Context, token string, handler ProxyHandler, request *ProxyRequest) (*ProxyResponse, error) {
	if request == nil || !validToken(token, request.GetToken()) {
		return nil, status.Error(codes.Unauthenticated, "invalid plugin token")
	}
	if IsControlPath(request.GetPath()) {
		if request.GetPath() == ShutdownPath && request.GetMethod() != http.MethodPost {
			return &ProxyResponse{StatusCode: http.StatusMethodNotAllowed, Headers: runtimeHeaders()}, nil
		}
		return &ProxyResponse{StatusCode: http.StatusNoContent, Headers: runtimeHeaders()}, nil
	}
	response, err := handler(ctx, request)
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, errors.New("plugin handler returned a nil response")
	}
	return response, nil
}

func IsControlPath(path string) bool {
	return path == HealthPath || path == HandshakePath || path == ShutdownPath
}

func runtimeHeaders() []*Header {
	return []*Header{
		{Name: APIHeader, Values: []string{runtimeAPIVersion()}},
		{Name: PluginIDHeader, Values: []string{os.Getenv(EnvPluginID)}},
		{Name: PluginVersionHeader, Values: []string{os.Getenv(EnvPluginVersion)}},
	}
}

func setRuntimeHTTPHeaders(headers http.Header) {
	for _, header := range runtimeHeaders() {
		if value := header.GetValues(); len(value) != 0 && value[0] != "" {
			headers.Set(header.GetName(), value[0])
		}
	}
}

func runtimeAPIVersion() string {
	if version := os.Getenv(EnvAPIVersion); version != "" {
		return version
	}
	return APIVersion
}

func validToken(expected, actual string) bool {
	return expected != "" && len(expected) == len(actual) && subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) == 1
}

func listenFromEnvironment() (net.Listener, func(), error) {
	switch transport := os.Getenv(EnvTransport); transport {
	case "tcp":
		listener, err := net.Listen("tcp", os.Getenv(EnvAddress))
		if err != nil {
			return nil, func() {}, err
		}
		cleanup, err := publishEndpoint(listener.Addr().String())
		if err != nil {
			_ = listener.Close()
			return nil, func() {}, err
		}
		return listener, cleanup, nil
	case "unix":
		socket := os.Getenv(EnvSocket)
		if socket == "" {
			return nil, func() {}, errors.New("PLUGIN_SOCKET is required")
		}
		if err := os.MkdirAll(filepath.Dir(socket), 0700); err != nil {
			return nil, func() {}, err
		}
		_ = os.Remove(socket)
		listener, err := net.Listen("unix", socket)
		return listener, func() { _ = os.Remove(socket) }, err
	default:
		return nil, func() {}, fmt.Errorf("unsupported plugin transport %q", transport)
	}
}

func publishEndpoint(address string) (func(), error) {
	path := os.Getenv(EnvEndpointFile)
	if path == "" {
		return func() {}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	temporary := fmt.Sprintf("%s.tmp-%d", path, os.Getpid())
	if err := os.WriteFile(temporary, []byte(address), 0600); err != nil {
		return nil, err
	}
	_ = os.Remove(path)
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return nil, err
	}
	return func() { _ = os.Remove(path) }, nil
}
