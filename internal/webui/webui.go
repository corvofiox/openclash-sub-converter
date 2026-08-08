// Package webui 提供 Web 管理台静态前端（go:embed 原生 HTML/CSS/JS 单页）。
//
// 前端零外部依赖（无 CDN/无框架/无外链资源），随二进制嵌入，离线可用。
// 所有 /api/v1 请求经 api 包的 authMiddleware 鉴权后由前端携带 X-Token；
// 页面与静态资源本身不鉴权（数据全在 /api/v1，浏览器导航无法携带令牌头）。
package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var FS embed.FS

// Handler 返回管理台静态资源处理器。
//
// 用 fs.Sub 把嵌入 FS 的根切换到 static/ 目录，使请求路径 / 直接命中
// index.html（http.FileServer 对目录自动查找 index.html）；资源以
// /app.js /style.css 相对根路径引用，因此不用 http.StripPrefix。
// 未命中的路径（如 /favicon.ico）由 FileServer 返回 404，不额外处理。
//
// 响应统一附加安全头：
//   - Content-Security-Policy: default-src 'self'; style-src 'self' 'unsafe-inline'
//     （零外链资源；app.js 通过 element.style 动态设置样式，保留 unsafe-inline）
//   - X-Frame-Options: DENY（禁止被 iframe 嵌入）
//   - Cache-Control: no-cache（开发便利，防升级后缓存旧 JS）
func Handler() http.Handler {
	sub, err := fs.Sub(FS, "static")
	if err != nil {
		// static 目录缺失是编译期错误（go:embed 保证存在），不可能失败
		panic("webui: embed static dir missing: " + err.Error())
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-cache")
		fileServer.ServeHTTP(w, r)
	})
}
