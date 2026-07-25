package service

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestBuildOpenAIWSFailureTerminal(t *testing.T) {
	t.Run("preserves response id and omits usage", func(t *testing.T) {
		payload, responseID, err := buildOpenAIWSFailureTerminal(" resp_known ", "")
		require.NoError(t, err)
		require.Equal(t, "resp_known", responseID)
		require.Equal(t, "response.failed", gjson.GetBytes(payload, "type").String())
		require.Equal(t, "resp_known", gjson.GetBytes(payload, "response.id").String())
		require.Equal(t, "response", gjson.GetBytes(payload, "response.object").String())
		require.Equal(t, "failed", gjson.GetBytes(payload, "response.status").String())
		require.Equal(t, openAIWSUpstreamClosedErrorCode, gjson.GetBytes(payload, "response.error.code").String())
		require.Equal(t, openAIWSUpstreamClosedMessage, gjson.GetBytes(payload, "response.error.message").String())
		require.False(t, gjson.GetBytes(payload, "response.usage").Exists())
	})

	t.Run("generates response id", func(t *testing.T) {
		payload, responseID, err := buildOpenAIWSFailureTerminal("", "Please retry this turn.")
		require.NoError(t, err)
		require.Regexp(t, `^resp_[0-9a-f-]{36}$`, responseID)
		require.Equal(t, responseID, gjson.GetBytes(payload, "response.id").String())
		require.Equal(t, "Please retry this turn.", gjson.GetBytes(payload, "response.error.message").String())
	})
}

func TestOpenAIWSTerminalClassification(t *testing.T) {
	require.True(t, isOpenAIWSSuccessTerminalEvent("response.completed"))
	require.True(t, isOpenAIWSSuccessTerminalEvent(" response.done "))
	require.False(t, isOpenAIWSSuccessTerminalEvent("response.failed"))
	require.True(t, isOpenAIWSFailureTerminalEvent("response.failed"))
	require.True(t, isOpenAIWSFailureTerminalEvent("response.incomplete"))
	require.True(t, isOpenAIWSFailureTerminalEvent("response.cancelled"))
	require.False(t, isOpenAIWSFailureTerminalEvent("response.output_text.done"))
}

func TestOpenAIWSSemanticOutputEvent(t *testing.T) {
	for _, eventType := range []string{
		"response.output_text.delta",
		"response.function_call_arguments.delta",
		"response.output_item.added",
		"response.output_item.done",
		"response.content_part.added",
		"response.image_generation_call.completed",
		"response.web_search_call.in_progress",
	} {
		require.True(t, isOpenAIWSSemanticOutputEvent(eventType), eventType)
	}
	for _, eventType := range []string{
		"response.created",
		"response.in_progress",
		"response.queued",
		"rate_limits.updated",
		"response.completed",
		"response.failed",
	} {
		require.False(t, isOpenAIWSSemanticOutputEvent(eventType), eventType)
	}
}

func TestWrapOpenAIWSIngressTurnErrorWithDetails(t *testing.T) {
	cause := errors.New("read failed")
	err := wrapOpenAIWSIngressTurnErrorWithDetails(
		" read_upstream ",
		cause,
		true,
		openAIWSIngressTurnErrorDetails{
			ResponseID:      " resp_1 ",
			EventCount:      3,
			TokenEventCount: 1,
			TerminalCount:   0,
			FirstEventType:  " response.created ",
			LastEventType:   " response.output_text.delta ",
			CloseStatus:     " 1006 ",
			CloseReason:     " closed ",
		},
	)
	turnErr, ok := err.(*openAIWSIngressTurnError)
	require.True(t, ok)
	require.Equal(t, "read_upstream", turnErr.stage)
	require.Equal(t, "resp_1", turnErr.responseID)
	require.Equal(t, 3, turnErr.eventCount)
	require.Equal(t, 1, turnErr.tokenEventCount)
	require.Equal(t, "response.created", turnErr.firstEventType)
	require.Equal(t, "response.output_text.delta", turnErr.lastEventType)
	require.Equal(t, "1006", turnErr.closeStatus)
	require.Equal(t, "closed", turnErr.closeReason)
	require.True(t, turnErr.wroteDownstream)
	require.ErrorIs(t, err, cause)
}
