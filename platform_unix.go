//go:build !windows

package main

import (
	"os"
	"reflect"
	"time"
)

const platformCaseInsensitive = false

func hiddenAttribute(string, os.FileInfo) bool { return false }

// Stat layouts differ across Unix systems (Atim on Linux, Atimespec on BSD).
// Reflection keeps this stdlib-only without assuming one syscall.Stat_t layout.
func accessTime(info os.FileInfo) time.Time {
	v := reflect.ValueOf(info.Sys())
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() == reflect.Struct {
		for _, name := range []string{"Atim", "Atimespec"} {
			stamp := v.FieldByName(name)
			if stamp.IsValid() && stamp.Kind() == reflect.Struct {
				sec, ns := stamp.FieldByName("Sec"), stamp.FieldByName("Nsec")
				if sec.IsValid() && ns.IsValid() && sec.CanInt() && ns.CanInt() {
					return time.Unix(sec.Int(), ns.Int())
				}
			}
		}
		sec := v.FieldByName("Atime")
		if sec.IsValid() && sec.CanInt() {
			var ns int64
			if field := v.FieldByName("Atimensec"); field.IsValid() && field.CanInt() {
				ns = field.Int()
			}
			return time.Unix(sec.Int(), ns)
		}
	}
	// Some non-Unix Go targets don't expose atime at all.
	return info.ModTime()
}
