//go:build linux || android || darwin || freebsd || netbsd || openbsd

package panel

import "syscall"

// detachSysProcAttr 让子进程进入独立会话：父进程（本面板进程）随即被重启脚本
// 杀掉时，子进程不会收到同组信号、也不会因父进程退出而结束。
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
