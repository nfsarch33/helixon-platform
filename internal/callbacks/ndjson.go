package callbacks

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"time"
)

// NDJSONHandler writes OnStart/OnEnd/OnError events as newline-delimited JSON.
type NDJSONHandler struct {
	mu  sync.Mutex
	f   *os.File
	enc *json.Encoder
}

// NewNDJSONHandler opens (or creates) the file at path for append-mode NDJSON writes.
func NewNDJSONHandler(path string) (*NDJSONHandler, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &NDJSONHandler{f: f, enc: json.NewEncoder(f)}, nil
}

func (h *NDJSONHandler) OnStart(ctx context.Context, info *RunInfo, input any) context.Context {
	h.write("start", info, "input", input, "")
	return ctx
}

func (h *NDJSONHandler) OnEnd(ctx context.Context, info *RunInfo, output any) context.Context {
	h.write("end", info, "output", output, "")
	return ctx
}

func (h *NDJSONHandler) OnError(ctx context.Context, info *RunInfo, err error) context.Context {
	h.write("error", info, "", nil, err.Error())
	return ctx
}

// Close flushes and closes the underlying file.
func (h *NDJSONHandler) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.f.Close()
}

func (h *NDJSONHandler) write(event string, info *RunInfo, payloadKey string, payload any, errMsg string) {
	rec := map[string]any{
		"event":          event,
		"component_name": info.ComponentName,
		"run_id":         info.RunID,
		"timestamp":      time.Now().UTC().Format(time.RFC3339Nano),
	}
	if payloadKey != "" && payload != nil {
		rec[payloadKey] = payload
	}
	if errMsg != "" {
		rec["error"] = errMsg
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	_ = h.enc.Encode(rec)
}
