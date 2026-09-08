package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Milesians/sub2api-plugin-zstd/internal/pluginv1"
	"github.com/klauspost/compress/zstd"
)

const pluginID = "milesians.openai.oauth.zstd"

// pluginVersion is overridden by build.sh so GetInfo matches manifest.json.
var pluginVersion = "0.2.0"

type config struct {
	Enabled              bool `json:"enabled"`
	Level                int  `json:"level"`
	FallbackUncompressed bool `json:"fallback_uncompressed"`
}

func defaultConfig() config { return config{Enabled: true, Level: 3, FallbackUncompressed: true} }

func normalize(raw []byte) (config, []byte, error) {
	c := defaultConfig()
	if len(raw) != 0 {
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			return c, nil, errors.New("config must be a JSON object")
		}
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			return c, nil, err
		}
		if dec.Decode(&struct{}{}) != io.EOF {
			return c, nil, errors.New("config must contain one JSON object")
		}
	}
	if c.Level == 0 {
		c.Level = 3
	}
	if c.Level < 1 || c.Level > 22 {
		return c, nil, fmt.Errorf("level must be 1..22")
	}
	normalized, err := json.Marshal(c)
	return c, normalized, err
}

type server struct {
	pluginv1.UnimplementedTransportPluginServer
	mu        sync.RWMutex
	cfg       config
	clients   map[string]*http.Client
	clientsMu sync.Mutex
}

func (s *server) GetInfo(context.Context, *pluginv1.GetInfoRequest) (*pluginv1.GetInfoResponse, error) {
	return &pluginv1.GetInfoResponse{PluginId: pluginID, PluginVersion: pluginVersion, ProtocolVersion: pluginv1.ProtocolVersion, TransportApiVersion: pluginv1.TransportAPIVersion, Capabilities: []string{"openai.oauth.outbound_transport.v1"}}, nil
}

func (s *server) Health(context.Context, *pluginv1.HealthRequest) (*pluginv1.HealthResponse, error) {
	return &pluginv1.HealthResponse{Healthy: true, Message: "ready"}, nil
}

func (s *server) ValidateConfig(_ context.Context, req *pluginv1.ValidateConfigRequest) (*pluginv1.ValidateConfigResponse, error) {
	_, normalized, err := normalize(req.GetConfigJson())
	if err != nil {
		return &pluginv1.ValidateConfigResponse{Valid: false, Message: err.Error()}, nil
	}
	return &pluginv1.ValidateConfigResponse{Valid: true, NormalizedConfigJson: normalized}, nil
}

func (s *server) ApplyConfig(_ context.Context, req *pluginv1.ApplyConfigRequest) (*pluginv1.ApplyConfigResponse, error) {
	c, _, err := normalize(req.GetConfigJson())
	if err != nil {
		return &pluginv1.ApplyConfigResponse{Applied: false, Message: err.Error()}, nil
	}
	s.mu.Lock()
	s.cfg = c
	s.mu.Unlock()
	return &pluginv1.ApplyConfigResponse{Applied: true, Message: "applied"}, nil
}

func (s *server) TestConfig(_ context.Context, req *pluginv1.TestConfigRequest) (*pluginv1.TestConfigResponse, error) {
	if _, _, err := normalize(req.GetConfigJson()); err != nil {
		return &pluginv1.TestConfigResponse{Success: false, Message: err.Error()}, nil
	}
	return &pluginv1.TestConfigResponse{Success: true, Message: "zstd transport ready"}, nil
}

func eligibleRequest(c config, start *pluginv1.ForwardRequestStart) bool {
	if !c.Enabled || start == nil || start.Platform != "openai" || start.AccountType != "oauth" || start.Method != http.MethodPost {
		return false
	}
	u, err := url.Parse(start.Url)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host != "chatgpt.com" && !strings.HasSuffix(host, ".chatgpt.com") {
		return false
	}
	return strings.HasPrefix(u.Path, "/backend-api/codex/") && strings.HasSuffix(u.Path, "/responses")
}

func (s *server) httpClient(proxyRaw string) (*http.Client, error) {
	proxyRaw = strings.TrimSpace(proxyRaw)
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	if s.clients == nil {
		s.clients = make(map[string]*http.Client)
	}
	if client := s.clients[proxyRaw]; client != nil {
		return client, nil
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if proxyRaw != "" {
		proxy, err := url.Parse(proxyRaw)
		if err != nil || (proxy.Scheme != "http" && proxy.Scheme != "https") {
			return nil, errors.New("proxy_url must be an http or https URL")
		}
		transport.Proxy = http.ProxyURL(proxy)
	}
	client := &http.Client{Transport: transport}
	s.clients[proxyRaw] = client
	return client, nil
}

func sendForwardError(stream pluginv1.TransportPlugin_ForwardServer, code, message string, sent bool) error {
	return stream.Send(&pluginv1.ForwardResponse{Frame: &pluginv1.ForwardResponse_Error{Error: &pluginv1.ForwardResponseError{Code: code, Message: message, RequestSent: sent}}})
}

func (s *server) Forward(stream pluginv1.TransportPlugin_ForwardServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	start := first.GetStart()
	if start == nil {
		_ = sendForwardError(stream, "INVALID_REQUEST", "first frame must be start", false)
		return nil
	}
	s.mu.RLock()
	c := s.cfg
	s.mu.RUnlock()
	client, err := s.httpClient(start.ProxyUrl)
	if err != nil {
		_ = sendForwardError(stream, "INVALID_PROXY", err.Error(), false)
		return nil
	}
	reader, writer := io.Pipe()
	var encoder *zstd.Encoder
	compress := eligibleRequest(c, start)
	if compress {
		encoder, err = zstd.NewWriter(writer, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(c.Level)))
		if err != nil {
			if !c.FallbackUncompressed {
				_ = sendForwardError(stream, "COMPRESSION_INIT_FAILED", err.Error(), false)
				return nil
			}
			compress = false
		}
	}
	request, err := http.NewRequestWithContext(stream.Context(), start.Method, start.Url, reader)
	if err != nil {
		_ = reader.Close()
		_ = writer.Close()
		_ = sendForwardError(stream, "INVALID_REQUEST", err.Error(), false)
		return nil
	}
	for key, values := range start.Headers {
		if values != nil {
			for _, value := range values.Values {
				request.Header.Add(key, value)
			}
		}
	}
	if compress {
		request.ContentLength = -1
		request.Header.Del("Content-Length")
		request.Header.Del("Content-Encoding")
		request.Header.Set("Content-Encoding", "zstd")
		request.Header.Set("Content-Type", "application/json")
	} else if start.ContentLength >= 0 {
		request.ContentLength = start.ContentLength
	}
	result := make(chan struct {
		response *http.Response
		err      error
	}, 1)
	go func() {
		response, requestErr := client.Do(request)
		result <- struct {
			response *http.Response
			err      error
		}{response, requestErr}
	}()
	var bodyErr error
	for {
		frame, recvErr := stream.Recv()
		if recvErr == io.EOF {
			bodyErr = errors.New("request ended without body_end")
			break
		}
		if recvErr != nil {
			bodyErr = recvErr
			break
		}
		if frame == nil {
			bodyErr = errors.New("request frame is nil")
			break
		}
		switch value := frame.Frame.(type) {
		case *pluginv1.ForwardRequest_BodyChunk:
			if encoder != nil {
				_, bodyErr = encoder.Write(value.BodyChunk)
			} else {
				_, bodyErr = writer.Write(value.BodyChunk)
			}
		case *pluginv1.ForwardRequest_BodyEnd:
			if encoder != nil {
				bodyErr = encoder.Close()
			}
			if bodyErr == nil {
				bodyErr = writer.Close()
			}
			goto bodyDone
		}
		if bodyErr != nil {
			break
		}
	}
	_ = reader.CloseWithError(bodyErr)
	_ = writer.Close()
bodyDone:
	if bodyErr != nil {
		go func() {
			completed := <-result
			if completed.response != nil {
				_ = completed.response.Body.Close()
			}
		}()
		return sendForwardError(stream, "REQUEST_BODY_ERROR", bodyErr.Error(), true)
	}
	completed := <-result
	if completed.err != nil {
		if completed.response != nil {
			_ = completed.response.Body.Close()
		}
		return sendForwardError(stream, "UPSTREAM_ERROR", completed.err.Error(), true)
	}
	response := completed.response
	defer response.Body.Close()
	headers := make(map[string]*pluginv1.HeaderValues, len(response.Header))
	for key, values := range response.Header {
		headers[key] = &pluginv1.HeaderValues{Values: append([]string(nil), values...)}
	}
	started := time.Now()
	if err := stream.Send(&pluginv1.ForwardResponse{Frame: &pluginv1.ForwardResponse_Start{Start: &pluginv1.ForwardResponseStart{StatusCode: int32(response.StatusCode), Status: response.Status, Protocol: response.Proto, ProtocolMajor: int32(response.ProtoMajor), ProtocolMinor: int32(response.ProtoMinor), Headers: headers, ContentLength: response.ContentLength}}}); err != nil {
		return err
	}
	buffer := make([]byte, 32*1024)
	var bytesReceived int64
	for {
		read, readErr := response.Body.Read(buffer)
		if read > 0 {
			bytesReceived += int64(read)
			if err := stream.Send(&pluginv1.ForwardResponse{Frame: &pluginv1.ForwardResponse_BodyChunk{BodyChunk: append([]byte(nil), buffer[:read]...)}}); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return sendForwardError(stream, "UPSTREAM_READ_ERROR", readErr.Error(), true)
		}
	}
	return stream.Send(&pluginv1.ForwardResponse{Frame: &pluginv1.ForwardResponse_End{End: &pluginv1.ForwardResponseEnd{BytesReceived: bytesReceived, DurationMs: time.Since(started).Milliseconds()}}})
}

func main() { pluginv1.Serve(&server{cfg: defaultConfig(), clients: make(map[string]*http.Client)}) }
