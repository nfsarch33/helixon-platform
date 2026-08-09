// runx-public-repo-gate: allow-file fleet_host_alias,network_topology
// Package grpc holds the gRPC control plane for fleet-agent (v0.6.0 work).
//
// In v0.5.0 this file is a placeholder — the real client/server code is
// generated from control.proto once protoc + connect-go are wired up. The
// hand-written Service interface here mirrors the .proto so callers can
// reference Service and FutureService in v0.5.0 without waiting for
// codegen.
//
// To regenerate after v0.6.0:
//   protoc -I . --go_out=. --go_opt=paths=source_relative \
//     --connect-go_out=. --connect-go_opt=paths=source_relative \
//     internal/grpc/control.proto
package grpc

import (
	"context"
	"errors"
)

// ErrNotImplemented is returned by the v0.5.0 stub. The v0.6.0 connect-go
// generated code will override these methods with real RPC handlers.
var ErrNotImplemented = errors.New("grpc v0.6.0: not implemented in v0.5.0")

// HeartbeatRequest mirrors control.proto's HeartbeatRequest.
type HeartbeatRequest struct {
	NodeID    string
	Version   string
	HTTPAddr  string
	TsUnixNano int64
}

// HeartbeatResponse mirrors control.proto's HeartbeatResponse.
type HeartbeatResponse struct {
	NodeID    string
	TsUnixNano int64
}

// ExecRequest mirrors control.proto's ExecRequest.
type ExecRequest struct {
	Peer      string
	Signature []byte
	Body      []byte
}

// ExecResponse mirrors control.proto's ExecResponse.
type ExecResponse struct {
	ExitCode int32
	Stdout   string
	Stderr   string
}

// UploadRequest mirrors control.proto's UploadRequest.
type UploadRequest struct {
	Peer       string
	Signature  []byte
	RemotePath string
	Mode       uint64
	Data       []byte
}

// UploadResponse mirrors control.proto's UploadResponse.
type UploadResponse struct {
	RemotePath   string
	BytesWritten uint64
}

// DownloadRequest mirrors control.proto's DownloadRequest.
type DownloadRequest struct {
	Peer       string
	Signature  []byte
	RemotePath string
}

// DownloadChunk mirrors control.proto's DownloadChunk.
type DownloadChunk struct {
	Data []byte
}

// SysConfigRequest mirrors control.proto's SysConfigRequest.
type SysConfigRequest struct {
	Peer      string
	Signature []byte
	Key       string
	Value     []byte
}

// SysConfigResponse mirrors control.proto's SysConfigResponse.
type SysConfigResponse struct {
	Key   string
	Value []byte
}

// ControlServiceServer is the server-side interface for fleet-agent.
//
// v0.5.0 ships a stub implementation that returns ErrNotImplemented.
// v0.6.0 will replace this with connect-go generated code.
type ControlServiceServer interface {
	Heartbeat(context.Context, *HeartbeatRequest) (*HeartbeatResponse, error)
	Exec(context.Context, *ExecRequest) (*ExecResponse, error)
	Upload(context.Context, *UploadRequest) (*UploadResponse, error)
	Download(*DownloadRequest, ControlServiceDownloadServer) error
	SysConfig(context.Context, *SysConfigRequest) (*SysConfigResponse, error)
}

// ControlServiceDownloadServer is the server-side stream for Download.
type ControlServiceDownloadServer interface {
	Send(*DownloadChunk) error
	Context() context.Context
}

// UnimplementedControlServiceServer returns ErrNotImplemented for every RPC.
// Embed this in your server until v0.6.0 lands.
type UnimplementedControlServiceServer struct{}

func (UnimplementedControlServiceServer) Heartbeat(_ context.Context, _ *HeartbeatRequest) (*HeartbeatResponse, error) {
	return nil, ErrNotImplemented
}
func (UnimplementedControlServiceServer) Exec(_ context.Context, _ *ExecRequest) (*ExecResponse, error) {
	return nil, ErrNotImplemented
}
func (UnimplementedControlServiceServer) Upload(_ context.Context, _ *UploadRequest) (*UploadResponse, error) {
	return nil, ErrNotImplemented
}
func (UnimplementedControlServiceServer) Download(_ *DownloadRequest, _ ControlServiceDownloadServer) error {
	return ErrNotImplemented
}
func (UnimplementedControlServiceServer) SysConfig(_ context.Context, _ *SysConfigRequest) (*SysConfigResponse, error) {
	return nil, ErrNotImplemented
}
