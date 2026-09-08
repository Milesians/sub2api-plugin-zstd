package main

import (
	"bytes"
	"context"
	"io"
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

type captureTransport struct {
	body     []byte
	encoding string
}

func (t *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.encoding = req.Header.Get("Content-Encoding")
	var err error
	if t.encoding == "zstd" {
		decoder, e := zstd.NewReader(req.Body)
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
	if transport.encoding != "zstd" || !bytes.Equal(transport.body, []byte("hello world")) {
		t.Fatalf("encoding=%q body=%q", transport.encoding, transport.body)
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
