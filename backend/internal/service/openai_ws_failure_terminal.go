package service

import (
	"encoding/json"
	"strings"

	"github.com/google/uuid"
)

const (
	openAIWSUpstreamClosedErrorCode = "upstream_websocket_closed_before_terminal"
	openAIWSUpstreamClosedMessage   = "The upstream WebSocket closed before a terminal response was received."
)

type openAIWSFailureTerminal struct {
	Type     string                  `json:"type"`
	Response openAIWSFailureResponse `json:"response"`
}

type openAIWSFailureResponse struct {
	ID     string               `json:"id"`
	Object string               `json:"object"`
	Status string               `json:"status"`
	Error  openAIWSFailureError `json:"error"`
}

type openAIWSFailureError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// buildOpenAIWSFailureTerminal creates the single terminal event emitted when an
// upstream WS turn ends without its own terminal event. The caller-provided
// message must already be suitable for clients; transport errors are deliberately
// not accepted here so credentials, hosts, and proxy details cannot leak.
func buildOpenAIWSFailureTerminal(responseID, message string) (payload []byte, resolvedResponseID string, err error) {
	return buildOpenAIWSFailureTerminalWithCode(responseID, openAIWSUpstreamClosedErrorCode, message)
}

func buildOpenAIWSFailureTerminalWithCode(responseID, code, message string) (payload []byte, resolvedResponseID string, err error) {
	resolvedResponseID = strings.TrimSpace(responseID)
	if resolvedResponseID == "" {
		resolvedResponseID = "resp_" + uuid.NewString()
	}
	code = strings.TrimSpace(code)
	if code == "" {
		code = openAIWSUpstreamClosedErrorCode
	}
	message = strings.TrimSpace(message)
	if message == "" {
		message = openAIWSUpstreamClosedMessage
	}
	payload, err = json.Marshal(openAIWSFailureTerminal{
		Type: "response.failed",
		Response: openAIWSFailureResponse{
			ID:     resolvedResponseID,
			Object: "response",
			Status: "failed",
			Error: openAIWSFailureError{
				Code:    code,
				Message: message,
			},
		},
	})
	return payload, resolvedResponseID, err
}

func isOpenAIWSSuccessTerminalEvent(eventType string) bool {
	switch normalizeOpenAIWSTerminalEvent(eventType) {
	case "response.completed", "response.done":
		return true
	default:
		return false
	}
}

func isOpenAIWSFailureTerminalEvent(eventType string) bool {
	terminal := normalizeOpenAIWSTerminalEvent(eventType)
	return terminal != "" && !isOpenAIWSSuccessTerminalEvent(terminal)
}

func shouldFailoverOpenAIWSErrorTurn(turn int, committed, schedulerAvailable, rateLimited bool, transientStatus int) bool {
	if turn != 1 || committed {
		return false
	}
	if rateLimited {
		return true
	}
	return schedulerAvailable && transientStatus >= 500 && transientStatus <= 599
}

// isOpenAIWSSemanticOutputEvent reports whether forwarding an event commits
// observable response content. Preamble and server-side rate-limit metadata can
// remain buffered so a failed, uncommitted attempt can be replayed invisibly.
func isOpenAIWSSemanticOutputEvent(eventType string) bool {
	eventType = strings.TrimSpace(eventType)
	if eventType == "" || isOpenAIWSTerminalEvent(eventType) {
		return false
	}
	switch eventType {
	case "response.created", "response.in_progress", "response.queued", "rate_limits.updated", "response.rate_limits.updated":
		return false
	case "response.output_item.added", "response.output_item.done":
		return true
	}
	if strings.Contains(eventType, ".delta") || strings.HasPrefix(eventType, "response.") {
		return true
	}
	return false
}
