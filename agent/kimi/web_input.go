package kimi

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/chenhg5/cc-connect/core"
)

type webUpload struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Size      int64  `json:"size"`
}

type webPrompt struct {
	ID     string `json:"prompt_id"`
	Status string `json:"status"`
}

func (s *webSession) promptContent(prompt string, images []core.ImageAttachment, files []core.FileAttachment) ([]map[string]any, []string, error) {
	content := []map[string]any{{"type": "text", "text": prompt}}
	for _, img := range images {
		mediaType := img.MimeType
		if mediaType == "" {
			mediaType = http.DetectContentType(img.Data)
		}
		content = append(content, map[string]any{"type": "image", "source": map[string]string{"kind": "base64", "media_type": mediaType, "data": base64.StdEncoding.EncodeToString(img.Data)}})
	}
	var uploads []string
	for _, file := range files {
		upload, err := s.agent.api.upload(s.ctx, file)
		if err != nil {
			s.discardUploads(uploads)
			return nil, nil, err
		}
		uploads = append(uploads, upload.ID)
		content = append(content, map[string]any{"type": "file", "file_id": upload.ID, "name": upload.Name, "media_type": upload.MediaType, "size": upload.Size})
	}
	return content, uploads, nil
}

func (a *webAPI) upload(ctx context.Context, file core.FileAttachment) (webUpload, error) {
	var out webUpload
	name := path.Base(strings.ReplaceAll(file.FileName, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	if name == "" || name == "." || name == ".." || name == "/" {
		name = "attachment"
	}
	mediaType := file.MimeType
	if mediaType == "" {
		mediaType = http.DetectContentType(file.Data)
	}
	parsed, params, err := mime.ParseMediaType(mediaType)
	if err != nil {
		return out, fmt.Errorf("kimi web: invalid attachment content type: %w", err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": name}))
	header.Set("Content-Type", mime.FormatMediaType(parsed, params))
	part, err := writer.CreatePart(header)
	if err != nil {
		return out, fmt.Errorf("kimi web: create upload: %w", err)
	}
	if _, err = part.Write(file.Data); err != nil {
		return out, fmt.Errorf("kimi web: write upload: %w", err)
	}
	if err = writer.WriteField("name", name); err != nil {
		return out, fmt.Errorf("kimi web: upload filename: %w", err)
	}
	if err = writer.Close(); err != nil {
		return out, fmt.Errorf("kimi web: finish upload: %w", err)
	}
	if err = a.requestBody(ctx, "POST", "/api/v1/files", writer.FormDataContentType(), &body, &out); err != nil {
		return out, fmt.Errorf("kimi web: upload file: %w", err)
	}
	if out.ID == "" {
		return out, fmt.Errorf("kimi web: upload response missing file ID")
	}
	if out.Name == "" {
		out.Name = name
	}
	if out.MediaType == "" {
		out.MediaType = mediaType
	}
	return out, nil
}

// Delete only uploads known to be unreferenced, before prompt submission.
func (s *webSession) discardUploads(ids []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, id := range ids {
		if err := s.agent.api.request(ctx, "DELETE", "/api/v1/files/"+url.PathEscape(id), nil, nil); err != nil {
			slog.Warn("kimi web: clean up unsubmitted upload", "error", err)
		}
	}
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
