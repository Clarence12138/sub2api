package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const openAIWSIngressFailureTestTimeout = 3 * time.Second

type openAIWSIngressFailureHarness struct {
	t           *testing.T
	client      *coderws.Conn
	server      *httptest.Server
	serverErrCh chan error
	dialer      *openAIWSQueueDialer
	pool        *openAIWSConnPool
	service     *OpenAIGatewayService
}

type openAIWSFailureConn struct {
	openAIWSCaptureConn
	writeErr error
	readErr  error
}

func (c *openAIWSFailureConn) WriteJSON(ctx context.Context, value any) error {
	if c.writeErr != nil {
		return c.writeErr
	}
	return c.openAIWSCaptureConn.WriteJSON(ctx, value)
}

func (c *openAIWSFailureConn) ReadMessage(context.Context) ([]byte, error) {
	if c.readErr != nil {
		return nil, c.readErr
	}
	return nil, io.EOF
}

func newOpenAIWSIngressFailureHarness(
	t *testing.T,
	upstreamConns ...openAIWSClientConn,
) *openAIWSIngressFailureHarness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := newOpenAIWSIngressFailureTestConfig()
	dialer := &openAIWSQueueDialer{conns: upstreamConns}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)
	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}
	h := &openAIWSIngressFailureHarness{
		t:           t,
		serverErrCh: make(chan error, 1),
		dialer:      dialer,
		pool:        pool,
		service:     svc,
	}
	h.server = httptest.NewServer(http.HandlerFunc(h.serveIngress))
	dialCtx, cancel := context.WithTimeout(context.Background(), openAIWSIngressFailureTestTimeout)
	defer cancel()
	client, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(h.server.URL, "http"), nil)
	require.NoError(t, err)
	h.client = client
	t.Cleanup(h.cleanup)
	return h
}

func newOpenAIWSIngressFailureTestConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	return cfg
}

func (h *openAIWSIngressFailureHarness) serveIngress(w http.ResponseWriter, r *http.Request) {
	conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{CompressionMode: coderws.CompressionContextTakeover})
	if err != nil {
		h.serverErrCh <- err
		return
	}
	defer func() { _ = conn.CloseNow() }()
	readCtx, cancel := context.WithTimeout(r.Context(), openAIWSIngressFailureTestTimeout)
	msgType, firstMessage, err := conn.Read(readCtx)
	cancel()
	if err != nil {
		h.serverErrCh <- err
		return
	}
	if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
		h.serverErrCh <- errors.New("unsupported websocket client message type")
		return
	}
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = r.Clone(r.Context())
	h.serverErrCh <- h.service.ProxyResponsesWebSocketFromClient(
		r.Context(), ginCtx, conn, newOpenAIWSIngressFailureTestAccount(), "sk-test", firstMessage, nil,
	)
}

func newOpenAIWSIngressFailureTestAccount() *Account {
	return &Account{
		ID:          918,
		Name:        "openai-ingress-failure-test",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test"},
		Extra:       map[string]any{"responses_websockets_v2_enabled": true},
	}
}

func (h *openAIWSIngressFailureHarness) write(payload string) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), openAIWSIngressFailureTestTimeout)
	defer cancel()
	require.NoError(h.t, h.client.Write(ctx, coderws.MessageText, []byte(payload)))
}

func (h *openAIWSIngressFailureHarness) read() []byte {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), openAIWSIngressFailureTestTimeout)
	defer cancel()
	msgType, message, err := h.client.Read(ctx)
	require.NoError(h.t, err)
	require.Equal(h.t, coderws.MessageText, msgType)
	return message
}

func (h *openAIWSIngressFailureHarness) closeAndWait() {
	h.t.Helper()
	require.NoError(h.t, h.client.Close(coderws.StatusNormalClosure, "done"))
	select {
	case err := <-h.serverErrCh:
		require.NoError(h.t, err)
	case <-time.After(5 * time.Second):
		h.t.Fatal("等待 ingress websocket 结束超时")
	}
}

func (h *openAIWSIngressFailureHarness) cleanup() {
	if h.client != nil {
		_ = h.client.CloseNow()
	}
	if h.server != nil {
		h.server.Close()
	}
	if h.pool != nil {
		h.pool.Close()
	}
}

func TestOpenAIWSIngress_CreatedThenEOFRetriesFreshWithoutLeakingPrefix(t *testing.T) {
	stale := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.created","response":{"id":"resp_stale_created"}}`),
	}}
	fresh := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.created","response":{"id":"resp_fresh_created"}}`),
		[]byte(`{"type":"response.completed","response":{"id":"resp_fresh_created","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
	}}
	h := newOpenAIWSIngressFailureHarness(t, stale, fresh)

	h.write(`{"type":"response.create","model":"gpt-5.1","stream":true}`)
	created := h.read()
	completed := h.read()

	require.Equal(t, "response.created", gjson.GetBytes(created, "type").String())
	require.Equal(t, "resp_fresh_created", gjson.GetBytes(created, "response.id").String())
	require.Equal(t, "response.completed", gjson.GetBytes(completed, "type").String())
	require.Equal(t, "resp_fresh_created", gjson.GetBytes(completed, "response.id").String())
	require.Equal(t, 2, h.dialer.DialCount(), "EOF 后必须强制 fresh dial，不能再次选 stale 连接")
	h.closeAndWait()
}

func TestOpenAIWSIngress_EarlyTransportFailuresRetryFresh(t *testing.T) {
	failures := []struct {
		name string
		err  error
	}{
		{name: "normal_close", err: coderws.CloseError{Code: coderws.StatusNormalClosure, Reason: "closed"}},
		{name: "abnormal_close", err: coderws.CloseError{Code: coderws.StatusAbnormalClosure, Reason: "reset"}},
		{name: "internal_error", err: coderws.CloseError{Code: coderws.StatusInternalError, Reason: "ping timeout"}},
		{name: "timeout", err: context.DeadlineExceeded},
		{name: "reset", err: errors.New("read tcp: connection reset by peer")},
	}
	for _, tc := range failures {
		t.Run(tc.name, func(t *testing.T) {
			stale := &openAIWSFailureConn{readErr: tc.err}
			fresh := &openAIWSCaptureConn{events: [][]byte{
				[]byte(`{"type":"response.completed","response":{"id":"resp_fresh_transport","model":"gpt-5.1"}}`),
			}}
			h := newOpenAIWSIngressFailureHarness(t, stale, fresh)

			h.write(`{"type":"response.create","model":"gpt-5.1","stream":false}`)
			require.Equal(t, "resp_fresh_transport", gjson.GetBytes(h.read(), "response.id").String())
			require.Equal(t, 2, h.dialer.DialCount())
			h.closeAndWait()
		})
	}
}

func TestOpenAIWSIngress_EarlyWriteFailureRetriesFresh(t *testing.T) {
	stale := &openAIWSFailureConn{writeErr: errors.New("write tcp: broken pipe")}
	fresh := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.completed","response":{"id":"resp_fresh_write","model":"gpt-5.1"}}`),
	}}
	h := newOpenAIWSIngressFailureHarness(t, stale, fresh)

	h.write(`{"type":"response.create","model":"gpt-5.1","stream":false}`)
	require.Equal(t, "resp_fresh_write", gjson.GetBytes(h.read(), "response.id").String())
	require.Equal(t, 2, h.dialer.DialCount())
	h.closeAndWait()
}

func TestOpenAIWSIngress_ConsecutiveEarlyEOFReturnsOneFailureAndKeepsClientSession(t *testing.T) {
	first := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.created","response":{"id":"resp_early_first"}}`),
	}}
	second := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.created","response":{"id":"resp_early_second"}}`),
	}}
	third := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.completed","response":{"id":"resp_after_early_failure","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
	}}
	h := newOpenAIWSIngressFailureHarness(t, first, second, third)

	h.write(`{"type":"response.create","model":"gpt-5.1","stream":true}`)
	failed := h.read()
	requireSyntheticUpstreamCloseFailure(t, failed)

	h.write(`{"type":"response.create","model":"gpt-5.1","stream":false}`)
	nextTurn := h.read()
	require.Equal(t, "response.completed", gjson.GetBytes(nextTurn, "type").String())
	require.Equal(t, "resp_after_early_failure", gjson.GetBytes(nextTurn, "response.id").String())
	require.Equal(t, 3, h.dialer.DialCount())
	h.closeAndWait()
}

func TestOpenAIWSIngress_DeltaThenEOFDoesNotReplayAndKeepsClientSession(t *testing.T) {
	partial := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.created","response":{"id":"resp_partial"}}`),
		[]byte(`{"type":"response.output_text.delta","response_id":"resp_partial","delta":"hello"}`),
	}}
	nextTurnConn := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.completed","response":{"id":"resp_after_partial","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
	}}
	h := newOpenAIWSIngressFailureHarness(t, partial, nextTurnConn)

	h.write(`{"type":"response.create","model":"gpt-5.1","stream":true}`)
	require.Equal(t, "response.created", gjson.GetBytes(h.read(), "type").String())
	require.Equal(t, "response.output_text.delta", gjson.GetBytes(h.read(), "type").String())
	requireSyntheticUpstreamCloseFailure(t, h.read())

	h.write(`{"type":"response.create","model":"gpt-5.1","stream":false}`)
	require.Equal(t, "resp_after_partial", gjson.GetBytes(h.read(), "response.id").String())
	require.Equal(t, 2, h.dialer.DialCount(), "语义 delta 已下发后不得重放当前 turn")
	h.closeAndWait()
	require.Len(t, partial.writes, 1)
	require.Len(t, nextTurnConn.writes, 1)
}

func TestOpenAIWSIngress_GenericErrorBecomesResponseFailed(t *testing.T) {
	upstream := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"error","error":{"type":"server_error","code":"server_error","message":"upstream exploded"}}`),
		[]byte(`{"type":"response.completed","response":{"id":"resp_after_generic_error","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
	}}
	h := newOpenAIWSIngressFailureHarness(t, upstream)

	h.write(`{"type":"response.create","model":"gpt-5.1","stream":true}`)
	failed := h.read()
	require.Equal(t, "response.failed", gjson.GetBytes(failed, "type").String())
	require.Equal(t, "failed", gjson.GetBytes(failed, "response.status").String())
	require.Equal(t, "server_error", gjson.GetBytes(failed, "response.error.code").String())

	h.write(`{"type":"response.create","model":"gpt-5.1","stream":false}`)
	nextTurn := h.read()
	require.Equal(t, "resp_after_generic_error", gjson.GetBytes(nextTurn, "response.id").String())
	h.closeAndWait()
	require.Len(t, upstream.writes, 2, "generic error 应结束当前 turn，而不是吞掉下一轮上游事件")
}

func TestOpenAIWSIngress_UnsuccessfulTerminalDoesNotCreateStickyBinding(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		status    string
	}{
		{name: "failed", eventType: "response.failed", status: "failed"},
		{name: "incomplete", eventType: "response.incomplete", status: "incomplete"},
		{name: "cancelled", eventType: "response.cancelled", status: "cancelled"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			responseID := "resp_unsuccessful_" + tc.name
			event := []byte(`{"type":"` + tc.eventType + `","response":{"id":"` + responseID + `","status":"` + tc.status + `"}}`)
			h := newOpenAIWSIngressFailureHarness(t, &openAIWSCaptureConn{events: [][]byte{event}})

			h.write(`{"type":"response.create","model":"gpt-5.1","stream":true}`)
			require.Equal(t, tc.eventType, gjson.GetBytes(h.read(), "type").String())
			h.closeAndWait()

			_, bound := h.service.getOpenAIWSStateStore().GetResponseConn(responseID)
			require.False(t, bound, "非成功终态不得建立 response -> conn sticky")
		})
	}
}

func TestOpenAIWSIngress_SecondTurnRateLimitDoesNotReplayFirstPayload(t *testing.T) {
	rateLimited := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.completed","response":{"id":"resp_rate_limit_seed","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		[]byte(`{"type":"error","error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"too many requests"}}`),
	}}
	afterFailure := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.completed","response":{"id":"resp_after_rate_limit","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
	}}
	h := newOpenAIWSIngressFailureHarness(t, rateLimited, afterFailure)

	firstPayload := `{"type":"response.create","model":"gpt-5.1","stream":false,"input":"first payload sentinel"}`
	secondPayload := `{"type":"response.create","model":"gpt-5.1","stream":false,"input":"second payload sentinel"}`
	h.write(firstPayload)
	require.Equal(t, "resp_rate_limit_seed", gjson.GetBytes(h.read(), "response.id").String())
	h.write(secondPayload)
	failed := h.read()
	require.Equal(t, "response.failed", gjson.GetBytes(failed, "type").String())

	h.write(`{"type":"response.create","model":"gpt-5.1","stream":false,"input":"third payload sentinel"}`)
	require.Equal(t, "resp_after_rate_limit", gjson.GetBytes(h.read(), "response.id").String())
	h.closeAndWait()
	require.Len(t, rateLimited.writes, 2)
	require.Len(t, afterFailure.writes, 1)
	require.Equal(t, "third payload sentinel", gjson.Get(requestToJSONString(afterFailure.writes[0]), "input").String())
}

func requireSyntheticUpstreamCloseFailure(t *testing.T, event []byte) {
	t.Helper()
	require.Equal(t, "response.failed", gjson.GetBytes(event, "type").String())
	require.Equal(t, "failed", gjson.GetBytes(event, "response.status").String())
	require.Equal(t, "upstream_websocket_closed_before_terminal", gjson.GetBytes(event, "response.error.code").String())
	require.True(t, strings.HasPrefix(gjson.GetBytes(event, "response.id").String(), "resp_"))
}
