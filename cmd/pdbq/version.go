package main

import "runtime/debug"

func init() {
	version = resolveVersion(version, debug.ReadBuildInfo)
}

func resolveVersion(stamped string, read func() (*debug.BuildInfo, bool)) string {
	if stamped != "dev" {
		return stamped
	}
	info, ok := read()
	if !ok || info.Main.Version == "" || info.Main.Version == "(devel)" {
		return stamped
	}
	return info.Main.Version
}
