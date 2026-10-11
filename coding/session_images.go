package coding

import (
	"context"

	"pi/agent"
	"pi/ai"
	"pi/internal/imageprocessing"
)

// prepareToolResult runs after every extension hook; a hook may change the
// active model or replace images. Failed processing preserves the source image.
func (s *Session) prepareToolResult(_ context.Context, result agent.AgentToolResult) agent.AgentToolResult {
	var options *ai.ModelImageResizeOptions
	if model := s.agent.Model(); model != nil && model.InputLimits != nil && model.InputLimits.Images != nil {
		options = model.InputLimits.Images.Resize
	}
	return imageprocessing.NormalizeToolResultImagesWithOptions(result, s.services.SettingsManager().GetImageAutoResize(), options)
}
