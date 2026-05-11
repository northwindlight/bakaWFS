//go:build windows

package main

import (
	"fmt"
	"io"
	"os"
	"unsafe"

	"github.com/mattn/go-colorable"
	"golang.org/x/sys/windows"
)

func tryEnableVT() bool {
	handle, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE)
	if err != nil {
		return false
	}
	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err != nil {
		return false
	}
	mode |= windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING
	return windows.SetConsoleMode(handle, mode) == nil
}

func isOldKernel() bool {
	major, _, build := windows.RtlGetNtVersionNumbers()
	if major < 10 {
		return true
	}
	// Windows 10 1607 之前不支持真彩 ANSI
	return build < 14393
}

func setupOutput() io.Writer {
	tryEnableVT()
	DisableQuickEdit()

	if !hasLegacyAncestor() {
		return os.Stdout
	}

	// 以下是 conhost 受害者专属逻辑
	if isOldKernel() {
		fmt.Println("都 2026 年了还在用 conhost？这坨 1995 年的垃圾能不能赶紧扔了")
		fmt.Println("下载 Windows Terminal: https://aka.ms/terminal")
		fmt.Println("如果你坚持，色彩转换已启用")
		fmt.Println()
		return colorable.NewColorableStdout()
	}
	return os.Stdout
}

// DisableQuickEdit 检测到 conhost 则关闭快速编辑模式，防止鼠标选取卡死程序。
// 如需恢复：右键标题栏 → 属性 → 选项 → 勾选"快速编辑模式"。
func DisableQuickEdit() {
	if !hasLegacyAncestor() {
		return
	}

	h, err := windows.GetStdHandle(windows.STD_INPUT_HANDLE)
	if err != nil {
		return
	}

	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return
	}

	const (
		ENABLE_QUICK_EDIT_MODE = 0x0040
		ENABLE_EXTENDED_FLAGS  = 0x0080
	)

	if (mode & ENABLE_QUICK_EDIT_MODE) == 0 {
		return
	}

	newMode := mode | ENABLE_EXTENDED_FLAGS
	newMode &^= ENABLE_QUICK_EDIT_MODE
	if windows.SetConsoleMode(h, newMode) == nil {
		fmt.Println("检测到 conhost，已关闭快速编辑模式（避免鼠标选取导致程序卡死）")
		fmt.Println("如需恢复：右键标题栏 → 属性 → 选项 → 快速编辑模式")
	}
}

// legacyTerminals 需要关闭 Quick Edit 的老终端最终父进程名。
var legacyTerminals = map[string]bool{
	"conhost.exe":     true,
	"cmd.exe":         true,
	"powershell.exe":  true,
}

// hasLegacyAncestor 遍历全部父进程链，取最终（顶层）父进程，判断是否为老终端。
// 结果缓存，setupOutput 和 DisableQuickEdit 各调一次不重复遍历。
func hasLegacyAncestor() bool {
	if termCache.checked {
		return termCache.result
	}
	termCache.checked = true

	pid := windows.GetCurrentProcessId()
	var finalName string
	for {
		ppid, name, err := parentProcess(pid)
		if err != nil || ppid == 0 || ppid == pid {
			break
		}
		finalName = name
		pid = ppid
	}

	termCache.result = legacyTerminals[finalName]
	return termCache.result
}

var termCache struct {
	checked bool
	result  bool
}

func parentProcess(pid uint32) (ppid uint32, name string, err error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0, "", err
	}
	defer windows.CloseHandle(snap)

	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))

	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		if pe.ProcessID == pid {
			return pe.ParentProcessID, windows.UTF16ToString(pe.ExeFile[:]), nil
		}
		pe.Size = uint32(unsafe.Sizeof(pe))
	}
	return 0, "", fmt.Errorf("process %d not found", pid)
}
