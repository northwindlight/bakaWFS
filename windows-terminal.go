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

func DisableQuickEdit() {
	h, err := windows.GetStdHandle(windows.STD_INPUT_HANDLE)
	if err != nil {
		return
	}
	// 2. 判断是否为控制台
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		// 不是控制台（重定向/管道）
		return
	}

	const (
		ENABLE_QUICK_EDIT_MODE = 0x0040
		ENABLE_EXTENDED_FLAGS  = 0x0080
	)

	// 3. 判断是否是传统控制台（是否开启了快速编辑）
	isLegacyConsole := (mode & ENABLE_QUICK_EDIT_MODE) != 0

	if !isLegacyConsole {
		// 现代终端，不提示，不处理
		return
	}
	return
	// 4. 关闭快速编辑（必须先加 EXTENDED_FLAGS）
	newMode := mode | ENABLE_EXTENDED_FLAGS
	newMode &^= ENABLE_QUICK_EDIT_MODE

	_ = windows.SetConsoleMode(h, newMode)
	fmt.Println("检测到cmd或powershell，已经关闭快速编辑模式")
}
