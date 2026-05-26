package llm

import (
	"context"
	"strings"
	"sync"
)

// MockProvider is a mock implementation of the Provider interface for testing.
type MockProvider struct {
	mu            sync.Mutex
	Responses     map[string]*CompletionResponse
	DefaultResp   *CompletionResponse
	Requests      []CompletionRequest
	ErrorToReturn error
}

// NewMockProvider creates a new MockProvider.
func NewMockProvider() *MockProvider {
	return &MockProvider{
		Responses: make(map[string]*CompletionResponse),
		DefaultResp: &CompletionResponse{
			Choices: []Choice{
				{
					Index: 0,
					Message: Message{
						Role:    "assistant",
						Content: "Mock response",
					},
				},
			},
		},
	}
}

// Complete records the request and returns a mock response.
func (m *MockProvider) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.Requests = append(m.Requests, req)

	if m.ErrorToReturn != nil {
		return nil, m.ErrorToReturn
	}

	// Try to find a specific response based on the last message content
	if len(req.Messages) > 0 {
		lastMsg := req.Messages[len(req.Messages)-1].Content
		for key, resp := range m.Responses {
			if strings.Contains(lastMsg, key) {
				return resp, nil
			}
		}
	}

	return m.DefaultResp, nil
}

// AddResponse adds a specific response for a given prompt keyword.
func (m *MockProvider) AddResponse(keyword string, content string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Responses[keyword] = &CompletionResponse{
		Choices: []Choice{
			{
				Index: 0,
				Message: Message{
					Role:    "assistant",
					Content: content,
				},
			},
		},
	}
}
