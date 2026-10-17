package kimi

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

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
