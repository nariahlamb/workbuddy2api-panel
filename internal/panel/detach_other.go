//go:build !(linux || android || darwin || freebsd || netbsd || openbsd)

package panel

import "syscall"

// detachSysProcAttr 非 Unix 平台（Windows）无会话概念，返回空 attr：
// 重启脚本本身只在 Android/Magisk 下有意义，其它平台重启接口会因脚本不存在返回 501。
func detachSysProcAttr() *syscall.SysProcAttr { return nil }
