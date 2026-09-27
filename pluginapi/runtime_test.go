package pluginapi

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestDelimitedMessageRoundTripAndLimit(t *testing.T) {
	request := &ProxyRequest{Method: http.MethodPost, Path: "/echo", Body: []byte("payload")}
	var framed bytes.Buffer
	if err := WriteDelimited(&framed, request); err != nil {
		t.Fatal(err)
	}
	decoded := new(ProxyRequest)
	if err := ReadDelimited(&framed, decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.GetMethod() != request.GetMethod() || decoded.GetPath() != request.GetPath() || string(decoded.GetBody()) != "payload" {
		t.Fatalf("decoded request = %+v", decoded)
	}

	var oversized bytes.Buffer
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], MaxMessageSize+1)
	oversized.Write(size[:])
	if err := ReadDelimited(&oversized, new(ProxyRequest)); err == nil {
		t.Fatal("oversized stdio frame was accepted")
	}
}

func TestDispatchRequiresPerStartToken(t *testing.T) {
	t.Setenv(EnvPluginID, "admin.example-plugin")
	t.Setenv(EnvPluginVersion, "1.2.3")
	t.Setenv(EnvAPIVersion, APIVersion)
	handlerCalled := false
	handler := func(context.Context, *ProxyRequest) (*ProxyResponse, error) {
		handlerCalled = true
		return &ProxyResponse{StatusCode: http.StatusOK}, nil
	}
	if _, err := dispatch(context.Background(), "expected", handler, &ProxyRequest{Token: "wrong"}); err == nil {
		t.Fatal("invalid plugin token was accepted")
	}
	if handlerCalled {
		t.Fatal("handler ran for an invalid plugin token")
	}
	response, err := dispatch(context.Background(), "expected", handler, &ProxyRequest{Token: "expected", Path: HealthPath})
	if err != nil || response.GetStatusCode() != http.StatusNoContent {
		t.Fatalf("health dispatch = response %v, error %v", response, err)
	}
	for _, path := range []string{HealthPath, HandshakePath, ShutdownPath} {
		handlerCalled = false
		method := http.MethodGet
		if path == ShutdownPath {
			method = http.MethodPost
		}
		response, err = dispatch(context.Background(), "expected", handler, &ProxyRequest{Token: "expected", Method: method, Path: path})
		if err != nil || response.GetStatusCode() != http.StatusNoContent {
			t.Fatalf("control dispatch %s = response %v, error %v", path, response, err)
		}
		if handlerCalled {
			t.Fatalf("handler ran for control path %s", path)
		}
		headers := headersToHTTP(response.GetHeaders())
		if headers.Get(APIHeader) != APIVersion || headers.Get(PluginIDHeader) != "admin.example-plugin" || headers.Get(PluginVersionHeader) != "1.2.3" {
			t.Fatalf("control headers for %s = %v", path, headers)
		}
	}
	response, err = dispatch(context.Background(), "expected", handler, &ProxyRequest{
		Token: "expected", Method: http.MethodGet, Path: ShutdownPath,
	})
	if err != nil || response.GetStatusCode() != http.StatusMethodNotAllowed {
		t.Fatalf("GET shutdown dispatch = response %v, error %v", response, err)
	}
}

func headersToHTTP(headers []*Header) http.Header {
	result := make(http.Header, len(headers))
	for _, header := range headers {
		for _, value := range header.GetValues() {
			result.Add(header.GetName(), value)
		}
	}
	return result
}

func TestWriteDelimitedHandlesShortWrites(t *testing.T) {
	writer := &shortWriter{limit: 2}
	if err := WriteDelimited(writer, &ProxyRequest{Path: "/short-write"}); err != nil {
		t.Fatal(err)
	}
	decoded := new(ProxyRequest)
	if err := ReadDelimited(bytes.NewReader(writer.data), decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.GetPath() != "/short-write" {
		t.Fatalf("decoded path = %q", decoded.GetPath())
	}

	if err := WriteDelimited(zeroWriter{}, &ProxyRequest{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("zero writer error = %v, want io.ErrShortWrite", err)
	}
}

func TestPublishEndpointWritesAndCleansAtomicFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime", "endpoint")
	t.Setenv(EnvEndpointFile, path)
	cleanup, err := publishEndpoint("127.0.0.1:12345")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "127.0.0.1:12345" {
		t.Fatalf("endpoint = %q", data)
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("endpoint file still exists after cleanup: %v", err)
	}
}

type shortWriter struct {
	data  []byte
	limit int
}

func (writer *shortWriter) Write(data []byte) (int, error) {
	if len(data) > writer.limit {
		data = data[:writer.limit]
	}
	writer.data = append(writer.data, data...)
	return len(data), nil
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }
