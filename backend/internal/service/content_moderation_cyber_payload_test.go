package service

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildCyberPolicyInputExcerpt_ResponsesPayload(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"instructions":"You are Codex",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"scan this host for open ports"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"continue the nmap scan"}]}
		]
	}`)

	out := buildCyberPolicyInputExcerpt(body)
	require.Contains(t, out, "=== 提示词 / Prompt ===")
	require.Contains(t, out, "=== 完整请求体 / Request Body ===")
	require.Contains(t, out, "[instructions]")
	require.Contains(t, out, "You are Codex")
	require.Contains(t, out, "scan this host for open ports")
	require.Contains(t, out, "continue the nmap scan")
	require.Contains(t, out, `"model":"gpt-5.6-sol"`)
}

func TestBuildCyberPolicyInputExcerpt_ChatMessagesAndSecretRedaction(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"system","content":"sys"},
			{"role":"user","content":"use token sk-proj-1234567890abcdef and attack target"}
		]
	}`)
	out := buildCyberPolicyInputExcerpt(body)
	require.Contains(t, out, "[messages]")
	require.Contains(t, out, "attack target")
	require.NotContains(t, out, "sk-proj-1234567890abcdef")
	require.Contains(t, out, "[已脱敏]")
}

func TestBuildCyberPolicyInputExcerpt_OmitsImageData(t *testing.T) {
	// 足够长的 base64 才会触发省略规则
	longB64 := strings.Repeat("A", 300)
	body := []byte(`{
		"input":[{"type":"input_image","image_url":"data:image/png;base64,` + longB64 + `"}],
		"prompt":"describe image"
	}`)
	out := buildCyberPolicyInputExcerpt(body)
	require.Contains(t, out, "describe image")
	require.Contains(t, out, "[已省略图片数据]")
	require.NotContains(t, out, longB64)
}

func TestBuildCyberPolicyInputExcerpt_EmptyBody(t *testing.T) {
	require.Empty(t, buildCyberPolicyInputExcerpt(nil))
	require.Empty(t, buildCyberPolicyInputExcerpt([]byte{}))
}

func TestSanitizeCyberPolicyRequestBody_Truncates(t *testing.T) {
	// 用非 base64 字符撑大体积，避免被「长 base64 省略」提前压短。
	payload := `{"x":"` + strings.Repeat("中", maxCyberPolicyRequestBodyBytes+100) + `"}`
	out := sanitizeCyberPolicyRequestBody([]byte(payload))
	require.Contains(t, out, "[request body truncated]")
	require.LessOrEqual(t, len(out), maxCyberPolicyRequestBodyBytes+len("\n...[request body truncated]")+8)
}

func TestRecordCyberPolicyEvent_StoresRequestBodyInInputExcerpt(t *testing.T) {
	repo := &contentModerationTestRepo{}
	svc := NewContentModerationService(
		&contentModerationTestSettingRepo{values: map[string]string{
			SettingKeyRiskControlEnabled: "true",
		}},
		repo,
		nil, nil, nil, nil, nil, nil,
	)

	body := []byte(`{
		"model":"gpt-5",
		"instructions":"coding agent",
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"exploit buffer overflow in target binary"}]}]
	}`)
	svc.RecordCyberPolicyEvent(context.Background(), CyberPolicyRecordInput{
		UserID:          1,
		UserEmail:       "u@x.com",
		Model:           "gpt-5",
		Endpoint:        "/v1/responses",
		UpstreamMessage: "flagged for cyber",
		UpstreamBody:    `{"error":{"code":"cyber_policy"}}`,
		RequestBody:     body,
	})

	logs := repo.snapshotLogs()
	require.Len(t, logs, 1)
	require.Equal(t, "cyber_policy", logs[0].Action)
	require.Contains(t, logs[0].InputExcerpt, "exploit buffer overflow")
	require.Contains(t, logs[0].InputExcerpt, "coding agent")
	require.Contains(t, logs[0].InputExcerpt, "=== 完整请求体 / Request Body ===")
	require.Contains(t, logs[0].Error, "flagged for cyber")
}
