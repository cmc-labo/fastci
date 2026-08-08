package pkga

import "samplemod/pkgb"

func Run() string {
	return pkgb.Greet() + " via a"
}
