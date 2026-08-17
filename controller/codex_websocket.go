package controller

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/service/relayconvert"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

const (
	codexWebsocketMessageLimit = 16 << 20
	codexWebsocketStateLimit   = 16 << 20
	codexWebsocketIdleTimeout  = 10 * time.Minute
	codexWebsocketWriteTimeout = 30 * time.Second
)

var (
	codexWebsocketUpgrader = websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin: func(_ *http.Request) bool {
			return true
		},
	}
)

// CodexResponsesWebsocket implements the Codex Responses websocket contract on
// the isolated /codex route. Every generated turn is still submitted through
// the existing local /v1/responses route, preserving its routing and billing.
func CodexResponsesWebsocket(c *gin.Context) {
	conn, err := codexWebsocketUpgrader.Upgrade(c.Writer, c.Request, codexWebsocketUpgradeHeaders(c.Request))
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(codexWebsocketMessageLimit)
	_ = conn.SetReadDeadline(time.Now().Add(codexWebsocketIdleTimeout))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(codexWebsocketIdleTimeout))
	})

	state := &relayconvert.CodexResponsesWebsocketState{}
	for {
		messageType, payload, readErr := conn.ReadMessage()
		if readErr != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(codexWebsocketIdleTimeout))
		if messageType != websocket.TextMessage && messageType != websocket.BinaryMessage {
			continue
		}

		snapshot := state.Snapshot()
		prewarm := !state.HasRequest() && isCodexWebsocketPrewarm(payload)
		requestBody, normalizeErr := state.NormalizeWebsocketRequest(payload, codexWebsocketStateLimit)
		if normalizeErr != nil {
			if writeCodexWebsocketError(conn, http.StatusBadRequest, "invalid_request_error", normalizeErr) != nil {
				return
			}
			continue
		}
		var requestMeta struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(requestBody, &requestMeta) != nil || !isCodexConversationModel(dto.OpenAIModels{Id: requestMeta.Model}, codexTextPricingProvider()) {
			state.Restore(snapshot)
			if writeCodexWebsocketError(conn, http.StatusBadRequest, "model_not_supported", fmt.Errorf("model %q is not a Codex conversation model", requestMeta.Model)) != nil {
				return
			}
			continue
		}
		if prewarm {
			if writeCodexWebsocketPrewarm(conn, state, requestBody) != nil {
				return
			}
			continue
		}

		if processErr := processCodexWebsocketTurn(c, conn, state, requestBody); processErr != nil {
			state.Restore(snapshot)
			if writeCodexWebsocketError(conn, processErr.status, processErr.code, processErr.err) != nil {
				return
			}
			continue
		}
		if state.StateBytes() > codexWebsocketStateLimit {
			_ = conn.WriteControl(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseMessageTooBig, "conversation state requires compaction"),
				time.Now().Add(codexWebsocketWriteTimeout),
			)
			return
		}
	}
}

func isCodexWebsocketPrewarm(payload []byte) bool {
	var request struct {
		Type     string `json:"type"`
		Generate *bool  `json:"generate"`
	}
	return json.Unmarshal(payload, &request) == nil && request.Type == "response.create" && request.Generate != nil && !*request.Generate
}

func writeCodexWebsocketPrewarm(conn *websocket.Conn, state *relayconvert.CodexResponsesWebsocketState, requestBody []byte) error {
	var request struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(requestBody, &request)
	responseID := fmt.Sprintf("resp_prewarm_%d", time.Now().UnixNano())
	createdAt := time.Now().Unix()
	created, _ := json.Marshal(gin.H{
		"type": "response.created", "sequence_number": 0,
		"response": gin.H{"id": responseID, "object": "response", "created_at": createdAt, "status": "in_progress", "model": request.Model, "output": []any{}},
	})
	completed, _ := json.Marshal(gin.H{
		"type": "response.completed", "sequence_number": 1,
		"response": gin.H{"id": responseID, "object": "response", "created_at": createdAt, "status": "completed", "model": request.Model, "output": []any{}, "usage": gin.H{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}},
	})
	if err := writeCodexWebsocketPayload(conn, created); err != nil {
		return err
	}
	state.ObserveWebsocketEvent(completed, make(map[int]json.RawMessage), &[]json.RawMessage{})
	return writeCodexWebsocketPayload(conn, completed)
}

type codexWebsocketTurnError struct {
	status int
	code   string
	err    error
}

func processCodexWebsocketTurn(c *gin.Context, conn *websocket.Conn, state *relayconvert.CodexResponsesWebsocketState, requestBody []byte) *codexWebsocketTurnError {
	upstreamBody, err := relayconvert.NormalizeCodexResponsesRequest(requestBody)
	if err != nil {
		return &codexWebsocketTurnError{http.StatusBadRequest, "invalid_request_error", err}
	}
	innerReq, err := newCodexInternalRequest(c, http.MethodPost, "/responses", bytes.NewReader(upstreamBody))
	if err != nil {
		return &codexWebsocketTurnError{http.StatusInternalServerError, "internal_request_failed", err}
	}
	innerReq.Header.Set("Content-Type", "application/json")

	resp, err := codexInternalHTTPClient.Do(innerReq)
	if err != nil {
		return &codexWebsocketTurnError{http.StatusBadGateway, "upstream_request_failed", fmt.Errorf("native Responses request failed: %w", err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return codexWebsocketHTTPError(resp)
	}

	if strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		return forwardCodexWebsocketSSE(conn, state, requestBody, resp.Body)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, codexWebsocketMessageLimit+1))
	if err != nil {
		return &codexWebsocketTurnError{http.StatusBadGateway, "invalid_upstream_response", err}
	}
	if len(body) > codexWebsocketMessageLimit {
		return &codexWebsocketTurnError{http.StatusBadGateway, "response_too_large", fmt.Errorf("upstream response exceeds websocket message limit")}
	}
	restored, err := relayconvert.RestoreCodexResponsesPayload(requestBody, body)
	if err != nil {
		return &codexWebsocketTurnError{http.StatusBadGateway, "invalid_upstream_response", err}
	}
	state.ObserveWebsocketEvent(restored, make(map[int]json.RawMessage), &[]json.RawMessage{})
	if err = writeCodexWebsocketPayload(conn, restored); err != nil {
		return &codexWebsocketTurnError{http.StatusBadGateway, "client_write_failed", err}
	}
	return nil
}

func forwardCodexWebsocketSSE(conn *websocket.Conn, state *relayconvert.CodexResponsesWebsocketState, originalRequest []byte, body io.Reader) *codexWebsocketTurnError {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), codexWebsocketMessageLimit)
	outputItems := make(map[int]json.RawMessage)
	fallbackItems := make([]json.RawMessage, 0, 4)
	seenPayload := false
	completed := false
	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		restored, err := relayconvert.RestoreCodexResponsesPayload(originalRequest, payload)
		if err != nil {
			return &codexWebsocketTurnError{http.StatusBadGateway, "invalid_upstream_response", err}
		}
		seenPayload = true
		if state.ObserveWebsocketEvent(restored, outputItems, &fallbackItems) {
			completed = true
		}
		if err = writeCodexWebsocketPayload(conn, restored); err != nil {
			return &codexWebsocketTurnError{http.StatusBadGateway, "client_write_failed", err}
		}
	}
	if err := scanner.Err(); err != nil {
		return &codexWebsocketTurnError{http.StatusBadGateway, "upstream_stream_failed", err}
	}
	if !seenPayload {
		return &codexWebsocketTurnError{http.StatusBadGateway, "empty_upstream_response", fmt.Errorf("upstream returned no Responses events")}
	}
	if !completed {
		return &codexWebsocketTurnError{http.StatusBadGateway, "incomplete_upstream_response", fmt.Errorf("upstream stream ended before response.completed")}
	}
	return nil
}

func writeCodexWebsocketPayload(conn *websocket.Conn, payload []byte) error {
	if err := conn.SetWriteDeadline(time.Now().Add(codexWebsocketWriteTimeout)); err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, payload)
}

func writeCodexWebsocketError(conn *websocket.Conn, status int, code string, err error) error {
	if status < http.StatusBadRequest {
		status = http.StatusInternalServerError
	}
	if strings.TrimSpace(code) == "" {
		code = "codex_websocket_error"
	}
	payload, _ := json.Marshal(gin.H{
		"type": "error",
		"error": gin.H{
			"type":    "codex_websocket_error",
			"code":    code,
			"message": err.Error(),
			"status":  status,
		},
	})
	return writeCodexWebsocketPayload(conn, payload)
}

func codexWebsocketHTTPError(resp *http.Response) *codexWebsocketTurnError {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return &codexWebsocketTurnError{resp.StatusCode, "upstream_error", err}
	}
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &envelope)
	message := strings.TrimSpace(envelope.Error.Message)
	if message == "" {
		message = strings.TrimSpace(string(body))
	}
	if message == "" {
		message = http.StatusText(resp.StatusCode)
	}
	code := strings.TrimSpace(envelope.Error.Code)
	if code == "" {
		code = "upstream_error"
	}
	return &codexWebsocketTurnError{resp.StatusCode, code, fmt.Errorf("%s", message)}
}

func codexWebsocketUpgradeHeaders(request *http.Request) http.Header {
	headers := make(http.Header)
	if request == nil {
		return headers
	}
	if turnState := strings.TrimSpace(request.Header.Get("x-codex-turn-state")); turnState != "" {
		headers.Set("x-codex-turn-state", turnState)
	}
	return headers
}
