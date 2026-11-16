// Package web 是管理界面的全部静态资源：桌面版和容器版共用同一份。
//
// 桌面版（main.go）把整个目录解包到磁盘，交给 WebView2 的虚拟主机；容器版由
// 网关直接把这些文件当静态资源发给浏览器（internal/zen/gateway/webui.go）。
// 两条路走的是同一份文件，所以界面不会长成两套。
package web

import (
	"embed"
	"io/fs"
)

//go:embed index.html login.html assets js styles
var files embed.FS

// FS 返回以界面根目录为根的只读文件树（index.html、js/、styles/、assets/）。
func FS() fs.FS { return files }
