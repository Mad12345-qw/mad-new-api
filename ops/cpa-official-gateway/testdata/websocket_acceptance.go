package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

func main() {
	if len(os.Args) != 2 {
		panic("token file path is required")
	}
	token, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	header := http.Header{"Authorization": []string{"Bearer " + strings.TrimSpace(string(token))}}
	conn, response, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:18318/v1/responses", header)
	if err != nil {
		if response != nil {
			panic(fmt.Sprintf("websocket dial failed: %s", response.Status))
		}
		panic(err)
	}
	defer conn.Close()

	first := map[string]any{
		"type":  "response.create",
		"model": "gpt-5.6-sol",
		"input": []any{map[string]any{
			"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": "Remember the number 314159 and reply ACK."}},
		}},
	}
	firstText := runTurn(conn, first)
	second := map[string]any{
		"type": "response.create",
		"input": []any{map[string]any{
			"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": "What number did I ask you to remember? Reply with digits only."}},
		}},
	}
	secondText := runTurn(conn, second)
	fmt.Printf("FIRST=%q\nSECOND=%q\n", firstText, secondText)
	if !strings.Contains(secondText, "314159") {
		panic("second turn lost websocket context")
	}
}

func runTurn(conn *websocket.Conn, request map[string]any) string {
	if err := conn.WriteJSON(request); err != nil {
		panic(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	var text strings.Builder
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			panic(err)
		}
		var event map[string]any
		if err = json.Unmarshal(payload, &event); err != nil {
			panic(err)
		}
		eventType, _ := event["type"].(string)
		if eventType == "response.output_text.delta" {
			if delta, ok := event["delta"].(string); ok {
				text.WriteString(delta)
			}
		}
		if eventType == "error" || eventType == "response.failed" {
			panic(string(payload))
		}
		if eventType == "response.completed" {
			return text.String()
		}
	}
}
