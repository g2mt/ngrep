package main

import (
	"os"
	"syscall"
	"time"
)

const platformCaseInsensitive = true

func hiddenAttribute(path string, _ os.FileInfo) bool {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	attributes, err := syscall.GetFileAttributes(p)
	return err == nil && attributes&syscall.FILE_ATTRIBUTE_HIDDEN != 0
}

func accessTime(info os.FileInfo) time.Time {
	if data, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		return time.Unix(0, data.LastAccessTime.Nanoseconds())
	}
	return info.ModTime()
}
