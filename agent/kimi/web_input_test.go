package kimi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestWebFilesUploadThenSubmitWithImages(t *testing.T) {
	f := newWebFixture(t)
	original := f.server.Config.Handler
	var mu sync.Mutex
	var uploads []string
	var content []map[string]any
	f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("missing token")
			http.Error(w, "unauthorized", 401)
			return
		}
		if r.Method == "POST" && r.URL.Path == "/api/v1/files" {
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
				http.Error(w, "bad upload", 400)
				return
			}
			defer r.MultipartForm.RemoveAll()
			file, h, err := r.FormFile("file")
			if err != nil {
				t.Error(err)
				return
			}
			defer file.Close()
			data, err := io.ReadAll(file)
			if err != nil {
				t.Error(err)
				return
			}
			if h.Filename != "报告.txt" || r.FormValue("name") != "报告.txt" || h.Header.Get("Content-Type") != "text/plain" {
				t.Errorf("unexpected upload metadata: %+v %s", h, r.FormValue("name"))
			}
			mu.Lock()
			uploads = append(uploads, string(data))
			id := fmt.Sprintf("file_%d", len(uploads))
			mu.Unlock()
			f.reply(w, map[string]any{"id": id, "name": h.Filename, "media_type": "text/plain", "size": len(data)})
			return
		}
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/prompts") {
			var body struct {
				ID      string           `json:"prompt_id"`
				Content []map[string]any `json:"content"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			content = body.Content
			mu.Unlock()
			f.reply(w, map[string]any{"prompt_id": body.ID, "status": "queued"})
			return
		}
		original.ServeHTTP(w, r)
	})
	s := f.attach()
	_, err := s.SendTurn("inspect both", "message", []core.ImageAttachment{{MimeType: "image/png", Data: []byte("image")}}, []core.FileAttachment{
		{FileName: "../../报告.txt", MimeType: "text/plain", Data: []byte("first file")},
		{FileName: "C:\\folder\\报告.txt", MimeType: "text/plain", Data: []byte("second file")},
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(uploads) != 2 || uploads[0] != "first file" || uploads[1] != "second file" {
		t.Fatal(uploads)
	}
	if len(content) != 4 || content[0]["text"] != "inspect both" || content[1]["type"] != "image" {
		t.Fatal(content)
	}
	for i, p := range content[2:] {
		if p["type"] != "file" || p["file_id"] != fmt.Sprintf("file_%d", i+1) || p["name"] != "报告.txt" {
			t.Fatal(p)
		}
	}
}

func TestWebFileUploadFailureDoesNotSendPartialPrompt(t *testing.T) {
	f := newWebFixture(t)
	original := f.server.Config.Handler
	var mu sync.Mutex
	uploads, prompts, deletes := 0, 0, 0
	f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/v1/files" {
			mu.Lock()
			uploads++
			n := uploads
			mu.Unlock()
			if n == 2 {
				http.Error(w, "upload failed", 500)
			} else {
				f.reply(w, map[string]any{"id": "first-upload", "name": "one.txt", "media_type": "text/plain", "size": 3})
			}
			return
		}
		if r.Method == "DELETE" && r.URL.Path == "/api/v1/files/first-upload" {
			mu.Lock()
			deletes++
			mu.Unlock()
			f.reply(w, map[string]any{"deleted": true})
			return
		}
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/prompts") {
			mu.Lock()
			prompts++
			mu.Unlock()
		}
		original.ServeHTTP(w, r)
	})
	s := f.attach()
	err := s.Send("", "message", nil, []core.FileAttachment{{FileName: "one.txt", Data: []byte("one")}, {FileName: "two.txt", Data: []byte("two")}})
	if err == nil {
		t.Fatal("failed upload accepted")
	}
	mu.Lock()
	defer mu.Unlock()
	if uploads != 2 || prompts != 0 || deletes != 1 {
		t.Fatalf("uploads=%d prompts=%d deletes=%d", uploads, prompts, deletes)
	}
}

func TestWebSteerNativeOutcomesNeverResubmit(t *testing.T) {
	for _, tc := range []struct {
		name string
		want core.SteerOutcome
	}{
		{"inserted", core.SteerInserted}, {"queued", core.SteerQueued}, {"running", core.SteerRunning},
		{"started-before-inject", core.SteerRunning}, {"processed", core.SteerProcessed}, {"unknown", core.SteerUnconfirmed},
		{"submission-lost", core.SteerUnconfirmed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWebFixture(t)
			original := f.server.Config.Handler
			var mu sync.Mutex
			id := ""
			posts, injects := 0, 0
			f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/prompts") {
					var body webPrompt
					var raw map[string]any
					if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
						t.Error(err)
						return
					}
					body.ID, _ = raw["prompt_id"].(string)
					mu.Lock()
					posts++
					id = body.ID
					mu.Unlock()
					if tc.name == "submission-lost" {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						conn.Close()
						return
					}
					status := "queued"
					if tc.name == "running" {
						status = "running"
					}
					f.reply(w, map[string]any{"prompt_id": body.ID, "status": status})
					return
				}
				if r.Method == "POST" && strings.HasSuffix(r.URL.Path, ":steer") {
					mu.Lock()
					injects++
					current := id
					mu.Unlock()
					if r.URL.Path != webSessionPath("session_test")+"/prompts/"+current+":steer" {
						t.Errorf("steered another prompt: %s", r.URL.Path)
					}
					if r.Header.Get("Content-Type") != "" {
						t.Error("empty action request must omit JSON content type")
					}
					if tc.name == "inserted" {
						f.reply(w, map[string]any{"steered": true, "prompt_ids": []string{current}})
					} else {
						json.NewEncoder(w).Encode(map[string]any{"code": 40402, "msg": "not queued", "data": nil})
					}
					return
				}
				if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/prompts") {
					mu.Lock()
					current := id
					mu.Unlock()
					var active any
					queued := []any{}
					if tc.name == "started-before-inject" {
						active = map[string]string{"prompt_id": current, "status": "running"}
					}
					if tc.name == "queued" {
						queued = append(queued, map[string]string{"prompt_id": current, "status": "queued"})
					}
					f.reply(w, map[string]any{"active": active, "queued": queued})
					return
				}
				if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/messages") && tc.name == "processed" {
					mu.Lock()
					current := id
					mu.Unlock()
					f.reply(w, map[string]any{"items": []any{map[string]any{"id": "m", "prompt_id": current, "role": "user", "content": []any{map[string]string{"type": "text", "text": "adjust"}}}}, "has_more": false})
					return
				}
				original.ServeHTTP(w, r)
			})
			s := f.attach()
			got, err := s.SteerCurrent("adjust")
			if err != nil || got != tc.want {
				t.Fatalf("got %s %v; want %s", got, err, tc.want)
			}
			mu.Lock()
			defer mu.Unlock()
			if posts != 1 {
				t.Fatalf("submitted %d times", posts)
			}
			wantInject := 1
			if tc.name == "running" || tc.name == "submission-lost" {
				wantInject = 0
			}
			if injects != wantInject {
				t.Fatalf("injected %d times", injects)
			}
		})
	}
}

func TestWebFilesRetainedWhenPromptSubmissionUncertain(t *testing.T) {
	f := newWebFixture(t)
	original := f.server.Config.Handler
	var mu sync.Mutex
	posts, deletes := 0, 0
	f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/api/v1/files" {
			f.reply(w, map[string]any{"id": "uploaded", "name": "log.txt", "media_type": "text/plain", "size": 3})
			return
		}
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/prompts") {
			mu.Lock()
			posts++
			mu.Unlock()
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
			return
		}
		if r.Method == "DELETE" {
			mu.Lock()
			deletes++
			mu.Unlock()
		}
		original.ServeHTTP(w, r)
	})
	s := f.attach()
	if err := s.Send("inspect", "message", nil, []core.FileAttachment{{FileName: "log.txt", Data: []byte("log")}}); err == nil {
		t.Fatal("lost response not reported")
	}
	mu.Lock()
	defer mu.Unlock()
	if posts != 1 || deletes != 0 {
		t.Fatalf("resubmitted or deleted possibly referenced file: posts=%d deletes=%d", posts, deletes)
	}
}
