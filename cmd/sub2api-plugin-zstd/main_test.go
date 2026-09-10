package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Milesians/sub2api-plugin-zstd/internal/pluginv1"
	"github.com/klauspost/compress/zstd"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestNormalizeConfig(t *testing.T) {
	c, raw, err := normalize(nil)
	if err != nil || c.Level != 3 || string(raw) != `{"enabled":true,"level":3,"fallback_uncompressed":true}` {
		t.Fatalf("defaults: %#v %s %v", c, raw, err)
	}
	if _, _, err := normalize([]byte(`{"enabled":true,"level":3,"unknown":1}`)); err == nil {
		t.Fatal("unknown field accepted")
	}
	if _, _, err := normalize([]byte(`{"level":23}`)); err == nil {
		t.Fatal("invalid level accepted")
	}
	if _, _, err := normalize([]byte(`null`)); err == nil {
		t.Fatal("null config accepted")
	}
	if _, _, err := normalize([]byte(`{"level":3} {}`)); err == nil {
		t.Fatal("trailing JSON accepted")
	}
}

func TestConfigCompressionSelfTest(t *testing.T) {
	s := &server{cfg: defaultConfig()}
	for _, tc := range []struct {
		name, config, message string
		success               bool
	}{
		{"default", `{}`, "解压一致", true},
		{"custom level", `{"level":1}`, "级别 1", true},
		{"disabled", `{"enabled":false}`, "压缩未启用", false},
		{"invalid", `{"level":23}`, "level must be", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := s.TestConfig(context.Background(), &pluginv1.TestConfigRequest{ConfigJson: []byte(tc.config)})
			if err != nil || result.Success != tc.success || !strings.Contains(result.Message, tc.message) {
				t.Fatalf("result=%v err=%v", result, err)
			}
			if s.cfg != defaultConfig() {
				t.Fatal("self-test modified active config")
			}
		})
	}
}

// Route the real Forward HTTP client to a local TLS upstream while retaining
// the eligible chatgpt.com URL. No OAuth credentials or external network needed.
func TestForwardCompressedHTTPRoundTrip(t *testing.T) {
	body := []byte(strings.Repeat(`{"input":"hello world"}`, 256))
	const responseBody = "data: {\"type\":\"response.completed\"}\n\n"
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "chatgpt.com" || r.Header.Get("Content-Encoding") != "zstd" {
			t.Errorf("host=%q encoding=%q", r.Host, r.Header.Get("Content-Encoding"))
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		if r.ContentLength != int64(len(raw)) || len(raw) >= len(body) {
			t.Errorf("content_length=%d compressed=%d original=%d", r.ContentLength, len(raw), len(body))
		}
		decoder, err := zstd.NewReader(nil)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		defer decoder.Close()
		decoded, err := decoder.DecodeAll(raw, nil)
		if err != nil || !bytes.Equal(decoded, body) {
			t.Errorf("decoded body mismatch: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, responseBody)
	}))
	defer upstream.Close()
	client := upstream.Client()
	transport := client.Transport.(*http.Transport)
	transport.Proxy = nil
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.ServerName = "example.com" // httptest certificate identity
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}
	s := &server{cfg: defaultConfig(), clients: map[string]*http.Client{"": client}}
	stream := &testForwardStream{ctx: context.Background(), requests: []*pluginv1.ForwardRequest{
		{Frame: &pluginv1.ForwardRequest_Start{Start: &pluginv1.ForwardRequestStart{Method: http.MethodPost, Url: "https://chatgpt.com/backend-api/codex/responses", Platform: "openai", AccountType: "oauth", ContentLength: int64(len(body)), HasBody: true}}},
		{Frame: &pluginv1.ForwardRequest_BodyChunk{BodyChunk: body[:100]}},
		{Frame: &pluginv1.ForwardRequest_BodyChunk{BodyChunk: body[100:]}},
		{Frame: &pluginv1.ForwardRequest_BodyEnd{BodyEnd: true}},
	}}
	if err := s.Forward(stream); err != nil {
		t.Fatal(err)
	}
	var received bytes.Buffer
	for _, frame := range stream.responses {
		if frame.GetError() != nil {
			t.Fatal(frame.GetError())
		}
		received.Write(frame.GetBodyChunk())
	}
	if len(stream.responses) < 3 || stream.responses[0].GetStart().GetStatusCode() != 200 || stream.responses[len(stream.responses)-1].GetEnd() == nil || received.String() != responseBody {
		t.Fatalf("incomplete response: %v", stream.responses)
	}
}

type captureTransport struct {
	body          []byte
	encoding      string
	contentLength int64
	wireLength    int
}

func (t *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.encoding = req.Header.Get("Content-Encoding")
	t.contentLength = req.ContentLength
	var err error
	if t.encoding == "zstd" {
		raw, e := io.ReadAll(req.Body)
		if e != nil {
			return nil, e
		}
		t.wireLength = len(raw)
		decoder, e := zstd.NewReader(bytes.NewReader(raw))
		if e != nil {
			return nil, e
		}
		t.body, err = io.ReadAll(decoder)
		decoder.Close()
	} else {
		t.body, err = io.ReadAll(req.Body)
	}
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: 200, Status: "200 OK", Proto: "HTTP/1.1", ProtoMajor: 1, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok")), Request: req}, nil
}

func TestForwardCompressesEligibleRequest(t *testing.T) {
	transport := &captureTransport{}
	s := &server{cfg: defaultConfig(), clients: map[string]*http.Client{"": {Transport: transport}}}
	stream := &testForwardStream{ctx: context.Background(), requests: []*pluginv1.ForwardRequest{
		{Frame: &pluginv1.ForwardRequest_Start{Start: &pluginv1.ForwardRequestStart{Method: http.MethodPost, Url: "https://chatgpt.com/backend-api/codex/abc/responses", Platform: "openai", AccountType: "oauth", ContentLength: 11, HasBody: true}}},
		{Frame: &pluginv1.ForwardRequest_BodyChunk{BodyChunk: []byte("hello ")}},
		{Frame: &pluginv1.ForwardRequest_BodyChunk{BodyChunk: []byte("world")}},
		{Frame: &pluginv1.ForwardRequest_BodyEnd{BodyEnd: true}},
	}}
	if err := s.Forward(stream); err != nil {
		t.Fatal(err)
	}
	if transport.encoding != "zstd" || !bytes.Equal(transport.body, []byte("hello world")) || transport.contentLength != int64(transport.wireLength) {
		t.Fatalf("encoding=%q content_length=%d wire_length=%d body=%q", transport.encoding, transport.contentLength, transport.wireLength, transport.body)
	}
}

func TestCompressRequestBodyUsesCodexDefaults(t *testing.T) {
	compressed, err := compressRequestBody([]byte("hello world"), codexZstdLevel)
	if err != nil {
		t.Fatal(err)
	}
	decoder, err := zstd.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	decompressed, err := io.ReadAll(decoder)
	decoder.Close()
	if err != nil || !bytes.Equal(decompressed, []byte("hello world")) {
		t.Fatalf("decompressed=%q err=%v", decompressed, err)
	}
	if len(compressed) < 5 || !bytes.Equal(compressed[:4], []byte{0x28, 0xb5, 0x2f, 0xfd}) || compressed[4]&0x04 != 0 {
		t.Fatalf("unexpected Codex-compatible frame header: %x", compressed)
	}
}

type testForwardStream struct {
	ctx       context.Context
	requests  []*pluginv1.ForwardRequest
	responses []*pluginv1.ForwardResponse
}

func (s *testForwardStream) Send(v *pluginv1.ForwardResponse) error {
	s.responses = append(s.responses, v)
	return nil
}
func (s *testForwardStream) Recv() (*pluginv1.ForwardRequest, error) {
	if len(s.requests) == 0 {
		return nil, io.EOF
	}
	v := s.requests[0]
	s.requests = s.requests[1:]
	return v, nil
}
func (s *testForwardStream) SetHeader(metadata.MD) error  { return nil }
func (s *testForwardStream) SendHeader(metadata.MD) error { return nil }
func (s *testForwardStream) SetTrailer(metadata.MD)       {}
func (s *testForwardStream) Context() context.Context     { return s.ctx }
func (s *testForwardStream) SendMsg(any) error            { return nil }
func (s *testForwardStream) RecvMsg(any) error            { return nil }

var _ grpc.ServerStream = (*testForwardStream)(nil)

func TestForwardStreamsBodyAndResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if string(body) != "hello world" {
			t.Errorf("body = %q", body)
		}
		w.Header().Add("X-Test", "one")
		w.Header().Add("X-Test", "two")
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()
	s := &server{cfg: defaultConfig(), clients: map[string]*http.Client{"": upstream.Client()}}
	stream := &testForwardStream{ctx: context.Background(), requests: []*pluginv1.ForwardRequest{
		{Frame: &pluginv1.ForwardRequest_Start{Start: &pluginv1.ForwardRequestStart{Method: http.MethodPost, Url: upstream.URL, ContentLength: 11, HasBody: true}}},
		{Frame: &pluginv1.ForwardRequest_BodyChunk{BodyChunk: []byte("hello ")}},
		{Frame: &pluginv1.ForwardRequest_BodyChunk{BodyChunk: []byte("world")}},
		{Frame: &pluginv1.ForwardRequest_BodyEnd{BodyEnd: true}},
	}}
	if err := s.Forward(stream); err != nil {
		t.Fatal(err)
	}
	if len(stream.responses) != 3 || stream.responses[0].GetStart() == nil || string(stream.responses[1].GetBodyChunk()) != "ok" || stream.responses[2].GetEnd() == nil {
		t.Fatalf("unexpected response frames: %#v", stream.responses)
	}
	if got := stream.responses[0].GetStart().Headers["X-Test"].Values; len(got) != 2 {
		t.Fatalf("headers = %#v", got)
	}
}

func TestEligibleRequest(t *testing.T) {
	base := &pluginv1.ForwardRequestStart{Method: http.MethodPost, Platform: "openai", AccountType: "oauth", Url: "https://chatgpt.com/backend-api/codex/abc/responses"}
	if !eligibleRequest(defaultConfig(), base) {
		t.Fatal("eligible request rejected")
	}
	for _, mutate := range []func(*pluginv1.ForwardRequestStart){
		func(s *pluginv1.ForwardRequestStart) { s.Method = http.MethodGet },
		func(s *pluginv1.ForwardRequestStart) {
			s.Url = "https://evilchatgpt.com/backend-api/codex/abc/responses"
		},
		func(s *pluginv1.ForwardRequestStart) { s.Url = "https://chatgpt.com/backend-api/other/responses" },
		func(s *pluginv1.ForwardRequestStart) { s.Platform = "anthropic" },
	} {
		copy := *base
		mutate(&copy)
		if eligibleRequest(defaultConfig(), &copy) {
			t.Fatalf("invalid request accepted: %#v", copy)
		}
	}
}
