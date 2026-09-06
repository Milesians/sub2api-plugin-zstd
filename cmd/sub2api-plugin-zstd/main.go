package main

import (
 "context"
 "encoding/json"
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

type config struct { Enabled bool `json:"enabled"`; Level int `json:"level"`; FallbackUncompressed bool `json:"fallback_uncompressed"` }
type server struct { pluginv1.UnimplementedTransportPluginServer; mu sync.RWMutex; cfg config }
func normalize(b []byte)(config,[]byte,error){ c:=config{Enabled:true,Level:3,FallbackUncompressed:true}; if len(b)>0 { d:=json.NewDecoder(strings.NewReader(string(b))); d.DisallowUnknownFields(); if err:=d.Decode(&c);err!=nil{return c,nil,err} }; if c.Level==0 {c.Level=3}; if c.Level<1||c.Level>22{return c,nil,fmt.Errorf("level must be 1..22")}; n,_:=json.Marshal(c); return c,n,nil }
func (s *server) GetInfo(context.Context,*pluginv1.GetInfoRequest)(*pluginv1.GetInfoResponse,error){return &pluginv1.GetInfoResponse{PluginId:"milesians.openai.oauth.zstd",PluginVersion:"0.1.0",ProtocolVersion:1,TransportApiVersion:1,Capabilities:[]string{"openai.oauth.outbound_transport.v1"}},nil}
func (s *server) Health(context.Context,*pluginv1.HealthRequest)(*pluginv1.HealthResponse,error){return &pluginv1.HealthResponse{Healthy:true,Message:"ready"},nil}
func (s *server) ValidateConfig(_ context.Context,r *pluginv1.ValidateConfigRequest)(*pluginv1.ValidateConfigResponse,error){c,n,e:=normalize(r.ConfigJson);_ = c;if e!=nil{return &pluginv1.ValidateConfigResponse{Message:e.Error()},nil};return &pluginv1.ValidateConfigResponse{Valid:true,NormalizedConfigJson:n},nil}
func (s *server) ApplyConfig(_ context.Context,r *pluginv1.ApplyConfigRequest)(*pluginv1.ApplyConfigResponse,error){c,_,e:=normalize(r.ConfigJson);if e!=nil{return &pluginv1.ApplyConfigResponse{Message:e.Error()},nil};s.mu.Lock();s.cfg=c;s.mu.Unlock();return &pluginv1.ApplyConfigResponse{Applied:true,Message:"applied"},nil}
func (s *server) TestConfig(context.Context,*pluginv1.TestConfigRequest)(*pluginv1.TestConfigResponse,error){return &pluginv1.TestConfigResponse{Success:true,Message:"zstd transport ready"},nil}
func (s *server) Forward(stream pluginv1.TransportPlugin_ForwardServer) error { var st *pluginv1.ForwardRequestStart; var body []byte; for { f,e:=stream.Recv(); if e==io.EOF{break}; if e!=nil{return e}; switch x:=f.Frame.(type){case *pluginv1.ForwardRequest_Start: st=x.Start; case *pluginv1.ForwardRequest_BodyChunk: body=append(body,x.BodyChunk...); case *pluginv1.ForwardRequest_BodyEnd: goto done} }; done: if st==nil{return fmt.Errorf("missing start")}; s.mu.RLock(); c:=s.cfg;s.mu.RUnlock(); target,err:=url.Parse(st.Url);if err!=nil{return err}; eligible:=c.Enabled&&st.Platform=="openai"&&st.AccountType=="oauth"&&strings.Contains(target.Host,"chatgpt.com")&&strings.Contains(target.Path,"/backend-api/codex/")&&strings.HasSuffix(target.Path,"/responses"); out:=body; if eligible { enc,e:=zstd.NewWriter(nil,zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(c.Level)));if e!=nil{return e};out=enc.EncodeAll(body,nil);enc.Close() }; req,e:=http.NewRequestWithContext(stream.Context(),st.Method,st.Url,strings.NewReader(string(out)));if e!=nil{return e}; for k,v:=range st.Headers {for _,x:=range v.Values {req.Header.Add(k,x)}};if eligible {req.Header.Del("Content-Encoding");req.Header.Set("Content-Encoding","zstd");req.Header.Set("Content-Type","application/json")}; resp,e:=http.DefaultClient.Do(req);if e!=nil{return e};defer resp.Body.Close(); h:=map[string]*pluginv1.HeaderValues{};for k,v:=range resp.Header {h[k]=&pluginv1.HeaderValues{Values:v}};if e=stream.Send(&pluginv1.ForwardResponse{Frame:&pluginv1.ForwardResponse_Start{Start:&pluginv1.ForwardResponseStart{StatusCode:int32(resp.StatusCode),Status:resp.Status,Protocol:resp.Proto,ProtocolMajor:int32(resp.ProtoMajor),ProtocolMinor:int32(resp.ProtoMinor),Headers:h}}});e!=nil{return e};buf:=make([]byte,32*1024);for {n,e:=resp.Body.Read(buf);if n>0 {if e2:=stream.Send(&pluginv1.ForwardResponse{Frame:&pluginv1.ForwardResponse_BodyChunk{BodyChunk:append([]byte(nil),buf[:n]...)}});e2!=nil{return e2}};if e==io.EOF{break};if e!=nil{return e}};return stream.Send(&pluginv1.ForwardResponse{Frame:&pluginv1.ForwardResponse_End{End:&pluginv1.ForwardResponseEnd{DurationMs:time.Now().UnixMilli()}}}) }
func main(){pluginv1.Serve(&server{cfg:config{Enabled:true,Level:3,FallbackUncompressed:true}})}
