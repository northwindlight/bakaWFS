//go:build windows

package main

import (
	"fmt"
	"io"
	"os"

	"github.com/mattn/go-colorable"
	"golang.org/x/sys/windows"
)

func getWindowsTermLevel() int {
	value, found := windows.Getenv("ConEmuANSI")
	if found == true && value == "ON" {
		return 2
	}

	major, _, build := windows.RtlGetNtVersionNumbers()

	if major < 10 {
		value, found = windows.Getenv("ANSICON")
		if found == false || value == "" {
			return 1
		}
		return 0
	}

	// Windows 10 早期版本支持 ANSI 但不支持真彩
	if build < 10586 {
		return 0
	}
	if build < 14931 {
		return 1
	}

	return 2
}

// 尝试开启 VT 模式 (驱魔仪式)
func tryEnableVT() bool {
	handle, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE)
	if err != nil {
		return false
	}

	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err != nil {
		return false
	}

	// 开启 VIRTUAL_TERMINAL_PROCESSING
	mode |= windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING
	if err := windows.SetConsoleMode(handle, mode); err != nil {
		return false
	}
	return true
}

func setupOutput() io.Writer {
	level := getWindowsTermLevel()
	tryEnableVT()
	DisableQuickEdit()
	// 1. 如果是高等级终端，或者成功开启了 VT 模式
	if level == 2 {
		// 这种情况下，即便不是 Windows Terminal，新版 CMD 也能显示颜色
		return os.Stdout
	}

	// 2. 只有当开启 VT 失败，且版本号确实很低时，才确定是“1995年垃圾”
	fmt.Println("检测到旧版本CMD")
	fmt.Println("都 2026 年了，大清早亡了，还在用 CMD？")
	fmt.Println("这坨1995年的垃圾能不能滚啊")
	fmt.Println("你知道我为了这坨垃圾单独适配色彩花了多少时间吗？就为了这个特例")
	fmt.Println("下载 Windows Terminal: https://aka.ms/terminal")
	fmt.Println("如果你坚持用 CMD，色彩转换已启用")
	fmt.Println()
	// CMD 需要用 colorable 转换
	return colorable.NewColorableStdout()
}

// DisableQuickEdit 检查并关闭传统 conhost 的快速编辑模式。
//
// 快速编辑模式允许用户在控制台中用鼠标选取文字，但 conhost 下选文字会
// 阻塞程序的控制台 I/O（程序完全卡住），因此理想情况是检测到 conhost
// 时自动关闭此模式。
//
// 问题在于：Windows Terminal 也可能复用 conhost 作为后端，此时 console
// mode 同样标记了快速编辑开启，但 WT 的选取由自己处理，不会阻塞程序。
// 如果一并关掉，反而导致 WT 无法用鼠标选文字。
//
// 理想方案是向上遍历父进程链找到最顶层宿主来判断终端类型，但实现复杂，
// 边界情况多（服务宿主、不同启动方式等），暂时搁置。
// 当前做法：无论哪种终端都不动快速编辑，优先保证 WT 用户的选取功能。
// ── 待改进：用 NtQueryInformationProcess / CreateToolhelp32Snapshot
//    遍历父进程树，识别终端类型后做不同处理。
func DisableQuickEdit() {
	h, err := windows.GetStdHandle(windows.STD_INPUT_HANDLE)
	if err != nil {
		return
	}

	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return
	}

	const ENABLE_QUICK_EDIT_MODE = 0x0040

	// 快速编辑未开启，无需处理
	if (mode & ENABLE_QUICK_EDIT_MODE) == 0 {
		return
	}

	// 目前暂时搁置区分逻辑，不影响程序正常运行。
	_ = h
	_ = mode
}
