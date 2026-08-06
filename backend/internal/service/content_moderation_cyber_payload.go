package service

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
)

// cyber 请求体落库时替换超长图片 data URL / 裸 base64，避免单条日志膨胀。
var cyberPolicyDataImagePattern = regexp.MustCompile(`(?i)data:image\/[a-z0-9.+-]+;base64,[A-Za-z0-9+/=\s]{32,}`)

// buildCyberPolicyInputExcerpt 组装 cyber 命中后的可复盘正文：提示词抽取 + 完整请求体。
// 调用方保证 requestBody 仅在异步落库 goroutine 中使用副本，避免与请求生命周期竞态。
func buildCyberPolicyInputExcerpt(requestBody []byte) string {
	if len(requestBody) == 0 {
		return ""
	}
	prompt := extractCyberPolicyPromptText(requestBody)
	bodyText := sanitizeCyberPolicyRequestBody(requestBody)

	var b strings.Builder
	if prompt != "" {
		b.WriteString("=== 提示词 / Prompt ===\n")
		b.WriteString(trimRunes(prompt, maxCyberPolicyPromptRunes))
		if bodyText != "" {
			b.WriteString("\n\n")
		}
	}
	if bodyText != "" {
		b.WriteString("=== 完整请求体 / Request Body ===\n")
		b.WriteString(bodyText)
	}
	if b.Len() == 0 {
		return ""
	}
	return trimRunes(redactContentModerationSecrets(b.String()), maxCyberPolicyInputExcerptRunes)
}

// extractCyberPolicyPromptText 从常见 OpenAI 兼容协议字段抽取可读提示词，
// 覆盖 instructions / input / messages / prompt，便于管理员快速定位违规语境。
func extractCyberPolicyPromptText(body []byte) string {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return ""
	}
	var sections []string

	if inst := strings.TrimSpace(gjson.GetBytes(body, "instructions").String()); inst != "" {
		sections = append(sections, "[instructions]\n"+inst)
	}

	if input := gjson.GetBytes(body, "input"); input.Exists() {
		var parts []string
		collectCyberPolicyTextValue(input, &parts)
		if joined := strings.TrimSpace(strings.Join(parts, "\n")); joined != "" {
			sections = append(sections, "[input]\n"+joined)
		}
	}

	if messages := gjson.GetBytes(body, "messages"); messages.IsArray() {
		var lines []string
		for _, msg := range messages.Array() {
			role := strings.TrimSpace(msg.Get("role").String())
			if role == "" {
				role = "unknown"
			}
			var parts []string
			// 仅抽取文本，图片以占位说明，避免把 base64 塞进提示词区。
			collectCyberPolicyTextValue(msg.Get("content"), &parts)
			if text := strings.TrimSpace(strings.Join(parts, "\n")); text != "" {
				lines = append(lines, fmt.Sprintf("[%s]\n%s", role, text))
			}
		}
		if len(lines) > 0 {
			sections = append(sections, "[messages]\n"+strings.Join(lines, "\n\n"))
		}
	}

	if prompt := strings.TrimSpace(gjson.GetBytes(body, "prompt").String()); prompt != "" {
		sections = append(sections, "[prompt]\n"+prompt)
	}

	// Anthropic / Gemini 兼容路径偶发字段
	if system := strings.TrimSpace(gjson.GetBytes(body, "system").String()); system != "" {
		sections = append(sections, "[system]\n"+system)
	}
	if contents := gjson.GetBytes(body, "contents"); contents.IsArray() {
		var parts []string
		collectCyberPolicyTextValue(contents, &parts)
		if joined := strings.TrimSpace(strings.Join(parts, "\n")); joined != "" {
			sections = append(sections, "[contents]\n"+joined)
		}
	}

	return strings.TrimSpace(strings.Join(sections, "\n\n"))
}

func collectCyberPolicyTextValue(value gjson.Result, parts *[]string) {
	switch {
	case !value.Exists():
		return
	case value.Type == gjson.String:
		text := strings.TrimSpace(value.String())
		if text == "" {
			return
		}
		// 跳过纯图片 data URL
		if strings.HasPrefix(strings.ToLower(text), "data:image/") {
			*parts = append(*parts, "[image omitted]")
			return
		}
		*parts = append(*parts, text)
	case value.IsArray():
		value.ForEach(func(_, item gjson.Result) bool {
			collectCyberPolicyTextValue(item, parts)
			return true
		})
	case value.IsObject():
		typ := strings.ToLower(strings.TrimSpace(value.Get("type").String()))
		switch typ {
		case "image_url", "input_image", "image":
			*parts = append(*parts, "[image omitted]")
			return
		}
		if text := strings.TrimSpace(value.Get("text").String()); text != "" {
			*parts = append(*parts, text)
		}
		if value.Get("content").Exists() {
			collectCyberPolicyTextValue(value.Get("content"), parts)
		}
		if value.Get("parts").Exists() {
			collectCyberPolicyTextValue(value.Get("parts"), parts)
		}
		// Responses 协议 input_text 可能直接挂在 item 上
		if typ == "input_text" || typ == "output_text" || typ == "text" || typ == "message" {
			// text 已处理；若仍无 content，不重复
		}
	}
}

// sanitizeCyberPolicyRequestBody 对原始请求体做图片省略与长度截断，便于落库复盘。
func sanitizeCyberPolicyRequestBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	text := string(body)
	text = cyberPolicyDataImagePattern.ReplaceAllString(text, "data:image/...;[已省略图片数据]")
	// 过长的裸 base64 段（常见于 image source.data）用启发式截短，避免日志爆炸。
	text = truncateLongBase64Literals(text)
	if len(text) > maxCyberPolicyRequestBodyBytes {
		text = text[:maxCyberPolicyRequestBodyBytes] + "\n...[request body truncated]"
	}
	return text
}

// truncateLongBase64Literals 将 JSON 字符串里超长的 base64 形态片段替换为占位符。
// 仅匹配引号内、长度足够大的连续 base64 字符，降低误伤普通短 token 的概率。
var cyberPolicyLongBase64Literal = regexp.MustCompile(`"([A-Za-z0-9+/]{256,}={0,2})"`)

func truncateLongBase64Literals(text string) string {
	return cyberPolicyLongBase64Literal.ReplaceAllStringFunc(text, func(match string) string {
		// match 含两侧引号；内容长度 = 总长 - 2
		n := len(match) - 2
		if n < 0 {
			n = 0
		}
		return fmt.Sprintf(`"[已省略长 base64 数据 len=%d]"`, n)
	})
}
