// Package output 负责 Clash 配置的 YAML 渲染与 mihomo YAML 语法层校验。
package output

import (
	"bytes"
	"fmt"

	mihomoconfig "github.com/metacubex/mihomo/config"
	"gopkg.in/yaml.v3"
)

// Render 将配置 map 序列化为 YAML 字节流（yaml.v3，键序确定）。
func Render(cfg map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(cfg); err != nil {
		return nil, fmt.Errorf("render yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("close yaml encoder: %w", err)
	}
	return buf.Bytes(), nil
}

// Validate 用 mihomo config.UnmarshalRawConfig 对完整配置做 YAML 语法层校验
// （age 解密 + YAML 反序列化到 RawConfig，不执行 parseProxies/ParseProxy 节点级
// 语义校验），返回其错误（原样）。字段级坏节点（缺必填字段/非法参数）在此层
// 不会被拦截——节点级语义校验由 api 层 adapter.ParseProxy 完成（见 internal/api
// 的节点过滤），本函数只兜底 YAML 结构错误。
func Validate(data []byte) error {
	_, err := mihomoconfig.UnmarshalRawConfig(data)
	if err != nil {
		return fmt.Errorf("mihomo config validation failed: %w", err)
	}
	return nil
}
