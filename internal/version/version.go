// Package version 是 openpe 与 openpe-server 共享的唯一版本事实源。
//
// 业务契约（docs/requirements/2026-08-25-version-and-update.md V2）：版本真相
// 只来自 Go 工具链写入二进制的 build 信息——`go install module@vX.Y.Z/@latest`
// 得到发布 tag，本地构建得到 VCS 伪版本（脏工作区带 +dirty），两个入口共享
// 本包实现，替代此前两份各自硬编码的 "dev" 变量（它们与 buildinfo 并存且
// 互不一致）。不读 ldflags：本项目没有也不引入构建注入流水线。
package version

import (
	"runtime/debug"
	"strings"
)

// Devel 是无可用模块版本时的归一值：非 module 构建（ReadBuildInfo 不可用）
// 或工具链标记的 "(devel)"（如 go test、禁用 buildvcs 的构建）。
const Devel = "devel"

// readBuildInfo 可在测试中替换，隔离对真实构建环境的依赖。
var readBuildInfo = debug.ReadBuildInfo

// Value 返回当前二进制的版本字符串：发布 tag（v0.1.0）、VCS 伪版本
// （v0.0.0-20260825…-abcdef+dirty）或 Devel。所有消费点（--version、
// GET /v1/info、lifecycle descriptor、启动日志）都必须经由本函数。
func Value() string {
	info, ok := readBuildInfo()
	if !ok {
		return Devel
	}
	return Normalize(info.Main.Version)
}

// Normalize 把 BuildInfo.Main.Version 的原始值映射为用户可见值：空串与
// "(devel)" 归一为 Devel，其余（tag、伪版本，含 +dirty 后缀）原样保留。
func Normalize(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "(devel)" {
		return Devel
	}
	return raw
}
