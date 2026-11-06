package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
)

// 这些路由是浏览器和 demo.sh 的唯一入口。控制面自己不存任务，全部转给托管项目的节点。

func (h *Hub) board(w http.ResponseWriter, r *http.Request) {
	projectID := r.URL.Query().Get("project")
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "invalid_argument", "缺少 project 参数")
		return
	}
	h.forward(w, r, projectID, "board", map[string]any{"projectId": projectID})
}

func (h *Hub) createCard(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	projectID := stringField(body, "projectId")
	title := stringField(body, "title")
	if projectID == "" || title == "" {
		writeError(w, http.StatusBadRequest, "invalid_argument", "projectId 和 title 都要有")
		return
	}
	h.forward(w, r, projectID, "card.create", map[string]any{
		"projectId":   projectID,
		"title":       title,
		"description": stringField(body, "description"),
	})
}

func (h *Hub) requestPlan(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	projectID := stringField(body, "projectId")
	taskID := stringField(body, "taskId")
	if projectID == "" || taskID == "" {
		writeError(w, http.StatusBadRequest, "invalid_argument", "projectId 和 taskId 都要有")
		return
	}
	h.forward(w, r, projectID, "plan.request", map[string]any{"projectId": projectID, "taskId": taskID})
}

func (h *Hub) decidePlan(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	projectID := stringField(body, "projectId")
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "invalid_argument", "缺少 projectId")
		return
	}
	h.forward(w, r, projectID, "plan.decide", map[string]any{
		"approvalId": r.PathValue("id"),
		"decision":   stringField(body, "decision"),
	})
}

func (h *Hub) dispatchTask(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	projectID := stringField(body, "projectId")
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "invalid_argument", "缺少 projectId")
		return
	}
	h.forward(w, r, projectID, "task.dispatch", map[string]any{"taskId": r.PathValue("id")})
}

func (h *Hub) reviewTask(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeBody(w, r)
	if !ok {
		return
	}
	projectID := stringField(body, "projectId")
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "invalid_argument", "缺少 projectId")
		return
	}
	h.forward(w, r, projectID, "task.review", map[string]any{
		"taskId":   r.PathValue("id"),
		"decision": stringField(body, "decision"),
		"note":     stringField(body, "note"),
	})
}

// forward 把一次请求转给节点，把结果原样写回去。
func (h *Hub) forward(w http.ResponseWriter, r *http.Request, projectID, method string, params map[string]any) {
	result, err := h.callNode(r.Context(), projectID, method, params)
	if err != nil {
		status, code := rpcStatus(err)
		log.Printf("%s 失败：%v", method, err)
		writeError(w, status, code, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if _, err := w.Write(result); err != nil {
		log.Printf("写响应失败：%v", err)
	}
}

func decodeBody(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "读不出请求体")
		return nil, false
	}
	if len(raw) == 0 {
		return map[string]any{}, true
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "请求体不是 JSON 对象")
		return nil, false
	}
	return body, true
}

func stringField(body map[string]any, key string) string {
	value, ok := body[key].(string)
	if !ok {
		return ""
	}
	return value
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
}
