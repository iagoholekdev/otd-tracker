package main

import "io/fs"

func staticSubFS() (fs.FS, error) {
	return fs.Sub(staticFiles, "static")
}
