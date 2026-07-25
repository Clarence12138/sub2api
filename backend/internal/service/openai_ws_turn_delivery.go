package service

import (
	"errors"
	"strconv"
	"strings"
)

type openAIWSTurnDelivery struct {
	pending   [][]byte
	committed bool
}

func (d *openAIWSTurnDelivery) Add(message []byte) {
	if d == nil {
		return
	}
	d.pending = append(d.pending, append([]byte(nil), message...))
}

func (d *openAIWSTurnDelivery) Flush(write func([]byte) error) error {
	if d == nil {
		return errors.New("websocket turn delivery is nil")
	}
	for len(d.pending) > 0 {
		if err := write(d.pending[0]); err != nil {
			return err
		}
		d.committed = true
		d.pending = d.pending[1:]
	}
	return nil
}

func (d *openAIWSTurnDelivery) Committed() bool {
	return d != nil && d.committed
}

func openAIWSAccountProxyIDForLog(account *Account) string {
	if account == nil || account.ProxyID == nil {
		return "-"
	}
	return strconv.FormatInt(*account.ProxyID, 10)
}

func openAIWSIngressTurnErrorDetailsFromState(
	responseID string,
	eventCount int,
	tokenEventCount int,
	terminalCount int,
	firstEventType string,
	lastEventType string,
	err error,
) openAIWSIngressTurnErrorDetails {
	closeStatus, closeReason := summarizeOpenAIWSReadCloseError(err)
	return openAIWSIngressTurnErrorDetails{
		ResponseID:      strings.TrimSpace(responseID),
		EventCount:      eventCount,
		TokenEventCount: tokenEventCount,
		TerminalCount:   terminalCount,
		FirstEventType:  strings.TrimSpace(firstEventType),
		LastEventType:   strings.TrimSpace(lastEventType),
		CloseStatus:     strings.TrimSpace(closeStatus),
		CloseReason:     strings.TrimSpace(closeReason),
	}
}
