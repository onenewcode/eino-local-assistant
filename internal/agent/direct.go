package agent

import (
	"context"
	"errors"

	"eino-local-assistant/internal/chat"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// DirectModel adapts a tool-free Eino model to the session streaming contract.
type DirectModel struct {
	model model.BaseChatModel
}

// NewDirectModel creates a model path that neither binds nor executes tools.
func NewDirectModel(chatModel model.BaseChatModel) (*DirectModel, error) {
	if chatModel == nil {
		return nil, errors.New("chat model is required")
	}
	return &DirectModel{model: chatModel}, nil
}

func (m *DirectModel) Stream(ctx context.Context, messages []*schema.Message) (chat.Stream, error) {
	if m == nil || m.model == nil {
		return nil, errors.New("chat model is required")
	}
	return m.model.Stream(ctx, messages)
}
