package kimi

import (
	"encoding/base64"
	"fmt"
	"net/http"

	"github.com/chenhg5/cc-connect/core"
)

type webPrompt struct {
	ID     string `json:"prompt_id"`
	Status string `json:"status"`
}

func (s *webSession) promptContent(prompt string, images []core.ImageAttachment, files []core.FileAttachment) ([]map[string]any, error) {
	if len(files) > 0 {
		return nil, fmt.Errorf("kimi web: file attachments are not supported")
	}
	content := []map[string]any{{"type": "text", "text": prompt}}
	for _, img := range images {
		mediaType := img.MimeType
		if mediaType == "" {
			mediaType = http.DetectContentType(img.Data)
		}
		content = append(content, map[string]any{"type": "image", "source": map[string]string{"kind": "base64", "media_type": mediaType, "data": base64.StdEncoding.EncodeToString(img.Data)}})
	}
	return content, nil
}

func (s *webSession) promptBody(id string, content []map[string]any) (map[string]any, error) {
	body := map[string]any{"content": content, "prompt_id": id}
	// Preserve existing session settings. Kimi 2.2 requires a first-input model
	// when a new/cold session still has an empty model.
	var record webRecord
	if err := s.agent.api.request(s.ctx, "GET", webSessionPath(s.id), nil, &record); err != nil {
		return nil, err
	}
	if record.AgentConfig.Model == "" {
		var config struct {
			Model string `json:"default_model"`
		}
		if err := s.agent.api.request(s.ctx, "GET", "/api/v1/config", nil, &config); err != nil {
			return nil, err
		}
		if config.Model != "" {
			body["model"] = config.Model
		}
	}
	return body, nil
}
func (s *webSession) postPrompt(body map[string]any) (webPrompt, error) {
	var result webPrompt
	err := s.agent.api.request(s.ctx, "POST", webSessionPath(s.id)+"/prompts", body, &result)
	return result, err
}
